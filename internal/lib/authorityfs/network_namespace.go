package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	networkNamespaceDirectoryName = "netns"
	networkNamespaceHandleMode    = uint32(0o600)

	networkNamespaceCreateFlags  = unix.O_RDWR | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
	networkNamespaceInspectFlags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	networkNamespaceStatxFlags   = unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
		unix.AT_STATX_SYNC_AS_STAT
	networkNamespaceStatxMask = unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
)

// NetworkNamespaceMountIdentityKind identifies the kernel mount-ID representation used for an exact comparison.
type NetworkNamespaceMountIdentityKind uint8

const (
	// NetworkNamespaceMountIdentityUnavailable means the kernel returned no mount-ID field.
	NetworkNamespaceMountIdentityUnavailable NetworkNamespaceMountIdentityKind = iota
	// NetworkNamespaceMountIdentityLegacy identifies STATX_MNT_ID.
	NetworkNamespaceMountIdentityLegacy
	// NetworkNamespaceMountIdentityUnique identifies STATX_MNT_ID_UNIQUE.
	NetworkNamespaceMountIdentityUnique
)

// NetworkNamespaceMountIdentity records an explicit unavailable, legacy, or unique mount identity.
type NetworkNamespaceMountIdentity struct {
	Kind  NetworkNamespaceMountIdentityKind
	Value uint64
}

// NetworkNamespaceTargetIdentity identifies the exact retained directory and placeholder selected for one namespace
// mount. It deliberately exposes no path or descriptor.
type NetworkNamespaceTargetIdentity struct {
	DirectoryDevice   uint64
	DirectoryInode    uint64
	DirectoryMount    NetworkNamespaceMountIdentity
	PlaceholderDevice uint64
	PlaceholderInode  uint64
	PlaceholderMount  NetworkNamespaceMountIdentity
}

type networkNamespaceIdentity struct {
	device uint64
	inode  uint64
	mount  NetworkNamespaceMountIdentity
}

// NetworkNamespaceHandle owns one root-created per-VM namespace placeholder beneath a NetworkUID capability. It
// exposes identity and checked cleanup only; namespace mounting remains owned by internal/service/network.
// THREAD-SAFE: copies share one close-once state guarded by an internal mutex.
type NetworkNamespaceHandle struct {
	state *networkNamespaceHandleState
}

type networkNamespaceHandleState struct {
	// mu guards every descriptor, binding, identity, and lifecycle field below.
	mu sync.Mutex

	deps     dependencies
	policy   policy
	ownerUID uint32
	key      VMKey

	directoryFD       int
	placeholderFD     int
	directoryIdentity networkNamespaceIdentity
	targetIdentity    networkNamespaceIdentity
	metadataPrepared  bool
	admitted          bool
	leafCreated       bool
	parentSyncPending bool
	removed           bool
	released          bool
	releaseErr        error
}

// CreateNamespaceHandle creates the exact root-owned netns/<VMKey> placeholder. Once the leaf exists, every error
// returns a non-nil handle so the caller retains cleanup authority.
func (uid *NetworkUID) CreateNamespaceHandle(
	ctx context.Context,
	key VMKey,
) (*NetworkNamespaceHandle, error) {
	if ctx == nil {
		return nil, errors.New("authorityfs: network namespace context is required")
	}
	if !isLowerHex(key.value, vmKeyLength) {
		return nil, errors.New("authorityfs: network namespace VM key is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: create network namespace handle: %w", err)
	}
	if uid == nil || uid.state == nil {
		return nil, errors.New("authorityfs: network UID authority is not active")
	}
	uidState := uid.state
	uidState.mu.RLock()
	defer uidState.mu.RUnlock()
	if !uidState.active {
		return nil, errors.New("authorityfs: network UID authority is released")
	}

	directoryFD, err := openManagedDirectory(
		ctx,
		uidState.deps,
		uidState.fd,
		networkNamespaceDirectoryName,
		directoryMode,
		resolveManaged,
		uidState.policy,
	)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open network namespace directory: %w", err)
	}
	directoryIdentity, err := inspectNetworkNamespaceDirectory(
		ctx,
		uidState.deps,
		directoryFD,
		uidState.policy,
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("authorityfs: inspect network namespace directory: %w", err),
			closeNetworkNamespaceFD(context.WithoutCancel(ctx), uidState.deps, directoryFD),
		)
	}

	placeholderFD, err := uidState.deps.openAt2(ctx, directoryFD, key.value, &unix.OpenHow{
		Flags:   networkNamespaceCreateFlags,
		Mode:    uint64(networkNamespaceHandleMode),
		Resolve: resolveManaged,
	})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("authorityfs: create network namespace placeholder: %w", err),
			closeNetworkNamespaceFD(context.WithoutCancel(ctx), uidState.deps, directoryFD),
		)
	}
	state := &networkNamespaceHandleState{
		deps:              uidState.deps,
		policy:            uidState.policy,
		ownerUID:          uidState.ownerUID,
		key:               key,
		directoryFD:       directoryFD,
		placeholderFD:     placeholderFD,
		directoryIdentity: directoryIdentity,
		leafCreated:       true,
	}
	handle := &NetworkNamespaceHandle{state: state}
	if err := prepareNetworkNamespacePlaceholder(ctx, state); err != nil {
		return handle, fmt.Errorf("authorityfs: prepare network namespace placeholder: %w", err)
	}
	return handle, nil
}

// Binding returns the authenticated owner and opaque VM key only while the handle remains fully admitted and live.
func (handle *NetworkNamespaceHandle) Binding() (uint32, VMKey, error) {
	if handle == nil || handle.state == nil {
		return 0, VMKey{}, errors.New("authorityfs: network namespace handle is not active")
	}
	state := handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.admitted || !state.leafCreated || state.parentSyncPending ||
		state.ownerUID == 0 || !isLowerHex(state.key.value, vmKeyLength) {
		return 0, VMKey{}, errors.New("authorityfs: network namespace handle is not active")
	}
	return state.ownerUID, state.key, nil
}

// TargetIdentity returns the reverified retained placeholder identity while the handle is active.
func (handle *NetworkNamespaceHandle) TargetIdentity(ctx context.Context) (NetworkNamespaceTargetIdentity, error) {
	if ctx == nil {
		return NetworkNamespaceTargetIdentity{}, errors.New("authorityfs: network namespace context is required")
	}
	if err := ctx.Err(); err != nil {
		return NetworkNamespaceTargetIdentity{}, fmt.Errorf(
			"authorityfs: inspect network namespace target identity: %w",
			err,
		)
	}
	if handle == nil || handle.state == nil {
		return NetworkNamespaceTargetIdentity{}, errors.New("authorityfs: network namespace handle is inactive")
	}
	state := handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.admitted || !state.leafCreated || state.parentSyncPending ||
		!validNetworkNamespaceIdentity(state.targetIdentity) {
		return NetworkNamespaceTargetIdentity{}, errors.New(
			"authorityfs: network namespace target identity is unavailable",
		)
	}
	directoryIdentity, err := inspectNetworkNamespaceDirectory(ctx, state.deps, state.directoryFD, state.policy)
	if err != nil {
		return NetworkNamespaceTargetIdentity{}, fmt.Errorf(
			"authorityfs: inspect retained network namespace directory: %w",
			err,
		)
	}
	if directoryIdentity != state.directoryIdentity {
		return NetworkNamespaceTargetIdentity{}, errors.New(
			"authorityfs: retained network namespace directory identity changed",
		)
	}
	targetIdentity, err := inspectOwnedNetworkNamespacePlaceholder(
		ctx,
		state.deps,
		state.placeholderFD,
		state.policy,
		true,
	)
	if err != nil {
		return NetworkNamespaceTargetIdentity{}, fmt.Errorf(
			"authorityfs: inspect retained network namespace placeholder: %w",
			err,
		)
	}
	if targetIdentity != state.targetIdentity {
		return NetworkNamespaceTargetIdentity{}, errors.New(
			"authorityfs: retained network namespace placeholder identity changed",
		)
	}
	return NetworkNamespaceTargetIdentity{
		DirectoryDevice:   state.directoryIdentity.device,
		DirectoryInode:    state.directoryIdentity.inode,
		DirectoryMount:    state.directoryIdentity.mount,
		PlaceholderDevice: state.targetIdentity.device,
		PlaceholderInode:  state.targetIdentity.inode,
		PlaceholderMount:  state.targetIdentity.mount,
	}, nil
}

// Remove unlinks only the exact retained placeholder. A mounted or replaced target fails closed.
func (handle *NetworkNamespaceHandle) Remove(ctx context.Context) error {
	if ctx == nil {
		return errors.New("authorityfs: network namespace cleanup context is required")
	}
	if handle == nil || handle.state == nil {
		return errors.New("authorityfs: network namespace handle is inactive")
	}
	state := handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.removed {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if state.leafCreated {
		if err := verifyRetainedNetworkNamespaceDirectory(cleanupCtx, state); err != nil {
			return err
		}
		retainedIdentity, err := inspectOwnedNetworkNamespacePlaceholder(
			cleanupCtx,
			state.deps,
			state.placeholderFD,
			state.policy,
			state.metadataPrepared,
		)
		if err != nil {
			return fmt.Errorf("authorityfs: inspect retained network namespace placeholder: %w", err)
		}
		if validNetworkNamespaceIdentity(state.targetIdentity) && retainedIdentity != state.targetIdentity {
			return errors.New("authorityfs: retained network namespace placeholder identity changed")
		}
		state.targetIdentity = retainedIdentity

		currentFD, err := state.deps.openAt2(cleanupCtx, state.directoryFD, state.key.value, &unix.OpenHow{
			Flags:   networkNamespaceInspectFlags,
			Resolve: resolveManaged,
		})
		if err != nil {
			return fmt.Errorf("authorityfs: reopen network namespace placeholder: %w", err)
		}
		currentIdentity, inspectErr := inspectOwnedNetworkNamespacePlaceholder(
			cleanupCtx,
			state.deps,
			currentFD,
			state.policy,
			state.metadataPrepared,
		)
		closeErr := closeNetworkNamespaceFD(cleanupCtx, state.deps, currentFD)
		if inspectErr != nil {
			return errors.Join(
				fmt.Errorf("authorityfs: inspect reopened network namespace placeholder: %w", inspectErr),
				closeErr,
			)
		}
		if closeErr != nil {
			return closeErr
		}
		if currentIdentity != state.targetIdentity {
			return errors.New("authorityfs: network namespace placeholder identity changed")
		}
		if err := state.deps.unlinkAt(cleanupCtx, state.directoryFD, state.key.value, 0); err != nil {
			unlinkErr := fmt.Errorf("authorityfs: unlink network namespace placeholder: %w", err)
			if reconcileErr := reconcileNetworkNamespaceUnlink(cleanupCtx, state); reconcileErr != nil {
				return errors.Join(unlinkErr, reconcileErr)
			}
			return unlinkErr
		}
		markNetworkNamespaceUnlinked(state)
	}
	if state.parentSyncPending {
		if err := state.deps.fsync(cleanupCtx, state.directoryFD); err != nil {
			return fmt.Errorf("authorityfs: sync removed network namespace placeholder: %w", err)
		}
		state.parentSyncPending = false
	}
	state.removed = true
	return nil
}

// Release closes the placeholder and directory descriptors exactly once after Remove succeeds.
func (handle *NetworkNamespaceHandle) Release(ctx context.Context) error {
	if handle == nil || handle.state == nil {
		return nil
	}
	state := handle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.removed {
		return errors.New("authorityfs: network namespace placeholder has not been removed")
	}
	cleanupCtx := context.Background()
	var result error
	if ctx == nil {
		result = errors.New("authorityfs: network namespace cleanup context is required")
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	placeholderFD := state.placeholderFD
	state.placeholderFD = -1
	if placeholderFD >= 0 {
		result = errors.Join(result, closeNetworkNamespaceFD(cleanupCtx, state.deps, placeholderFD))
	}
	directoryFD := state.directoryFD
	state.directoryFD = -1
	if directoryFD >= 0 {
		result = errors.Join(result, closeNetworkNamespaceFD(cleanupCtx, state.deps, directoryFD))
	}
	state.directoryIdentity = networkNamespaceIdentity{}
	state.targetIdentity = networkNamespaceIdentity{}
	state.ownerUID = 0
	state.key = VMKey{}
	state.metadataPrepared = false
	state.admitted = false
	state.leafCreated = false
	state.parentSyncPending = false
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}

func prepareNetworkNamespacePlaceholder(ctx context.Context, state *networkNamespaceHandleState) error {
	if err := state.deps.fchown(
		ctx,
		state.placeholderFD,
		int(state.policy.expectedUID),
		int(state.policy.expectedGID),
	); err != nil {
		return fmt.Errorf("set exact owner: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.deps.fchmod(ctx, state.placeholderFD, networkNamespaceHandleMode); err != nil {
		return fmt.Errorf("set exact mode: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := inspectOwnedNetworkNamespacePlaceholder(
		ctx,
		state.deps,
		state.placeholderFD,
		state.policy,
		true,
	)
	if err != nil {
		return err
	}
	state.targetIdentity = identity
	state.metadataPrepared = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.deps.fsync(ctx, state.placeholderFD); err != nil {
		return fmt.Errorf("sync placeholder: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.deps.fsync(ctx, state.directoryFD); err != nil {
		return fmt.Errorf("sync placeholder directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state.admitted = true
	return nil
}

func inspectNetworkNamespaceDirectory(
	ctx context.Context,
	deps dependencies,
	fd int,
	policy policy,
) (networkNamespaceIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return networkNamespaceIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&0o7777 != directoryMode || stat.Dev == 0 || stat.Ino == 0 {
		return networkNamespaceIdentity{}, errors.New("network namespace directory has unexpected identity")
	}
	mountIdentity, err := inspectNetworkNamespaceMountIdentity(ctx, deps, fd)
	if err != nil {
		return networkNamespaceIdentity{}, err
	}
	return networkNamespaceIdentity{
		device: stat.Dev, inode: stat.Ino, mount: mountIdentity,
	}, nil
}

func inspectOwnedNetworkNamespacePlaceholder(
	ctx context.Context,
	deps dependencies,
	fd int,
	policy policy,
	exactMode bool,
) (networkNamespaceIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return networkNamespaceIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Nlink != 1 || stat.Size != 0 || stat.Dev == 0 || stat.Ino == 0 {
		return networkNamespaceIdentity{}, errors.New(
			"network namespace placeholder is not an empty root-owned one-link regular file",
		)
	}
	permissions := stat.Mode & 0o7777
	if (exactMode && permissions != networkNamespaceHandleMode) ||
		(!exactMode && permissions&^networkNamespaceHandleMode != 0) {
		return networkNamespaceIdentity{}, errors.New("network namespace placeholder has unexpected mode")
	}
	mountIdentity, err := inspectNetworkNamespaceMountIdentity(ctx, deps, fd)
	if err != nil {
		return networkNamespaceIdentity{}, err
	}
	return networkNamespaceIdentity{
		device: stat.Dev, inode: stat.Ino, mount: mountIdentity,
	}, nil
}

func inspectNetworkNamespaceMountIdentity(
	ctx context.Context,
	deps dependencies,
	fd int,
) (NetworkNamespaceMountIdentity, error) {
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, networkNamespaceStatxFlags, networkNamespaceStatxMask, &stat); err != nil {
		return NetworkNamespaceMountIdentity{}, fmt.Errorf("inspect network namespace mount identity: %w", err)
	}
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
		if stat.Mnt_id == 0 {
			return NetworkNamespaceMountIdentity{}, errors.New("network namespace unique mount identity is zero")
		}
		return NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityUnique, Value: stat.Mnt_id}, nil
	case stat.Mask&unix.STATX_MNT_ID != 0:
		if stat.Mnt_id == 0 {
			return NetworkNamespaceMountIdentity{}, errors.New("network namespace mount identity is zero")
		}
		return NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityLegacy, Value: stat.Mnt_id}, nil
	default:
		return NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityUnavailable}, nil
	}
}

func reconcileNetworkNamespaceUnlink(ctx context.Context, state *networkNamespaceHandleState) error {
	currentFD, err := state.deps.openAt2(ctx, state.directoryFD, state.key.value, &unix.OpenHow{
		Flags:   networkNamespaceInspectFlags,
		Resolve: resolveManaged,
	})
	if errors.Is(err, unix.ENOENT) {
		markNetworkNamespaceUnlinked(state)
		return nil
	}
	if err != nil {
		return fmt.Errorf("authorityfs: reconcile network namespace placeholder after unlink error: %w", err)
	}
	currentIdentity, inspectErr := inspectOwnedNetworkNamespacePlaceholder(
		ctx,
		state.deps,
		currentFD,
		state.policy,
		state.metadataPrepared,
	)
	closeErr := closeNetworkNamespaceFD(ctx, state.deps, currentFD)
	if inspectErr != nil {
		return errors.Join(
			fmt.Errorf("authorityfs: inspect network namespace placeholder after unlink error: %w", inspectErr),
			closeErr,
		)
	}
	if closeErr != nil {
		return closeErr
	}
	if currentIdentity != state.targetIdentity {
		return errors.New("authorityfs: network namespace placeholder identity changed after unlink error")
	}
	return nil
}

func markNetworkNamespaceUnlinked(state *networkNamespaceHandleState) {
	state.admitted = false
	state.leafCreated = false
	state.parentSyncPending = true
}

func verifyRetainedNetworkNamespaceDirectory(ctx context.Context, state *networkNamespaceHandleState) error {
	identity, err := inspectNetworkNamespaceDirectory(ctx, state.deps, state.directoryFD, state.policy)
	if err != nil {
		return fmt.Errorf("authorityfs: verify retained network namespace directory: %w", err)
	}
	if identity != state.directoryIdentity {
		return errors.New("authorityfs: retained network namespace directory identity changed")
	}
	return nil
}

func validNetworkNamespaceIdentity(identity networkNamespaceIdentity) bool {
	return identity.device != 0 && identity.inode != 0 && validNetworkNamespaceMountIdentity(identity.mount)
}

func validNetworkNamespaceMountIdentity(identity NetworkNamespaceMountIdentity) bool {
	switch identity.Kind {
	case NetworkNamespaceMountIdentityUnavailable:
		return identity.Value == 0
	case NetworkNamespaceMountIdentityLegacy, NetworkNamespaceMountIdentityUnique:
		return identity.Value != 0
	default:
		return false
	}
}

func closeNetworkNamespaceFD(ctx context.Context, deps dependencies, fd int) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return fmt.Errorf("authorityfs: close network namespace descriptor: %w", err)
	}
	return nil
}
