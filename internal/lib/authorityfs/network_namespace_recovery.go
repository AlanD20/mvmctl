package authorityfs

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// NetworkNamespaceRecovery retains the exact directory and hidden placeholder identity for a
// published namespace. It never exposes a descriptor or accepts a caller-selected path.
// THREAD-SAFE: copies share the underlying handle's mutex and close-once state.
type NetworkNamespaceRecovery struct {
	handle  *NetworkNamespaceHandle
	mounted bool
}

// OpenNamespaceRecovery reopens an existing published namespace under a live owner-bound VM hold.
// It creates or repairs no namespace directory, mount, or placeholder.
func (uid *NetworkUID) OpenNamespaceRecovery(
	ctx context.Context,
	key VMKey,
	target NetworkNamespaceTargetIdentity,
	namespaceDevice, namespaceInode uint64,
	hold *TopologyHold,
) (_ *NetworkNamespaceRecovery, returnErr error) {
	if ctx == nil {
		return nil, errors.New("authorityfs: namespace recovery context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner, boundKey, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	if uid == nil || uid.state == nil || !isLowerHex(key.value, vmKeyLength) || boundKey != key ||
		namespaceDevice == 0 || namespaceInode == 0 {
		return nil, errors.New("authorityfs: namespace recovery binding is invalid")
	}
	source := uid.state
	source.mu.RLock()
	defer source.mu.RUnlock()
	if !source.active || owner != source.ownerUID {
		return nil, errors.New("authorityfs: namespace recovery owner is not active")
	}
	directory := networkNamespaceIdentity{
		device: target.DirectoryDevice, inode: target.DirectoryInode, mount: target.DirectoryMount,
	}
	placeholder := networkNamespaceIdentity{
		device: target.PlaceholderDevice, inode: target.PlaceholderInode, mount: target.PlaceholderMount,
	}
	if !validNetworkNamespaceIdentity(directory) || !validNetworkNamespaceIdentity(placeholder) {
		return nil, errors.New("authorityfs: namespace recovery target identity is invalid")
	}
	fd, err := source.deps.openAt2(ctx, source.fd, networkNamespaceDirectoryName, &unix.OpenHow{
		Flags: directoryFlags, Resolve: resolveManaged,
	})
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open existing namespace directory: %w", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeNetworkNamespaceFD(context.WithoutCancel(ctx), source.deps, fd))
		}
	}()
	observed, err := inspectNetworkNamespaceDirectory(ctx, source.deps, fd, source.policy)
	if err != nil || observed != directory {
		return nil, errors.Join(errors.New("authorityfs: namespace recovery directory identity changed"), err)
	}
	namespaceFD, err := source.deps.openAt2(ctx, fd, key.value, &unix.OpenHow{
		Flags: networkNamespaceInspectFlags, Resolve: resolveBeneath,
	})
	if errors.Is(err, unix.ENOENT) {
		return &NetworkNamespaceRecovery{handle: &NetworkNamespaceHandle{state: &networkNamespaceHandleState{
			deps: source.deps, policy: source.policy, ownerUID: owner, key: key,
			directoryFD: fd, placeholderFD: -1, directoryIdentity: directory, targetIdentity: placeholder,
			metadataPrepared: true, parentSyncPending: true,
		}}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open published namespace for recovery: %w", err)
	}
	inspectErr := inspectRecoveryNamespace(
		ctx,
		source.deps,
		source.policy,
		namespaceFD,
		namespaceDevice,
		namespaceInode,
	)
	mounted := inspectErr == nil
	if inspectErr != nil {
		// An interrupted exact unmount exposes the original placeholder. It grants only cleanup
		// authority; it can never admit links or a Jailer handoff as a live namespace.
		unmounted, placeholderErr := inspectOwnedNetworkNamespacePlaceholder(
			ctx,
			source.deps,
			namespaceFD,
			source.policy,
			true,
		)
		if placeholderErr == nil && unmounted == placeholder {
			inspectErr = nil
		} else {
			inspectErr = errors.Join(inspectErr, placeholderErr)
		}
	}
	closeErr := closeNetworkNamespaceFD(context.WithoutCancel(ctx), source.deps, namespaceFD)
	if inspectErr != nil || closeErr != nil {
		return nil, errors.Join(inspectErr, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &NetworkNamespaceRecovery{
		mounted: mounted,
		handle: &NetworkNamespaceHandle{state: &networkNamespaceHandleState{
			deps: source.deps, policy: source.policy, ownerUID: owner, key: key,
			directoryFD: fd, placeholderFD: -1, directoryIdentity: directory, targetIdentity: placeholder,
			metadataPrepared: true, admitted: true, leafCreated: true,
		}},
	}, nil
}

// NamespaceMounted reports whether recovery admitted a published namespace. False means the
// exact original placeholder or its absence was observed and the authority permits cleanup only.
func (recovery *NetworkNamespaceRecovery) NamespaceMounted() bool {
	return recovery != nil && recovery.mounted
}

func inspectRecoveryNamespace(
	ctx context.Context,
	deps dependencies,
	policy policy,
	fd int,
	device, inode uint64,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return err
	}
	if stat.Dev != device || stat.Ino != inode || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o7777 != 0o444 || stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Nlink != 1 || stat.Size != 0 {
		return errors.New("authorityfs: published namespace identity changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(fd, &filesystem); err != nil {
		return fmt.Errorf("authorityfs: inspect recovery namespace filesystem: %w", err)
	}
	if filesystem.Type != unix.NSFS_MAGIC {
		return errors.New("authorityfs: recovery target is not nsfs")
	}
	kind, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil {
		return fmt.Errorf("authorityfs: inspect recovery namespace type: %w", err)
	}
	if kind != unix.CLONE_NEWNET {
		return errors.New("authorityfs: recovery target is not a network namespace")
	}
	return nil
}

// Remove admits the formerly hidden placeholder only after exact namespace unmount, then
// delegates to the existing identity-bound, retryable unlink and directory-sync operation.
func (recovery *NetworkNamespaceRecovery) Remove(ctx context.Context) error {
	if ctx == nil || recovery == nil || recovery.handle == nil || recovery.handle.state == nil {
		return errors.New("authorityfs: namespace recovery cleanup is inactive")
	}
	state := recovery.handle.state
	state.mu.Lock()
	if state.released || state.removed || state.placeholderFD >= 0 {
		state.mu.Unlock()
		return recovery.handle.Remove(ctx)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if err := verifyRetainedNetworkNamespaceDirectory(cleanupCtx, state); err != nil {
		state.mu.Unlock()
		return err
	}
	if !state.leafCreated {
		fd, err := state.deps.openAt2(cleanupCtx, state.directoryFD, state.key.value, &unix.OpenHow{
			Flags: networkNamespaceInspectFlags, Resolve: resolveBeneath,
		})
		if err == nil {
			closeErr := closeNetworkNamespaceFD(cleanupCtx, state.deps, fd)
			state.mu.Unlock()
			return errors.Join(errors.New("authorityfs: removed namespace target reappeared"), closeErr)
		}
		if !errors.Is(err, unix.ENOENT) {
			state.mu.Unlock()
			return fmt.Errorf("authorityfs: verify absent namespace target: %w", err)
		}
		state.mu.Unlock()
		return recovery.handle.Remove(cleanupCtx)
	}
	fd, err := state.deps.openAt2(cleanupCtx, state.directoryFD, state.key.value, &unix.OpenHow{
		Flags: networkNamespaceInspectFlags, Resolve: resolveManaged,
	})
	if err != nil {
		state.mu.Unlock()
		return fmt.Errorf("authorityfs: reopen unmounted namespace placeholder: %w", err)
	}
	identity, inspectErr := inspectOwnedNetworkNamespacePlaceholder(cleanupCtx, state.deps, fd, state.policy, true)
	if inspectErr != nil || identity != state.targetIdentity {
		err = errors.Join(
			errors.New("authorityfs: unmounted namespace placeholder identity changed"), inspectErr,
			closeNetworkNamespaceFD(cleanupCtx, state.deps, fd),
		)
		state.mu.Unlock()
		return err
	}
	state.placeholderFD = fd
	state.mu.Unlock()
	return recovery.handle.Remove(cleanupCtx)
}

// Release closes recovery authority only after exact placeholder removal succeeds.
func (recovery *NetworkNamespaceRecovery) Release(ctx context.Context) error {
	if recovery == nil {
		return nil
	}
	return recovery.handle.Release(ctx)
}

// DetachActive closes only retained authority descriptors; the persistent nsfs mount remains.
// CALLER MUST HOLD: the VM topology hold, after committing and verifying active topology.
func (recovery *NetworkNamespaceRecovery) DetachActive(ctx context.Context) error {
	if recovery == nil {
		return nil
	}
	return recovery.handle.DetachActive(ctx)
}

// Discard closes a recovery admission without unlinking, unmounting, or syncing anything.
// It permits rejecting a cleanup-only admission without consuming durable cleanup progress.
func (recovery *NetworkNamespaceRecovery) Discard(ctx context.Context) error {
	if ctx == nil || recovery == nil || recovery.handle == nil || recovery.handle.state == nil {
		return errors.New("authorityfs: namespace recovery discard is inactive")
	}
	state := recovery.handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	return closeDetachedNetworkNamespace(context.WithoutCancel(ctx), state)
}

// DetachActive closes the handle without unlinking its placeholder or touching its mount.
// CRITICAL: The caller must durably commit active authority and reverify the published namespace
// before detaching. Release deliberately remains cleanup-only.
func (handle *NetworkNamespaceHandle) DetachActive(ctx context.Context) error {
	if ctx == nil {
		return errors.New("authorityfs: namespace detach context is required")
	}
	if handle == nil || handle.state == nil {
		return errors.New("authorityfs: namespace detach authority is inactive")
	}
	state := handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.admitted || state.removed || !state.leafCreated || state.parentSyncPending {
		return errors.New("authorityfs: namespace detach authority is not admitted")
	}
	return closeDetachedNetworkNamespace(context.WithoutCancel(ctx), state)
}

// CALLER MUST HOLD: state.mu.
func closeDetachedNetworkNamespace(ctx context.Context, state *networkNamespaceHandleState) error {
	var result error
	if state.placeholderFD >= 0 {
		fd := state.placeholderFD
		state.placeholderFD = -1
		result = closeNetworkNamespaceFD(ctx, state.deps, fd)
	}
	fd := state.directoryFD
	state.directoryFD = -1
	result = errors.Join(result, closeNetworkNamespaceFD(ctx, state.deps, fd))
	state.directoryIdentity = networkNamespaceIdentity{}
	state.targetIdentity = networkNamespaceIdentity{}
	state.ownerUID = 0
	state.key = VMKey{}
	state.admitted = false
	state.released = true
	state.releaseErr = result
	return result
}
