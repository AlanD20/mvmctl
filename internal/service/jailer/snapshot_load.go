package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/service/network"
)

func (prepared *preparedSnapshotRestore) loadIntoActive(
	ctx context.Context,
	active *activeLease,
	topology network.JailerNetworkConfig,
) (returnErr error) {
	if prepared.released || !prepared.registered || active == nil {
		return instanceValidationError("snapshot restore launch capability is inactive")
	}
	record := active.snapshot()
	source := prepared.metadata.Source
	if record.launch == nil || record.active == nil || record.vmID != prepared.targetVMID || record.ownerUID != prepared.ownerUID ||
		record.launch.snapshotID != prepared.snapshotID || record.launch.release.version != source.Release.Version ||
		record.launch.release.architecture != source.Release.Architecture || record.launch.release.firecrackerSHA256 != source.FirecrackerSHA256 ||
		record.launch.release.jailerSHA256 != source.JailerSHA256 || topology.OwnerUID != prepared.ownerUID || topology.VMID != prepared.targetVMID ||
		topology.NetworkID != source.NetworkID ||
		topology.GuestIPv4.String() != source.GuestIPv4 ||
		topology.GuestMAC != source.GuestMAC ||
		!managedVMHostDevicePattern.MatchString(topology.TAPName) {
		return instanceValidationError("snapshot restore process/release/endpoint differs from captured authority")
	}
	processes, err := reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, processes.Release(context.WithoutCancel(ctx))) }()
	if _, err := verifyRecoveredActiveIdentity(ctx, realProcessIdentityVerifierDeps(), record); err != nil {
		return err
	}
	if err := processes.verifyLocators(ctx); err != nil {
		return err
	}
	session, err := awaitSnapshotAPISession(ctx, record, processes)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, session.Release(context.WithoutCancel(ctx))) }()
	if prepared.metadata.Mode == "disks" {
		// Disk-only capture has no CPU state to resume. This is an ordinary cold boot;
		// --no-resume pauses that boot once the admitted API is ready.
		if !prepared.resume {
			if err := session.pause(ctx); err != nil {
				return err
			}
		}
		// Independent cold-boot disks no longer depend on capture artifacts. Only
		// a full restore retains immutable guest memory for its VM lifetime.
		if err := removeSnapshotReference(
			ctx,
			prepared.ownerUID,
			prepared.cacheIdentity,
			prepared.snapshotID,
			prepared.targetVMID,
		); err != nil {
			return err
		}
		prepared.referenceCreated = false
		return nil
	}
	if prepared.memoryFD < 0 || prepared.stateFD < 0 {
		return instanceValidationError("full restore lacks immutable memory/state descriptors")
	}
	// Logging and metrics are process configuration, not guest snapshot state. The
	// full restore child deliberately did not consume --config-file.
	if source.Config.Logger != nil {
		if source.Config.Logger.LogPath != managedVMPersistentMountPath+"/"+infra.VMFirecrackerLogFilename {
			return instanceValidationError("captured logger path is outside the fixed output layout")
		}
		body, err := json.Marshal(source.Config.Logger)
		if err != nil {
			return err
		}
		if _, err := session.exchange(ctx, http.MethodPut, "/logger", body, http.StatusNoContent); err != nil {
			return err
		}
	}
	if source.Config.Metrics != nil {
		if source.Config.Metrics.MetricsPath != managedVMPersistentMountPath+"/"+infra.VMFirecrackerMetricsFilename {
			return instanceValidationError("captured metrics path is outside the fixed output layout")
		}
		body, err := json.Marshal(source.Config.Metrics)
		if err != nil {
			return err
		}
		if _, err := session.exchange(ctx, http.MethodPut, "/metrics", body, http.StatusNoContent); err != nil {
			return err
		}
	}
	type networkOverride struct {
		IfaceID     string `json:"iface_id"`
		HostDevName string `json:"host_dev_name"`
	}
	type memoryBackend struct {
		Type string `json:"backend_type"`
		Path string `json:"backend_path"`
	}
	type vsockOverride struct {
		Path string `json:"uds_path"`
	}
	request := struct {
		State           string            `json:"snapshot_path"`
		Memory          memoryBackend     `json:"mem_backend"`
		TrackDirtyPages bool              `json:"track_dirty_pages"`
		Resume          bool              `json:"resume_vm"`
		Networks        []networkOverride `json:"network_overrides"`
		Vsock           *vsockOverride    `json:"vsock_override,omitempty"`
	}{
		State:           "/snapshot/vmstate",
		Memory:          memoryBackend{Type: "File", Path: "/snapshot/memory"},
		TrackDirtyPages: source.Config.MachineConfig.TrackDirtyPages,
		Resume:          prepared.resume,
		Networks: []networkOverride{
			{IfaceID: source.Config.NetworkInterfaces[0].IfaceID, HostDevName: topology.TAPName},
		},
	}
	if source.Config.Vsock != nil {
		request.Vsock = &vsockOverride{Path: managedVMPrivateRuntimeMountPath + "/" + infra.VMVsockSocketFilename}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = session.exchange(ctx, http.MethodPut, "/snapshot/load", body, http.StatusNoContent)
	return err
}

func awaitSnapshotAPISession(
	ctx context.Context,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
) (*snapshotAPISession, error) {
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		session, err := openSnapshotAPISession(ready, record, processes)
		if err == nil {
			return session, nil
		}
		// Only absence before any request is retryable. A peer mismatch or a possibly
		// applied mutation never causes a reconnect/replay.
		if !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ECONNREFUSED) {
			return nil, err
		}
		select {
		case <-ready.Done():
			return nil, errors.Join(ready.Err(), err)
		case <-ticker.C:
		}
	}
}
