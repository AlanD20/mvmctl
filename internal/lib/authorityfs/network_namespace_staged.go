package authorityfs

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const networklessNamespaceTokenAttribute = "trusted.mvmctl.networkless"

// NetworklessNamespaceTarget owns an anonymous, preidentified placeholder. Its fixed VM leaf is linked only after
// the caller has durably recorded TargetIdentity; an unlinked anonymous inode disappears on process death.
type NetworklessNamespaceTarget struct{ handle *NetworkNamespaceHandle }

func (uid *NetworkUID) PrepareNetworklessNamespaceTarget(
	ctx context.Context, hold *TopologyHold, token [32]byte,
) (_ *NetworklessNamespaceTarget, returnErr error) {
	if ctx == nil || uid == nil || uid.state == nil || hold == nil || token == ([32]byte{}) {
		return nil, errors.New("authorityfs: networkless target requires VM authority and token")
	}
	owner, key, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	state := uid.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active || state.ownerUID != owner {
		return nil, errors.New("authorityfs: networkless target owner changed")
	}
	directoryFD, err := openManagedDirectory(ctx, state.deps, state.fd, networkNamespaceDirectoryName,
		directoryMode, resolveManaged, state.policy)
	if err != nil {
		return nil, err
	}
	directory, err := inspectNetworkNamespaceDirectory(ctx, state.deps, directoryFD, state.policy)
	if err != nil {
		return nil, errors.Join(err, closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, directoryFD))
	}
	if directory.mount.Kind != NetworkNamespaceMountIdentityUnique {
		return nil, errors.Join(errors.New("authorityfs: unique directory mount ID is unavailable"),
			closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, directoryFD))
	}
	// O_TMPFILE is the prepublication feature check and keeps every pre-receipt failure anonymous.
	placeholderFD, err := unix.Openat(directoryFD, ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errors.Join(err, closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, directoryFD))
	}
	owned := &networkNamespaceHandleState{deps: state.deps, policy: state.policy, ownerUID: owner, key: key,
		directoryFD: directoryFD, placeholderFD: placeholderFD, directoryIdentity: directory,
		metadataPrepared: true, networkless: true, networklessToken: token}
	target := &NetworklessNamespaceTarget{handle: &NetworkNamespaceHandle{state: owned}}
	if err := state.deps.fchown(
		ctx,
		placeholderFD,
		int(state.policy.expectedUID),
		int(state.policy.expectedGID),
	); err != nil {
		return target, err
	}
	if err := state.deps.fchmod(ctx, placeholderFD, networkNamespaceHandleMode); err != nil {
		return target, err
	}
	if err := unix.Fsetxattr(
		placeholderFD,
		networklessNamespaceTokenAttribute,
		token[:],
		unix.XATTR_CREATE,
	); err != nil {
		return target, fmt.Errorf("authorityfs: set trusted networkless token: %w", err)
	}
	if err := verifyNetworklessPlaceholderToken(placeholderFD, token); err != nil {
		return target, err
	}
	identity, err := inspectAnonymousNetworklessPlaceholder(ctx, owned)
	if err != nil {
		return target, err
	}
	owned.targetIdentity = identity
	if err := state.deps.fsync(ctx, placeholderFD); err != nil {
		return target, err
	}
	if err := ctx.Err(); err != nil {
		return target, err
	}
	return target, nil
}

func (target *NetworklessNamespaceTarget) TargetIdentity(ctx context.Context) (NetworkNamespaceTargetIdentity, error) {
	if ctx == nil || target == nil || target.handle == nil || target.handle.state == nil {
		return NetworkNamespaceTargetIdentity{}, errors.New("authorityfs: networkless target is inactive")
	}
	state := target.handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.networkless || state.leafCreated {
		return NetworkNamespaceTargetIdentity{}, errors.New("authorityfs: networkless target is no longer anonymous")
	}
	if err := verifyRetainedNetworkNamespaceDirectory(ctx, state); err != nil {
		return NetworkNamespaceTargetIdentity{}, err
	}
	identity, err := inspectAnonymousNetworklessPlaceholder(ctx, state)
	if err != nil || identity != state.targetIdentity {
		return NetworkNamespaceTargetIdentity{}, errors.Join(
			err,
			errors.New("authorityfs: anonymous target identity changed"),
		)
	}
	return NetworkNamespaceTargetIdentity{DirectoryDevice: state.directoryIdentity.device,
		DirectoryInode: state.directoryIdentity.inode, DirectoryMount: state.directoryIdentity.mount,
		PlaceholderDevice: identity.device, PlaceholderInode: identity.inode, PlaceholderMount: identity.mount}, nil
}

// Link publishes only the recorded anonymous inode. An uncertain syscall outcome is re-observed before returning.
func (target *NetworklessNamespaceTarget) Link(ctx context.Context) error {
	if ctx == nil || target == nil || target.handle == nil || target.handle.state == nil {
		return errors.New("authorityfs: networkless target is inactive")
	}
	state := target.handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.networkless {
		return errors.New("authorityfs: networkless target is unavailable")
	}
	if state.leafCreated {
		return nil
	}
	if err := verifyRetainedNetworkNamespaceDirectory(ctx, state); err != nil {
		return err
	}
	identity, err := inspectAnonymousNetworklessPlaceholder(ctx, state)
	if err != nil || identity != state.targetIdentity {
		return errors.Join(err, errors.New("authorityfs: anonymous target identity changed"))
	}
	linkErr := unix.Linkat(state.placeholderFD, "", state.directoryFD, state.key.value, unix.AT_EMPTY_PATH)
	fd, openErr := state.deps.openAt2(context.WithoutCancel(ctx), state.directoryFD, state.key.value, &unix.OpenHow{
		Flags: networkNamespaceInspectFlags, Resolve: resolveManaged,
	})
	if errors.Is(openErr, unix.ENOENT) {
		if linkErr == nil {
			return errors.New("authorityfs: linked networkless target disappeared")
		}
		return linkErr
	}
	if openErr != nil {
		return errors.Join(linkErr, openErr)
	}
	observed, inspectErr := inspectOwnedNetworkNamespacePlaceholder(context.WithoutCancel(ctx), state.deps,
		fd, state.policy, true)
	tokenErr := verifyNetworklessPlaceholderToken(fd, state.networklessToken)
	closeErr := closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, fd)
	if inspectErr != nil || tokenErr != nil || closeErr != nil || observed != state.targetIdentity {
		return errors.Join(
			linkErr,
			inspectErr,
			tokenErr,
			closeErr,
			errors.New("authorityfs: linked networkless target differs"),
		)
	}
	state.leafCreated, state.admitted, state.parentSyncPending = true, true, true
	syncErr := state.deps.fsync(context.WithoutCancel(ctx), state.directoryFD)
	if syncErr == nil {
		state.parentSyncPending = false
	}
	return errors.Join(linkErr, syncErr)
}

func (target *NetworklessNamespaceTarget) LinkedIdentity(ctx context.Context) (NetworkNamespaceTargetIdentity, error) {
	if ctx == nil || target == nil || target.handle == nil || target.handle.state == nil {
		return NetworkNamespaceTargetIdentity{}, errors.New("authorityfs: linked networkless target is inactive")
	}
	state := target.handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.networkless || !state.leafCreated || !state.admitted {
		return NetworkNamespaceTargetIdentity{}, errors.New("authorityfs: networkless target is not linked")
	}
	if err := verifyRetainedNetworkNamespaceDirectory(ctx, state); err != nil {
		return NetworkNamespaceTargetIdentity{}, err
	}
	identity, err := inspectOwnedNetworkNamespacePlaceholder(ctx, state.deps, state.placeholderFD, state.policy, true)
	if err != nil || identity != state.targetIdentity {
		return NetworkNamespaceTargetIdentity{}, errors.Join(
			err,
			errors.New("authorityfs: retained networkless target changed"),
		)
	}
	if err := verifyNetworklessPlaceholderToken(state.placeholderFD, state.networklessToken); err != nil {
		return NetworkNamespaceTargetIdentity{}, err
	}
	fd, err := state.deps.openAt2(ctx, state.directoryFD, state.key.value, &unix.OpenHow{
		Flags: networkNamespaceInspectFlags, Resolve: resolveManaged,
	})
	if err != nil {
		return NetworkNamespaceTargetIdentity{}, err
	}
	current, inspectErr := inspectOwnedNetworkNamespacePlaceholder(ctx, state.deps, fd, state.policy, true)
	tokenErr := verifyNetworklessPlaceholderToken(fd, state.networklessToken)
	closeErr := closeNetworkNamespaceFD(context.WithoutCancel(ctx), state.deps, fd)
	if inspectErr != nil || tokenErr != nil || closeErr != nil || current != identity {
		return NetworkNamespaceTargetIdentity{}, errors.Join(inspectErr, tokenErr, closeErr,
			errors.New("authorityfs: linked networkless target differs"))
	}
	return NetworkNamespaceTargetIdentity{DirectoryDevice: state.directoryIdentity.device,
		DirectoryInode: state.directoryIdentity.inode, DirectoryMount: state.directoryIdentity.mount,
		PlaceholderDevice: identity.device, PlaceholderInode: identity.inode, PlaceholderMount: identity.mount}, nil
}

func (target *NetworklessNamespaceTarget) Remove(ctx context.Context) error {
	if target == nil {
		return nil
	}
	return target.handle.Remove(ctx)
}

func (target *NetworklessNamespaceTarget) Release(ctx context.Context) error {
	if target == nil {
		return nil
	}
	return target.handle.Release(ctx)
}

func (target *NetworklessNamespaceTarget) DetachActive(ctx context.Context) error {
	if target == nil {
		return nil
	}
	return target.handle.DetachActive(ctx)
}

// Discard closes a rejected recovery admission without removing any on-disk target.
func (target *NetworklessNamespaceTarget) Discard(ctx context.Context) error {
	if ctx == nil || target == nil || target.handle == nil || target.handle.state == nil {
		return errors.New("authorityfs: networkless target discard is inactive")
	}
	state := target.handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	return closeDetachedNetworkNamespace(context.WithoutCancel(ctx), state)
}

func inspectAnonymousNetworklessPlaceholder(
	ctx context.Context, state *networkNamespaceHandleState,
) (networkNamespaceIdentity, error) {
	var stat unix.Stat_t
	if err := state.deps.fstat(ctx, state.placeholderFD, &stat); err != nil {
		return networkNamespaceIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != networkNamespaceHandleMode ||
		stat.Uid != state.policy.expectedUID || stat.Gid != state.policy.expectedGID || stat.Nlink != 0 ||
		stat.Size != 0 || stat.Dev == 0 || stat.Ino == 0 {
		return networkNamespaceIdentity{}, errors.New("authorityfs: anonymous networkless target identity is invalid")
	}
	if err := verifyNetworklessPlaceholderToken(state.placeholderFD, state.networklessToken); err != nil {
		return networkNamespaceIdentity{}, err
	}
	mount, err := inspectNetworkNamespaceMountIdentity(ctx, state.deps, state.placeholderFD)
	if err != nil || mount.Kind != NetworkNamespaceMountIdentityUnique {
		return networkNamespaceIdentity{}, errors.Join(
			err,
			errors.New("authorityfs: unique placeholder mount ID unavailable"),
		)
	}
	return networkNamespaceIdentity{device: stat.Dev, inode: stat.Ino, mount: mount}, nil
}

func verifyNetworklessPlaceholderToken(fd int, want [32]byte) error {
	var observed [32]byte
	count, err := unix.Fgetxattr(fd, networklessNamespaceTokenAttribute, observed[:])
	if err != nil || count != len(observed) || observed != want {
		return errors.Join(errors.New("authorityfs: networkless placeholder token changed"), err)
	}
	return nil
}
