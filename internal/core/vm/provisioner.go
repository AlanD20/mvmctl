package vm

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/system"
)

// ── Provisioner ──
// Matches Python's core/vm/_provisioner.py:Provisioner exactly.
//
// Provisioner provides unified VM rootfs provisioning via backends.
// Selected by provisioner_type (LOOP_MOUNT or GUESTFS).
// All builder methods queue operations. Call .Run() to execute
// everything in a single session.

// ProvisionerBackend is the interface that provisioning backends must implement.
// Matches Python's ProvisionerBackend.
type ProvisionerBackend interface {
	DetectOS() string
	Resize(targetSizeBytes int64) error
	SetHostname(hostname string) error
	InjectDNS(dnsServer string) error
	SetupSSH(user string, sshPubkeys []string) error
	DisableCloudInit() error
	InjectCloudInit(cloudInitDir string) error
	FixFstab() error
	Deblob(osType string) error
	Run() error
}

// Provisioner matches Python's Provisioner class.
type Provisioner struct {
	backend ProvisionerBackend
}

// NewProvisioner creates a new VM provisioner.
// Matches Python's Provisioner(rootfs_path, provisioner_type, fs_type, ...).
// Returns an error for unknown provisioner types (Python raises ValueError).
func NewProvisioner(
	rootfsPath string,
	provisionerType provisioner.ProvisionerType,
	fsType string,
	opts ...ProvisionerOption,
) (*Provisioner, error) {
	p := &ProvisionerOptions{
		RootUID: 0,
		RootGID: 0,
		UserUID: 1000,
		UserGID: 1000,
	}
	for _, opt := range opts {
		opt(p)
	}

	var backend ProvisionerBackend
	switch provisionerType {
	case provisioner.ProvisionerLoopMount:
		backend = newLoopMountBackend(rootfsPath, fsType, p)
	case provisioner.ProvisionerGuestFS:
		// Must ensure guestfs appliance cache is available (Python's _ensure_guestfs_appliance())
		cacheDir, err := infra.GetCacheDir()
		if err != nil {
			return nil, fmt.Errorf("provisioner: failed to get cache dir: %w", err)
		}
		if err := provisioner.EnsureGuestfsAppliance(cacheDir); err != nil {
			return nil, err
		}
		backend = newGuestfsBackend(rootfsPath, fsType, p)
	default:
		// Python raises ValueError for unknown provisioner types
		return nil, fmt.Errorf("unknown provisioner type: %s", provisionerType)
	}

	return &Provisioner{backend: backend}, nil
}

// ProvisionerOptions holds optional parameters for the provisioner.
type ProvisionerOptions struct {
	RootUID int
	RootGID int
	UserUID int
	UserGID int
}

// ProvisionerOption is a functional option for Provisioner.
type ProvisionerOption func(*ProvisionerOptions)

// WithRootUID sets the root UID. Default: 0.
func WithRootUID(uid int) ProvisionerOption {
	return func(o *ProvisionerOptions) { o.RootUID = uid }
}

// WithRootGID sets the root GID. Default: 0.
func WithRootGID(gid int) ProvisionerOption {
	return func(o *ProvisionerOptions) { o.RootGID = gid }
}

// WithUserUID sets the user UID. Default: 1000.
func WithUserUID(uid int) ProvisionerOption {
	return func(o *ProvisionerOptions) { o.UserUID = uid }
}

// WithUserGID sets the user GID. Default: 1000.
func WithUserGID(gid int) ProvisionerOption {
	return func(o *ProvisionerOptions) { o.UserGID = gid }
}

// =========================================================================
// Inline loop-mount backend
// =========================================================================

type loopMountBackend struct {
	rootfsPath string
	fsType     string
	opts       *ProvisionerOptions
	ops        []provisioner.Operation
}

func newLoopMountBackend(rootfsPath, fsType string, opts *ProvisionerOptions) *loopMountBackend {
	return &loopMountBackend{
		rootfsPath: rootfsPath,
		fsType:     fsType,
		opts:       opts,
	}
}

func (b *loopMountBackend) DetectOS() string {
	osType, err := b.detectOSInternal()
	if err != nil {
		slog.Warn("OS detection failed, falling back to 'linux'", "error", err)
		return "linux"
	}
	return osType
}

func (b *loopMountBackend) detectOSInternal() (string, error) {
	mountPoint, loopDev, err := mountImage(b.rootfsPath)
	if err != nil {
		return "", fmt.Errorf("mount for OS detection: %w", err)
	}
	defer unmountImage(mountPoint, loopDev)

	content, err := os.ReadFile(filepath.Join(mountPoint, "etc/os-release"))
	if err != nil {
		content, err = os.ReadFile(filepath.Join(mountPoint, "usr/lib/os-release"))
		if err != nil {
			return "", fmt.Errorf("no os-release file found in rootfs")
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "ID=") {
			idVal := strings.TrimPrefix(line, "ID=")
			idVal = strings.Trim(idVal, "\"'")
			return strings.ToLower(idVal), nil
		}
	}

	return "linux", nil
}

func (b *loopMountBackend) Resize(targetSizeBytes int64) error {
	pc := provisioner.ProvisionerContent{}
	if targetSizeBytes == 0 {
		b.ops = append(b.ops, pc.BuildShrinkOps(0)...)
	} else {
		b.ops = append(b.ops, pc.BuildResizeOps(targetSizeBytes)...)
	}
	return nil
}

func (b *loopMountBackend) SetHostname(hostname string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildHostnameOps(hostname)...)
	return nil
}

func (b *loopMountBackend) InjectDNS(dnsServer string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildDNSOps(dnsServer)...)
	return nil
}

func (b *loopMountBackend) SetupSSH(user string, sshPubkeys []string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildSSHOps(user, sshPubkeys)...)
	return nil
}

func (b *loopMountBackend) DisableCloudInit() error {
	// Queue cloud-init datasource blocking + service masking (Python's build_cloud_init_disable_ops)
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildCloudInitDisableOps()...)
	return nil
}

func (b *loopMountBackend) InjectCloudInit(cloudInitDir string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildCloudInitInjectOps(cloudInitDir)...)
	return nil
}

func (b *loopMountBackend) FixFstab() error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildFixFstabOps()...)
	return nil
}

func (b *loopMountBackend) Deblob(osType string) error {
	if osType == "" {
		osType = b.DetectOS()
	}
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildDeblobOps(osType)...)
	return nil
}

func (b *loopMountBackend) Run() error {
	if len(b.ops) == 0 {
		slog.Debug("No operations queued, skipping loop-mount Run()")
		return nil
	}

	var preOps []provisioner.Operation
	var resizeOp *provisioner.ResizeOp

	for _, op := range b.ops {
		if ro, ok := op.(provisioner.ResizeOp); ok {
			resizeOp = &ro
		} else {
			preOps = append(preOps, op)
		}
	}

	if len(preOps) > 0 {
		mountPoint, loopDev, err := mountImage(b.rootfsPath)
		if err != nil {
			return fmt.Errorf("mount for Run(): %w", err)
		}

		for _, op := range preOps {
			switch o := op.(type) {
			case provisioner.FileOp:
				if err := executeFileOp(mountPoint, o); err != nil {
					unmountImage(mountPoint, loopDev)
					return fmt.Errorf("file op failed: %w", err)
				}
			case provisioner.ChrootOp:
				if err := executeChrootOp(mountPoint, o); err != nil {
					unmountImage(mountPoint, loopDev)
					return fmt.Errorf("chroot op failed: %w", err)
				}
			case provisioner.CopyDirOp:
				if err := executeCopyDirOp(mountPoint, o); err != nil {
					unmountImage(mountPoint, loopDev)
					return fmt.Errorf("copy dir op failed: %w", err)
				}
			}
		}

		unmountImage(mountPoint, loopDev)
	}

	if resizeOp != nil {
		if err := executeResizeOp(b.rootfsPath, *resizeOp); err != nil {
			return fmt.Errorf("resize op failed: %w", err)
		}
	}

	slog.Debug("Loop-mount provisioning succeeded")
	return nil
}

// =========================================================================
// Inline guestfs backend
// =========================================================================

type guestfsBackend struct {
	rootfsPath string
	fsType     string
	opts       *ProvisionerOptions
	ops        []provisioner.Operation
}

func newGuestfsBackend(rootfsPath, fsType string, opts *ProvisionerOptions) *guestfsBackend {
	return &guestfsBackend{
		rootfsPath: rootfsPath,
		fsType:     fsType,
		opts:       opts,
	}
}

func (b *guestfsBackend) DetectOS() string {
	osType, err := b.detectOSInternal()
	if err != nil {
		slog.Warn("Guestfs OS detection failed, falling back to 'linux'", "error", err)
		return "linux"
	}
	return osType
}

func (b *guestfsBackend) detectOSInternal() (string, error) {
	content, err := runGuestfishCmd(b.rootfsPath, true, "read-file", "/etc/os-release")
	if err != nil {
		content, err = runGuestfishCmd(b.rootfsPath, true, "read-file", "/usr/lib/os-release")
		if err != nil {
			return "", fmt.Errorf("guestfs: no os-release found")
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "ID=") {
			idVal := strings.TrimPrefix(line, "ID=")
			idVal = strings.Trim(idVal, "\"'")
			return strings.ToLower(idVal), nil
		}
	}

	return "linux", nil
}

func (b *guestfsBackend) Resize(targetSizeBytes int64) error {
	pc := provisioner.ProvisionerContent{}
	if targetSizeBytes == 0 {
		b.ops = append(b.ops, pc.BuildShrinkOps(0)...)
	} else {
		b.ops = append(b.ops, pc.BuildResizeOps(targetSizeBytes)...)
	}
	return nil
}

func (b *guestfsBackend) SetHostname(hostname string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildHostnameOps(hostname)...)
	return nil
}

func (b *guestfsBackend) InjectDNS(dnsServer string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildDNSOps(dnsServer)...)
	return nil
}

func (b *guestfsBackend) SetupSSH(user string, sshPubkeys []string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildSSHOps(user, sshPubkeys)...)
	return nil
}

func (b *guestfsBackend) DisableCloudInit() error {
	// Queue cloud-init datasource blocking + service masking (Python's build_cloud_init_disable_ops)
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildCloudInitDisableOps()...)
	return nil
}

func (b *guestfsBackend) InjectCloudInit(cloudInitDir string) error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildCloudInitInjectOps(cloudInitDir)...)
	return nil
}

func (b *guestfsBackend) FixFstab() error {
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildFixFstabOps()...)
	return nil
}

func (b *guestfsBackend) Deblob(osType string) error {
	// os_type is ignored for guestfs backend (it detects OS internally).
	// Parameter accepted for interface compatibility with loopMountBackend.
	pc := provisioner.ProvisionerContent{}
	b.ops = append(b.ops, pc.BuildDeblobOps(osType)...)
	return nil
}

func (b *guestfsBackend) Run() error {
	if len(b.ops) == 0 {
		slog.Debug("No operations queued, skipping guestfs Run()")
		return nil
	}

	var preOps []provisioner.Operation
	var resizeOp *provisioner.ResizeOp

	for _, op := range b.ops {
		if ro, ok := op.(provisioner.ResizeOp); ok {
			resizeOp = &ro
		} else {
			preOps = append(preOps, op)
		}
	}

	if len(preOps) > 0 {
		var gfCommands []string
		gfCommands = append(gfCommands, "launch")
		gfCommands = append(gfCommands, fmt.Sprintf("add-drive %s", b.rootfsPath))
		gfCommands = append(gfCommands, "mount /dev/sda /")

		for _, op := range preOps {
			switch o := op.(type) {
			case provisioner.FileOp:
				gfCommands = append(gfCommands,
					fmt.Sprintf("write-file %s %q %d", o.Path, string(o.Data), o.Mode))
			case provisioner.ChrootOp:
				gfCommands = append(gfCommands,
					fmt.Sprintf("sh %q", o.Command))
			case provisioner.CopyDirOp:
				gfCommands = append(gfCommands,
					fmt.Sprintf("copy-in %s %s", o.Src, o.Dst))
			}
		}

		if err := runGuestfishSession(b.rootfsPath, false, gfCommands); err != nil {
			return fmt.Errorf("guestfs Run() failed: %w", err)
		}
	}

	if resizeOp != nil {
		if err := executeResizeOp(b.rootfsPath, *resizeOp); err != nil {
			return fmt.Errorf("guestfs resize op failed: %w", err)
		}
	}

	slog.Debug("Guestfs provisioning succeeded")
	return nil
}

// =========================================================================
// Inline operation execution helpers
// =========================================================================

func executeFileOp(mountPoint string, op provisioner.FileOp) error {
	fullPath := filepath.Join(mountPoint, op.Path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(fullPath, op.Data, os.FileMode(op.Mode)); err != nil {
		return fmt.Errorf("write %s: %w", op.Path, err)
	}
	return nil
}

func executeChrootOp(mountPoint string, op provisioner.ChrootOp) error {
	stdout, stderr, err := system.RunCmd(context.Background(), "chroot", mountPoint, "/bin/sh", "-c", op.Command)
	if err != nil {
		return fmt.Errorf("chroot command exited: %s\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return nil
}

func executeCopyDirOp(mountPoint string, op provisioner.CopyDirOp) error {
	dstPath := filepath.Join(mountPoint, op.Dst)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dstPath), err)
	}
	stdout, stderr, err := system.RunCmd(context.Background(), "cp", "-a", op.Src, dstPath+"/")
	if err != nil {
		return fmt.Errorf("cp -a %s -> %s: %w\nstdout: %s\nstderr: %s", op.Src, dstPath, err, stdout, stderr)
	}
	return nil
}

func executeResizeOp(imagePath string, op provisioner.ResizeOp) error {
	switch op.Action {
	case provisioner.ResizeActionShrink:
		slog.Debug("Shrinking filesystem", "image", imagePath)
		if err := runCommand("e2fsck", "-f", "-y", imagePath); err != nil {
			return fmt.Errorf("e2fsck failed for shrink: %w", err)
		}
		if err := runCommand("resize2fs", "-M", imagePath); err != nil {
			return fmt.Errorf("resize2fs -M failed: %w", err)
		}
		slog.Debug("Filesystem shrunk", "image", imagePath)

	case provisioner.ResizeActionGrow:
		slog.Debug("Growing filesystem", "image", imagePath, "bytes", op.Bytes)
		if err := runCommand("e2fsck", "-f", "-y", imagePath); err != nil {
			return fmt.Errorf("e2fsck failed for grow: %w", err)
		}
		if op.Bytes > 0 {
			if err := runCommand("resize2fs", imagePath, fmt.Sprintf("%d", op.Bytes)); err != nil {
				return fmt.Errorf("resize2fs failed: %w", err)
			}
		} else {
			if err := runCommand("resize2fs", imagePath); err != nil {
				return fmt.Errorf("resize2fs failed: %w", err)
			}
		}
		slog.Debug("Filesystem grown", "image", imagePath)
	}
	return nil
}

// =========================================================================
// Image mounting helpers
// TODO(verdict#33): move mountImage, hasPartitionTable, unmountImage, detachLoopDevice to infra/
// =========================================================================

func mountImage(imagePath string) (mountPoint string, loopDev string, err error) {
	mountPoint, err = os.MkdirTemp("", "mvm-provision-*")
	if err != nil {
		return "", "", fmt.Errorf("mkdtemp: %w", err)
	}

	if hasPartitionTable(imagePath) {
		out, _, err := system.RunCmd(context.Background(), "losetup", "-Pf", "--show", imagePath)
		if err != nil {
			os.RemoveAll(mountPoint)
			return "", "", fmt.Errorf("losetup -Pf: %w", err)
		}
		loopDev = strings.TrimSpace(out)

		partDev := loopDev + "p1"
		_, _, err = system.RunCmd(context.Background(), "mount", partDev, mountPoint)
		if err != nil {
			detachLoopDevice(loopDev)
			os.RemoveAll(mountPoint)

			mountPoint2, err2 := os.MkdirTemp("", "mvm-provision-*")
			if err2 != nil {
				return "", "", fmt.Errorf("mkdtemp: %w", err2)
			}
			mountPoint = mountPoint2
			loopDev = ""
			goto tryRawMount
		}
		return mountPoint, loopDev, nil
	}

tryRawMount:
	_, _, err = system.RunCmd(context.Background(), "mount", "-o", "loop", imagePath, mountPoint)
	if err != nil {
		os.RemoveAll(mountPoint)
		return "", "", fmt.Errorf("mount -o loop: %w", err)
	}
	return mountPoint, "", nil
}

func hasPartitionTable(imagePath string) bool {
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	result := system.RunCmdCompat(context.Background(),
		[]string{"blkid", "-o", "value", "-s", "TYPE", imagePath}, opts)
	if result.ExitCode != 0 || result.Stdout == "" {
		return false
	}
	fsType := strings.TrimSpace(result.Stdout)
	if fsType == "ext4" || fsType == "ext3" || fsType == "ext2" ||
		fsType == "btrfs" || fsType == "xfs" {
		return false
	}
	return true
}

func unmountImage(mountPoint string, loopDev string) {
	if mountPoint != "" {
		_, _, _ = system.RunCmd(context.Background(), "umount", "-l", mountPoint)
		_ = os.RemoveAll(mountPoint)
	}
	if loopDev != "" {
		_ = detachLoopDevice(loopDev)
	}
}

func detachLoopDevice(loopDev string) error {
	_, _, err := system.RunCmd(context.Background(), "losetup", "-d", loopDev)
	return err
}

// =========================================================================
// Guestfish command helpers
// TODO(verdict#33): move runGuestfishCmd, runGuestfishSession to infra/
// =========================================================================

func runGuestfishCmd(diskPath string, readonly bool, args ...string) (string, error) {
	gfArgs := []string{}
	if readonly {
		gfArgs = append(gfArgs, "--ro")
	}
	gfArgs = append(gfArgs, "-a", diskPath, "--cachemode", "writeback", "-i")
	gfArgs = append(gfArgs, args...)

	opts := system.DefaultRunCmdOpts()
	opts.Check = true
	opts.Capture = true
	result := system.RunCmdCompat(context.Background(), append([]string{"guestfish"}, gfArgs...), opts)
	if result.Err != nil {
		return "", result.Err
	}
	return result.Stdout, nil
}

func runGuestfishSession(diskPath string, readonly bool, commands []string) error {
	gfArgs := []string{}
	if readonly {
		gfArgs = append(gfArgs, "--ro")
	}
	gfArgs = append(gfArgs, "-a", diskPath, "--cachemode", "writeback")

	var script strings.Builder
	for _, cmd := range commands {
		script.WriteString(cmd)
		script.WriteString("\n")
	}

	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	opts.Input = script.String()
	result := system.RunCmdCompat(context.Background(), append([]string{"guestfish"}, gfArgs...), opts)
	if result.ExitCode != 0 {
		return fmt.Errorf("guestfish session failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	return nil
}

// =========================================================================
// General command helpers
// =========================================================================

func runCommand(name string, args ...string) error {
	_, stderr, err := system.RunCmd(context.Background(), name, args...)
	if err != nil {
		return fmt.Errorf("%s failed: %w\nstderr: %s", name, err, stderr)
	}
	return nil
}

// =========================================================================
// Builder methods
// =========================================================================

func (p *Provisioner) DetectOS() string {
	return p.backend.DetectOS()
}

func (p *Provisioner) Resize(targetSizeBytes int64) error {
	return p.backend.Resize(targetSizeBytes)
}

func (p *Provisioner) SetHostname(hostname string) error {
	return p.backend.SetHostname(hostname)
}

func (p *Provisioner) InjectDNS(dnsServer string) error {
	return p.backend.InjectDNS(dnsServer)
}

func (p *Provisioner) SetupSSH(user string, sshPubkeys []string) error {
	return p.backend.SetupSSH(user, sshPubkeys)
}

func (p *Provisioner) DisableCloudInit() error {
	return p.backend.DisableCloudInit()
}

func (p *Provisioner) InjectCloudInit(cloudInitDir string) error {
	return p.backend.InjectCloudInit(cloudInitDir)
}

func (p *Provisioner) FixFstab() error {
	return p.backend.FixFstab()
}

func (p *Provisioner) Deblob(osType string) error {
	return p.backend.Deblob(osType)
}

func (p *Provisioner) Run() error {
	return p.backend.Run()
}
