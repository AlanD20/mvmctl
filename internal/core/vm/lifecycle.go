package vm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

// RuntimePath is a display/client connection path, never process or filesystem authority.
func RuntimePath(ownerUID int, vmID, leaf string) string {
	return filepath.Join("/run/mvmctl/runtime", strconv.Itoa(ownerUID), vmID, leaf)
}

// Spawn executes only the typed root lifecycle. The normal process never inherits
// root descriptors, installs mounts, or treats a PID projection as signaling authority.
func (s *FirecrackerSpawner) Spawn(ctx context.Context) (returnErr error) {
	request, err := s.lifecycleRequest()
	if err != nil {
		return err
	}
	client := jailersvc.NewVMLifecycleClient()
	result, err := client.Launch(ctx, request)
	if err != nil {
		return err
	}
	s.PID = &result.PID
	start := int64(result.StartTimeTicks)
	s.ProcessStartTime = &start
	s.APISocketPath = RuntimePath(os.Geteuid(), s.vmID(), infra.VMFirecrackerAPISocketFilename)
	s.config.APISocketPath = s.APISocketPath
	defer func() {
		if returnErr != nil {
			_, abortErr := client.Abort(context.WithoutCancel(ctx), jailersvc.VMCleanupRequest{VMID: s.vmID()})
			returnErr = errors.Join(returnErr, abortErr)
		}
	}()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, dialErr := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "unix", s.APISocketPath)
		if dialErr == nil {
			return connection.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errs.WrapMsg(
				errs.CodeFirecrackerSpawnError,
				"committed Firecracker API did not become ready",
				dialErr,
			)
		case <-ticker.C:
		}
	}
}

func (s *FirecrackerSpawner) lifecycleRequest() (jailersvc.VMLaunchRequest, error) {
	if s.config.SnapshotMode || s.config.SnapshotDir != "" {
		return jailersvc.VMLaunchRequest{}, errs.New(
			errs.CodeFirecrackerSpawnError,
			"snapshot launch requires admitted snapshot capture authority",
		)
	}
	config, err := s.Generate()
	if err != nil {
		return jailersvc.VMLaunchRequest{}, err
	}
	rootfs, err := os.Stat(s.config.RootfsPath)
	if err != nil {
		return jailersvc.VMLaunchRequest{}, err
	}
	if rootfs.Size() <= 0 {
		return jailersvc.VMLaunchRequest{}, fmt.Errorf("managed rootfs is empty")
	}
	volumes, err := s.lifecycleVolumes()
	if err != nil {
		return jailersvc.VMLaunchRequest{}, err
	}
	request := jailersvc.VMLaunchRequest{
		VMID:      s.vmID(),
		CacheRoot: s.config.CacheRoot,
		KernelID:  s.config.KernelID,
		RootfsBytes: uint64(
			rootfs.Size(),
		),
		Release: jailersvc.ReleaseSlot{
			Version:      s.config.BinaryVersion,
			Architecture: s.config.ReleaseArchitecture,
		},
		NetworkID:       s.config.NetworkID,
		GuestIPv4:       s.config.GuestIP,
		GuestMAC:        s.config.GuestMAC,
		BootArgs:        config.BootSource.BootArgs,
		VCPUs:           config.MachineConfig.VCPUCount,
		MemoryMiB:       config.MachineConfig.MemSizeMiB,
		SMT:             config.MachineConfig.SMT,
		TrackDirtyPages: config.MachineConfig.TrackDirtyPages,
		RootfsCacheType: config.Drives[0].CacheType,
		CloudInit:       s.config.CloudInitISOPath != nil && *s.config.CloudInitISOPath != "",
		Logging:         s.config.EnableLogging,
		LogLevel:        s.config.LogLevel,
		Metrics:         s.config.EnableMetrics,
		Console:         s.config.EnableConsole,
		CPUConfig:       config.CPUConfig,
		CgroupLimits:    s.config.CgroupLimits,
		Volumes:         volumes,
		PCIEnabled:      s.config.PCIEnabled,
		NestedVirt:      s.config.NestedVirt,
	}
	if s.config.VsockGuestCID != nil {
		cid := uint32(*s.config.VsockGuestCID)
		request.VsockCID = &cid
	}
	return request, request.Validate()
}

func (s *FirecrackerSpawner) lifecycleVolumes() ([]jailersvc.VMLaunchVolume, error) {
	volumes := make([]jailersvc.VMLaunchVolume, 0, len(s.config.ExtraDrives))
	for _, drive := range s.config.ExtraDrives {
		if len(drive.DriveID) != 64 || strings.ToLower(drive.DriveID) != drive.DriveID {
			return nil, errs.New(errs.CodeFirecrackerSpawnError, "volume launch requires a canonical volume ID")
		}
		if _, err := hex.DecodeString(drive.DriveID); err != nil {
			return nil, errs.WrapMsg(
				errs.CodeFirecrackerSpawnError,
				"volume launch requires a canonical volume ID",
				err,
			)
		}
		selection := jailersvc.VMLaunchVolume{
			ID: drive.DriveID, Format: model.VolumeFormatRaw,
			ReadOnly: drive.IsReadOnly, CacheType: drive.CacheType,
		}
		canonical := filepath.Join(s.config.CacheRoot, "volumes", drive.DriveID+".raw")
		if filepath.Clean(drive.PathOnHost) != canonical || drive.IsRootDevice ||
			drive.Partuuid != nil || drive.IOEngine != "Sync" || drive.RateLimiter != nil || drive.Socket != nil {
			return nil, errs.New(errs.CodeFirecrackerSpawnError, "volume launch requires a canonical raw data drive")
		}
		// Caller metadata supplies expected-size intent only. Root pins the canonical file
		// independently and requires exact length, ownership, and resource identity at handoff.
		info, err := os.Lstat(canonical)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeFirecrackerSpawnError, "inspect canonical raw launch volume", err)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil, errs.New(errs.CodeFirecrackerSpawnError, "volume launch requires a nonempty regular raw file")
		}
		selection.ExpectedBytes = uint64(info.Size())
		if err := selection.Validate(); err != nil {
			return nil, err
		}
		volumes = append(volumes, selection)
	}
	return volumes, nil
}
