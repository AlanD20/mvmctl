package provisioner

import (
	"context"
	"fmt"

	"mvmctl/internal/lib/provisioner/guestfs"
	"mvmctl/internal/lib/provisioner/loopmount"
)

type ProvisionerType string

const (
	ProvisionerLoopMount ProvisionerType = "loop_mount"
	ProvisionerGuestFS   ProvisionerType = "guestfs"
)

// Backend interface for rootfs provisioning.
// Matches the methods exposed by _LoopMountBackend and _GuestfsBackend in Python.
// Every method takes a context.Context as the first parameter (rule #16).
type Backend interface {
	Resize(ctx context.Context, targetSizeBytes int64) error
	SetHostname(ctx context.Context, hostname string) error
	InjectDNS(ctx context.Context, dnsServer string) error
	SetupSSH(ctx context.Context, user string, sshPubkeys []string) error
	SetupSudo(ctx context.Context, user string) error
	DisableCloudInit(ctx context.Context) error
	InjectCloudInit(ctx context.Context, cloudInitDir string) error
	DetectOS(ctx context.Context) (string, error)
	// Deblob queues OS cache cleanup operations.
	// osType is the detected OS identifier (e.g. "ubuntu", "alpine", "arch").
	// If nil, the backend should detect the OS first (matching Python's
	// _LoopMountBackend.deblob(self, os_type=None) which calls self.detect_os()).
	// GuestfsBackend ignores osType (it detects internally).
	Deblob(ctx context.Context, osType *string) error
	FixFstab(ctx context.Context) error
	Shrink(ctx context.Context) error
	ExtractPartition(
		ctx context.Context,
		rawPath, outputPath string,
		partition int,
		disabledDetectors []string,
	) (string, error)
	ConvertTo(ctx context.Context, targetFS string) error
	Run(ctx context.Context) error
	// InjectVsockAgent queues the vsock guest agent binary, auth token, and
	// init system integration files into the rootfs. Called during VM creation
	// to inject the agent before first boot.
	InjectVsockAgent(ctx context.Context, agentBinary []byte, port int, token string) error
}

// BackendOpts configures a backend via NewBackend.
// RootUID/RootGID/UserUID/UserGID are only meaningful for guestfs backends;
// loopmount ignores them.
type BackendOpts struct {
	RootfsPath      string
	FsType          string
	CacheDir        string
	ProvisionerType ProvisionerType
	RootUID         int
	RootGID         int
	UserUID         int
	UserGID         int
}

// NewBackend constructs the correct Backend for the given provisioner type.
func NewBackend(ctx context.Context, opts BackendOpts) (Backend, error) {
	switch opts.ProvisionerType {
	case ProvisionerLoopMount:
		return loopmount.NewLoopMountBackend(ctx, opts.RootfsPath, opts.FsType, opts.CacheDir), nil
	case ProvisionerGuestFS:
		if err := guestfs.EnsureAppliance(opts.CacheDir); err != nil {
			return nil, err
		}
		return guestfs.NewGuestfsBackend(
			opts.RootfsPath,
			opts.RootUID,
			opts.RootGID,
			opts.UserUID,
			opts.UserGID,
		), nil
	default:
		return nil, fmt.Errorf("provisioner: unknown provisioner type: %s", opts.ProvisionerType)
	}
}
