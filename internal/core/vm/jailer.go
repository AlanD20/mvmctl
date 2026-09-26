package vm

import (
	"context"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
)

func (s *FirecrackerSpawner) vmID() string {
	return filepath.Base(filepath.Clean(s.config.VMDir))
}

func jailedRuntimePath(hostPath string) string {
	return filepath.Join("/run/mvm", filepath.Base(hostPath))
}

func jailedVolumePath(driveID string) string {
	return filepath.Join("/volumes", driveID+".raw")
}

func translateConfigForJail(config *model.FirecrackerVMConfig) *model.FirecrackerVMConfig {
	translated := *config
	translated.BootSource = config.BootSource
	translated.BootSource.KernelImagePath = "/kernel"
	translated.Drives = make([]model.DriveConfig, len(config.Drives))
	for i, drive := range config.Drives {
		translated.Drives[i] = drive
		switch drive.DriveID {
		case "rootfs":
			translated.Drives[i].PathOnHost = "/rootfs"
		case "cloud-init":
			translated.Drives[i].PathOnHost = "/cloud-init.iso"
		default:
			translated.Drives[i].PathOnHost = jailedVolumePath(drive.DriveID)
		}
	}
	if config.Logger != nil {
		logger := *config.Logger
		logger.LogPath = jailedRuntimePath(logger.LogPath)
		translated.Logger = &logger
	}
	if config.Metrics != nil {
		metrics := *config.Metrics
		metrics.MetricsPath = jailedRuntimePath(metrics.MetricsPath)
		translated.Metrics = &metrics
	}
	if config.Vsock != nil {
		vsock := *config.Vsock
		vsock.UDSPath = filepath.Join("/vmm-runtime", infra.VMVsockSocketFilename)
		translated.Vsock = &vsock
	}
	return &translated
}

// CleanupJail removes all mounts and chroot state for one VM.
func CleanupJail(ctx context.Context, vmID string) error {
	_, err := jailersvc.NewVMLifecycleClient().Cleanup(ctx, jailersvc.VMCleanupRequest{VMID: vmID})
	return err
}

// CgroupPath returns the fixed cgroup-v2 path for one canonical Jailer launch.
func CgroupPath(vmID string) string {
	return filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent, vmID)
}
