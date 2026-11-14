package jailer

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// observeRuntimeStatus is private until public lifecycle operations create schema-3 authority. Calling it for
// transitional launches would correctly find no authority but incorrectly project those legacy VMs as stopped.
func (authority *instanceAuthority) observeRuntimeStatus(
	ctx context.Context,
	caller instanceCaller,
	vmIDs []string,
) (map[string]runtimeStatus, error) {
	return authority.observeRuntimeStatusWithDeps(
		ctx,
		caller,
		vmIDs,
		realProcessRecoveryDeps(),
		realProcessIdentityVerifierDeps(),
	)
}

// observeRuntimeStatusWithDeps returns a complete observation or no result. It never recovers launching/cleaning,
// signals a process, repairs durable state, or inspects a PID supplied by the normal process.
func (authority *instanceAuthority) observeRuntimeStatusWithDeps(
	ctx context.Context,
	caller instanceCaller,
	vmIDs []string,
	recovery processRecoveryDeps,
	identity processIdentityVerifierDeps,
) (result map[string]runtimeStatus, returnErr error) {
	defer func() {
		if returnErr != nil {
			result = nil
			slog.Error("runtime status observation failed", "caller_uid", caller.uid, "error", returnErr)
		}
	}()
	if ctx == nil {
		return nil, instanceValidationError("runtime status context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, runtimeStatusUnavailableError("", "runtime status context ended", err)
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if len(vmIDs) > runtimeStatusMaxVMs {
		return nil, instanceValidationError("runtime status batch exceeds 1000 VMs")
	}
	ids := append([]string{}, vmIDs...)
	sort.Strings(ids)
	for index, id := range ids {
		if !vmIDPattern.MatchString(id) || index > 0 && ids[index-1] == id {
			return nil, instanceValidationError("runtime status VM IDs must be canonical and unique")
		}
	}
	result = make(map[string]runtimeStatus, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if authority == nil ||
		authority.authorityTree == nil ||
		authority.deps.open == nil ||
		authority.deps.openAt == nil ||
		authority.deps.fstat == nil ||
		authority.deps.read == nil ||
		authority.deps.close == nil ||
		authority.deps.readDirNames == nil ||
		identity.readBootID == nil ||
		identity.openProcessDirectory == nil ||
		identity.readProcessMetadata == nil ||
		identity.pinProcessObject == nil ||
		identity.descriptorFlags == nil ||
		identity.fstat == nil ||
		identity.close == nil ||
		recovery.pidfdOpen == nil ||
		recovery.pidfdAlive == nil ||
		recovery.deriveLocator == nil ||
		recovery.descriptorFlags == nil ||
		recovery.close == nil {
		return nil, runtimeStatusUnavailableError("", "runtime status dependencies are incomplete", nil)
	}
	bootID, err := identity.readBootID(ctx)
	if err != nil || !hostBootIDPattern.MatchString(bootID) {
		return nil, runtimeStatusUnavailableError("", "read runtime status host boot ID", err)
	}
	// The index excludes publication while absent records are classified. Each existing VM is observed under its
	// own lock; keeping only one VM lock/pidfd set at a time bounds descriptors independently of batch size.
	tree, err := authority.authorityTree.openJailer(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release runtime status authority",
			tree.Release(context.WithoutCancel(ctx)),
		)
	}()
	indexLock, err := tree.acquireIndex(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release runtime status index",
			indexLock.Release(context.WithoutCancel(ctx)),
		)
	}()
	roots, found, err := openRuntimeStatusState(ctx, authority.deps, authority.policy, tree)
	if err != nil {
		return nil, err
	}
	if roots != nil {
		defer func() {
			returnErr = appendInstanceOperationError(returnErr, "close runtime status state",
				closeInstanceFDs(context.WithoutCancel(ctx), roots.deps, roots.retained))
		}()
	}
	owners := map[string]uint32{}
	if found {
		owners, err = roots.findRuntimeStatusOwners(ctx, ids)
		if err != nil {
			return nil, err
		}
	}
	for id, owner := range owners {
		if owner != caller.uid {
			return nil, unauthorizedInstanceError(id)
		}
	}
	var directories *instanceUIDDirectories
	if len(owners) > 0 {
		fd, err := openInstanceDirectory(ctx, roots.deps, roots.stateFD, strconv.FormatUint(uint64(caller.uid), 10),
			false, instancePrivateDirectory, roots.policy)
		if err != nil {
			return nil, err
		}
		directories, err = roots.openAuthorityForLocation(ctx, &instanceLocation{uid: caller.uid, stateFD: fd})
		if err != nil {
			return nil, err
		}
		defer func() {
			returnErr = appendInstanceOperationError(returnErr, "release runtime status owner",
				directories.Release(context.WithoutCancel(ctx)))
		}()
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, runtimeStatusUnavailableError(id, "runtime status context ended", err)
		}
		if _, owned := owners[id]; !owned {
			result[id] = runtimeStatusStopped
			continue
		}
		status, err := observeLockedRuntimeStatus(ctx, caller, directories, id, bootID, recovery, identity)
		if err != nil {
			return nil, err
		}
		result[id] = status
	}
	after, err := identity.readBootID(ctx)
	if err != nil || after != bootID {
		return nil, runtimeStatusUnavailableError(
			"",
			"host boot identity changed during runtime status observation",
			err,
		)
	}
	return result, nil
}

func observeLockedRuntimeStatus(
	ctx context.Context,
	caller instanceCaller,
	directories *instanceUIDDirectories,
	vmID, bootID string,
	recovery processRecoveryDeps,
	identity processIdentityVerifierDeps,
) (status runtimeStatus, returnErr error) {
	lock, err := directories.acquireVMLock(ctx, vmID)
	if err != nil {
		return runtimeStatusUnavailable, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"release observed VM lock",
			lock.Release(context.WithoutCancel(ctx)),
		)
		if returnErr != nil {
			status = runtimeStatusUnavailable
		}
	}()
	record, found, err := directories.readRecord(ctx, vmID)
	if err != nil {
		return runtimeStatusUnavailable, err
	}
	if !found {
		return runtimeStatusUnavailable, instanceAtomicError("runtime authority disappeared while locked", nil)
	}
	if err := authorizeInstanceRecord(caller, record); err != nil {
		return runtimeStatusUnavailable, err
	}
	if record.lifecycle == instanceLifecycleCleaned {
		return runtimeStatusStopped, nil
	}
	if record.launch.hostBootID != bootID {
		return runtimeStatusStopped, nil
	}
	if record.lifecycle != instanceLifecycleActive {
		return runtimeStatusUnavailable, runtimeStatusUnavailableError(
			vmID,
			"runtime authority requires lifecycle reconciliation",
			nil,
		)
	}
	return observeRuntimeActive(ctx, record, recovery, identity)
}

// openRuntimeStatusState opens existing durable authority only. Missing managed components prove absence; unsafe or
// inaccessible components never do. The caller separately owns tree and its already-held index lock.
func openRuntimeStatusState(
	ctx context.Context,
	deps instanceAuthorityDeps,
	policy instanceAuthorityPolicy,
	tree instanceJailerAuthority,
) (roots *instanceAuthorityRoots, found bool, returnErr error) {
	names, err := instanceAuthorityPathComponents(infra.JailerInstanceRoot, 4)
	if err != nil {
		return nil, false, err
	}
	roots = &instanceAuthorityRoots{deps: deps, policy: policy, authority: tree, stateFD: -1}
	defer func() {
		if returnErr != nil {
			returnErr = appendInstanceOperationError(returnErr, "close rejected runtime status state",
				closeInstanceFDs(context.WithoutCancel(ctx), deps, roots.retained))
			roots = nil
		}
	}()
	fd, err := deps.open(ctx, policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return roots, false, err
	}
	roots.retained = append(roots.retained, fd)
	if err := verifyInstanceDirectory(ctx, deps, fd, "filesystem root", policy, instanceAncestorDirectory); err != nil {
		return roots, false, err
	}
	for index, name := range names {
		class := instanceAncestorDirectory
		if index == 2 {
			class = instanceStateBaseDirectory
		} else if index == 3 {
			class = instancePrivateDirectory
		}
		// Classify only the opening syscall's ENOENT as absence. A metadata rejection joined with a close
		// failure carrying ENOENT must remain unavailable.
		next, err := deps.openAt(ctx, fd, name, instanceDirectoryFlags, 0)
		if index >= 2 && errors.Is(err, unix.ENOENT) {
			return roots, false, nil
		}
		if err != nil {
			return roots, false, instanceAtomicError("open runtime status state directory "+name, err)
		}
		roots.retained = append(roots.retained, next)
		if err := verifyInstanceDirectory(ctx, deps, next, name, policy, class); err != nil {
			return roots, false, err
		}
		fd = next
	}
	roots.stateFD = fd
	return roots, true, nil
}
