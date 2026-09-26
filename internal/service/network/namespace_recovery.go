package network

import (
	"context"
	"errors"
	"strconv"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

type recoveredNamespaceTarget struct {
	value    *authorityfs.NetworkNamespaceRecovery
	identity namespaceTargetIdentity
}

// CALLER MUST HOLD: the owner-bound VM topology hold, network lock, and firewall lock.
// Recovery admits recorded authority only; it never creates or repairs a missing mount.
func (effect *namespaceEffect) recoverNamespace(
	ctx context.Context,
	key namespaceKey,
	handle namespaceHandle,
	target namespaceTargetIdentity,
	hold *authorityfs.TopologyHold,
) (_ *namespacePin, returnErr error) {
	if ctx == nil {
		return nil, topologyRemoveError(string(key.vmID), "namespace recovery context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, topologyRemoveError(string(key.vmID), "namespace recovery cancelled", err)
	}
	if effect == nil || validateNamespaceEffectDeps(effect.deps) != nil {
		return nil, topologyRemoveError(string(key.vmID), "namespace recovery dependencies are incomplete", nil)
	}
	if err := validateNamespaceKey(key); err != nil {
		return nil, err
	}
	if err := validateNamespaceHandle(handle, deriveNamespaceHandle(key)); err != nil {
		return nil, err
	}
	vmKey, err := authorityfs.NewVMKey(string(key.vmID))
	if err != nil {
		return nil, topologyRemoveError(string(key.vmID), "derive namespace recovery key", err)
	}
	owner, boundKey, err := hold.Binding()
	if err != nil || owner != uint32(key.ownerUID) || boundKey != vmKey {
		return nil, topologyRemoveError(string(key.vmID), "namespace recovery VM hold does not match", err)
	}
	root, err := authorityfs.OpenNetwork(ctx)
	if err != nil {
		return nil, topologyRemoveError(string(key.vmID), "open namespace recovery authority", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Release(context.WithoutCancel(ctx)))
	}()
	uid, err := root.OpenUID(ctx, uint32(key.ownerUID))
	if err != nil {
		return nil, topologyRemoveError(string(key.vmID), "open namespace recovery owner", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, uid.Release(context.WithoutCancel(ctx)))
	}()
	authorityTarget := authorityfs.NetworkNamespaceTargetIdentity{
		DirectoryDevice: target.directoryDevice,
		DirectoryInode:  target.directoryInode,
		DirectoryMount: authorityfs.NetworkNamespaceMountIdentity{
			Kind:  authorityfs.NetworkNamespaceMountIdentityKind(target.directoryMount.kind),
			Value: target.directoryMount.value,
		},
		PlaceholderDevice: target.placeholderDevice,
		PlaceholderInode:  target.placeholderInode,
		PlaceholderMount: authorityfs.NetworkNamespaceMountIdentity{
			Kind:  authorityfs.NetworkNamespaceMountIdentityKind(target.placeholderMount.kind),
			Value: target.placeholderMount.value,
		},
	}
	recovery, err := uid.OpenNamespaceRecovery(
		ctx, vmKey, authorityTarget, handle.identity.device, handle.identity.inode, hold,
	)
	if err != nil {
		return nil, topologyRemoveError(string(key.vmID), "admit namespace recovery target", err)
	}
	state := &namespacePinState{
		owner: effect, deps: effect.deps, target: &recoveredNamespaceTarget{value: recovery, identity: target},
		targetID: target, key: key, handlePath: handle.path, namespaceID: handle.identity, handle: handle,
		parentFD: -1, targetFD: -1, netnsFD: -1, publishFD: -1,
		created: true, pinned: true, published: true, mounted: true, restored: true,
	}
	// No mutation has occurred during admission. A failed reopen only discards these new
	// descriptors, leaving the durable record and published namespace for another recovery.
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		returnErr = errors.Join(returnErr,
			closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.publishFD),
			closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.netnsFD),
			closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.parentFD),
			recovery.Discard(cleanupCtx),
		)
	}()
	state.parentFD, err = openNamespaceRecoveryDirectory(ctx, effect, key, target)
	if err != nil {
		return nil, topologyRemoveError(string(key.vmID), "reopen namespace recovery directory", err)
	}
	if !recovery.NamespaceMounted() {
		state.mounted = false
		state.handle = namespaceHandle{}
		// The coordinator admits this branch only after all recorded link effects are absent.
		// Leaving it unadmitted prevents both link operations and Jailer namespace handoff.
		return &namespacePin{state: state}, nil
	}
	state.netnsFD, err = openPublishedNamespace(ctx, effect.deps, effect.policy, state.parentFD, key.vmID, target)
	if err != nil {
		return nil, topologyRemoveError(string(key.vmID), "reopen published namespace", err)
	}
	if err := verifyPublishedNamespace(ctx, state); err != nil {
		return nil, topologyRemoveError(string(key.vmID), "verify recovered namespace", err)
	}
	state.verified = true
	state.admitted = true
	return &namespacePin{state: state}, nil
}

func openNamespaceRecoveryDirectory(
	ctx context.Context,
	effect *namespaceEffect,
	key namespaceKey,
	target namespaceTargetIdentity,
) (_ int, returnErr error) {
	rootFD, err := effect.deps.open(ctx, effect.policy.rootPath, namespaceDirectoryOpenFlags, 0)
	if err != nil {
		return -1, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeConsumedNamespaceFD(context.WithoutCancel(ctx), effect.deps, &rootFD))
	}()
	if err := verifyNamespaceDirectory(ctx, effect.deps, rootFD, effect.policy, false, namespaceTargetIdentity{}); err != nil {
		return -1, err
	}
	parentName := "run/mvmctl/authority/" + strconv.FormatUint(uint64(key.ownerUID), 10) + "/netns"
	fd, err := effect.deps.openAt2(ctx, rootFD, parentName, &unix.OpenHow{
		Flags: namespaceDirectoryOpenFlags, Resolve: namespaceResolveBeneath,
	})
	if err != nil {
		return -1, err
	}
	if err := verifyNamespaceDirectory(ctx, effect.deps, fd, effect.policy, true, target); err != nil {
		return -1, errors.Join(err, closeConsumedNamespaceFD(context.WithoutCancel(ctx), effect.deps, &fd))
	}
	return fd, nil
}

func (target *recoveredNamespaceTarget) targetIdentity(context.Context) (namespaceTargetIdentity, error) {
	return target.identity, nil
}

func (target *recoveredNamespaceTarget) remove(ctx context.Context) error {
	return target.value.Remove(ctx)
}

func (target *recoveredNamespaceTarget) release(ctx context.Context) error {
	return target.value.Release(ctx)
}

// discardRecovery drops only newly reopened descriptors when the coordinator rejects admission.
// It never advances or consumes the durable topology's cleanup stages.
func (pin *namespacePin) discardRecovery(ctx context.Context) error {
	if ctx == nil || pin == nil || pin.state == nil {
		return topologyRemoveError("", "namespace recovery discard is inactive", nil)
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	target, ok := state.target.(*recoveredNamespaceTarget)
	if !ok || target == nil || target.value == nil {
		return topologyRemoveError(string(state.key.vmID), "namespace pin is not a recovery admission", nil)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	result := errors.Join(
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.publishFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.netnsFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.targetFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.parentFD),
		target.value.Discard(cleanupCtx),
	)
	state.target = nil
	state.handle = namespaceHandle{}
	state.namespaceID = namespaceIdentity{}
	state.admitted = false
	state.released = true
	if result != nil {
		state.releaseErr = topologyRemoveError(string(state.key.vmID), "discard namespace recovery admission", result)
	}
	return state.releaseErr
}

// CALLER MUST HOLD: retained VM topology authority after durable active instance and topology commits.
func (pin *namespacePin) detachActive(ctx context.Context) error {
	if ctx == nil || pin == nil || pin.state == nil {
		return topologyRemoveError("", "namespace detach authority is inactive", nil)
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.admitted || !state.mounted || !state.verified || state.removed || state.unmountOutcomeUnknown {
		return topologyRemoveError(string(state.key.vmID), "namespace detach authority is not active", nil)
	}
	if err := verifyPublishedNamespace(ctx, state); err != nil {
		return topologyRemoveError(string(state.key.vmID), "verify namespace before active detach", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	var detachErr error
	switch target := state.target.(type) {
	case *namespaceHandleLease:
		typed, ok := target.target.(*systemNamespaceAuthorityTarget)
		if !ok || typed == nil || typed.value == nil {
			return topologyRemoveError(string(state.key.vmID), "namespace detach target is not concrete authority", nil)
		}
		detachErr = typed.value.DetachActive(cleanupCtx)
	case *recoveredNamespaceTarget:
		detachErr = target.value.DetachActive(cleanupCtx)
	default:
		return topologyRemoveError(string(state.key.vmID), "namespace detach target is not concrete authority", nil)
	}
	result := errors.Join(detachErr,
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.publishFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.netnsFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.targetFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.parentFD),
	)
	state.target = nil
	state.namespaceID = namespaceIdentity{}
	state.handle = namespaceHandle{}
	state.admitted = false
	state.released = true
	if result != nil {
		state.releaseErr = topologyRemoveError(string(state.key.vmID), "close active namespace authority", result)
	}
	return state.releaseErr
}
