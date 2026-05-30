package provisioner

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/guestfs"
	"mvmctl/internal/infra/system"
)

var logger = slog.With("name", "mvmctl.core._shared._provisioner._backend")

// Backend interface for rootfs provisioning.
// Matches the methods exposed by _LoopMountBackend and _GuestfsBackend in Python.
type Backend interface {
	Resize(targetSizeBytes int64) error
	SetHostname(hostname string) error
	InjectDNS(dnsServer string) error
	SetupSSH(user string, sshPubkeys []string) error
	DisableCloudInit() error
	InjectCloudInit(cloudInitDir string) error
	DetectOS() (string, error)
	// Deblob queues OS cache cleanup operations.
	// osType is the detected OS identifier (e.g. "ubuntu", "alpine", "arch").
	// If empty, the backend should detect the OS first (matching Python's
	// _LoopMountBackend.deblob(self, os_type=None) which calls self.detect_os()).
	Deblob(osType string) error
	FixFstab() error
	Shrink() error
	ExtractPartition(rawPath, outputPath string, partition int, disabledDetectors []string) (string, error)
	ConvertTo(targetFS string) error
	Run() error
}

// NoPartitionTable is a sentinel: raw image has no partition table and
// should be used as-is. Matches Python's _NoPartitionTable class.
type NoPartitionTable struct{}

// NoPartitionTableSentinel is the singleton instance of NoPartitionTable.
// Used as a return value from partition parsing to indicate "no partition table found".
var NoPartitionTableSentinel = &NoPartitionTable{}

// LoopMountBackendConstructor is a function type for constructing a
// LoopMountBackend. This breaks the circular dependency between the
// provisioner and loopmount packages.
// Matches Python's _LoopMountBackend.__init__(self, rootfs_path, fs_type).
type LoopMountBackendConstructor func(rootfsPath string, fsType string, cacheDir string) Backend

// ProvisionerBackendFactory constructs the correct backend based on ProvisionerType.
// Matches Python's ProvisionerBackend class.
//
// The NewLoopMount field must be set before calling GetVM or GetImage
// to avoid circular imports between provisioner and loopmount packages.
type ProvisionerBackendFactory struct {
	// NewLoopMount is the constructor for LoopMountBackend.
	// Must be set by the caller (app.go or API layer) to break circular deps.
	NewLoopMount LoopMountBackendConstructor

	// CacheDir is the MVM cache directory for binary resolution.
	CacheDir string
}

// GetVM constructs a backend for VM provisioning.
// Matches Python's ProvisionerBackend.get_vm().
func (f *ProvisionerBackendFactory) GetVM(
	rootfsPath string,
	provisionerType ProvisionerType,
	fsType string,
	rootUID int,
	rootGID int,
	userUID int,
	userGID int,
) (Backend, error) {
	switch provisionerType {
	case ProvisionerLoopMount:
		if f.NewLoopMount == nil {
			return nil, fmt.Errorf("provisioner: LoopMountBackend constructor not set")
		}
		return f.NewLoopMount(rootfsPath, fsType, f.CacheDir), nil
	case ProvisionerGuestFS:
		// Must ensure guestfs appliance cache is available
		if err := EnsureGuestfsAppliance(f.CacheDir); err != nil {
			return nil, err
		}
		return NewGuestfsBackend(rootfsPath, rootUID, rootGID, userUID, userGID), nil
	default:
		return nil, fmt.Errorf("provisioner: unknown provisioner type: %s", provisionerType)
	}
}

// GetImage constructs a backend for image optimization.
// fs_type is only meaningful for the LOOP_MOUNT backend.
// Matches Python's ProvisionerBackend.get_image() — no UID/GID overrides.
func (f *ProvisionerBackendFactory) GetImage(
	imagePath string,
	provisionerType ProvisionerType,
	fsType string,
) (Backend, error) {
	switch provisionerType {
	case ProvisionerLoopMount:
		if f.NewLoopMount == nil {
			return nil, fmt.Errorf("provisioner: LoopMountBackend constructor not set")
		}
		return f.NewLoopMount(imagePath, fsType, f.CacheDir), nil
	case ProvisionerGuestFS:
		if err := EnsureGuestfsAppliance(f.CacheDir); err != nil {
			return nil, err
		}
		return NewGuestfsBackend(imagePath, 0, 0, 1000, 1000), nil
	default:
		return nil, fmt.Errorf("provisioner: unknown provisioner type: %s", provisionerType)
	}
}

// EnsureGuestfsAppliance checks if the libguestfs appliance cache exists.
// Matches Python's _ensure_guestfs_appliance() exactly — raises the same
// error message.
func EnsureGuestfsAppliance(cacheDir string) error {
	applianceDir := filepath.Join(cacheDir, "appliance")
	required := map[string]bool{"kernel": false, "initrd": false, "root": false}

	entries, err := filepath.Glob(filepath.Join(applianceDir, "*"))
	if err != nil || len(entries) == 0 {
		return errs.GuestfsError(
			"libguestfs appliance cache not found. Run: mvm cache init",
		)
	}

	for _, entry := range entries {
		name := filepath.Base(entry)
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}

	for _, found := range required {
		if !found {
			return errs.GuestfsError(
				"libguestfs appliance cache not found. Run: mvm cache init",
			)
		}
	}

	return nil
}

// =========================================================================
// GuestFS Backend — delegates all operations to guestfish CLI.
// Matches Python's _GuestfsBackend.
// =========================================================================

type GuestfsBackend struct {
	rootfsPath string
	readonly   bool
	ctx        context.Context
	rootUID    int
	rootGID    int
	userUID    int
	userGID    int

	// Internal provisioner for queued operations (lazily created)
	provisioner *guestfs.GuestfsProvisioner
}

// NewGuestfsBackend creates a new GuestFS backend for the given rootfs path.
func NewGuestfsBackend(rootfsPath string, rootUID, rootGID, userUID, userGID int) *GuestfsBackend {
	return &GuestfsBackend{
		rootfsPath: rootfsPath,
		rootUID:    rootUID,
		rootGID:    rootGID,
		userUID:    userUID,
		userGID:    userGID,
		ctx:        context.Background(),
	}
}

// SetContext sets the context for this backend.
func (b *GuestfsBackend) SetContext(ctx context.Context) {
	b.ctx = ctx
}

// getProvisioner returns the guestfs provisioner, creating it lazily.
func (b *GuestfsBackend) getProvisioner() *guestfs.GuestfsProvisioner {
	if b.provisioner == nil {
		b.provisioner = guestfs.NewProvisioner(b.rootfsPath,
			guestfs.WithReadonly(b.readonly),
			guestfs.WithRootUID(b.rootUID),
			guestfs.WithRootGID(b.rootGID),
			guestfs.WithUserUID(b.userUID),
			guestfs.WithUserGID(b.userGID),
		)
	}
	return b.provisioner
}

// ── Backend interface implementation ──────────────────────────────────────────

// Resize queues or directly performs a rootfs resize operation.
// If targetSizeBytes is 0, this is a shrink-to-minimum operation.
func (b *GuestfsBackend) Resize(targetSizeBytes int64) error {
	p := b.getProvisioner()
	p.Resize(targetSizeBytes)
	return nil
}

// SetHostname queues hostname + /etc/hosts setup.
func (b *GuestfsBackend) SetHostname(hostname string) error {
	p := b.getProvisioner()
	p.SetHostname(hostname)
	return nil
}

// InjectDNS queues DNS resolver injection.
func (b *GuestfsBackend) InjectDNS(dnsServer string) error {
	p := b.getProvisioner()
	p.InjectDNS(dnsServer)
	return nil
}

// SetupSSH queues SSH key, config, and host-key generation.
func (b *GuestfsBackend) SetupSSH(user string, sshPubkeys []string) error {
	p := b.getProvisioner()
	p.SetupSSH(user, sshPubkeys)
	return nil
}

// DisableCloudInit queues cloud-init datasource blocking + service masking.
func (b *GuestfsBackend) DisableCloudInit() error {
	p := b.getProvisioner()
	p.DisableCloudInit()
	return nil
}

// InjectCloudInit queues cloud-init seed directory injection.
func (b *GuestfsBackend) InjectCloudInit(cloudInitDir string) error {
	p := b.getProvisioner()
	p.InjectCloudInit(cloudInitDir)
	return nil
}

// DetectOS detects OS type from the rootfs by reading /etc/os-release
// (or /usr/lib/os-release as fallback) via a read-only guestfish session.
// Matches Python's _GuestfsBackend.detect_os() which checks both paths.
func (b *GuestfsBackend) DetectOS() (string, error) {
	osReleaseContent := ""

	out, err := runGuestfish(b.ctx, b.rootfsPath, true, "read-file", "/etc/os-release")
	if err == nil {
		osReleaseContent = out
	}

	if osReleaseContent == "" {
		out2, err2 := runGuestfish(b.ctx, b.rootfsPath, true, "read-file", "/usr/lib/os-release")
		if err2 == nil {
			osReleaseContent = out2
		}
	}

	if osReleaseContent == "" {
		logger.Debug("OS detection via guestfs failed, falling back to 'linux'")
		return "linux", nil
	}

	idVal := ""
	for _, line := range strings.Split(osReleaseContent, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			idVal = strings.Trim(strings.TrimPrefix(line, "ID="), "\"'")
			idVal = strings.ToLower(idVal)
			break
		}
	}

	if idVal != "" {
		return idVal, nil
	}
	return "linux", nil
}

// Deblob queues OS cache cleanup + fstab fix operation.
// osType is accepted for interface compatibility with the loop-mount backend
// but is ignored — guestfs detects the OS internally.
// Matches Python's _GuestfsBackend.deblob(self, os_type=None).
func (b *GuestfsBackend) Deblob(osType string) error {
	p := b.getProvisioner()
	p.Deblob()
	return nil
}

// FixFstab queues fstab fix for Firecracker (PARTUUID -> /dev/vda).
func (b *GuestfsBackend) FixFstab() error {
	p := b.getProvisioner()
	p.FixFstab()
	return nil
}

// Shrink queues filesystem shrink to minimum size.
func (b *GuestfsBackend) Shrink() error {
	p := b.getProvisioner()
	p.Shrink()
	return nil
}

// ExtractPartition extracts root partition from a raw disk image.
// Delegates to guestfs.ExtractPartition() via the guestfs package.
// Matches Python's _GuestfsBackend.extract_partition() exactly.
func (b *GuestfsBackend) ExtractPartition(
	rawPath string,
	outputPath string,
	partition int,
	disabledDetectors []string,
) (string, error) {
	// Convert partition int to *int (0 or less = nil = auto-detect).
	// Matches Python's `partition: int | None = None` parameter.
	var partitionPtr *int
	if partition > 0 {
		partitionPtr = &partition
	}

	result, err := guestfs.ExtractPartition(b.ctx, rawPath, outputPath, partitionPtr)
	if err != nil {
		return "", fmt.Errorf("Guestfs partition extraction failed: %w", err)
	}
	if result == "" {
		return "", fmt.Errorf("Guestfs partition extraction failed")
	}
	return result, nil
}

// ConvertTo converts the image filesystem to targetFS using guestfish.
func (b *GuestfsBackend) ConvertTo(targetFS string) error {
	p := b.getProvisioner()
	return p.ConvertTo(b.ctx, targetFS)
}

// Run executes all queued operations in a single guestfish session.
func (b *GuestfsBackend) Run() error {
	p := b.getProvisioner()
	return p.Run(b.ctx)
}

// =========================================================================
// RootPartitionDetector — inline helper for partition detection
// =========================================================================

// PartitionEntry represents a parsed partition entry.
type PartitionEntry struct {
	Start  int64  `json:"start"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	Node   string `json:"node"`
	Fstype string `json:"fstype,omitempty"`
}

// ParseResult is the result of partition table parsing.
// Can be a list of partitions + requested partition number, or NoPartitionTable sentinel.
type ParseResult struct {
	Partitions         []PartitionEntry
	RequestedPartition int
	NoPartitionTable   bool
}

// RootPartitionDetect selects the root partition from multiple candidates.
// Matches Python's RootPartitionDetector from utils/_disk.py.
func RootPartitionDetect(partitions []PartitionEntry, disabledDetectors []string) int {
	for i, p := range partitions {
		fsType := strings.ToLower(p.Fstype)
		if fsType == "ext4" || fsType == "ext3" || fsType == "ext2" ||
			fsType == "btrfs" || fsType == "xfs" || p.Type == "83" {
			return i + 1 // 1-based index
		}
	}
	if len(partitions) > 0 {
		return 1
	}
	return -1
}

// ── Guestfish helpers (avoiding direct import of internal/infra/guestfs) ──

// runGuestfish runs a guestfish command with inspect mode.
// Matches guestfs.RunGuestfishInspect but local to break import cycle.
func runGuestfish(ctx context.Context, diskPath string, readonly bool, args ...string) (string, error) {
	guestfishArgs := []string{}
	if readonly {
		guestfishArgs = append(guestfishArgs, "--ro")
	}
	guestfishArgs = append(guestfishArgs, "-a", diskPath, "--cachemode", "writeback", "-i")
	guestfishArgs = append(guestfishArgs, args...)

	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	result := system.RunCmdCompat(ctx, append([]string{"guestfish"}, guestfishArgs...), opts)
	if result.ExitCode != 0 {
		return "", fmt.Errorf("guestfish: %s", strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}
