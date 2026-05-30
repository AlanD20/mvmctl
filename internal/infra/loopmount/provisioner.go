package loopmount

import (
	"context"
	"log/slog"
	"os"

	"mvmctl/internal/infra/provisionercontent"

	loopmountsvc "mvmctl/internal/service/loopmount"
)

// LoopMountProvisioner accumulates provisioning operations and executes
// them via the loopmount service layer (internal/service/loopmount).
//
// All content generation is delegated to the shared provisionercontent.ProvisionerContent
// builders. This class only stores the pre-built ops and serializes them to the
// service types expected by the loopmount provisioner.
//
// Matches Python's LoopMountProvisioner in _loopmount/_provisionercontent.py.
type LoopMountProvisioner struct {
	rootfsPath string
	fsType     string
	cacheDir   string
	ops        []provisionercontent.Operation
}

// NewLoopMountProvisioner creates a new LoopMountProvisioner.
// Matches Python's __init__().
func NewLoopMountProvisioner(rootfsPath string, fsType string, cacheDir string) *LoopMountProvisioner {
	return &LoopMountProvisioner{
		rootfsPath: rootfsPath,
		fsType:     fsType,
		cacheDir:   cacheDir,
		ops:        nil,
	}
}

// ---------------------------------------------------------------------------
// Builder methods — queue provisioning operations
// ---------------------------------------------------------------------------

// Resize queues a rootfs resize operation.
// Matches Python's resize().
func (lp *LoopMountProvisioner) Resize(targetSizeBytes int64) {
	pc := provisionercontent.ProvisionerContent{}
	lp.ops = append(lp.ops, pc.BuildResizeOps(targetSizeBytes)...)
}

// SetHostname queues hostname + /etc/hosts setup.
// Matches Python's set_hostname().
func (lp *LoopMountProvisioner) SetHostname(hostname string) {
	pc := provisionercontent.ProvisionerContent{}
	lp.ops = append(lp.ops, pc.BuildHostnameOps(hostname)...)
}

// InjectDNS queues DNS resolver injection.
// Matches Python's inject_dns().
func (lp *LoopMountProvisioner) InjectDNS(dnsServer string) {
	pc := provisionercontent.ProvisionerContent{}
	lp.ops = append(lp.ops, pc.BuildDNSOps(dnsServer)...)
}

// SetupSSH queues SSH key, config, and host-key generation.
// Matches Python's setup_ssh().
func (lp *LoopMountProvisioner) SetupSSH(user string, sshPubkeys []string) {
	pc := provisionercontent.ProvisionerContent{}
	lp.ops = append(lp.ops, pc.BuildSSHOps(user, sshPubkeys)...)
}

// DisableCloudInit is a no-op at VM creation time.
// Cloud-init is disabled at image import time via BuildDeblobOps.
// Matches Python's disable_cloud_init().
func (lp *LoopMountProvisioner) DisableCloudInit() {
	// No-op — cloud-init is disabled during image import, not VM creation
}

// InjectCloudInit queues cloud-init seed directory injection.
// Matches Python's inject_cloud_init().
func (lp *LoopMountProvisioner) InjectCloudInit(cloudInitDir string) {
	pc := provisionercontent.ProvisionerContent{}
	lp.ops = append(lp.ops, pc.BuildCloudInitInjectOps(cloudInitDir)...)
}

// ---------------------------------------------------------------------------
// Filesystem conversion
// ---------------------------------------------------------------------------

// ConvertTo converts the image to a different filesystem type in-place.
// This is an independent action — it bypasses the regular provisioning ops flow
// and calls the loopmount service directly.
//
// Matches Python's convert_to().
func (lp *LoopMountProvisioner) ConvertTo(ctx context.Context, targetFS string) (map[string]any, error) {
	svc := loopmountsvc.NewProvisioner(lp.cacheDir)
	results, err := svc.Execute(ctx, []loopmountsvc.Op{
		{
			Image:    lp.rootfsPath,
			Action:   "convert_fs",
			TargetFS: targetFS,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	r := results[0]
	return map[string]any{
		"new_fs_type":    r.NewFSType,
		"new_size_bytes": r.NewSizeBytes,
	}, nil
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// Run executes all queued operations via the loopmount service.
// Matches Python's run() exactly — always invokes the provisioner even with empty ops.
func (lp *LoopMountProvisioner) Run(ctx context.Context) error {
	// Convert queued operations to service types
	op := loopmountsvc.Op{
		Image:  lp.rootfsPath,
		FsType: lp.fsType,
	}

	for _, o := range lp.ops {
		switch o := o.(type) {
		case provisionercontent.FileOp:
			mode := o.Mode
			if mode == 0 {
				mode = 0644
			}
			op.Files = append(op.Files, loopmountsvc.FileOp{
				Path: o.Path,
				Data: o.Data,
				Mode: os.FileMode(mode),
				UID:  o.UID,
				GID:  o.GID,
			})
		case provisionercontent.ChrootOp:
			op.Commands = append(op.Commands, o.Command)
		case provisionercontent.CopyDirOp:
			op.CopyDirs = append(op.CopyDirs, loopmountsvc.CopyDirOp{
				Src:  o.Src,
				Dst:  o.Dst,
				Mode: 0755,
			})
		case provisionercontent.ResizeOp:
			op.Resize = &loopmountsvc.ResizeOp{
				Action: string(o.Action),
				Bytes:  o.Bytes,
			}
		}
	}

	svc := loopmountsvc.NewProvisioner(lp.cacheDir)
	_, err := svc.Execute(ctx, []loopmountsvc.Op{op})
	if err != nil {
		// Pass through the error from Execute without wrapping — matches Python's
		// LoopMountProvisioner.run() which lets errors propagate as-is.
		return err
	}

	slog.Info("Loop-mount provisioning succeeded")
	return nil
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

// CleanupMount unmounts and removes a stale provision mount point.
// Matches Python's cleanup_mount() static method.
func CleanupMount(ctx context.Context, cacheDir string, mountPoint string) bool {
	return loopmountsvc.CleanupMount(mountPoint)
}

// QueueOps appends raw operation objects to the internal ops queue.
// Matches Python's _queue_ops() in _backend.py which does self._lp._ops.extend(ops).
func (lp *LoopMountProvisioner) QueueOps(ops []provisionercontent.Operation) {
	lp.ops = append(lp.ops, ops...)
}

// RootfsPath returns the rootfs path for this provisionercontent.
func (lp *LoopMountProvisioner) RootfsPath() string {
	return lp.rootfsPath
}

// FsType returns the filesystem type for this provisionercontent.
func (lp *LoopMountProvisioner) FsType() string {
	return lp.fsType
}
