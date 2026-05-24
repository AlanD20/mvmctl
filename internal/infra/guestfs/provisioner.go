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
	"mvmctl/internal/infra/provisionercontent"
	"mvmctl/internal/infra/system"
)

// writeGuestfishFile writes content to a temp file and returns guestfish
// commands to upload it to the given guest path. This avoids content
// corruption from sending multi-line content via stdin write commands.
func (p *GuestfsProvisioner) writeGuestfishFile(guestPath string, data []byte) ([]string, error) {
	if p.tmpDir == "" {
		return nil, fmt.Errorf("tmpDir not set — Run() must be called first")
	}
	relPath := strings.ReplaceAll(guestPath, "/", "_")
	tmpFile := filepath.Join(p.tmpDir, relPath)
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return nil, fmt.Errorf("write temp file for %s: %w", guestPath, err)
	}
	return []string{
		fmt.Sprintf("upload %s %s", tmpFile, guestPath),
	}, nil
}

// ── GuestfsProvisioner ──────────────────────────────────────────────────────
//
// Mirrors src/mvmctl/core/_shared/_guestfs/_provtypes.py GuestfsProvisioner.
// All guestfs setup operations. Stateful — holds guestfs handle configuration.
//
// APPROACH: Since Go has no native libguestfs bindings, this implementation
// uses the guestfish CLI tool as a subprocess. Each method generates guestfish
// commands. The Run() method batches all operations into a single guestfish
// session for efficiency.

// GuestfsProvisioner provides all guestfs setup operations.
// Implements the builder pattern: methods queue operations, Run() executes
// them in a single guestfish session.
type GuestfsProvisioner struct {
	rootfsPath string
	readonly   bool
	rootUID    int
	rootGID    int
	userUID    int
	userGID    int

	// Builder state
	targetSize   int64
	hostname     string
	user         string
	sshPubkeys   []string
	cloudInitDir string
	dnsServer    string
	shrinkResult int64

	// Pre-read cache (populated by preReadSetupSSHFiles)
	preReadPasswd   string
	preReadShadow   string
	preReadGroup    string
	preReadAuthKeys string

	// Temp dir for file uploads (set during Run, cleaned up after)
	tmpDir string

	// Operation queue (ordered list of op names)
	ops []string
}

// NewProvisioner creates a new GuestfsProvisioner.
func NewProvisioner(rootfsPath string, opts ...ProvisionerOption) *GuestfsProvisioner {
	p := &GuestfsProvisioner{
		rootfsPath: rootfsPath,
		rootUID:    0,
		rootGID:    0,
		userUID:    1000,
		userGID:    1000,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ProvisionerOption is an option for NewProvisioner.
type ProvisionerOption func(*GuestfsProvisioner)

// WithReadonly sets the readonly flag.
func WithReadonly(readonly bool) ProvisionerOption {
	return func(p *GuestfsProvisioner) { p.readonly = readonly }
}

// WithRootUID sets the root UID.
func WithRootUID(uid int) ProvisionerOption {
	return func(p *GuestfsProvisioner) { p.rootUID = uid }
}

// WithRootGID sets the root GID.
func WithRootGID(gid int) ProvisionerOption {
	return func(p *GuestfsProvisioner) { p.rootGID = gid }
}

// WithUserUID sets the default user UID.
func WithUserUID(uid int) ProvisionerOption {
	return func(p *GuestfsProvisioner) { p.userUID = uid }
}

// WithUserGID sets the default user GID.
func WithUserGID(gid int) ProvisionerOption {
	return func(p *GuestfsProvisioner) { p.userGID = gid }
}

// ═════════════════════════════════════════════════════════════════════════════
// Builder methods — queue operations for a single guestfs session
// ═════════════════════════════════════════════════════════════════════════════

// Resize queues a resize operation.
func (p *GuestfsProvisioner) Resize(targetSizeBytes int64) *GuestfsProvisioner {
	p.targetSize = targetSizeBytes
	return p
}

// SetHostname queues hostname setup.
func (p *GuestfsProvisioner) SetHostname(hostname string) *GuestfsProvisioner {
	p.hostname = hostname
	p.ops = append(p.ops, "set_hostname")
	return p
}

// InjectDNS queues DNS injection.
func (p *GuestfsProvisioner) InjectDNS(dnsServer string) *GuestfsProvisioner {
	p.dnsServer = dnsServer
	p.ops = append(p.ops, "inject_dns")
	return p
}

// SetupSSH queues SSH setup.
func (p *GuestfsProvisioner) SetupSSH(user string, sshPubkeys []string) *GuestfsProvisioner {
	p.user = user
	p.sshPubkeys = sshPubkeys
	p.ops = append(p.ops, "setup_ssh")
	return p
}

// InjectCloudInit queues cloud-init seed file injection.
func (p *GuestfsProvisioner) InjectCloudInit(cloudInitDir string) *GuestfsProvisioner {
	p.cloudInitDir = cloudInitDir
	p.ops = append(p.ops, "inject_cloud_init")
	return p
}

// DisableCloudInit queues cloud-init disable.
func (p *GuestfsProvisioner) DisableCloudInit() *GuestfsProvisioner {
	p.ops = append(p.ops, "disable_cloud_init")
	return p
}

// Shrink queues shrink-to-minimum operation.
func (p *GuestfsProvisioner) Shrink() *GuestfsProvisioner {
	p.ops = append(p.ops, "shrink")
	return p
}

// Deblob queues deblob (OS cache cleanup + fstab fix) operation.
func (p *GuestfsProvisioner) Deblob() *GuestfsProvisioner {
	p.ops = append(p.ops, "deblob")
	return p
}

// FixFstab queues fstab fix for Firecracker (PARTUUID → /dev/vda).
func (p *GuestfsProvisioner) FixFstab() *GuestfsProvisioner {
	p.ops = append(p.ops, "fix_fstab")
	return p
}

// ═════════════════════════════════════════════════════════════════════════════
// Filesystem conversion (independent — not an _op)
// ═════════════════════════════════════════════════════════════════════════════

// ConvertTo converts the image filesystem to targetFs using guestfish.
// Opens a fresh guestfish session with both drives, copies all files
// via tar --one-file-system, then replaces the original.
func (p *GuestfsProvisioner) ConvertTo(ctx context.Context, targetFs string) error {
	outputPath := p.rootfsPath + ".ext4"

	info, err := os.Stat(p.rootfsPath)
	if err != nil {
		return fmt.Errorf("stat rootfs: %w", err)
	}
	dataSize := info.Size()
	sizeBytes := dataSize + int64(infra.RootfsMinHeadroomBytes)
	mebi := int64(infra.MebibyteBytes)
	sizeBytes = ((sizeBytes + mebi - 1) / mebi) * mebi
	sizeMiB := sizeBytes / mebi

	// Create sparse output file
	result := system.RunCmdCompat(ctx, []string{"truncate", "-s", fmt.Sprintf("%dM", sizeMiB), outputPath}, system.RunCmdOptions{Capture: true, Check: true})
	if result.Err != nil {
		return fmt.Errorf("truncate output: %s: %w", result.Stdout, result.Err)
	}

	// Build a guestfish script for dual-drive operation
	var scriptLines []string
	scriptLines = append(scriptLines,
		fmt.Sprintf("add-drive-opts %s format:raw readonly:true", p.rootfsPath),
		fmt.Sprintf("add-drive-opts %s format:raw readonly:false", outputPath),
		"run",
	)
	// Try mounting source, then create and populate target
	scriptLines = append(scriptLines,
		"mount /dev/sda /",
		"mkdir-p /ext4",
		fmt.Sprintf("mkfs %s /dev/sdb", targetFs),
		"mount /dev/sdb /ext4",
		`sh "tar cf - --one-file-system / | tar xf - -C /ext4"`,
		"umount /ext4",
		"umount /",
		"shutdown",
	)

	// Write script to temp file
	tmpScript := filepath.Join(os.TempDir(), "mvm-guestfs-convert-"+filepath.Base(p.rootfsPath)+".gf")
	scriptContent := strings.Join(scriptLines, "\n")
	if err := os.WriteFile(tmpScript, []byte(scriptContent), 0600); err != nil {
		return fmt.Errorf("write guestfish script: %w", err)
	}
	defer os.Remove(tmpScript)

	// Save and set environment (matching Python's OptimizedGuestfs._setup_environment).
	// Python saves: LIBGUESTFS_BACKEND, LIBGUESTFS_CACHEDIR, QEMU_LOCKING,
	// SUPERMIN_KERNEL, SUPERMIN_MODULES.
	type envSnap struct{ key string; val *string }
	var snapshots []envSnap
	for _, key := range []string{"LIBGUESTFS_BACKEND", "LIBGUESTFS_CACHEDIR", "QEMU_LOCKING", "SUPERMIN_KERNEL", "SUPERMIN_MODULES"} {
		val, ok := os.LookupEnv(key)
		if ok {
			snapshots = append(snapshots, envSnap{key: key, val: &val})
		} else {
			snapshots = append(snapshots, envSnap{key: key, val: nil})
		}
	}
	os.Setenv("LIBGUESTFS_BACKEND", "direct")
	if _, err := os.Stat("/dev/shm"); err == nil {
		os.Setenv("LIBGUESTFS_CACHEDIR", "/dev/shm")
	}
	os.Setenv("QEMU_LOCKING", "off")
	kd := &KernelDetector{}
	if kernelPath, modulesDir, kerr := kd.FindBestKernel(ctx); kerr == nil && kernelPath != "" {
		os.Setenv("SUPERMIN_KERNEL", kernelPath)
		os.Setenv("SUPERMIN_MODULES", modulesDir)
	}
	defer func() {
		for _, snap := range snapshots {
			if snap.val != nil {
				os.Setenv(snap.key, *snap.val)
			} else {
				os.Unsetenv(snap.key)
			}
		}
	}()

	// Run guestfish with the script and common flags matching Python's:
	// set_recovery_proc(False)  → --no-recovery-proc
	// set_autosync(False)       → --no-autosync
	// set_network(False)        → --no-network
	// set_smp(1)                → --smp 1
	// set_memsize(256)          → --memsize 256
	// set_backend("direct")     → --backend direct
	result2 := system.RunCmdCompat(ctx, []string{"guestfish",
		"--no-recovery-proc",
		"--no-autosync",
		"--no-network",
		"--smp", "1",
		"--memsize", "256",
		"--backend", "direct",
		"-f", tmpScript,
	}, system.RunCmdOptions{Capture: true, Check: true})
	if result2.Err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("guestfish convert failed: %s: %s: %w",
			result2.Stderr, strings.TrimSpace(result2.Stdout), result2.Err)
	}

	// Replace original with the new file
	if err := os.Rename(outputPath, p.rootfsPath); err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("rename converted image: %w", err)
	}

	slog.Info("Converted filesystem",
		"image", filepath.Base(p.rootfsPath),
		"target_fs", targetFs,
	)
	return nil
}

// ═════════════════════════════════════════════════════════════════════════════
// Execution — single guestfs session for all queued operations
// ═════════════════════════════════════════════════════════════════════════════

// Run executes all queued operations in a single guestfish session.
// Matches Python's GuestfsProvisioner.run().
func (p *GuestfsProvisioner) Run(ctx context.Context) error {
	needsResize := p.targetSize > 0

	if needsResize {
		info, err := os.Stat(p.rootfsPath)
		if err == nil && info.Size() >= p.targetSize {
			needsResize = false
		}
	}

	if len(p.ops) == 0 && !needsResize {
		return nil
	}

	// Phase 0: file truncation (before guestfish mount) — only when resizing
	if needsResize {
		p.doTruncateFile(p.rootfsPath, p.targetSize)
	}

	// Create temp dir for file uploads (cleaned up after guestfish session)
	tmpDir, err := os.MkdirTemp("", "mvm-guestfs-*")
	if err != nil {
		return fmt.Errorf("create guestfs temp dir: %w", err)
	}
	p.tmpDir = tmpDir
	defer os.RemoveAll(tmpDir)

	// Detect root device for mount (matches Python's og.mount_rootfs())
	rootDevice, err := p.detectRootDevice(ctx)
	if err != nil {
		return fmt.Errorf("guestfs: root device detection failed: %w", err)
	}

	// Build all guestfish commands as a batch script for Phase 1
	var commands []string

	// CRITICAL: Add run and mount commands (matching Python's og.launch() + og.mount_rootfs())
	// Without these, guestfish won't have the filesystem available for operations.
	commands = append(commands, "# Launch appliance and mount root filesystem")
	commands = append(commands, "run")
	commands = append(commands, fmt.Sprintf("mount %s /", rootDevice))

	// Phase 1a: filesystem resize
	if needsResize {
		fsCmds, err := p.buildFilesystemResize(ctx)
		if err != nil {
			return fmt.Errorf("guestfs: build filesystem resize: %w", err)
		}
		commands = append(commands, "# Filesystem resize (grow)")
		commands = append(commands, fsCmds...)
	}

	// Phase 1b: queued operations
	for _, opName := range p.ops {
		opCmds, err := p.buildOp(ctx, opName)
		if err != nil {
			return fmt.Errorf("guestfs: build operation %s: %w", opName, err)
		}
		commands = append(commands, opCmds...)
	}

	if len(commands) == 0 {
		return nil
	}

	commands = append(commands, "sync")
	commands = append(commands, "# END")

	// Setup environment with proper save/restore (matching Python's
	// OptimizedGuestfs._setup_environment / _restore_environment).
	type envSnap struct{ key string; val *string }
	var snapshots []envSnap
	for _, key := range []string{"LIBGUESTFS_BACKEND", "LIBGUESTFS_CACHEDIR", "QEMU_LOCKING", "SUPERMIN_KERNEL", "SUPERMIN_MODULES"} {
		val, ok := os.LookupEnv(key)
		if ok {
			snapshots = append(snapshots, envSnap{key: key, val: &val})
		} else {
			snapshots = append(snapshots, envSnap{key: key, val: nil})
		}
	}
	os.Setenv("LIBGUESTFS_BACKEND", "direct")
	if _, err := os.Stat("/dev/shm"); err == nil {
		os.Setenv("LIBGUESTFS_CACHEDIR", "/dev/shm")
	}
	os.Setenv("QEMU_LOCKING", "off")
	kd := &KernelDetector{}
	if kp, md, err := kd.FindBestKernel(ctx); err == nil && kp != "" {
		os.Setenv("SUPERMIN_KERNEL", kp)
		os.Setenv("SUPERMIN_MODULES", md)
	}
	defer func() {
		for _, snap := range snapshots {
			if snap.val != nil {
				os.Setenv(snap.key, *snap.val)
			} else {
				os.Unsetenv(snap.key)
			}
		}
	}()

	// Write commands to temp script file (avoids stdin quoting issues)
	scriptPath := filepath.Join(tmpDir, "guestfish-script.gf")
	input := strings.Join(commands, "\n")
	if err := os.WriteFile(scriptPath, []byte(input), 0600); err != nil {
		return fmt.Errorf("write guestfish script: %w", err)
	}

	// Build args with guestfish common flags matching Python's:
	// set_recovery_proc(False)  → --no-recovery-proc
	// set_autosync(False)       → --no-autosync
	// set_network(False)        → --no-network
	// set_smp(1)                → --smp 1
	// set_memsize(256)          → --memsize 256
	// set_backend("direct")     → --backend direct
	allArgs := []string{
		"-a", p.rootfsPath,
		"--cachemode", "writeback",
		"--no-recovery-proc",
		"--no-autosync",
		"--no-network",
		"--smp", "1",
		"--memsize", "256",
		"--backend", "direct",
	}
	if p.readonly {
		allArgs = append(allArgs, "--ro")
	}
	allArgs = append(allArgs, "-f", scriptPath)

	result3 := system.RunCmdCompat(ctx, append([]string{"guestfish"}, allArgs...), system.RunCmdOptions{Capture: true, Check: true})

	slog.Debug("Running guestfish provisioner batch",
		"image", filepath.Base(p.rootfsPath),
		"ops", len(p.ops),
	)
	if result3.Err != nil {
		return fmt.Errorf("guestfish provisioner session failed: %s: %w",
			result3.Stderr, result3.Err)
	}

	// Phase 2: post-session shrink result capture + truncation
	hasShrink := false
	for _, opName := range p.ops {
		if opName == "shrink" {
			hasShrink = true
			break
		}
	}
	if hasShrink && p.shrinkResult <= 0 {
		rootDevice, err := p.detectRootDevice(ctx)
		if err == nil {
			result, err := guestfishRaw(ctx, p.rootfsPath, true, "blockdev-getsize64", rootDevice)
			if err == nil {
				if val, parseErr := strconv.ParseInt(strings.TrimSpace(result), 10, 64); parseErr == nil {
					p.shrinkResult = val
				}
			}
		}
	}

	if p.shrinkResult > 0 {
		finalSize := int64(float64(p.shrinkResult) * infra.ShrinkSafetyMargin)
		if err := os.Truncate(p.rootfsPath, finalSize); err != nil {
			slog.Warn("Failed to truncate after shrink", "error", err)
		}
	}

	return nil
}

// ── Private helpers ─────────────────────────────────────────────────────────

func (p *GuestfsProvisioner) doTruncateFile(path string, targetSize int64) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Size() < targetSize {
		if err := os.Truncate(path, targetSize); err != nil {
			slog.Warn("Failed to truncate file", "path", path, "error", err)
		}
	}
}

// detectRootDevice detects the root device by running guestfish list-filesystems
// and iterating through known candidates (matching Python's iteration over
// ["/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"]).
func (p *GuestfsProvisioner) detectRootDevice(ctx context.Context) (string, error) {
	out, err := guestfishRaw(ctx, p.rootfsPath, true, "list-filesystems")
	if err != nil {
		return "", &GuestfsError{msg: fmt.Sprintf("Failed to list filesystems for root device detection: %v", err)}
	}

	// Build a set of filesystem devices from output
	type fsEntry struct {
		device string
		fstype string
	}
	var entries []fsEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) == 2 {
			entries = append(entries, fsEntry{device: parts[0], fstype: parts[1]})
		}
	}

	// Build a set of device names for fast lookup
	deviceSet := make(map[string]string)
	for _, e := range entries {
		deviceSet[e.device] = e.fstype
	}

	// Try known candidates in order (Python: ["/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"])
	candidates := []string{"/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"}
	for _, cand := range candidates {
		if _, ok := deviceSet[cand]; ok {
			return cand, nil
		}
	}

	// Fallback: first filesystem key
	for _, e := range entries {
		return e.device, nil
	}

	return "", &GuestfsError{msg: fmt.Sprintf("No filesystem found in %s", p.rootfsPath)}
}

func (p *GuestfsProvisioner) buildFilesystemResize(ctx context.Context) ([]string, error) {
	rootDevice, err := p.detectRootDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("filesystem resize: %w", err)
	}

	// Detect filesystem type (Python: fs_type = handle.vfs_type(root_device))
	fsTypeOut, fsErr := guestfishInspect(ctx, p.rootfsPath, true,
		"vfs-type", rootDevice)
	fsType := strings.TrimSpace(fsTypeOut)
	if fsErr != nil || fsType == "" {
		return nil, fmt.Errorf("filesystem resize: unable to detect fs type for %s", rootDevice)
	}

	switch fsType {
	case "ext2", "ext3", "ext4":
		return []string{
			"# Growing filesystem to fill truncated disk",
			fmt.Sprintf("resize2fs %s", rootDevice),
		}, nil
	case "btrfs":
		// Python: mount(root_device, "/"), btrfs_filesystem_resize("/", target_size), umount(root_device)
		// Note: no ":" separators needed since commands are newline-separated in script.
		return []string{
			"# Growing btrfs filesystem",
			fmt.Sprintf("mount %s /", rootDevice),
			fmt.Sprintf("btrfs-filesystem-resize / %d", p.targetSize),
			fmt.Sprintf("umount %s", rootDevice),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported filesystem type for resize: %s", fsType)
	}
}

func (p *GuestfsProvisioner) buildOp(ctx context.Context, opName string) ([]string, error) {
	switch opName {
	case "set_hostname":
		return p.buildSetHostname(ctx), nil
	case "inject_dns":
		return p.buildInjectDNS(), nil
	case "setup_ssh":
		return p.buildSetupSSH(ctx), nil
	case "inject_cloud_init":
		return p.buildInjectCloudInit()
	case "disable_cloud_init":
		return buildDisableCloudInit(), nil
	case "shrink":
		return p.buildShrink(ctx), nil
	case "deblob":
		return p.buildDeblob(ctx), nil
	case "fix_fstab":
		return p.buildFixFstab(), nil
	default:
		slog.Warn("Unknown guestfs provisioner op", "op", opName)
		return nil, nil
	}
}

// ── set_hostname ────────────────────────────────────────────────────────────

func (p *GuestfsProvisioner) buildSetHostname(ctx context.Context) []string {
	if p.hostname == "" {
		return nil
	}
	hostname := p.hostname

	// Read existing /etc/hosts (Python reads via handle.exists/handle.read_file)
	existingHosts := ""
	out, err := guestfishInspect(ctx, p.rootfsPath, true,
		"read-file", "/etc/hosts")
	if err == nil {
		existingHosts = out
	}

	// Process: find/update 127.0.1.1 line matching <hostname>.localdomain <hostname>,
	// preserving all other entries (matching Python's _do_set_hostname).
	lines := strings.Split(existingHosts, "\n")
	var newLines []string
	foundHostEntry := false
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			newLines = append(newLines, line)
		} else if strings.HasPrefix(stripped, "127.0.1.1") {
			newLines = append(newLines, fmt.Sprintf("127.0.1.1\t%s", hostname))
			foundHostEntry = true
		} else {
			newLines = append(newLines, line)
		}
	}

	if !foundHostEntry {
		newLines = append(newLines, fmt.Sprintf("127.0.1.1\t%s", hostname))
	}

	newHostsContent := strings.Join(newLines, "\n") + "\n"

	return []string{
		"# Set hostname",
		fmt.Sprintf("write /etc/hostname %q", hostname),
		fmt.Sprintf("write /etc/hosts %q", newHostsContent),
		"sync",
	}
}

// ── inject_dns ──────────────────────────────────────────────────────────────

func (p *GuestfsProvisioner) buildInjectDNS() []string {
	if p.dnsServer == "" {
		return nil
	}
	// Pre-read existing resolv.conf — if it has a nameserver entry, skip entirely
	out, err := guestfishInspect(context.Background(), p.rootfsPath, true,
		"read-file", "/etc/resolv.conf")
	if err == nil {
		existing := strings.TrimSpace(out)
		if existing != "" && strings.Contains(strings.ToLower(existing), "nameserver") {
			slog.Debug("Skipping DNS injection — /etc/resolv.conf already has nameserver entry",
				"image", filepath.Base(p.rootfsPath))
			return nil
		}
	}

	dnsContent := fmt.Sprintf("nameserver %s\n", p.dnsServer)

	// Python: try write; on RuntimeError (e.g. dangling symlink) → rm then write
	// In guestfish batch mode, detect if file is a dangling symlink preemptively:
	//   - If read-file failed AND the path exists → dangling symlink → rm+write
	//   - Otherwise → just write (file doesn't exist, or is a real file)
	isDangling := false
	if err != nil {
		out2, err2 := guestfishRaw(context.Background(), p.rootfsPath, true,
			"exists", "/etc/resolv.conf")
		if err2 == nil && strings.TrimSpace(out2) == "true" {
			isDangling = true
		}
	}

	if isDangling {
		return []string{
			"# Inject DNS (dangling symlink workaround)",
			"rm /etc/resolv.conf",
			fmt.Sprintf("write /etc/resolv.conf %q", dnsContent),
		}
	}
	return []string{
		"# Inject DNS",
		fmt.Sprintf("write /etc/resolv.conf %q", dnsContent),
	}
}

// ── setup_ssh ───────────────────────────────────────────────────────────────

func (p *GuestfsProvisioner) preReadSetupSSHFiles(ctx context.Context) {
	user := p.user
	if user == "" {
		user = "root"
	}
	sshHomeDir := "/root"
	if user != "root" {
		sshHomeDir = "/home/" + user
	}
	authKeysPath := fmt.Sprintf("%s/.ssh/authorized_keys", sshHomeDir)

	raw, err := guestfishRaw(ctx, p.rootfsPath, true,
		"read-file", "/etc/passwd",
		":", "echo", "---PASSWD_END---",
		":", "read-file", "/etc/shadow",
		":", "echo", "---SHADOW_END---",
		":", "read-file", "/etc/group",
		":", "echo", "---GROUP_END---",
		":", "read-file", authKeysPath,
		":", "echo", "---AUTHKEYS_END---",
	)
	if err != nil {
		return
	}

	parts := strings.Split(raw, "---PASSWD_END---\n")
	if len(parts) >= 2 {
		p.preReadPasswd = strings.TrimSpace(parts[0])
		rest := parts[1]
		shadowParts := strings.SplitN(rest, "---SHADOW_END---\n", 2)
		if len(shadowParts) >= 2 {
			p.preReadShadow = strings.TrimSpace(shadowParts[0])
			rest2 := shadowParts[1]
			groupParts := strings.SplitN(rest2, "---GROUP_END---\n", 2)
			if len(groupParts) >= 2 {
				p.preReadGroup = strings.TrimSpace(groupParts[0])
				rest3 := groupParts[1]
				authParts := strings.SplitN(rest3, "---AUTHKEYS_END---\n", 2)
				if len(authParts) >= 2 {
					p.preReadAuthKeys = strings.TrimSpace(authParts[0])
				}
			}
		}
	}
}

func (p *GuestfsProvisioner) buildSetupSSH(ctx context.Context) []string {
	if len(p.sshPubkeys) == 0 {
		slog.Debug("Skipping SSH setup — no SSH pubkeys provided",
			"image", filepath.Base(p.rootfsPath),
		)
		return nil
	}

	var cmds []string
	cmds = append(cmds, "# Setup SSH")

	// Pre-read all SSH-related files into the cache (single consolidated location)
	p.preReadSetupSSHFiles(ctx)

	user := p.user
	if user == "" {
		user = "root"
	}
	sshHomeDir := "/root"
	if user != "root" {
		sshHomeDir = "/home/" + user
	}

	// Python: self.ensure_user(handle) — user creation, group, home dir, sudoers
	// Pass pre-read values from cache (matches Python's handle.read_file through mounted guest)
	ensureCmds := p.buildEnsureUser(ctx, p.preReadPasswd, p.preReadShadow, p.preReadGroup)
	cmds = append(cmds, ensureCmds...)

	// Ensure directories
	cmds = append(cmds,
		"mkdir-p /root",
		fmt.Sprintf("chmod %o /root", infra.CacheDirPerm),
		fmt.Sprintf("chown %d %d /root", p.rootUID, p.rootGID),
		fmt.Sprintf("mkdir-p %s/.ssh", sshHomeDir),
		fmt.Sprintf("chmod %o %s/.ssh", infra.CacheDirPerm, sshHomeDir),
		fmt.Sprintf("chown %d %d %s/.ssh", p.rootUID, p.rootGID, sshHomeDir),
	)

	// Merge SSH keys with existing authorized_keys (Python reads existing, deduplicates,
	// then writes combined content — matches _provtypes.py lines 514-538).
	authKeysPath := fmt.Sprintf("%s/.ssh/authorized_keys", sshHomeDir)

	// Use pre-read authorized_keys from cache (no separate subprocess call)
	existingKeys := p.preReadAuthKeys

	// Build set of existing keys for deduplication
	existingSet := make(map[string]bool)
	if existingKeys != "" {
		for _, k := range strings.Split(strings.TrimSpace(existingKeys), "\n") {
			k = strings.TrimSpace(k)
			if k != "" {
				existingSet[k] = true
			}
		}
	}

	// Filter out duplicates from new pubkeys
	var newKeys []string
	for _, k := range p.sshPubkeys {
		k = strings.TrimSpace(k)
		if k != "" && !existingSet[k] {
			newKeys = append(newKeys, k)
		}
	}

	// Only write if there are new keys to add (matches Python: if new_keys:)
	if len(newKeys) > 0 {
		combined := existingKeys
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += strings.Join(newKeys, "\n") + "\n"

		cmds = append(cmds,
			fmt.Sprintf("write %s %q", authKeysPath, combined),
			fmt.Sprintf("chmod %o %s", infra.PrivateKeyPerm, authKeysPath),
		)
	}

	// Enable SSH (systemd + OpenRC paths) — includes configure_ssh_keys (sshd_config)
	cmds = append(cmds, p.buildEnableSSH()...)

	// Python: self.generate_host_keys(handle) — host key regeneration on first boot
	hostKeyCmds := p.buildGenerateHostKeys(ctx)
	cmds = append(cmds, hostKeyCmds...)

	// Write first-boot installer
	cmds = append(cmds,
		"mkdir-p /usr/local/bin",
		fmt.Sprintf("write /usr/local/bin/first-boot-ssh-installer.sh %q", provisionerContentFirstBootInstaller()),
		fmt.Sprintf("chmod %o /usr/local/bin/first-boot-ssh-installer.sh", infra.ExecutablePerm),
		"mkdir-p /etc/systemd/system",
		fmt.Sprintf("write /etc/systemd/system/first-boot-ssh-installer.service %q", provisionerContentFirstBootService()),
		fmt.Sprintf("chmod %o /etc/systemd/system/first-boot-ssh-installer.service", infra.PublicKeyPerm),
		"mkdir-p /etc/systemd/system/multi-user.target.wants",
		"ln-s /etc/systemd/system/first-boot-ssh-installer.service /etc/systemd/system/multi-user.target.wants/first-boot-ssh-installer.service",
	)

	return cmds
}

// buildEnsureUser returns guestfish commands to create a user, group, home dir,
// and sudoers in the guest. Corresponds to Python GuestfsProvisioner.ensure_user().
// Accepts pre-read file contents as parameters (avoids separate subprocess calls).
func (p *GuestfsProvisioner) buildEnsureUser(ctx context.Context, passwdContent, shadowContent, groupContent string) []string {
	if p.user == "" || p.user == "root" {
		return nil
	}

	// Check if user already exists using pre-read content (Python: handle.exists + read_file)
	if passwdContent != "" {
		for _, line := range strings.Split(passwdContent, "\n") {
			if strings.HasPrefix(line, p.user+":") {
				slog.Debug("User already exists, skipping ensure_user",
					"user", p.user,
					"image", filepath.Base(p.rootfsPath),
				)
				return nil
			}
		}
	}

	homeDir := "/home/" + p.user

	// Build passwd content with appended entry
	combinedPasswd := passwdContent
	if combinedPasswd != "" && !strings.HasSuffix(combinedPasswd, "\n") {
		combinedPasswd += "\n"
	}
	combinedPasswd += fmt.Sprintf("%s:!:%d:%d::%s:/bin/bash\n", p.user, p.userUID, p.userGID, homeDir)

	combinedShadow := shadowContent
	if combinedShadow != "" && !strings.HasSuffix(combinedShadow, "\n") {
		combinedShadow += "\n"
	}
	combinedShadow += fmt.Sprintf("%s:!:%d:%d:%d:%d:::\n",
		p.user, infra.ShadowDaysSinceEpoch,
		infra.ShadowMinDays, infra.ShadowMaxDays, infra.ShadowWarnDays)

	combinedGroup := groupContent
	if combinedGroup != "" && !strings.HasSuffix(combinedGroup, "\n") {
		combinedGroup += "\n"
	}
	combinedGroup += fmt.Sprintf("%s:x:%d:\n", p.user, p.userGID)

	return []string{
		"# Ensure user: " + p.user,
		fmt.Sprintf("mkdir-p %s", homeDir),
		fmt.Sprintf("mkdir-p %s/.ssh", homeDir),
		fmt.Sprintf("write /etc/passwd %q", combinedPasswd),
		fmt.Sprintf("chmod %o /etc/passwd", infra.PublicKeyPerm),
		fmt.Sprintf("write /etc/shadow %q", combinedShadow),
		fmt.Sprintf("chmod %o /etc/shadow", infra.ShadowPerm),
		fmt.Sprintf("write /etc/group %q", combinedGroup),
		fmt.Sprintf("chmod %o /etc/group", infra.PublicKeyPerm),
		"mkdir-p /etc/sudoers.d",
		fmt.Sprintf("write /etc/sudoers.d/%s %q", p.user, fmt.Sprintf("%s ALL=(ALL) NOPASSWD: ALL\n", p.user)),
		fmt.Sprintf("chmod %o /etc/sudoers.d/%s", infra.SudoersPerm, p.user),
		fmt.Sprintf("chown %d %d %s", p.userUID, p.userGID, homeDir),
		fmt.Sprintf("chown %d %d %s/.ssh", p.userUID, p.userGID, homeDir),
	}
}

func (p *GuestfsProvisioner) buildEnableSSH() []string {
	return []string{
		"mkdir-p /etc/systemd/system/multi-user.target.wants",
		`sh "for f in /usr/lib/systemd/system/ssh.service /lib/systemd/system/ssh.service /etc/systemd/system/ssh.service; do if [ -f \"$f\" ]; then ln -sf \"$f\" /etc/systemd/system/multi-user.target.wants/ 2>/dev/null || true; break; fi; done"`,
		`sh "for f in /usr/lib/systemd/system/sshd.service /lib/systemd/system/sshd.service /etc/systemd/system/sshd.service; do if [ -f \"$f\" ]; then ln -sf \"$f\" /etc/systemd/system/multi-user.target.wants/ 2>/dev/null || true; break; fi; done"`,
		"mkdir-p /etc/runlevels/default",
		`sh "if [ -f /etc/init.d/sshd ]; then ln -sf /etc/init.d/sshd /etc/runlevels/default/sshd 2>/dev/null || true; fi"`,
		`sh "if [ -f /etc/init.d/ssh ]; then ln -sf /etc/init.d/ssh /etc/runlevels/default/ssh 2>/dev/null || true; fi"`,
		// sysvinit: create rc.d symlinks for runlevels 2-5 (Python: _guestfs/_provisioner.py:778-788)
		"mkdir-p /etc/rc2.d /etc/rc3.d /etc/rc4.d /etc/rc5.d",
		`sh "if [ -f /etc/init.d/ssh ] && [ ! -e /etc/rc2.d/S02ssh ]; then ln -sf ../init.d/ssh /etc/rc2.d/S02ssh 2>/dev/null || true; fi"`,
		`sh "if [ -f /etc/init.d/ssh ] && [ ! -e /etc/rc3.d/S02ssh ]; then ln -sf ../init.d/ssh /etc/rc3.d/S02ssh 2>/dev/null || true; fi"`,
		`sh "if [ -f /etc/init.d/ssh ] && [ ! -e /etc/rc4.d/S02ssh ]; then ln -sf ../init.d/ssh /etc/rc4.d/S02ssh 2>/dev/null || true; fi"`,
		`sh "if [ -f /etc/init.d/ssh ] && [ ! -e /etc/rc5.d/S02ssh ]; then ln -sf ../init.d/ssh /etc/rc5.d/S02ssh 2>/dev/null || true; fi"`,
		"mkdir-p /etc/ssh/sshd_config.d",
		fmt.Sprintf("write /etc/ssh/sshd_config.d/mvm.conf %q", provisionerContentSshdConfig(p.user)),
		fmt.Sprintf("chmod %o /etc/ssh/sshd_config.d/mvm.conf", infra.PublicKeyPerm),
	}
}

// buildGenerateHostKeys returns guestfish commands to set up SSH host key
// generation on first boot. Corresponds to Python's generate_host_keys().
// Creates /etc/local.d/ssh-keygen.start, ssh-hostkeygen.service, and
// OpenRC support.
func (p *GuestfsProvisioner) buildGenerateHostKeys(ctx context.Context) []string {
	var cmds []string
	cmds = append(cmds, "# Generate SSH host keys (first-boot)")

	keyTypes := []string{
		"ssh_host_rsa_key",
		"ssh_host_ecdsa_key",
		"ssh_host_ed25519_key",
	}

	// Pre-check which keys exist (Python: checks handle.exists for each)
	allExist := true
	for _, key := range keyTypes {
		res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", p.rootfsPath, "--ro", "-i",
			"exists", "/etc/ssh/" + key}, system.RunCmdOptions{Capture: true, Check: true})
		if res.Err != nil || strings.TrimSpace(res.Stdout) != "true" {
			allExist = false
			break
		}
	}
	if allExist {
		slog.Debug("All SSH host keys already exist, skipping generate_host_keys",
			"image", filepath.Base(p.rootfsPath),
		)
		return nil
	}

	cmds = append(cmds,
		"mkdir-p /etc/local.d",
		fmt.Sprintf("write /etc/local.d/ssh-keygen.start %q", provisionerContentSSHKeygenScript()),
		fmt.Sprintf("chmod %o /etc/local.d/ssh-keygen.start", infra.ExecutablePerm),
		"mkdir-p /etc/systemd/system",
		fmt.Sprintf("write /etc/systemd/system/ssh-hostkeygen.service %q", provisionerContentSSHKeygenService()),
		fmt.Sprintf("chmod %o /etc/systemd/system/ssh-hostkeygen.service", infra.PublicKeyPerm),
		"mkdir-p /etc/systemd/system/multi-user.target.wants",
		"ln-s /etc/systemd/system/ssh-hostkeygen.service /etc/systemd/system/multi-user.target.wants/ssh-hostkeygen.service",
	)

	// OpenRC support (Python: checks /sbin/openrc or /usr/sbin/openrc)
	hasOpenRC := false
	res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", p.rootfsPath, "--ro", "-i",
		"exists", "/sbin/openrc"}, system.RunCmdOptions{Capture: true, Check: true})
	hasOpenRC = res.Err == nil && strings.TrimSpace(res.Stdout) == "true"
	if !hasOpenRC {
		res = system.RunCmdCompat(ctx, []string{"guestfish", "-a", p.rootfsPath, "--ro", "-i",
			"exists", "/usr/sbin/openrc"}, system.RunCmdOptions{Capture: true, Check: true})
		hasOpenRC = res.Err == nil && strings.TrimSpace(res.Stdout) == "true"
	}
	if hasOpenRC {
		cmds = append(cmds,
			"mkdir-p /etc/runlevels/default",
			"ln-sf /sbin/openrc-local /etc/runlevels/default/local",
		)
	}

	return cmds
}

// ── disable_cloud_init ──────────────────────────────────────────────────────

func buildDisableCloudInit() []string {
	return []string{
		"# Disable cloud-init",
		"mkdir-p /etc/cloud/cloud.cfg.d",
		`write /etc/cloud/cloud.cfg.d/99-disable-datasources.cfg "datasource_list: [None]\n"`,
		`write /etc/cloud/cloud-init.disabled "disabled by mvmctl\n"`,
		"mkdir-p /etc/systemd/system/snapd.seeded.service.d",
		`write /etc/systemd/system/snapd.seeded.service.d/override.conf "[Service]\nExecStart=\nExecStart=/bin/true\n"`,
		"mkdir-p /etc/systemd/system/systemd-networkd-wait-online.service.d",
		`write /etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf "[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n"`,
		"ln-sf /dev/null /etc/systemd/system/cloud-init.service",
		"ln-sf /dev/null /etc/systemd/system/cloud-init-local.service",
		"ln-sf /dev/null /etc/systemd/system/cloud-config.service",
		"ln-sf /dev/null /etc/systemd/system/cloud-final.service",
	}
}

// ── inject_cloud_init ───────────────────────────────────────────────────────

func (p *GuestfsProvisioner) buildInjectCloudInit() ([]string, error) {
	if p.cloudInitDir == "" {
		return nil, nil
	}

	seedDir := "/var/lib/cloud/seed/nocloud"
	var cmds []string
	cmds = append(cmds,
		"# Inject cloud-init seed files",
		"mkdir-p "+seedDir,
	)

	requiredFiles := []string{"meta-data", "user-data"}
	optionalFiles := []string{"network-config"}

	for _, filename := range requiredFiles {
		src := filepath.Join(p.cloudInitDir, filename)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			return nil, fmt.Errorf("required cloud-init file not found: %s", src)
		}
		cmds = append(cmds, fmt.Sprintf("upload %s %s/%s", src, seedDir, filename))
	}
	for _, filename := range optionalFiles {
		src := filepath.Join(p.cloudInitDir, filename)
		if _, err := os.Stat(src); err == nil {
			cmds = append(cmds, fmt.Sprintf("upload %s %s/%s", src, seedDir, filename))
		}
	}

	return cmds, nil
}

// ── shrink ──────────────────────────────────────────────────────────────────

func (p *GuestfsProvisioner) buildShrink(ctx context.Context) []string {
	// Detect root device (matches Python's handle.list_filesystems() iteration)
	rootDevice, err := p.detectRootDevice(ctx)
	if err != nil {
		slog.Warn("Cannot detect root device for shrink, using /dev/sda",
			"image", filepath.Base(p.rootfsPath))
		rootDevice = "/dev/sda"
	}

	// Detect filesystem type (Python: fs_type = handle.vfs_type(root_device))
	fsTypeOut, fsErr := guestfishInspect(ctx, p.rootfsPath, true, "vfs-type", rootDevice)
	fsType := strings.TrimSpace(fsTypeOut)
	if fsErr != nil || fsType == "" {
		fsType = "ext4"
	}

	// Skip shrink for unsupported filesystem types (Python: if fs_type not in ("ext2","ext3","ext4","btrfs"))
	if fsType != "ext2" && fsType != "ext3" && fsType != "ext4" && fsType != "btrfs" {
		slog.Debug("Skipping shrink: unsupported filesystem type", "type", fsType)
		return nil
	}

	// Check free space — skip if there isn't enough to reclaim (Python: free_ratio <= 0.02)
	out, svErr := guestfishInspect(ctx, p.rootfsPath, true, "statvfs", "/")
	if svErr == nil {
		blocks := parseStatvfsField(out, "blocks")
		bfree := parseStatvfsField(out, "bfree")
		if blocks > 0 {
			freeRatio := float64(bfree) / float64(blocks)
			if freeRatio <= 0.02 {
				slog.Debug("Filesystem has limited free space after deblob, skipping shrink",
					"free_ratio", freeRatio)
				return nil
			}
		}
	}

	if fsType == "btrfs" {
		return []string{
			"# Shrink btrfs filesystem to minimum",
			`sh "fstrim -av / 2>/dev/null || true"`,
			"btrfs-filesystem-sync /",
			"btrfs-filesystem-resize / 0",
			"umount /",
		}
	}
	// ext2/3/4
	return []string{
		"# Shrink filesystem to minimum",
		"zero-free-space /",
		"umount /",
		fmt.Sprintf("e2fsck %s correct:true", rootDevice),
		fmt.Sprintf("resize2fs-size %s 0", rootDevice),
	}
}

// ── deblob & fix_fstab ──────────────────────────────────────────────────────
//
// These methods delegate to ProvisionerContent (in infra/provisioner/content.go)
// for the actual operation content, converting the generic operations to
// guestfish commands. This mirrors Python's _do_deblob and _do_fix_fstab which
// import ProvisionerContent and execute ops on the guestfs handle.

func (p *GuestfsProvisioner) buildDeblob(ctx context.Context) []string {
	osID, osIDLike := p.parseOSRelease(ctx)
	osType := osID
	if osType == "" {
		osType = osIDLike
	}
	if osType == "" {
		osType = "linux"
	}

	// Use ProvisionerContent for the actual operations (Python: ProvisionerContent.build_deblob_ops())
	pc := provisionercontent.ProvisionerContent{}
	ops := pc.BuildDeblobOps(osType)
	ops = append(ops, pc.BuildFixFstabOps()...)

	return p.convertOpsToGuestfishCommands(ops)
}

// convertOpsToGuestfishCommands converts ProvisionerContent operations to guestfish
// commands (mirrors Python's match/case in _do_deblob and _do_fix_fstab).
// ChrootOp → sh "command", FileOp → mkdir-p + upload (from temp file).
// Uses upload instead of write to avoid content corruption from multi-line data.
func (p *GuestfsProvisioner) convertOpsToGuestfishCommands(ops []provisionercontent.Operation) []string {
	var cmds []string
	cmds = append(cmds, "# Deblob & fix_fstab (from ProvisionerContent)")
	for _, op := range ops {
		switch o := op.(type) {
		case provisionercontent.ChrootOp:
			cmds = append(cmds, fmt.Sprintf("sh %q", o.Command))
		case provisionercontent.FileOp:
			if idx := strings.LastIndex(o.Path, "/"); idx > 0 {
				parent := o.Path[:idx]
				if parent != "" {
					cmds = append(cmds, fmt.Sprintf("mkdir-p %s", parent))
				}
			}
			uploadCmds, err := p.writeGuestfishFile(o.Path, o.Data)
			if err != nil {
				slog.Warn("Failed to prepare upload for file", "path", o.Path, "error", err)
			} else {
				cmds = append(cmds, uploadCmds...)
			}
		case provisionercontent.CopyDirOp:
			slog.Warn("CopyDirOp not supported in guestfish subprocess mode", "src", o.Src, "dst", o.Dst)
		case provisionercontent.ResizeOp:
		}
	}
	return cmds
}

func (p *GuestfsProvisioner) parseOSRelease(ctx context.Context) (string, string) {
	// Try /etc/os-release first, then fall back to /usr/lib/os-release
	// (Python: matches both paths that systemd supports)
	paths := []string{"/etc/os-release", "/usr/lib/os-release"}
	for _, osReleasePath := range paths {
		res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", p.rootfsPath, "--ro", "-i", "read-file", osReleasePath}, system.RunCmdOptions{Capture: true, Check: true})
		if res.Err != nil {
			continue
		}
		out := res.Stdout

		idVal := ""
		idLikeVal := ""
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ID=") {
				idVal = strings.Trim(strings.TrimPrefix(line, "ID="), "\"'")
				idVal = strings.ToLower(idVal)
			} else if strings.HasPrefix(line, "ID_LIKE=") {
				idLikeVal = strings.Trim(strings.TrimPrefix(line, "ID_LIKE="), "\"'")
				idLikeVal = strings.ToLower(idLikeVal)
			}
		}
		if idVal != "" || idLikeVal != "" {
			return idVal, idLikeVal
		}
	}
	return "", ""
}

// ── fix_fstab ───────────────────────────────────────────────────────────────

func (p *GuestfsProvisioner) buildFixFstab() []string {
	pc := provisionercontent.ProvisionerContent{}
	ops := pc.BuildFixFstabOps()
	return p.convertOpsToGuestfishCommands(ops)
}

// ═════════════════════════════════════════════════════════════════════════════
// Free-standing helpers (mirrors Python's enable_ssh, configure_ssh_keys, etc.)
// ═════════════════════════════════════════════════════════════════════════════

// EnableSSH detects init system and enables SSH service in the guest.
// Returns true if SSH was enabled.
func EnableSSH(ctx context.Context, diskPath string) bool {
	// Detect init system using guestfish
	systemdPaths := []string{"/lib/systemd/systemd", "/usr/lib/systemd/systemd"}
	hasSystemd := false
	for _, path := range systemdPaths {
		res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
			"exists", path}, system.RunCmdOptions{Capture: true, Check: true})
		if res.Err == nil && strings.TrimSpace(res.Stdout) == "true" {
			hasSystemd = true
			break
		}
	}

	openrcPaths := []string{"/sbin/openrc", "/usr/sbin/openrc"}
	hasOpenRC := false
	if !hasSystemd {
		for _, path := range openrcPaths {
			res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
				"exists", path}, system.RunCmdOptions{Capture: true, Check: true})
			if res.Err == nil && strings.TrimSpace(res.Stdout) == "true" {
				hasOpenRC = true
				break
			}
		}
	}

	if hasSystemd {
		sshServices := []string{
			"/usr/lib/systemd/system/ssh.service",
			"/lib/systemd/system/ssh.service",
			"/etc/systemd/system/ssh.service",
			"/usr/lib/systemd/system/sshd.service",
			"/lib/systemd/system/sshd.service",
			"/etc/systemd/system/sshd.service",
		}
		for _, svc := range sshServices {
			res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
				"exists", svc}, system.RunCmdOptions{Capture: true, Check: true})
			if res.Err == nil && strings.TrimSpace(res.Stdout) == "true" {
				svcName := filepath.Base(svc)
				// Enable the service
				system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
					"mkdir-p", "/etc/systemd/system/multi-user.target.wants",
					":", "ln-sf", svc, "/etc/systemd/system/multi-user.target.wants/" + svcName}, system.RunCmdOptions{})
				return true
			}
		}
		return false
	}

	if hasOpenRC {
		system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
			"mkdir-p", "/etc/runlevels/default",
			":", "ln-sf", "/etc/init.d/sshd", "/etc/runlevels/default/sshd",
		}, system.RunCmdOptions{})

		res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
			"exists", "/etc/init.d/sshd"}, system.RunCmdOptions{Capture: true, Check: true})
		if res.Err == nil && strings.TrimSpace(res.Stdout) == "true" {
			return true
		}
		res = system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
			"exists", "/etc/init.d/ssh"}, system.RunCmdOptions{Capture: true, Check: true})
		if res.Err == nil && strings.TrimSpace(res.Stdout) == "true" {
			return true
		}
		return false
	}

	// sysvinit
	res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
		"exists", "/etc/init.d/ssh"}, system.RunCmdOptions{Capture: true, Check: true})
	if res.Err == nil && strings.TrimSpace(res.Stdout) == "true" {
		for _, level := range []string{"2", "3", "4", "5"} {
			system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
				"mkdir-p", "/etc/rc" + level + ".d",
				":", "ln-sf", "../init.d/ssh", "/etc/rc" + level + ".d/S02ssh",
			}, system.RunCmdOptions{})
		}
		return true
	}

	slog.Warn("Unknown init system or SSH not found",
		"image", filepath.Base(diskPath))
	return false
}

// ConfigureSSHKeys configures SSH key authentication in the guest.
func ConfigureSSHKeys(ctx context.Context, diskPath string, user string) {
	res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
		"exists", "/etc/ssh/sshd_config"}, system.RunCmdOptions{Capture: true, Check: true})
	if res.Err != nil || strings.TrimSpace(res.Stdout) != "true" {
		slog.Warn("sshd_config not found", "image", filepath.Base(diskPath))
		return
	}

	sshdConfig := sshdConfigContent(user)
	system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
		"mkdir-p", "/etc/ssh/sshd_config.d",
		":", fmt.Sprintf("write /etc/ssh/sshd_config.d/mvm.conf %q", sshdConfig),
		":", fmt.Sprintf("chmod %o /etc/ssh/sshd_config.d/mvm.conf", infra.PublicKeyPerm),
	}, system.RunCmdOptions{})

	slog.Info("Configured SSH key authentication",
		"user", user,
		"image", filepath.Base(diskPath),
	)
}

// EnsureUser creates a user in the guest with sudoers.
// Python source: _provtypes.py :: GuestfsProvisioner.ensure_user()
// Uses read-modify-write pattern instead of mode="a" (guestfish has no append mode).
func EnsureUser(ctx context.Context, diskPath string, user string, userUID, userGID int) {
	if user == "" || user == "root" {
		return
	}

	// Check if user already exists + read existing content for append
	res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
		"read-file", "/etc/passwd"}, system.RunCmdOptions{Capture: true, Check: true})
	if res.Err != nil {
		return
	}
	existingPasswd := res.Stdout

	for _, line := range strings.Split(existingPasswd, "\n") {
		if strings.HasPrefix(line, user+":") {
			slog.Debug("User already exists", "user", user, "image", filepath.Base(diskPath))
			return
		}
	}

	// Read existing /etc/shadow and /etc/group for append
	existingShadow := ""
	res2 := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
		"read-file", "/etc/shadow"}, system.RunCmdOptions{Capture: true, Check: true})
	if res2.Err == nil {
		existingShadow = res2.Stdout
	}

	existingGroup := ""
	res3 := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
		"read-file", "/etc/group"}, system.RunCmdOptions{Capture: true, Check: true})
	if res3.Err == nil {
		existingGroup = res3.Stdout
	}

	homeDir := "/home/" + user

	// Build combined content with real newlines — %q will convert them to
	// \n escape sequences for the guestfish write command, which guestfish
	// interprets as actual newlines.
	combinedPasswd := existingPasswd
	if combinedPasswd != "" && !strings.HasSuffix(combinedPasswd, "\n") {
		combinedPasswd += "\n"
	}
	combinedPasswd += fmt.Sprintf("%s:!:%d:%d::%s:/bin/bash\n", user, userUID, userGID, homeDir)

	combinedShadow := existingShadow
	if combinedShadow != "" && !strings.HasSuffix(combinedShadow, "\n") {
		combinedShadow += "\n"
	}
	combinedShadow += fmt.Sprintf("%s:!:%d:%d:%d:%d:::\n",
		user, infra.ShadowDaysSinceEpoch,
		infra.ShadowMinDays, infra.ShadowMaxDays, infra.ShadowWarnDays)

	combinedGroup := existingGroup
	if combinedGroup != "" && !strings.HasSuffix(combinedGroup, "\n") {
		combinedGroup += "\n"
	}
	combinedGroup += fmt.Sprintf("%s:x:%d:\n", user, userGID)

	system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
		"mkdir-p", homeDir,
		":", "mkdir-p", homeDir + "/.ssh",
		":", fmt.Sprintf("write /etc/passwd %q", combinedPasswd),
		":", fmt.Sprintf("chmod %o /etc/passwd", infra.PublicKeyPerm),
		":", fmt.Sprintf("write /etc/shadow %q", combinedShadow),
		":", fmt.Sprintf("chmod %o /etc/shadow", infra.ShadowPerm),
		":", fmt.Sprintf("write /etc/group %q", combinedGroup),
		":", fmt.Sprintf("chmod %o /etc/group", infra.PublicKeyPerm),
		":", "mkdir-p", "/etc/sudoers.d",
		":", fmt.Sprintf("write /etc/sudoers.d/%s %q", user, fmt.Sprintf("%s ALL=(ALL) NOPASSWD: ALL\\n", user)),
		":", fmt.Sprintf("chmod %o /etc/sudoers.d/%s", infra.SudoersPerm, user),
		":", fmt.Sprintf("chown %d %d %s", userUID, userGID, homeDir),
		":", fmt.Sprintf("chown %d %d %s/.ssh", userUID, userGID, homeDir),
	}, system.RunCmdOptions{})

	slog.Info("Created user",
		"user", user,
		"uid", userUID,
		"image", filepath.Base(diskPath),
	)
}

// GenerateHostKeys sets up SSH host key generation service in the guest.
func GenerateHostKeys(ctx context.Context, diskPath string) {
	keyTypes := []string{
		"ssh_host_rsa_key",
		"ssh_host_ecdsa_key",
		"ssh_host_ed25519_key",
	}

	// Check which keys exist
	allExist := true
	for _, key := range keyTypes {
		res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
			"exists", "/etc/ssh/" + key}, system.RunCmdOptions{Capture: true, Check: true})
		if res.Err != nil || strings.TrimSpace(res.Stdout) != "true" {
			allExist = false
			break
		}
	}
	if allExist {
		slog.Debug("All SSH host keys already exist", "image", filepath.Base(diskPath))
		return
	}

	system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
		"mkdir-p", "/etc/local.d",
		":", fmt.Sprintf("write /etc/local.d/ssh-keygen.start %q", provisionerContentSSHKeygenScript()),
		":", fmt.Sprintf("chmod %o /etc/local.d/ssh-keygen.start", infra.ExecutablePerm),
		":", "mkdir-p", "/etc/systemd/system",
		":", fmt.Sprintf("write /etc/systemd/system/ssh-hostkeygen.service %q", provisionerContentSSHKeygenService()),
		":", fmt.Sprintf("chmod %o /etc/systemd/system/ssh-hostkeygen.service", infra.PublicKeyPerm),
		":", "mkdir-p", "/etc/systemd/system/multi-user.target.wants",
		":", "ln-s", "/etc/systemd/system/ssh-hostkeygen.service",
		"/etc/systemd/system/multi-user.target.wants/ssh-hostkeygen.service",
	}, system.RunCmdOptions{})

	// Also check for OpenRC
	res := system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
		"exists", "/sbin/openrc"}, system.RunCmdOptions{Capture: true, Check: true})
	hasOpenRC := res.Err == nil && strings.TrimSpace(res.Stdout) == "true"

	if !hasOpenRC {
		res = system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "--ro", "-i",
			"exists", "/usr/sbin/openrc"}, system.RunCmdOptions{Capture: true, Check: true})
		hasOpenRC = res.Err == nil && strings.TrimSpace(res.Stdout) == "true"
	}

	if hasOpenRC {
		system.RunCmdCompat(ctx, []string{"guestfish", "-a", diskPath, "-i",
			"mkdir-p", "/etc/runlevels/default",
			":", "ln-sf", "/sbin/openrc-local", "/etc/runlevels/default/local",
		}, system.RunCmdOptions{})
	}

	slog.Info("Created SSH host key generation service",
		"image", filepath.Base(diskPath),
	)
}

// ═════════════════════════════════════════════════════════════════════════════
// Provisioner content helpers (inlined from _content.py ProvisionerContent)
// ═════════════════════════════════════════════════════════════════════════════

// sshdConfigContent returns the SSH daemon config fragment matching
// Python's ProvisionerContent.sshd_config(user).
// Delegates to provisionercontent which has the canonical implementation.
func sshdConfigContent(user string) string {
	pc := provisionercontent.ProvisionerContent{}
	return pc.SSHDConfig(user)
}

// The function is also named differently in the provisioner context.
func provisionerContentSshdConfig(user string) string {
	return sshdConfigContent(user)
}

// provisionerContentFirstBootInstaller returns the first-boot SSH installer script
// matching Python's ProvisionerContent.first_boot_installer().
func provisionerContentFirstBootInstaller() string {
	pc := provisionercontent.ProvisionerContent{}
	return pc.FirstBootInstaller()
}

// provisionerContentFirstBootService returns the systemd service unit matching
// Python's ProvisionerContent.first_boot_service().
func provisionerContentFirstBootService() string {
	pc := provisionercontent.ProvisionerContent{}
	return pc.FirstBootService()
}

// provisionerContentSSHKeygenScript returns the SSH host key generation script
// matching Python's generate_host_keys script.
func provisionerContentSSHKeygenScript() string {
	return "#!/bin/bash\n" +
		"SSH_KEYDIR=\"/etc/ssh\"\n" +
		"for key_type in ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key; do\n" +
		"  key_path=\"$SSH_KEYDIR/$key_type\"\n" +
		"  if [ ! -f \"$key_path\" ]; then\n" +
		"    case \"$key_type\" in\n" +
		"      ssh_host_rsa_key) ssh-keygen -t rsa -f \"$key_path\" -N \"\" -q 2>/dev/null ;;\n" +
		"      ssh_host_ecdsa_key) ssh-keygen -t ecdsa -f \"$key_path\" -N \"\" -q 2>/dev/null ;;\n" +
		"      ssh_host_ed25519_key) ssh-keygen -t ed25519 -f \"$key_path\" -N \"\" -q 2>/dev/null ;;\n" +
		"    esac\n" +
		"    chmod 600 \"$key_path\" 2>/dev/null\n" +
		"    chmod 644 \"${key_path}.pub\" 2>/dev/null\n" +
		"  fi\n" +
		"done\n" +
		"rm -f /etc/local.d/ssh-keygen.start 2>/dev/null\n" +
		"exit 0\n"
}

// provisionerContentSSHKeygenService returns the systemd service unit for
// SSH host key generation.
func provisionerContentSSHKeygenService() string {
	return "[Unit]\n" +
		"Description=SSH Host Key Generation\n" +
		"Before=ssh.service\n" +
		"After=local-fs.target\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/bin/bash /etc/local.d/ssh-keygen.start\n" +
		"RemainAfterExit=yes\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}


