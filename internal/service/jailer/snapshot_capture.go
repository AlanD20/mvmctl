package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

// Capture authenticates the producer before pausing, and keeps every backing lease
// exclusively held until one immutable capture is published. No caller disk copy is used.
func (server *VMSnapshotServer) Capture(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMSnapshotCaptureRequest,
) (_ model.SnapshotCaptureResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	caller := instanceCaller{uid: ownerUID, gid: ownerGID}
	locked, err := server.instances.lockOwnedInstance(ctx, caller, request.VMID)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, locked.Release(context.WithoutCancel(ctx))) }()
	record := locked.record
	if (record.lifecycle != instanceLifecycleActive && record.lifecycle != instanceLifecycleCleaned) ||
		(record.lifecycle == instanceLifecycleActive && record.launch == nil) {
		return model.SnapshotCaptureResult{}, instanceStateError(
			request.VMID,
			"snapshot requires an active or fully cleaned source",
		)
	}
	cache, err := pinSnapshotCache(ctx, caller, request.CacheRoot)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	var source vmLaunchDeviceAuthority
	var session *snapshotAPISession
	rootFD := -1
	if record.lifecycle == instanceLifecycleActive {
		processes, err := reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, processes.Release(context.WithoutCancel(ctx))) }()
		if _, err := verifyRecoveredActiveIdentity(ctx, realProcessIdentityVerifierDeps(), record); err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		if err := processes.verifyLocators(ctx); err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		rootFD, err = pinSnapshotActiveRoot(ctx, record)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, unix.Close(rootFD)) }()
		source, err = readVMLaunchDeviceAuthority(ctx, record, rootFD)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		source.Volumes, err = readCurrentVMVolumeInventory(ctx, record, rootFD)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		if err := updateSnapshotDeviceConfig(&source); err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		session, err = openSnapshotAPISession(ctx, record, processes)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, session.Release(context.WithoutCancel(ctx))) }()
	} else {
		source, err = readStoppedVMLaunchDeviceAuthority(ctx, record)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
	}
	if protectedCacheIdentity(cache.identity) != protectedCacheIdentity(source.Rootfs.cacheIdentity) {
		return model.SnapshotCaptureResult{}, instanceStateError(
			request.VMID,
			"snapshot cache differs from recorded source",
		)
	}
	backings, err := lockSnapshotBackings(ctx, record, source)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() {
		for i := len(backings) - 1; i >= 0; i-- {
			returnErr = errors.Join(returnErr, backings[i].lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	store, err := openProtectedSnapshotStore(ctx, cache, true)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	entry, err := lockSnapshotStoreEntry(ctx, store, request.SnapshotID, true)
	if errors.Is(err, unix.EEXIST) {
		existing, openErr := lockSnapshotStoreEntry(ctx, store, request.SnapshotID, false)
		if openErr != nil {
			return model.SnapshotCaptureResult{}, openErr
		}
		prior, readErr := existing.readProgress(ctx)
		closeErr := existing.Release(context.WithoutCancel(ctx))
		if readErr != nil || closeErr != nil {
			return model.SnapshotCaptureResult{}, errors.Join(readErr, closeErr)
		}
		if prior.SourceVMID != request.VMID || prior.OwnerUID != ownerUID {
			return model.SnapshotCaptureResult{}, instanceValidationError(
				"existing snapshot progress belongs to another source",
			)
		}
		prior.VMState = "unknown" // A durable past observation is not current process state.
		return model.SnapshotCaptureResult{}, snapshotCaptureOutcomeError(
			prior,
			instanceValidationError(
				"snapshot identity already has a capture transaction; inspect or remove it before creating a new capture",
			),
		)
	}
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.Release(context.WithoutCancel(ctx))) }()
	progress := snapshotCaptureProgress{SnapshotID: request.SnapshotID, SourceVMID: request.VMID, OwnerUID: ownerUID,
		LaunchGeneration: record.launchGeneration, Stage: "prepared", VMState: "stopped"}
	if session != nil {
		progress.VMState = "unknown"
	}
	if err := entry.writeProgress(ctx, progress); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = snapshotCaptureOutcomeError(progress, returnErr)
		}
	}()
	capture := snapshotCaptureRecord{
		Schema:     1,
		SnapshotID: request.SnapshotID,
		OwnerUID:   ownerUID,
		Mode:       "disks",
		Source:     source,
	}
	wasRunning := false
	producerFD := -1
	if session != nil {
		state, err := session.state(ctx)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		wasRunning = state == "Running"
		progress.VMState = "paused"
		if wasRunning {
			progress.Stage, progress.VMState = "pause_inflight", "unknown"
			if err := entry.writeProgress(ctx, progress); err != nil {
				return model.SnapshotCaptureResult{}, err
			}
			if err := session.pause(ctx); err != nil {
				return model.SnapshotCaptureResult{}, err
			}
		}
		progress.Stage, progress.VMState = "paused", "paused"
		if err := entry.writeProgress(ctx, progress); err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		// An uncertain or failed operation deliberately leaves the source paused. No
		// deferred resume may race an unfinished capture after a broken API exchange.
		producerFD, err = prepareSnapshotProducerDirectory(ctx, session, request.SnapshotID)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, unix.Close(producerFD)) }()
		progress.Stage = "capture_inflight"
		if err := entry.writeProgress(ctx, progress); err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		if err := session.capture(ctx, request.SnapshotID); err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		capture.Mode = "full"
	}
	progress.Stage = "copying"
	if err := entry.writeProgress(ctx, progress); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	for _, backing := range backings {
		artifact, err := entry.copyArtifact(ctx, backing.name, backing.lease.fd, backing.binding.bytes)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		capture.Artifacts = append(capture.Artifacts, artifact)
	}
	if session != nil {
		memoryBytes, err := managedSnapshotMemoryBytes(source.Config.MachineConfig.MemSizeMiB)
		if err != nil {
			return model.SnapshotCaptureResult{}, err
		}
		for _, leaf := range []string{"memory", "vmstate"} {
			artifact, err := copySnapshotProducerArtifact(ctx, entry, producerFD, leaf, record, memoryBytes)
			if err != nil {
				return model.SnapshotCaptureResult{}, err
			}
			capture.Artifacts = append(capture.Artifacts, artifact)
		}
	}
	progress.Stage = "publishing"
	if err := entry.writeProgress(ctx, progress); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	if err := entry.publish(ctx, capture); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	progress.Stage, progress.Published = "published", true
	if err := entry.writeProgress(ctx, progress); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	paused := session != nil
	if session != nil {
		if err := removeSnapshotProducerDirectory(ctx, session, producerFD, request.SnapshotID); err != nil {
			return capture.projection(paused), err
		}
		if wasRunning && !request.PauseOnly {
			progress.Stage, progress.VMState = "resume_inflight", "unknown"
			if err := entry.writeProgress(ctx, progress); err != nil {
				return capture.projection(true), err
			}
			if err := session.resume(ctx); err != nil {
				return capture.projection(true), err
			}
			paused = false
			progress.VMState = "running"
		}
	}
	progress.Stage = "complete"
	if err := entry.writeProgress(ctx, progress); err != nil {
		return capture.projection(paused), err
	}
	return capture.projection(paused), nil
}

type snapshotCapturedBacking struct {
	name    string
	binding protectedBackingBinding
	lease   *protectedBackingLease
}

func lockSnapshotBackings(
	ctx context.Context,
	record instanceRecord,
	source vmLaunchDeviceAuthority,
) (_ []snapshotCapturedBacking, returnErr error) {
	items := []snapshotCapturedBacking{
		{name: "rootfs.img", binding: source.Rootfs},
		{name: "kernel", binding: source.KernelBacking},
	}
	if source.CloudInit {
		if source.CloudInitBacking == nil {
			return nil, instanceValidationError("snapshot cloud-init backing is absent")
		}
		items = append(items, snapshotCapturedBacking{name: "cloud-init.iso", binding: *source.CloudInitBacking})
	}
	for _, volume := range source.Volumes {
		items = append(
			items,
			snapshotCapturedBacking{name: "volume-" + volume.GuestDriveID + ".raw", binding: volume.Backing},
		)
	}
	slices.SortFunc(items, func(a, b snapshotCapturedBacking) int {
		return strings.Compare(a.binding.kind+"/"+a.binding.logicalID, b.binding.kind+"/"+b.binding.logicalID)
	})
	defer func() {
		if returnErr != nil {
			for i := len(items) - 1; i >= 0; i-- {
				if items[i].lease != nil {
					returnErr = errors.Join(returnErr, items[i].lease.Release(context.WithoutCancel(ctx)))
				}
			}
		}
	}()
	for i := range items {
		if i > 0 && items[i-1].binding.kind == items[i].binding.kind &&
			items[i-1].binding.logicalID == items[i].binding.logicalID {
			return nil, instanceValidationError("snapshot device graph repeats a protected backing")
		}
		if items[i].binding.ownerUID != record.ownerUID ||
			protectedCacheIdentity(
				items[i].binding.cacheIdentity,
			) != protectedCacheIdentity(
				source.Rootfs.cacheIdentity,
			) {
			return nil, instanceValidationError("snapshot backing owner/cache differs from producer")
		}
		lease, err := reopenProtectedBacking(ctx, items[i].binding)
		if err != nil {
			return nil, err
		}
		items[i].lease = lease
		if record.lifecycle == instanceLifecycleCleaned {
			if err := lease.RequireStoppedCapture(ctx); err != nil {
				return nil, err
			}
		} else if err := lease.RequireExclusiveCapture(ctx, record.vmID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func updateSnapshotDeviceConfig(source *vmLaunchDeviceAuthority) error {
	base := 1
	if source.CloudInit {
		base++
	}
	if len(source.Config.Drives) < base {
		return instanceValidationError("snapshot producer fixed drives are incomplete")
	}
	source.Config.Drives = slices.Clone(source.Config.Drives[:base])
	for _, volume := range source.Volumes {
		source.Config.Drives = append(source.Config.Drives, managedFirecrackerDrive{
			DriveID: volume.GuestDriveID, PathOnHost: volume.Path, IsReadOnly: volume.Selection.ReadOnly,
			CacheType: volume.Selection.CacheType, IOEngine: "Sync",
		})
	}
	encoded, err := json.Marshal(source.Config)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	source.ConfigSHA256 = hex.EncodeToString(digest[:])
	return source.validate()
}

func pinSnapshotActiveRoot(ctx context.Context, record instanceRecord) (_ int, returnErr error) {
	deps := realProcessIdentityVerifierDeps()
	process, err := deps.openProcessDirectory(ctx, record.launch.process.pid)
	if err != nil {
		return -1, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(process)) }()
	root, err := deps.pinProcessObject(ctx, process, processRootObject)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(root, &stat)
	if err == nil &&
		(record.active == nil || uint64(stat.Dev) != record.active.jailRoot.device || stat.Ino != record.active.jailRoot.inode || stat.Uid != 0 || stat.Gid != 0 || stat.Mode != unix.S_IFDIR|0755) {
		err = instanceStateError(record.vmID, "snapshot source root differs from admitted process")
	}
	if err != nil {
		return -1, errors.Join(err, unix.Close(root))
	}
	return root, nil
}
