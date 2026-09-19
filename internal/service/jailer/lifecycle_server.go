package jailer

import (
	"context"
	"errors"
	"net/netip"

	"mvmctl/internal/service/network"
)

// VMLifecycleServer owns the fixed schema-3 lifecycle transaction. Caller identity
// is supplied separately by the authenticated privileged receiver, never by JSON.
type VMLifecycleServer struct {
	releases  *releaseAuthority
	instances *instanceAuthority
}

// NewVMLifecycleServer constructs the production root authority without effect hooks.
func NewVMLifecycleServer() *VMLifecycleServer {
	releases := newReleaseAuthority()
	return &VMLifecycleServer{releases: releases, instances: releases.instances}
}

// Launch registers the blocked child before any mutable launch effect, then admits
// and commits the exact running instance before relinquishing descriptor ownership.
func (server *VMLifecycleServer) Launch(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMLaunchRequest,
) (_ VMLaunchResult, returnErr error) {
	return server.launch(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request, nil)
}

func (server *VMLifecycleServer) launch(
	ctx context.Context,
	caller instanceCaller,
	request VMLaunchRequest,
	restore *preparedSnapshotRestore,
) (_ VMLaunchResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return VMLaunchResult{}, err
	}
	if err := validateInstanceCaller(caller); err != nil {
		return VMLaunchResult{}, err
	}
	locator, err := parseManagedCacheLocator(request.CacheRoot)
	if err != nil {
		return VMLaunchResult{}, err
	}
	cache, err := pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(), caller, locator)
	if err != nil {
		return VMLaunchResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release launch cache",
			cache.Release(context.WithoutCancel(ctx)),
		)
	}()
	if restore != nil &&
		(restore.ownerUID != caller.uid || restore.targetVMID != request.VMID || restore.cacheIdentity != cache.identity || restore.released) {
		return VMLaunchResult{}, instanceValidationError("snapshot restore authority does not match launch")
	}
	prepared, err := server.releases.prepareInstalled(ctx, request.Release.private())
	if err != nil {
		return VMLaunchResult{}, err
	}
	if restore != nil {
		identity := prepared.snapshot().identity
		if identity.firecrackerSHA256 != restore.metadata.Source.FirecrackerSHA256 ||
			identity.jailerSHA256 != restore.metadata.Source.JailerSHA256 {
			return VMLaunchResult{}, errors.Join(
				instanceValidationError("restore release differs from captured binaries"),
				prepared.Release(context.WithoutCancel(ctx)),
			)
		}
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release prepared launch release",
			prepared.Release(context.WithoutCancel(ctx)),
		)
	}()
	var deviceProfile *vmFirecrackerDeviceProfile
	if len(request.Volumes) != 0 || request.PCIEnabled || request.NestedVirt {
		deviceProfile, err = prepared.deviceProfile()
		if err != nil {
			return VMLaunchResult{}, err
		}
		intent := request.launchIntent(vmFirecrackerTapIdentity{})
		intent.deviceProfile = deviceProfile
		if err := validateVMFirecrackerDeviceBudget(intent); err != nil {
			return VMLaunchResult{}, err
		}
	}
	bootID, err := readCurrentProcessBootID(ctx)
	if err != nil {
		return VMLaunchResult{}, err
	}
	processes, session, err := spawnBlockedLaunchChild(ctx, request.VMID)
	var launching *launchLease
	var active *activeLease
	var console *launchConsoleSession
	var consoleSource *blockedLaunchProcessSource
	committed := false
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		returnErr = appendInstanceOperationError(
			returnErr,
			"release unconsumed console source",
			releaseUnconsumedConsoleSource(cleanupCtx, consoleSource),
		)
		if console != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"close parent console session",
				console.Release(cleanupCtx),
			)
		}
		if session != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"close child setup channel",
				session.Release(cleanupCtx),
			)
		}
		if !committed {
			var cleanup *cleanupLease
			var cleanupErr error
			if active != nil {
				cleanup, cleanupErr = active.BeginCleanup(cleanupCtx)
			} else if launching != nil {
				cleanup, cleanupErr = launching.Abort(cleanupCtx)
			}
			returnErr = appendInstanceOperationError(returnErr, "enter failed launch cleanup", cleanupErr)
			if cleanup != nil && cleanupErr == nil {
				returnErr = appendInstanceOperationError(
					returnErr,
					"clean failed launch",
					finishLifecycleCleanup(cleanupCtx, cleanup, false),
				)
			}
			if launching == nil && active == nil && processes != nil {
				returnErr = appendInstanceOperationError(
					returnErr,
					"terminate unregistered launch child",
					processes.terminateAndReap(cleanupCtx),
				)
			}
		}
		if processes != nil && launching == nil && active == nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"release unregistered launch child",
				processes.Release(cleanupCtx),
			)
		}
	}()
	if err != nil {
		return VMLaunchResult{}, err
	}
	if request.Console {
		consoleSource, console, err = spawnBlockedLaunchConsole(ctx, request.VMID, caller)
		if err != nil {
			return VMLaunchResult{}, err
		}
		if err := processes.attachBlockedLaunchConsole(ctx, consoleSource); err != nil {
			return VMLaunchResult{}, err
		}
	}
	intent := launchIntent{
		vmID:          request.VMID,
		hostBootID:    bootID,
		cacheIdentity: cache.identity,
		cacheLocator:  cache.locator,
	}
	if restore != nil {
		intent.snapshotID = restore.snapshotID
		restore.registered = true
	}
	launching, err = prepared.registerLaunching(
		ctx,
		caller,
		intent,
		processes,
	)
	if err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.prepareJailDestination(ctx, realJailDestinationDeps()); err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.prepareManagedVMCgroup(ctx, request.CgroupLimits); err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.prepareRuntimeDirectory(
		ctx,
		realManagedVMRuntimeDeps(),
		productionManagedVMRuntimePolicy(),
	); err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.preparePrivateRuntimeDirectory(ctx); err != nil {
		return VMLaunchResult{}, err
	}
	outputCache, err := pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(), caller, locator)
	if err != nil {
		return VMLaunchResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release output cache locator",
			outputCache.Release(context.WithoutCancel(ctx)),
		)
	}()
	metrics := managedPersistentMetricsDisabled
	if request.Metrics {
		metrics = managedPersistentMetricsEnabled
	}
	if err := launching.preparePersistentOutputs(ctx, outputCache, metrics); err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.grantExecutionOutputAccess(ctx); err != nil {
		return VMLaunchResult{}, err
	}
	if console != nil {
		if err := console.prepare(ctx, launching); err != nil {
			return VMLaunchResult{}, err
		}
	}
	guestIP, _ := netip.ParseAddr(request.GuestIPv4)
	topology, err := launching.prepareProductionTopology(
		ctx,
		network.VMIntent{NetworkID: request.NetworkID, GuestIPv4: guestIP, MAC: &request.GuestMAC},
	)
	if err != nil {
		return VMLaunchResult{}, err
	}
	resourceIntent := request.launchIntent(vmFirecrackerTapIdentity{name: topology.TAPName})
	resourceIntent.deviceProfile = deviceProfile
	spec, err := newVMLaunchResourceSpec(request.VMID, request.KernelID, request.RootfsBytes, resourceIntent)
	if err != nil {
		return VMLaunchResult{}, err
	}
	var resources *vmLaunchResourceLease
	if restore != nil {
		defer func() { returnErr = errors.Join(returnErr, restore.releaseDescriptors(context.WithoutCancel(ctx))) }()
		resources, err = prepareRestoredVMLaunchResources(ctx, cache, spec, launching.state.record, restore)
	} else {
		var backings *isolatedLaunchBackings
		backings, err = prepareIsolatedLaunchBackings(ctx, cache, spec, launching.state.record)
		if err != nil {
			return VMLaunchResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, backings.Release(context.WithoutCancel(ctx))) }()
		resources, err = cache.pinIsolatedVMLaunchResources(ctx, spec, backings)
	}
	if err != nil {
		return VMLaunchResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release launch resource pins",
			resources.Release(context.WithoutCancel(ctx)),
		)
	}()
	if err := launching.sendChildSetup(ctx, session, resources, console); err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.releaseToJailer(ctx); err != nil {
		return VMLaunchResult{}, err
	}
	target, err := launching.childNamespaceTarget(ctx, session)
	if err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.state.topology.CompleteActivation(ctx); err != nil {
		return VMLaunchResult{}, errors.Join(err, target.Release(context.WithoutCancel(ctx)))
	}
	sendErr := launching.state.networkNamespace.production.SendToJailer(ctx, target)
	if err := errors.Join(sendErr, target.Release(context.WithoutCancel(ctx))); err != nil {
		return VMLaunchResult{}, err
	}
	if err := launching.awaitCopiedLaunchExecutable(ctx); err != nil {
		return VMLaunchResult{}, err
	}
	if console != nil {
		if err := console.waitReady(ctx); err != nil {
			return VMLaunchResult{}, err
		}
	}
	active, err = launching.activateVerifiedProcess(ctx)
	if err != nil {
		return VMLaunchResult{}, err
	}
	if restore != nil {
		if err := restore.loadIntoActive(ctx, active, topology); err != nil {
			return VMLaunchResult{}, err
		}
	}
	record := active.state.record
	result := VMLaunchResult{
		VMID:             record.vmID,
		LaunchGeneration: record.launchGeneration,
		PID:              record.launch.process.pid,
		StartTimeTicks:   record.launch.process.startTimeTicks,
	}
	// A detach attempt never falls back to destructive rollback: close-only partial
	// completion is recovered from the durable active identity on the next operation.
	committed = true
	if err := active.detachCommittedLaunch(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// Abort uses the same owner-bound recovered cleanup transaction as Stop.
func (server *VMLifecycleServer) Abort(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMCleanupRequest,
) (VMCleanupResult, error) {
	return server.Cleanup(ctx, ownerUID, ownerGID, request)
}

// Cleanup is idempotent for an existing cleaned authority record and never signals a numeric PID.
func (server *VMLifecycleServer) Cleanup(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMCleanupRequest,
) (VMCleanupResult, error) {
	if err := request.Validate(); err != nil {
		return VMCleanupResult{}, err
	}
	cleanup, err := server.instances.beginLifecycleCleanup(
		ctx,
		instanceCaller{uid: ownerUID, gid: ownerGID},
		request.VMID,
	)
	if err != nil {
		return VMCleanupResult{}, err
	}
	if cleanup != nil {
		if err := finishLifecycleCleanup(ctx, cleanup, true); err != nil {
			return VMCleanupResult{}, err
		}
	}
	return VMCleanupResult{VMID: request.VMID, Cleaned: true}, nil
}

func finishLifecycleCleanup(ctx context.Context, cleanup *cleanupLease, recoverTopology bool) error {
	if recoverTopology {
		if err := cleanup.recoverProductionTopology(ctx); err != nil {
			return err
		}
	}
	if err := cleanup.removeLifecycleJailTree(ctx); err != nil {
		return err
	}
	if err := cleanup.Complete(ctx); err != nil {
		return err
	}
	return cleanup.Release(context.WithoutCancel(ctx))
}

// Remove completes Stop first, then invalidates the exact stopped endpoint's grants.
// The cleaned instance tombstone remains available for idempotent retry.
func (server *VMLifecycleServer) Remove(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMCleanupRequest,
) (_ VMCleanupResult, returnErr error) {
	result, err := server.Cleanup(ctx, ownerUID, ownerGID, request)
	if err != nil {
		return VMCleanupResult{}, err
	}
	locked, err := server.instances.lockOwnedInstance(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.VMID)
	if err != nil {
		return VMCleanupResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release final VM authority",
			locked.Release(context.WithoutCancel(ctx)),
		)
	}()
	if locked.record.lifecycle != instanceLifecycleCleaned {
		return VMCleanupResult{}, instanceStateError(request.VMID, "final VM removal requires cleaned authority")
	}
	retained, err := locked.vmLock.retainTopologyAuthority(ctx)
	if err != nil {
		return VMCleanupResult{}, err
	}
	hold, err := retained.transferProductionHold(ctx)
	if err != nil {
		return VMCleanupResult{}, errors.Join(err, retained.Release(context.WithoutCancel(ctx)))
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release final VM topology hold",
			hold.Release(context.WithoutCancel(ctx)),
		)
	}()
	if err := network.ForgetVMEndpoint(ctx, hold, locked.record.launchGeneration); err != nil {
		return VMCleanupResult{}, err
	}
	if err := retireRemovedVMExecution(ctx, locked, instanceCaller{uid: ownerUID, gid: ownerGID}); err != nil {
		return VMCleanupResult{}, err
	}
	if err := removeStoppedVMLaunchDeviceAuthority(ctx, locked.record); err != nil {
		return VMCleanupResult{}, err
	}
	return result, nil
}
