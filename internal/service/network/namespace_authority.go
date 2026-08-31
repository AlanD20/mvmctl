package network

import (
	"context"
	"errors"
	"fmt"

	"mvmctl/internal/lib/authorityfs"
)

type namespaceTargetMountIdentityKind uint8

const (
	namespaceTargetMountIdentityUnavailable namespaceTargetMountIdentityKind = iota
	namespaceTargetMountIdentityLegacy
	namespaceTargetMountIdentityUnique
)

type namespaceTargetMountIdentity struct {
	kind  namespaceTargetMountIdentityKind
	value uint64
}

type namespaceTargetIdentity struct {
	directoryDevice   uint64
	directoryInode    uint64
	directoryMount    namespaceTargetMountIdentity
	placeholderDevice uint64
	placeholderInode  uint64
	placeholderMount  namespaceTargetMountIdentity
}

// namespaceAuthorityTarget is the exact authorityfs capability behind the network package's private adapter. It
// deliberately omits descriptors, paths, callbacks, generic locks, and mount operations.
type namespaceAuthorityTarget interface {
	binding() (uint32, authorityfs.VMKey, error)
	targetIdentity(context.Context) (namespaceTargetIdentity, error)
	remove(context.Context) error
	release(context.Context) error
}

// namespaceTargetLease is the namespace effect's closed authority over one exact placeholder. A non-nil lease returned
// with an error still owns cleanup responsibility for a placeholder created before that error.
type namespaceTargetLease interface {
	targetIdentity(context.Context) (namespaceTargetIdentity, error)
	remove(context.Context) error
	release(context.Context) error
}

type namespaceTargetAuthority interface {
	create(context.Context, topologyKey) (namespaceTargetLease, error)
}

type namespaceHandleAuthority struct {
	tree networkAuthorityTree
}

type namespaceHandleLease struct {
	target   namespaceAuthorityTarget
	ownerUID uint32
	vmID     VMID
	vmKey    authorityfs.VMKey
}

type systemNamespaceAuthorityTarget struct {
	value *authorityfs.NetworkNamespaceHandle
}

func newNamespaceHandleAuthority(tree networkAuthorityTree) *namespaceHandleAuthority {
	if tree == nil {
		panic("network namespace authority tree is required")
	}
	return &namespaceHandleAuthority{tree: tree}
}

func (authority *namespaceHandleAuthority) create(
	ctx context.Context,
	key topologyKey,
) (_ namespaceTargetLease, returnErr error) {
	if ctx == nil {
		return nil, topologyPrepareError(
			"create namespace authority handle",
			string(key.vmID),
			errors.New("context is required"),
			false,
		)
	}
	vmKey, err := authorityfs.NewVMKey(string(key.vmID))
	if err != nil {
		return nil, topologyPrepareError("derive namespace authority key", string(key.vmID), err, false)
	}
	var root networkLockRoot
	var uid networkLockUID
	var lease namespaceTargetLease
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		var cleanupErr error
		if uid != nil {
			cleanupErr = errors.Join(cleanupErr, uid.Release(cleanupCtx))
		}
		if root != nil {
			cleanupErr = errors.Join(cleanupErr, root.Release(cleanupCtx))
		}
		if cleanupErr != nil {
			returnErr = topologyPrepareError(
				"release namespace authority parents",
				string(key.vmID),
				errors.Join(returnErr, cleanupErr),
				lease != nil,
			)
		}
	}()

	root, err = authority.tree.openNetwork(ctx)
	if err != nil {
		return nil, topologyPrepareError("open namespace authority", string(key.vmID), err, false)
	}
	uid, err = root.openUID(ctx, uint32(key.ownerUID))
	if err != nil {
		return nil, topologyPrepareError("open namespace owner authority", string(key.vmID), err, false)
	}
	target, err := uid.createNamespaceHandle(ctx, vmKey)
	var handleLease *namespaceHandleLease
	if target != nil {
		handleLease = &namespaceHandleLease{
			target: target, ownerUID: uint32(key.ownerUID), vmID: key.vmID, vmKey: vmKey,
		}
		lease = handleLease
	}
	if err != nil {
		return lease, topologyPrepareError(
			"create namespace authority handle",
			string(key.vmID),
			err,
			lease != nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return lease, topologyPrepareError(
			"create namespace authority handle",
			string(key.vmID),
			err,
			lease != nil,
		)
	}
	if lease == nil {
		return nil, topologyPrepareError(
			"create namespace authority handle",
			string(key.vmID),
			errors.New("namespace authority returned no handle"),
			false,
		)
	}
	if err := handleLease.verifyBinding(); err != nil {
		return lease, topologyPrepareError(
			"verify namespace authority binding",
			string(key.vmID),
			err,
			true,
		)
	}
	return lease, nil
}

func (lease *namespaceHandleLease) targetIdentity(ctx context.Context) (namespaceTargetIdentity, error) {
	if lease == nil || lease.target == nil {
		return namespaceTargetIdentity{}, topologyPrepareError(
			"inspect namespace authority handle",
			"",
			errors.New("namespace authority handle is unavailable"),
			false,
		)
	}
	if err := lease.verifyBinding(); err != nil {
		return namespaceTargetIdentity{}, topologyPrepareError(
			"verify namespace authority binding",
			string(lease.vmID),
			err,
			true,
		)
	}
	identity, err := lease.target.targetIdentity(ctx)
	if err != nil {
		return namespaceTargetIdentity{}, topologyPrepareError(
			"inspect namespace authority handle",
			string(lease.vmID),
			err,
			true,
		)
	}
	if identity.directoryDevice == 0 || identity.directoryInode == 0 ||
		!validNamespaceTargetMountIdentity(identity.directoryMount) || identity.placeholderDevice == 0 ||
		identity.placeholderInode == 0 || !validNamespaceTargetMountIdentity(identity.placeholderMount) {
		return namespaceTargetIdentity{}, topologyPrepareError(
			"inspect namespace authority handle",
			string(lease.vmID),
			errors.New("namespace placeholder identity is invalid"),
			true,
		)
	}
	return identity, nil
}

func (lease *namespaceHandleLease) verifyBinding() error {
	ownerUID, vmKey, err := lease.target.binding()
	if err != nil {
		return fmt.Errorf("inspect namespace authority binding: %w", err)
	}
	if ownerUID != lease.ownerUID || vmKey != lease.vmKey {
		return errors.New("namespace authority binding does not match the requested topology")
	}
	return nil
}

func (lease *namespaceHandleLease) remove(ctx context.Context) error {
	if lease == nil || lease.target == nil {
		return topologyRemoveError("", "namespace authority handle is unavailable", nil)
	}
	if err := lease.target.remove(ctx); err != nil {
		return topologyRemoveError(string(lease.vmID), "failed to remove namespace authority handle", err)
	}
	return nil
}

func (lease *namespaceHandleLease) release(ctx context.Context) error {
	if lease == nil || lease.target == nil {
		return nil
	}
	if err := lease.target.release(ctx); err != nil {
		return topologyRemoveError(string(lease.vmID), "failed to release namespace authority handle", err)
	}
	return nil
}

func (target *systemNamespaceAuthorityTarget) targetIdentity(
	ctx context.Context,
) (namespaceTargetIdentity, error) {
	if target == nil || target.value == nil {
		return namespaceTargetIdentity{}, errors.New("network namespace authority target is unavailable")
	}
	identity, err := target.value.TargetIdentity(ctx)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	directoryMount, err := mapNamespaceTargetMountIdentity(identity.DirectoryMount)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	placeholderMount, err := mapNamespaceTargetMountIdentity(identity.PlaceholderMount)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	return namespaceTargetIdentity{
		directoryDevice:   identity.DirectoryDevice,
		directoryInode:    identity.DirectoryInode,
		directoryMount:    directoryMount,
		placeholderDevice: identity.PlaceholderDevice,
		placeholderInode:  identity.PlaceholderInode,
		placeholderMount:  placeholderMount,
	}, nil
}

func (target *systemNamespaceAuthorityTarget) binding() (uint32, authorityfs.VMKey, error) {
	if target == nil || target.value == nil {
		return 0, authorityfs.VMKey{}, errors.New("network namespace authority target is unavailable")
	}
	return target.value.Binding()
}

func (target *systemNamespaceAuthorityTarget) remove(ctx context.Context) error {
	if target == nil || target.value == nil {
		return errors.New("network namespace authority target is unavailable")
	}
	return target.value.Remove(ctx)
}

func (target *systemNamespaceAuthorityTarget) release(ctx context.Context) error {
	if target == nil || target.value == nil {
		return nil
	}
	return target.value.Release(ctx)
}

func mapNamespaceTargetMountIdentity(
	identity authorityfs.NetworkNamespaceMountIdentity,
) (namespaceTargetMountIdentity, error) {
	var kind namespaceTargetMountIdentityKind
	switch identity.Kind {
	case authorityfs.NetworkNamespaceMountIdentityUnavailable:
		kind = namespaceTargetMountIdentityUnavailable
	case authorityfs.NetworkNamespaceMountIdentityLegacy:
		kind = namespaceTargetMountIdentityLegacy
	case authorityfs.NetworkNamespaceMountIdentityUnique:
		kind = namespaceTargetMountIdentityUnique
	default:
		return namespaceTargetMountIdentity{}, errors.New("network namespace mount identity kind is invalid")
	}
	mount := namespaceTargetMountIdentity{kind: kind, value: identity.Value}
	if !validNamespaceTargetMountIdentity(mount) {
		return namespaceTargetMountIdentity{}, errors.New("network namespace mount identity value is invalid")
	}
	return mount, nil
}

func validNamespaceTargetMountIdentity(identity namespaceTargetMountIdentity) bool {
	switch identity.kind {
	case namespaceTargetMountIdentityUnavailable:
		return identity.value == 0
	case namespaceTargetMountIdentityLegacy, namespaceTargetMountIdentityUnique:
		return identity.value != 0
	default:
		return false
	}
}
