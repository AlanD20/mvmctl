package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
)

func (s *FirecrackerSpawner) writeJailerManifest() error {
	volumes := make([]jailersvc.VolumeMount, 0, len(s.config.ExtraDrives))
	for _, drive := range s.config.ExtraDrives {
		volumes = append(volumes, jailersvc.VolumeMount{
			DriveID: drive.DriveID, HostPath: drive.PathOnHost, ReadOnly: drive.IsReadOnly,
		})
	}
	isoPath := ""
	if s.config.CloudInitISOPath != nil {
		isoPath = *s.config.CloudInitISOPath
	}
	manifest := jailersvc.LaunchManifest{
		VMID: s.vmID(), VMDir: s.config.VMDir, Version: s.config.BinaryVersion,
		ConfigPath: s.config.ConfigPath, PIDPath: s.config.PIDPath,
		KernelPath: s.config.KernelPath, RootfsPath: s.config.RootfsPath,
		ISOPath: isoPath, SnapshotDir: s.config.SnapshotDir,
		Volumes: volumes, APISocket: s.config.APISocketPath,
		PCIEnabled: s.config.PCIEnabled, SnapshotMode: s.config.SnapshotMode,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal jailed launch manifest: %w", err)
	}
	path := filepath.Join(s.config.VMDir, infra.JailerManifestFilename)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write jailed launch manifest: %w", err)
	}
	return nil
}

func (s *FirecrackerSpawner) vmID() string {
	return filepath.Base(filepath.Clean(s.config.VMDir))
}

func jailedRuntimePath(hostPath string) string {
	return filepath.Join("/run/mvm", filepath.Base(hostPath))
}

func jailedVolumePath(driveID string) string {
	return filepath.Join("/volumes", driveID)
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
		vsock.UDSPath = jailedRuntimePath(vsock.UDSPath)
		translated.Vsock = &vsock
	}
	return &translated
}

// ExposeSnapshot exposes one managed snapshot directory inside an existing VM jail.
func ExposeSnapshot(ctx context.Context, vmID, snapshotDir string) error {
	return jailersvc.ExposeSnapshot(ctx, vmID, snapshotDir)
}

// CleanupJail removes all mounts and chroot state for one VM.
func CleanupJail(ctx context.Context, vmID string) error {
	return jailersvc.Cleanup(ctx, vmID)
}

func exposeVolume(ctx context.Context, vmID string, drive model.DriveConfig) error {
	return jailersvc.ExposeVolume(ctx, vmID, drive.DriveID, drive.PathOnHost, drive.IsReadOnly)
}

func removeVolume(ctx context.Context, vmID, driveID string) error {
	return jailersvc.RemoveVolume(ctx, vmID, driveID)
}
