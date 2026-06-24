package guestfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/provcontent"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

var pc = provcontent.Builder{}

// --- ProvisioningConfig ---

// ProvisioningConfig describes an entire provisioning operation.
// Pass to RunDeferred — no builder pattern, one shot, two guestfish sessions.
type ProvisioningConfig struct {
	RootfsPath string
	Readonly   bool
	RootUID    int
	RootGID    int
	UserUID    int
	UserGID    int

	// Operations (zero-value = skip)
	TargetSize       int64                   // resize target, 0 = no resize
	Hostname         string                  // set hostname, "" = skip
	User             string                  // ensure user, "" = skip
	SSHPubkeys       []string                // SSH authorized keys, nil = skip
	CloudInitDir     string                  // inject cloud-init seed, "" = skip
	DNSServer        string                  // inject DNS, "" = skip
	Shrink           bool                    // shrink filesystem to minimum
	Deblob           bool                    // OS cache cleanup + mask services
	FixFstab         bool                    // fix PARTUUID → /dev/vda in fstab
	DisableCloudInit bool                    // mask cloud-init services
	SetupSudo        bool                    // fix sudo ownership + setuid + sudoers drop-in
	CustomOps        []provcontent.Operation // arbitrary FileOp/ChrootOp ops queued via ApplyOps
}

// --- RunDeferred -- one-shot, two guestfish sessions ---

// RunDeferred executes all provisioning operations in a single guestfish session
// (plus one pre-read session for root device detection).
// All conditional logic runs inside the guest via sh commands.
func RunDeferred(ctx context.Context, cfg ProvisioningConfig) error {
	needsResize := cfg.TargetSize > 0
	if needsResize {
		info, err := os.Stat(cfg.RootfsPath)
		if err == nil && info.Size() >= cfg.TargetSize {
			needsResize = false
		}
	}
	hasOps := cfg.Hostname != "" || cfg.DNSServer != "" || len(cfg.SSHPubkeys) > 0 ||
		cfg.CloudInitDir != "" || cfg.DisableCloudInit || cfg.Shrink || cfg.Deblob || cfg.FixFstab
	if !needsResize && !hasOps {
		return nil
	}

	// Session 1: detect root device
	rootDevice, err := detectRootDevice(ctx, cfg.RootfsPath)
	if err != nil {
		return fmt.Errorf("guestfs: root device detection failed: %w", err)
	}

	// Phase 0: file truncation (before guestfish mount) — only when resizing
	if needsResize {
		doTruncateFile(cfg.RootfsPath, cfg.TargetSize)
	}

	// Create temp dir for file uploads
	tmpDir, err := os.MkdirTemp(infra.GetTempDir(), "mvm-guestfs-*")
	if err != nil {
		return fmt.Errorf("create guestfs temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Build the guestfish script
	commands, cmdsErr := buildScript(cfg, rootDevice, needsResize, tmpDir)
	if cmdsErr != nil {
		return fmt.Errorf("build guestfish script: %w", cmdsErr)
	}

	if len(commands) == 0 {
		return nil
	}

	// Write script to temp file
	scriptPath := filepath.Join(tmpDir, "guestfish-script.gf")
	scriptContent := strings.Join(commands, "\n")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), infra.PrivateKeyPerm); err != nil {
		return fmt.Errorf("write guestfish script: %w", err)
	}

	initEnv(ctx)
	allArgs := []string{
		"-a", cfg.RootfsPath,
		"--no-sync",
	}
	if cfg.Readonly {
		allArgs = append(allArgs, "--ro")
	}
	allArgs = append(allArgs, "-f", scriptPath)

	result, err := system.DefaultRunner.Run(
		ctx,
		append([]string{"guestfish"}, allArgs...),
		system.RunCmdOpts{Capture: true, Check: true},
	)
	if err != nil {
		return fmt.Errorf("guestfish session failed: %s: %w", result.Stderr, err)
	}

	// Post-session shrink: get block device size for truncation
	var shrinkResult int64
	if cfg.Shrink {
		sz, szErr := guestfishRun(ctx, cfg.RootfsPath, true, "", "blockdev-getsize64", rootDevice)
		if szErr == nil {
			if val, parseErr := strconv.ParseInt(strings.TrimSpace(sz), 10, 64); parseErr == nil {
				shrinkResult = val
			}
		}
	}
	if shrinkResult > 0 {
		finalSize := int64(float64(shrinkResult) * infra.ShrinkSafetyMargin)
		if err := os.Truncate(cfg.RootfsPath, finalSize); err != nil {
			slog.Warn("Failed to truncate after shrink", "error", err)
		}
	}

	return nil
}

// --- Script builder ---

func buildScript(cfg ProvisioningConfig, rootDevice string, needsResize bool, tmpDir string) ([]string, error) {
	var cmds []string
	cmds = append(cmds, "run")
	cmds = append(cmds, fmt.Sprintf("mount %s /", rootDevice))

	// Resize (grow) — before other modifications
	if needsResize {
		// Check fstype via guestfish vfs-type command inside the script.
		// We need a pre-read vfs-type. For now, assume ext4 (most common).
		cmds = append(cmds, "# Filesystem resize (grow)")
		cmds = append(cmds, fmt.Sprintf("resize2fs %s", rootDevice))
	}

	// Set hostname
	if cfg.Hostname != "" {
		cmds = append(cmds, "# Set hostname")
		cmds = append(
			cmds,
			fmt.Sprintf("sh %q", infra.ExecTemplate(setHostnameTmpl, hostnameData{Hostname: cfg.Hostname})),
		)
	}

	// Inject DNS
	if cfg.DNSServer != "" {
		cmds = append(cmds, "# Inject DNS")
		cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(injectDNSTmpl, dnsData{DNSServer: cfg.DNSServer})))
	}

	// Ensure user — unconditional when a non-empty user is specified.
	// --user flag should always create the user, regardless of SSH keys.
	if cfg.User != "" {
		cmds = append(cmds, "# Ensure user")
		cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(ensureUserTmpl, userData{
			User: cfg.User, UserUID: cfg.UserUID, UserGID: cfg.UserGID,
		})))

		// Add SSH keys only if provided
		if len(cfg.SSHPubkeys) > 0 {
			cmds = append(cmds, "# Add SSH keys")
			homeDir := "/root"
			if cfg.User != "root" {
				homeDir = "/home/" + cfg.User
			}
			cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(addSSHKeysTmpl, sshKeysData{
				User: cfg.User, Home: homeDir, Keys: cfg.SSHPubkeys,
			})))
		}
	}

	// Generate host keys
	cmds = append(cmds, "# Generate SSH host keys")
	cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(generateHostKeysTmpl, struct{}{})))

	// Enable SSH
	cmds = append(cmds, "# Enable SSH")
	cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(enableSSHTmpl, struct{}{})))

	// Upload SSH config files (sshd_config, first-boot installer, first-boot service)
	// Moved here from deblob section — these are SSH infrastructure, not OS cleanup.
	if len(cfg.SSHPubkeys) > 0 {
		uploadCmds := buildFileOps(tmpDir)
		cmds = append(cmds, uploadCmds...)
	}

	// Fix sudo — some Ubuntu cloud images ship with broken ownership
	// on /etc/sudo.conf and /usr/bin/sudo (owned by uid 1000 instead of
	// root). Without this, non-root users get "sudo: /etc/sudo.conf is
	// owned by uid 1000, should be 0" when running sudo.
	if cfg.SetupSudo {
		cmds = append(cmds, "# Fix sudo ownership")
		cmds = append(
			cmds,
			fmt.Sprintf(
				"sh %q",
				`test -f /etc/sudo.conf && chown root:root /etc/sudo.conf && chmod 0440 /etc/sudo.conf; test -f /usr/bin/sudo && chown root:root /usr/bin/sudo && chmod 4755 /usr/bin/sudo; true`,
			),
		)
	}

	// Disable cloud-init
	if cfg.DisableCloudInit {
		cmds = append(cmds, "# Disable cloud-init")
		cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(disableCloudInitTmpl, struct{}{})))
	}

	// Inject cloud-init seed files (needs upload, not sh)
	if cfg.CloudInitDir != "" {
		cmds = append(cmds, "# Inject cloud-init seed files")
		uploadCmds := buildCloudInitUploads(cfg.CloudInitDir)
		cmds = append(cmds, uploadCmds...)
	}

	// Deblob + fix fstab
	if cfg.Deblob {
		cmds = append(cmds, "# Deblob + fix fstab")
		cmds = append(cmds, fmt.Sprintf("sh %q", infra.ExecTemplate(deblobTmpl, struct{}{})))
	}

	// Standalone fstab fix (when SkipDeblob is true but FixFstab is still needed)
	if cfg.FixFstab && !cfg.Deblob {
		cmds = append(cmds, "# Fix fstab (PARTUUID → /dev/vda)")
		cmds = append(cmds, fmt.Sprintf(
			"sh %q",
			`if [ -f /etc/fstab ]; then sed -i 's/^PARTUUID=[^[:space:]]*/\/dev\/vda/' /etc/fstab 2>/dev/null || true; fi`,
		))
	}

	// Shrink (always runs — guestfish commands, not shell)
	if cfg.Shrink {
		cmds = append(cmds, "# Shrink filesystem to minimum")
		cmds = append(cmds, "zero-free-space /")
		cmds = append(cmds, "umount /")
		cmds = append(cmds, fmt.Sprintf("e2fsck %s correct:true", rootDevice))
		cmds = append(cmds, fmt.Sprintf("resize2fs-size %s 0", rootDevice))
	}

	// Custom ops (queued via ApplyOps) — FileOp (upload + chmod) and ChrootOp (sh)
	if len(cfg.CustomOps) > 0 {
		cmds = append(cmds, "# Custom operations (ApplyOps)")
		var hasCustomOps bool
		for _, op := range cfg.CustomOps {
			switch o := op.(type) {
			case provcontent.FileOp:
				hasCustomOps = true
				// Write file content to a temp file, then upload via guestfish
				relPath := strings.ReplaceAll(o.Path, "/", "_")
				tmpFile := filepath.Join(tmpDir, relPath)
				if err := os.WriteFile(tmpFile, o.Data, 0644); err != nil {
					return nil, fmt.Errorf("write custom op temp file %s: %w", o.Path, err)
				}
				if idx := strings.LastIndex(o.Path, "/"); idx > 0 {
					// Use sh command instead of guestfish mkdir-p because
					// mkdir-p does NOT follow symlinks (e.g., /var/run → /run).
					cmds = append(cmds, fmt.Sprintf(`sh "mkdir -p %s"`, o.Path[:idx]))
				}
				cmds = append(cmds, fmt.Sprintf("upload %s %s", tmpFile, o.Path))
				mode := o.Mode
				if mode == 0 {
					mode = 0644
				}
				cmds = append(cmds, fmt.Sprintf("chmod %o %s", mode, o.Path))
			case provcontent.ChrootOp:
				hasCustomOps = true
				// Execute as shell command inside the guest
				cmds = append(cmds, fmt.Sprintf("sh %q", o.Command))
			}
		}
		if !hasCustomOps {
			_ = cmds // no useful custom ops added
		}
	}

	cmds = append(cmds, "sync")
	cmds = append(cmds, "# END")
	return cmds, nil
}

// buildCloudInitUploads returns guestfish upload commands for cloud-init seed files.
func buildCloudInitUploads(cloudInitDir string) []string {
	seedDir := "/var/lib/cloud/seed/nocloud"
	var cmds []string
	// Use sh instead of mkdir-p: guestfish mkdir-p fails on symlinked paths like /var/run → /run.
	cmds = append(cmds, fmt.Sprintf(`sh "mkdir -p %s"`, seedDir))

	for _, filename := range []string{"meta-data", "user-data"} {
		src := filepath.Join(cloudInitDir, filename)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("upload %s %s/%s", src, seedDir, filename))
	}
	for _, filename := range []string{"network-config"} {
		src := filepath.Join(cloudInitDir, filename)
		if _, err := os.Stat(src); err == nil {
			cmds = append(cmds, fmt.Sprintf("upload %s %s/%s", src, seedDir, filename))
		}
	}
	return cmds
}

// buildFileOps writes provcontent FileOps to temp files and returns upload commands.
func buildFileOps(tmpDir string) []string {
	var cmds []string
	fileOps := []struct {
		path string
		data string
		mode int
	}{
		{"/etc/ssh/sshd_config.d/mvm.conf", pc.SSHDConfig("root"), infra.PublicKeyPerm},
		{"/usr/local/bin/first-boot-ssh-installer.sh", pc.FirstBootInstaller(), infra.ExecutablePerm},
		{"/etc/systemd/system/first-boot-ssh-installer.service", pc.FirstBootService(), infra.PublicKeyPerm},
	}

	for _, f := range fileOps {
		if f.data == "" {
			continue
		}
		relPath := strings.ReplaceAll(f.path, "/", "_")
		tmpFile := filepath.Join(tmpDir, relPath)
		if err := os.WriteFile(tmpFile, []byte(f.data), infra.PrivateKeyPerm); err != nil {
			slog.Warn("Failed to write temp file for upload", "path", f.path, "error", err)
			continue
		}
		if idx := strings.LastIndex(f.path, "/"); idx > 0 {
			// Use sh instead of mkdir-p: guestfish mkdir-p fails on symlinked paths like /var/run → /run.
			cmds = append(cmds, fmt.Sprintf(`sh "mkdir -p %s"`, f.path[:idx]))
		}
		cmds = append(cmds, fmt.Sprintf("upload %s %s", tmpFile, f.path))
		if f.mode != 0 {
			cmds = append(cmds, fmt.Sprintf("chmod %o %s", f.mode, f.path))
		}
	}
	return cmds
}

// --- Helpers ---

// detectRootDevice detects the root device by running guestfish list-filesystems.
func detectRootDevice(ctx context.Context, rootfsPath string) (string, error) {
	out, err := guestfishRun(ctx, rootfsPath, true, "", "list-filesystems")
	if err != nil {
		return "", errs.New(
			errs.CodeGuestfsError,
			fmt.Sprintf("Failed to list filesystems for root device detection: %v", err),
		)
	}

	type fsEntry struct {
		device string
		fstype string
	}
	var entries []fsEntry
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) == 2 {
			entries = append(entries, fsEntry{device: parts[0], fstype: parts[1]})
		}
	}

	deviceSet := make(map[string]string)
	for _, e := range entries {
		deviceSet[e.device] = e.fstype
	}

	candidates := []string{"/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"}
	for _, cand := range candidates {
		if _, ok := deviceSet[cand]; ok {
			return cand, nil
		}
	}

	for _, e := range entries {
		return e.device, nil
	}

	return "", errs.New(errs.CodeGuestfsError, fmt.Sprintf("No filesystem found in %s", rootfsPath))
}

// ConvertTo converts a disk image's filesystem to targetFs using guestfish
// in a dual-drive setup. Writes a temp script, runs guestfish once (plus one
// list-filesystems pre-read for root device detection).
func ConvertTo(ctx context.Context, rootfsPath string, targetFs string) error {
	ext := "." + targetFs
	if ext == "." {
		ext = ".img"
	}
	outputPath := rootfsPath + ext

	info, err := os.Stat(rootfsPath)
	if err != nil {
		return fmt.Errorf("stat rootfs: %w", err)
	}
	dataSize := info.Size()
	sizeBytes := dataSize + int64(infra.RootfsMinHeadroomBytes)
	mebi := int64(disk.MebibyteBytes)
	sizeBytes = ((sizeBytes + mebi - 1) / mebi) * mebi
	sizeMiB := sizeBytes / mebi

	result, err := system.DefaultRunner.Run(ctx,
		[]string{"truncate", "-s", fmt.Sprintf("%dM", sizeMiB), outputPath},
		system.RunCmdOpts{Capture: true, Check: true},
	)
	if err != nil {
		return fmt.Errorf("truncate output: %s: %w", result.Stdout, err)
	}

	rootDev, err := detectRootDevice(ctx, rootfsPath)
	if err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("detect root device: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "mvm-guestfs-convert-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	scriptPath := filepath.Join(tmpDir, "convert.gf")
	scriptContent := fmt.Sprintf(
		"add-drive-opts %s format:raw readonly:true\n"+
			"add-drive-opts %s format:raw readonly:false\n"+
			"run\n"+
			"mount %s /\n"+
			"mkdir-p /ext4\n"+
			"mkfs %s /dev/sdb\n"+
			"mount /dev/sdb /ext4\n"+
			`sh "tar cf - --one-file-system / | tar xf - -C /ext4"`+"\n"+
			"umount /ext4\n"+
			"umount /\n"+
			"shutdown\n",
		rootfsPath, outputPath, rootDev, targetFs,
	)
	if err := os.WriteFile(scriptPath, []byte(scriptContent), infra.PrivateKeyPerm); err != nil {
		return fmt.Errorf("write guestfish script: %w", err)
	}

	// initEnv already called by detectRootDevice's guestfishRun (sync.Once)
	result2, err := system.DefaultRunner.Run(ctx, []string{"guestfish",
		"--no-sync", "-f", scriptPath,
	}, system.RunCmdOpts{Capture: true, Check: true})
	if err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("guestfish convert failed: %s: %s: %w",
			result2.Stderr, strings.TrimSpace(result2.Stdout), err)
	}

	if err := os.Rename(outputPath, rootfsPath); err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("rename converted image: %w", err)
	}

	slog.Info("Converted filesystem",
		"image", filepath.Base(rootfsPath),
		"target_fs", targetFs,
	)
	return nil
}
