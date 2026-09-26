package authorityfs

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// OpenNetworklessPlaceholderRecovery admits only the token-bound, previously journaled placeholder or its absence.
// A mounted leaf is never accepted without the separately journaled nsfs and unique mount identities.
func (uid *NetworkUID) OpenNetworklessPlaceholderRecovery(
	ctx context.Context, hold *TopologyHold, target NetworkNamespaceTargetIdentity, token [32]byte,
) (_ *NetworklessNamespaceTarget, returnErr error) {
	if ctx == nil || uid == nil || uid.state == nil || hold == nil || token == ([32]byte{}) {
		return nil, errors.New("authorityfs: networkless placeholder recovery authority is invalid")
	}
	owner, key, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	state := uid.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active || state.ownerUID != owner {
		return nil, errors.New("authorityfs: networkless recovery owner changed")
	}
	directory := networkNamespaceIdentity{device: target.DirectoryDevice, inode: target.DirectoryInode,
		mount: target.DirectoryMount}
	placeholder := networkNamespaceIdentity{device: target.PlaceholderDevice, inode: target.PlaceholderInode,
		mount: target.PlaceholderMount}
	if !validNetworkNamespaceIdentity(directory) || !validNetworkNamespaceIdentity(placeholder) ||
		directory.mount.Kind != NetworkNamespaceMountIdentityUnique ||
		placeholder.mount.Kind != NetworkNamespaceMountIdentityUnique {
		return nil, errors.New("authorityfs: networkless target receipt lacks unique identity")
	}
	directoryFD, err := state.deps.openAt2(ctx, state.fd, networkNamespaceDirectoryName, &unix.OpenHow{
		Flags: directoryFlags, Resolve: resolveManaged,
	})
	if err != nil {
		return nil, err
	}
	observedDirectory, err := inspectNetworkNamespaceDirectory(ctx, state.deps, directoryFD, state.policy)
	if err != nil || observedDirectory != directory {
		return nil, errors.Join(errors.New("authorityfs: networkless directory identity changed"), err,
			closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, directoryFD))
	}
	owned := &networkNamespaceHandleState{deps: state.deps, policy: state.policy, ownerUID: owner, key: key,
		directoryFD: directoryFD, placeholderFD: -1, directoryIdentity: directory, targetIdentity: placeholder,
		metadataPrepared: true, networkless: true, networklessToken: token, parentSyncPending: true}
	recovery := &NetworklessNamespaceTarget{handle: &NetworkNamespaceHandle{state: owned}}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, recovery.Discard(context.WithoutCancel(ctx)))
		}
	}()
	leafFD, err := state.deps.openAt2(ctx, directoryFD, key.value, &unix.OpenHow{
		Flags: networkNamespaceInspectFlags, Resolve: resolveBeneath,
	})
	if errors.Is(err, unix.ENOENT) {
		return recovery, nil
	}
	if err != nil {
		return recovery, err
	}
	observedLeaf, inspectErr := inspectOwnedNetworkNamespacePlaceholder(ctx, state.deps, leafFD, state.policy, true)
	tokenErr := verifyNetworklessPlaceholderToken(leafFD, token)
	if inspectErr != nil || tokenErr != nil || observedLeaf != placeholder {
		return recovery, errors.Join(errors.New("authorityfs: networkless placeholder receipt differs"), inspectErr,
			tokenErr, closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, leafFD))
	}
	owned.placeholderFD, owned.leafCreated, owned.admitted = leafFD, true, true
	return recovery, nil
}

// ArmNetworklessEvidence binds a reopened namespace to its prepublication unique mount ID and hidden token.
// The caller must discard a rejected admission; this method never mutates the namespace target.
func (recovery *NetworkNamespaceRecovery) ArmNetworklessEvidence(
	ctx context.Context, token [32]byte, mountID uint64,
) error {
	if ctx == nil || recovery == nil || recovery.handle == nil || recovery.handle.state == nil ||
		token == ([32]byte{}) || mountID == 0 {
		return errors.New("authorityfs: networkless namespace recovery receipt is incomplete")
	}
	state := recovery.handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || state.networkless {
		return errors.New("authorityfs: networkless recovery is unavailable")
	}
	key := state.key
	if !recovery.mounted {
		if !state.leafCreated {
			state.networkless, state.networklessToken = true, token
			return nil
		}
		fd, err := state.deps.openAt2(ctx, state.directoryFD, key.value, &unix.OpenHow{
			Flags: networkNamespaceInspectFlags, Resolve: resolveManaged,
		})
		if err != nil {
			return err
		}
		tokenErr := verifyNetworklessPlaceholderToken(fd, token)
		closeErr := closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, fd)
		if tokenErr != nil || closeErr != nil {
			return errors.Join(tokenErr, closeErr)
		}
		state.networkless, state.networklessToken = true, token
		return nil
	}
	fd, err := state.deps.openAt2(ctx, state.directoryFD, key.value, &unix.OpenHow{
		Flags: networkNamespaceInspectFlags, Resolve: resolveBeneath,
	})
	if err != nil {
		return err
	}
	mount, mountErr := inspectNetworkNamespaceMountIdentity(ctx, state.deps, fd)
	closeErr := closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, fd)
	if mountErr != nil || closeErr != nil || mount.Kind != NetworkNamespaceMountIdentityUnique ||
		mount.Value != mountID {
		return errors.Join(errors.New("authorityfs: networkless nsfs mount identity changed"), mountErr, closeErr)
	}
	state.networkless, state.networklessToken = true, token
	return nil
}
