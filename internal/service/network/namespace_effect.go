package network

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	namespaceOpenTreeFlags  = unix.AT_EMPTY_PATH | unix.OPEN_TREE_CLONE | unix.OPEN_TREE_CLOEXEC
	namespaceMoveMountFlags = unix.MOVE_MOUNT_F_EMPTY_PATH | unix.MOVE_MOUNT_T_EMPTY_PATH
)

// namespaceEffect owns only creation and removal of one root-pinned network namespace. The private topology effect
// composes it with the separate typed link adapter.
type namespaceEffect struct {
	deps      namespaceEffectDeps
	authority namespaceTargetAuthority
	policy    namespaceEffectPolicy
}

type namespacePin struct {
	state *namespacePinState
}

type linkNamespaceLease struct {
	state *linkNamespaceLeaseState
}

type linkNamespaceLeaseState struct {
	// mu guards the retained descriptor and cached close result.
	mu sync.Mutex

	deps       namespaceEffectDeps
	fd         int
	identity   namespaceIdentity
	ownerUID   OwnerUID
	closed     bool
	closeError error
}

type namespacePinState struct {
	// mu guards every descriptor, retained authority capability, outcome flag, and lifecycle field below.
	mu sync.Mutex

	owner                  *namespaceEffect
	deps                   namespaceEffectDeps
	target                 namespaceTargetLease
	targetID               namespaceTargetIdentity
	key                    namespaceKey
	handlePath             namespaceHandlePath
	namespaceID            namespaceIdentity
	networklessMountID     uint64
	handle                 namespaceHandle
	parentFD               int
	targetFD               int
	netnsFD                int
	publishFD              int
	created                bool
	pinned                 bool
	published              bool
	mounted                bool
	verified               bool
	restored               bool
	admitted               bool
	unmountOutcomeUnknown  bool
	authorityTargetRemoved bool
	removed                bool
	released               bool
	releaseErr             error
}

type namespaceCreateOutcome struct {
	cleanupRequired bool
	created         bool
	pinned          bool
	published       bool
	verified        bool
	restored        bool
}

type namespaceRemoveOutcome struct {
	cleanupRequired        bool
	published              bool
	unmounted              bool
	authorityTargetRemoved bool
	unmountOutcomeUnknown  bool
}

type namespaceUnmountObservation uint8

const (
	namespaceUnmountObservationMounted namespaceUnmountObservation = iota + 1
	namespaceUnmountObservationUnmounted
)

func newNamespaceEffect() *namespaceEffect {
	return newNamespaceEffectWithDeps(
		systemNamespaceEffectDeps(),
		newNamespaceHandleAuthority(systemNetworkAuthorityTree{}),
		productionNamespaceEffectPolicy(),
	)
}

func newNamespaceEffectWithDeps(
	deps namespaceEffectDeps,
	authority namespaceTargetAuthority,
	policy namespaceEffectPolicy,
) *namespaceEffect {
	return &namespaceEffect{deps: deps, authority: authority, policy: policy}
}

func (effect *namespaceEffect) CreateNamespace(
	ctx context.Context,
	spec namespaceCreateSpec,
) (NamespacePin, error) {
	return effect.createNamespace(ctx, spec, nil)
}

func (effect *namespaceEffect) CreateNetworklessNamespace(
	ctx context.Context, spec namespaceCreateSpec, publication *networklessPublication,
) (NamespacePin, error) {
	if publication == nil || publication.target == nil {
		return nil, namespaceEffectCreateError(spec.key, "create networkless namespace",
			errors.New("staged publication authority is unavailable"), namespaceCreateOutcome{})
	}
	return effect.createNamespace(ctx, spec, publication)
}

func (effect *namespaceEffect) createNamespace(
	ctx context.Context, spec namespaceCreateSpec, publication *networklessPublication,
) (NamespacePin, error) {
	if ctx == nil {
		return nil, namespaceEffectCreateError(
			spec.key,
			"create network namespace",
			errors.New("context is required"),
			namespaceCreateOutcome{},
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, namespaceEffectCreateError(
			spec.key,
			"create network namespace",
			err,
			namespaceCreateOutcome{},
		)
	}
	if effect == nil || effect.authority == nil || validateNamespaceEffectDeps(effect.deps) != nil {
		return nil, namespaceEffectCreateError(
			spec.key,
			"create network namespace",
			errors.New("dependencies are incomplete"),
			namespaceCreateOutcome{},
		)
	}
	var pin NamespacePin
	err := effect.deps.runWorker(ctx, func(workerCtx context.Context) error {
		var workerErr error
		pin, workerErr = effect.createNamespaceOnWorker(workerCtx, spec, publication)
		return workerErr
	})
	if err != nil {
		if errs.AsDomainError(err) != nil {
			return pin, err
		}
		return pin, namespaceEffectCreateError(
			spec.key,
			"run network namespace worker",
			err,
			namespaceCreateOutcome{},
		)
	}
	created, ok := pin.(*namespacePin)
	if !ok || created == nil || created.state == nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"admit network namespace handle",
			errors.New("namespace worker returned no retained pin"),
			namespaceCreateOutcome{},
		)
	}
	state := created.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.mounted || !state.verified || !state.restored || !validNamespaceIdentity(state.namespaceID) {
		return pin, namespaceEffectCreateError(
			spec.key,
			"admit network namespace handle",
			errors.New("namespace worker returned incomplete publication evidence"),
			state.createOutcome(),
		)
	}
	state.handle = namespaceHandle{
		path: state.handlePath, identity: state.namespaceID, ownerUID: 0, ownerGID: 0, mode: namespaceHandleMode,
	}
	state.admitted = true
	return pin, nil
}

func (effect *namespaceEffect) createNamespaceOnWorker(
	ctx context.Context,
	spec namespaceCreateSpec,
	publication *networklessPublication,
) (_ NamespacePin, returnErr error) {
	if ctx == nil {
		return nil, namespaceEffectCreateError(
			spec.key,
			"create network namespace",
			errors.New("context is required"),
			namespaceCreateOutcome{},
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, namespaceEffectCreateError(
			spec.key,
			"create network namespace",
			err,
			namespaceCreateOutcome{},
		)
	}
	if effect == nil || effect.authority == nil || validateNamespaceEffectDeps(effect.deps) != nil {
		return nil, namespaceEffectCreateError(
			spec.key,
			"create network namespace",
			errors.New("dependencies are incomplete"),
			namespaceCreateOutcome{},
		)
	}

	var target namespaceTargetLease
	var err error
	if publication != nil {
		target = publication.target
		if target == nil {
			err = errors.New("networkless namespace target is unavailable")
		}
	} else {
		target, err = effect.authority.create(ctx, spec.key)
	}
	if target == nil {
		if err == nil {
			err = errors.New("namespace authority returned no cleanup capability")
		}
		return nil, namespaceEffectCreateError(
			spec.key,
			"create namespace authority target",
			err,
			namespaceCreateOutcome{},
		)
	}
	state := &namespacePinState{
		owner:      effect,
		deps:       effect.deps,
		target:     target,
		key:        spec.key,
		handlePath: deriveNamespaceHandle(spec.key),
		parentFD:   -1, targetFD: -1, netnsFD: -1, publishFD: -1,
	}
	pin := &namespacePin{state: state}
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"create namespace authority target",
			err,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"create namespace authority target",
			err,
			state.createOutcome(),
		)
	}
	targetIdentity, err := target.targetIdentity(ctx)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect namespace authority target",
			err,
			state.createOutcome(),
		)
	}
	state.targetID = targetIdentity
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect namespace authority target",
			err,
			state.createOutcome(),
		)
	}
	state.parentFD, state.targetFD, err = openExactNamespaceTarget(
		ctx,
		effect.deps,
		effect.policy,
		spec.key,
		targetIdentity,
	)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"open exact namespace authority target",
			err,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"open exact namespace authority target",
			err,
			state.createOutcome(),
		)
	}

	threadLocked := false
	mustRestore := false
	restoreAttempted := false
	safeToUnlock := false
	hostFD := -1
	mountFD := -1
	publicationTargetFD := -1
	hostIdentity := namespaceIdentity{}
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if mustRestore && !restoreAttempted {
			restoreAttempted = true
			restoreSafe, restoreErr := restoreHostNetworkNamespace(
				cleanupCtx,
				effect.deps,
				effect.policy,
				hostFD,
				hostIdentity,
			)
			safeToUnlock = restoreSafe
			state.restored = restoreSafe
			if restoreErr != nil {
				if restoreSafe {
					returnErr = appendNamespaceEffectCreateCleanupError(
						spec.key,
						returnErr,
						restoreErr,
						state.createOutcome(),
					)
				} else {
					returnErr = markNamespaceThreadFatal(
						spec.key,
						returnErr,
						restoreErr,
						state.createOutcome(),
					)
				}
			}
		}
		returnErr = appendNamespaceEffectCreateCleanupError(
			spec.key,
			returnErr,
			errors.Join(
				closeConsumedNamespaceFD(cleanupCtx, effect.deps, &publicationTargetFD),
				closeConsumedNamespaceFD(cleanupCtx, effect.deps, &mountFD),
				closeConsumedNamespaceFD(cleanupCtx, effect.deps, &hostFD),
			),
			state.createOutcome(),
		)
		if threadLocked && (!mustRestore || safeToUnlock) {
			returnErr = appendNamespaceEffectCreateCleanupError(
				spec.key,
				returnErr,
				effect.deps.unlockThread(cleanupCtx),
				state.createOutcome(),
			)
		}
		if returnErr != nil {
			returnErr = namespaceEffectCreateError(
				spec.key,
				"create network namespace",
				returnErr,
				state.createOutcome(),
			)
		}
	}()

	if err := effect.deps.lockThread(ctx); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"lock network namespace thread",
			err,
			state.createOutcome(),
		)
	}
	threadLocked = true
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"lock network namespace thread",
			err,
			state.createOutcome(),
		)
	}
	hostFD, err = effect.deps.open(ctx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"open host network namespace",
			err,
			state.createOutcome(),
		)
	}
	hostIdentity, err = inspectNetworkNamespaceFD(ctx, effect.deps, effect.policy, hostFD)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect host network namespace",
			err,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect host network namespace",
			err,
			state.createOutcome(),
		)
	}
	if err := effect.deps.unshare(ctx, unix.CLONE_NEWNET); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"unshare network namespace",
			err,
			state.createOutcome(),
		)
	}
	mustRestore = true
	state.created = true
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"unshare network namespace",
			err,
			state.createOutcome(),
		)
	}
	state.netnsFD, err = effect.deps.open(ctx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"open created network namespace",
			err,
			state.createOutcome(),
		)
	}
	createdIdentity, err := inspectNetworkNamespaceFD(ctx, effect.deps, effect.policy, state.netnsFD)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect created network namespace",
			err,
			state.createOutcome(),
		)
	}
	if createdIdentity == hostIdentity {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect created network namespace",
			errors.New("created namespace matches host namespace"),
			state.createOutcome(),
		)
	}
	state.namespaceID = createdIdentity
	state.pinned = true
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect created network namespace",
			err,
			state.createOutcome(),
		)
	}
	mountFD, err = effect.deps.openTree(ctx, state.netnsFD, "", uint(namespaceOpenTreeFlags))
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"clone created namespace mount",
			err,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"clone created namespace mount",
			err,
			state.createOutcome(),
		)
	}
	if err := verifyNamespaceDirectory(
		ctx,
		effect.deps,
		state.parentFD,
		effect.policy,
		true,
		targetIdentity,
	); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"recheck namespace authority directory",
			err,
			state.createOutcome(),
		)
	}
	publicationTargetFD, err = openExactNamespacePlaceholder(
		ctx,
		effect.deps,
		effect.policy,
		state.parentFD,
		spec.key.vmID,
		targetIdentity,
	)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"recheck namespace authority placeholder",
			err,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"recheck namespace authority placeholder",
			err,
			state.createOutcome(),
		)
	}
	if publication != nil {
		mountIdentity, mountErr := inspectNamespaceTargetMountIdentity(ctx, effect.deps, mountFD)
		if mountErr != nil || mountIdentity.kind != namespaceTargetMountIdentityUnique {
			return pin, namespaceEffectCreateError(spec.key, "inspect detached namespace unique mount identity",
				errors.Join(mountErr, errors.New("unique detached mount ID is unavailable")), state.createOutcome())
		}
		if err := publication.recordDetachedMount(ctx, createdIdentity, mountIdentity.value); err != nil {
			return pin, namespaceEffectCreateError(spec.key, "record detached networkless namespace", err,
				state.createOutcome())
		}
		state.networklessMountID = mountIdentity.value
	}
	if err := effect.deps.moveMount(
		ctx,
		mountFD,
		"",
		publicationTargetFD,
		"",
		namespaceMoveMountFlags,
	); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"publish network namespace mount",
			err,
			state.createOutcome(),
		)
	}
	state.mounted = true
	state.published = true
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"publish network namespace mount",
			err,
			state.createOutcome(),
		)
	}
	if err := effect.deps.fsync(ctx, state.parentFD); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"sync network namespace publication",
			err,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"sync network namespace publication",
			err,
			state.createOutcome(),
		)
	}
	state.publishFD, err = openPublishedNamespace(
		ctx,
		effect.deps,
		effect.policy,
		state.parentFD,
		spec.key.vmID,
		targetIdentity,
	)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"open published network namespace",
			err,
			state.createOutcome(),
		)
	}
	publishedIdentity, err := inspectNetworkNamespaceFD(ctx, effect.deps, effect.policy, state.publishFD)
	if err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect published network namespace",
			err,
			state.createOutcome(),
		)
	}
	if publishedIdentity != createdIdentity {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect published network namespace",
			errors.New("published namespace identity changed"),
			state.createOutcome(),
		)
	}
	state.verified = true
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"inspect published network namespace",
			err,
			state.createOutcome(),
		)
	}

	restoreAttempted = true
	restoreSafe, restoreErr := restoreHostNetworkNamespace(
		context.WithoutCancel(ctx),
		effect.deps,
		effect.policy,
		hostFD,
		hostIdentity,
	)
	safeToUnlock = restoreSafe
	state.restored = restoreSafe
	if restoreErr != nil {
		if restoreSafe {
			return pin, namespaceEffectCreateError(
				spec.key,
				"verify restored host network namespace",
				restoreErr,
				state.createOutcome(),
			)
		}
		return pin, markNamespaceThreadFatal(
			spec.key,
			nil,
			restoreErr,
			state.createOutcome(),
		)
	}
	if err := ctx.Err(); err != nil {
		return pin, namespaceEffectCreateError(
			spec.key,
			"finish network namespace publication",
			err,
			state.createOutcome(),
		)
	}
	return pin, nil
}

func (effect *namespaceEffect) DeleteNamespace(
	ctx context.Context,
	namespace NamespacePin,
) error {
	if ctx == nil {
		return namespaceEffectRemoveError(
			"",
			"network namespace cleanup context is required",
			nil,
			namespaceRemoveOutcome{},
		)
	}
	pin, ok := namespace.(*namespacePin)
	if effect == nil || !ok || pin == nil || pin.state == nil || pin.state.owner != effect {
		return namespaceEffectRemoveError(
			"",
			"network namespace capability is not owned by this effect",
			nil,
			namespaceRemoveOutcome{},
		)
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.removed {
		return nil
	}
	state.admitted = false
	cleanupCtx := context.WithoutCancel(ctx)
	if state.unmountOutcomeUnknown {
		observation, err := observeNamespaceUnmount(cleanupCtx, state)
		if observation == namespaceUnmountObservationUnmounted {
			state.mounted = false
			state.verified = false
		}
		if observation != 0 {
			state.unmountOutcomeUnknown = false
		}
		if err != nil {
			return namespaceEffectRemoveError(
				string(state.key.vmID),
				"reconcile uncertain network namespace unmount",
				err,
				state.removeOutcome(),
			)
		}
	}
	if state.mounted {
		if err := verifyPublishedNamespace(cleanupCtx, state); err != nil {
			return namespaceEffectRemoveError(
				string(state.key.vmID),
				"published network namespace identity changed",
				err,
				state.removeOutcome(),
			)
		}
		if err := closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.publishFD); err != nil {
			return namespaceEffectRemoveError(
				string(state.key.vmID),
				"close published namespace descriptor",
				err,
				state.removeOutcome(),
			)
		}
		path := namespaceProcFDPathPrefix + strconv.Itoa(state.parentFD) + "/" + string(state.key.vmID)
		// Non-lazy unmount fails closed with EBUSY instead of hiding an in-use namespace behind deferred teardown.
		if err := state.deps.unmount(cleanupCtx, path, 0); err != nil {
			unmountErr := err
			observation, observeErr := observeNamespaceUnmount(cleanupCtx, state)
			if observation == namespaceUnmountObservationUnmounted {
				state.mounted = false
				state.verified = false
			}
			if observation == 0 {
				state.unmountOutcomeUnknown = true
			}
			if observeErr != nil {
				unmountErr = errors.Join(unmountErr, observeErr)
			}
			return namespaceEffectRemoveError(
				string(state.key.vmID),
				"unmount exact namespace handle",
				unmountErr,
				state.removeOutcome(),
			)
		}
		state.mounted = false
		state.verified = false
	}
	if err := state.target.remove(cleanupCtx); err != nil {
		return namespaceEffectRemoveError(
			string(state.key.vmID),
			"remove exact namespace authority target",
			err,
			state.removeOutcome(),
		)
	}
	state.authorityTargetRemoved = true
	state.admitted = false
	state.removed = true
	return nil
}

func (pin *namespacePin) Handle() namespaceHandle {
	if pin == nil || pin.state == nil {
		return namespaceHandle{}
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.admitted {
		return namespaceHandle{}
	}
	return state.handle
}

// retainLinkNamespace duplicates the exact live namespace pin for one link operation. The returned lease exposes no
// public descriptor or path and remains independent of later closure of the caller's pin.
func (effect *namespaceEffect) retainLinkNamespace(
	ctx context.Context,
	namespace NamespacePin,
) (_ *linkNamespaceLease, returnErr error) {
	if ctx == nil {
		return nil, errors.New("network namespace retention context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pin, ok := namespace.(*namespacePin)
	if effect == nil || !ok || pin == nil || pin.state == nil || pin.state.owner != effect {
		return nil, errors.New("network namespace capability is not owned by this effect")
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.removed || !state.admitted || !state.mounted || !state.verified || state.netnsFD <= 0 {
		return nil, errors.New("network namespace capability is not live")
	}
	if state.handle.identity != state.namespaceID || !validNamespaceIdentity(state.namespaceID) {
		return nil, errors.New("network namespace capability identity is incomplete")
	}
	pinned, err := inspectNetworkNamespaceFD(ctx, state.deps, effect.policy, state.netnsFD)
	if err != nil {
		return nil, fmt.Errorf("recheck retained network namespace: %w", err)
	}
	if pinned != state.namespaceID {
		return nil, errors.New("retained network namespace identity changed")
	}
	fd, err := state.deps.dupCloexec(ctx, state.netnsFD, 3)
	if err != nil {
		return nil, fmt.Errorf("duplicate retained network namespace: %w", err)
	}
	defer func() {
		if returnErr == nil || fd < 0 {
			return
		}
		returnErr = errors.Join(
			returnErr,
			closeConsumedNamespaceFD(context.WithoutCancel(ctx), state.deps, &fd),
		)
	}()
	if fd <= 0 {
		return nil, errors.New("duplicated network namespace descriptor is not explicit")
	}
	duplicated, err := inspectNetworkNamespaceFD(ctx, state.deps, effect.policy, fd)
	if err != nil {
		return nil, fmt.Errorf("inspect duplicated network namespace: %w", err)
	}
	if duplicated != state.namespaceID {
		return nil, errors.New("duplicated network namespace identity changed")
	}
	return &linkNamespaceLease{state: &linkNamespaceLeaseState{
		deps: state.deps, fd: fd, identity: duplicated, ownerUID: state.key.ownerUID,
	}}, nil
}

func (lease *linkNamespaceLease) descriptor() (int, namespaceIdentity, error) {
	if lease == nil || lease.state == nil {
		return -1, namespaceIdentity{}, errors.New("retained link namespace is inactive")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.fd <= 0 || !validNamespaceIdentity(state.identity) {
		return -1, namespaceIdentity{}, errors.New("retained link namespace is closed")
	}
	return state.fd, state.identity, nil
}

func (lease *linkNamespaceLease) owner() (OwnerUID, error) {
	if lease == nil || lease.state == nil {
		return 0, errors.New("retained link namespace is inactive")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.fd <= 0 || state.ownerUID == 0 {
		return 0, errors.New("retained link namespace has no authenticated owner")
	}
	return state.ownerUID, nil
}

func (lease *linkNamespaceLease) close(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	if lease.state == nil {
		return errors.New("retained link namespace is inactive")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return state.closeError
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if err := closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.fd); err != nil {
		state.closeError = err
	}
	state.identity = namespaceIdentity{}
	state.ownerUID = 0
	state.closed = true
	return state.closeError
}

func (pin *namespacePin) Release(ctx context.Context) error {
	if pin == nil {
		return nil
	}
	if pin.state == nil {
		return namespaceEffectRemoveError(
			"",
			"network namespace capability is inactive",
			nil,
			namespaceRemoveOutcome{},
		)
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.removed {
		return namespaceEffectRemoveError(
			string(state.key.vmID),
			"network namespace handle has not been removed",
			nil,
			state.removeOutcome(),
		)
	}
	cleanupCtx := context.Background()
	var result error
	if ctx == nil {
		result = namespaceEffectRemoveError(
			string(state.key.vmID),
			"network namespace cleanup context is required",
			nil,
			state.removeOutcome(),
		)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	result = errors.Join(
		result,
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.netnsFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.targetFD),
		closeConsumedNamespaceFD(cleanupCtx, state.deps, &state.parentFD),
		state.target.release(cleanupCtx),
	)
	state.target = nil
	state.namespaceID = namespaceIdentity{}
	state.handle = namespaceHandle{}
	state.released = true
	if result != nil {
		state.releaseErr = namespaceEffectRemoveError(
			string(state.key.vmID),
			"failed to release network namespace capability",
			result,
			state.removeOutcome(),
		)
	}
	return state.releaseErr
}

// detachNetworklessActive closes only the staged target and effect descriptors after the journal is active.
func (pin *namespacePin) detachNetworklessActive(ctx context.Context) error {
	if ctx == nil || pin == nil || pin.state == nil {
		return networkAuthorityCorruptError("networkless namespace detach authority is unavailable", nil)
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	target, ok := state.target.(*networklessTargetLease)
	if !ok || target == nil || target.value == nil || state.released || state.removed || !state.admitted ||
		!state.mounted || !state.verified || state.unmountOutcomeUnknown {
		return networkAuthorityCorruptError("networkless namespace is not active for detach", nil)
	}
	if err := verifyPublishedNamespace(ctx, state); err != nil {
		return err
	}
	cleanup := context.WithoutCancel(ctx)
	result := errors.Join(target.value.DetachActive(cleanup),
		closeConsumedNamespaceFD(cleanup, state.deps, &state.publishFD),
		closeConsumedNamespaceFD(cleanup, state.deps, &state.netnsFD),
		closeConsumedNamespaceFD(cleanup, state.deps, &state.targetFD),
		closeConsumedNamespaceFD(cleanup, state.deps, &state.parentFD))
	state.target = nil
	state.namespaceID = namespaceIdentity{}
	state.handle = namespaceHandle{}
	state.admitted = false
	state.released = true
	state.releaseErr = result
	return result
}

func (state *namespacePinState) createOutcome() namespaceCreateOutcome {
	if state == nil {
		return namespaceCreateOutcome{}
	}
	return namespaceCreateOutcome{
		cleanupRequired: state.target != nil,
		created:         state.created,
		pinned:          state.pinned,
		published:       state.published,
		verified:        state.verified,
		restored:        state.restored,
	}
}

func (state *namespacePinState) removeOutcome() namespaceRemoveOutcome {
	if state == nil {
		return namespaceRemoveOutcome{}
	}
	return namespaceRemoveOutcome{
		cleanupRequired:        state.target != nil && !state.released,
		published:              state.published,
		unmounted:              state.published && !state.mounted && !state.unmountOutcomeUnknown,
		authorityTargetRemoved: state.authorityTargetRemoved,
		unmountOutcomeUnknown:  state.unmountOutcomeUnknown,
	}
}

func openPublishedNamespace(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
	parentFD int,
	vmID VMID,
	target namespaceTargetIdentity,
) (int, error) {
	if err := verifyNamespaceDirectory(
		ctx,
		deps,
		parentFD,
		policy,
		true,
		target,
	); err != nil {
		return -1, err
	}
	return deps.openAt2(ctx, parentFD, string(vmID), &unix.OpenHow{
		Flags: namespaceLeafInspectFlags, Resolve: namespaceResolveBeneath,
	})
}

func verifyPublishedNamespace(ctx context.Context, state *namespacePinState) error {
	if !validNamespaceIdentity(state.namespaceID) || state.netnsFD < 0 {
		return errors.New("retained namespace identity is incomplete")
	}
	if err := verifyNamespaceDirectory(
		ctx,
		state.deps,
		state.parentFD,
		state.owner.policy,
		true,
		state.targetID,
	); err != nil {
		return err
	}
	pinned, err := inspectNetworkNamespaceFD(ctx, state.deps, state.owner.policy, state.netnsFD)
	if err != nil {
		return err
	}
	if pinned != state.namespaceID {
		return errors.New("created namespace descriptor changed")
	}
	if state.publishFD >= 0 {
		published, err := inspectNetworkNamespaceFD(ctx, state.deps, state.owner.policy, state.publishFD)
		if err != nil {
			return err
		}
		if published != state.namespaceID {
			return errors.New("retained published namespace descriptor changed")
		}
		if err := verifyNetworklessMountID(ctx, state, state.publishFD); err != nil {
			return err
		}
	}
	currentFD, err := state.deps.openAt2(ctx, state.parentFD, string(state.key.vmID), &unix.OpenHow{
		Flags: namespaceLeafInspectFlags, Resolve: namespaceResolveBeneath,
	})
	if err != nil {
		return err
	}
	current, inspectErr := inspectNetworkNamespaceFD(ctx, state.deps, state.owner.policy, currentFD)
	mountErr := verifyNetworklessMountID(ctx, state, currentFD)
	closeErr := closeConsumedNamespaceFD(ctx, state.deps, &currentFD)
	if inspectErr != nil || mountErr != nil || closeErr != nil {
		return errors.Join(inspectErr, mountErr, closeErr)
	}
	if current != state.namespaceID {
		return errors.New("published namespace identity changed")
	}
	return nil
}

func verifyNetworklessMountID(ctx context.Context, state *namespacePinState, fd int) error {
	if state.networklessMountID == 0 {
		return nil
	}
	mount, err := inspectNamespaceTargetMountIdentity(ctx, state.deps, fd)
	if err != nil || mount.kind != namespaceTargetMountIdentityUnique || mount.value != state.networklessMountID {
		return errors.Join(errors.New("networkless namespace mount identity changed"), err)
	}
	return nil
}

func observeNamespaceUnmount(
	ctx context.Context,
	state *namespacePinState,
) (namespaceUnmountObservation, error) {
	if !validNamespaceIdentity(state.namespaceID) || state.netnsFD < 0 {
		return 0, errors.New("retained namespace identity is incomplete")
	}
	if err := verifyNamespaceDirectory(
		ctx,
		state.deps,
		state.parentFD,
		state.owner.policy,
		true,
		state.targetID,
	); err != nil {
		return 0, err
	}
	pinned, err := inspectNetworkNamespaceFD(ctx, state.deps, state.owner.policy, state.netnsFD)
	if err != nil {
		return 0, err
	}
	if pinned != state.namespaceID {
		return 0, errors.New("created namespace descriptor changed")
	}
	currentFD, err := state.deps.openAt2(ctx, state.parentFD, string(state.key.vmID), &unix.OpenHow{
		Flags: namespaceLeafInspectFlags, Resolve: namespaceResolveBeneath,
	})
	if err != nil {
		return 0, err
	}
	currentNamespace, namespaceErr := inspectNetworkNamespaceFD(ctx, state.deps, state.owner.policy, currentFD)
	if namespaceErr == nil && currentNamespace == state.namespaceID {
		closeErr := closeConsumedNamespaceFD(ctx, state.deps, &currentFD)
		return namespaceUnmountObservationMounted, closeErr
	}
	placeholderErr := verifyNamespacePlaceholder(ctx, state.deps, currentFD, state.owner.policy, state.targetID)
	closeErr := closeConsumedNamespaceFD(ctx, state.deps, &currentFD)
	if placeholderErr == nil {
		return namespaceUnmountObservationUnmounted, closeErr
	}
	return 0, errors.Join(
		errors.New("reserved namespace name is neither the retained mount nor original placeholder"),
		namespaceErr,
		placeholderErr,
		closeErr,
	)
}

func namespaceEffectCreateError(
	key namespaceKey,
	stage string,
	cause error,
	outcome namespaceCreateOutcome,
) error {
	if cause == nil {
		cause = errors.New("failed to " + stage)
	}
	domainErr := errs.AsDomainError(cause)
	if domainErr != nil {
		result := *domainErr
		result.Details = namespaceCreateDetails(domainErr.Details, outcome)
		result.Err = cause
		return &result
	}
	return errs.WrapMsg(
		errs.CodeNetworkCreateFailed,
		"failed to "+stage,
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(string(key.vmID)),
		errs.WithDetails(namespaceCreateDetails(nil, outcome)),
	)
}

func appendNamespaceEffectCreateCleanupError(
	key namespaceKey,
	primary error,
	cleanup error,
	outcome namespaceCreateOutcome,
) error {
	if cleanup == nil {
		return primary
	}
	outcome.cleanupRequired = true
	return namespaceEffectCreateError(
		key,
		"release network namespace creation resources",
		errors.Join(primary, cleanup),
		outcome,
	)
}

func markNamespaceThreadFatal(
	key namespaceKey,
	primary error,
	cause error,
	outcome namespaceCreateOutcome,
) *errs.DomainError {
	outcome.cleanupRequired = true
	details := namespaceCreateDetails(nil, outcome)
	details["network_worker_retired"] = true
	details["network_thread_contaminated"] = true
	return errs.WrapMsg(
		errs.CodeNetworkCreateFailed,
		"network namespace thread restoration failed",
		errors.Join(primary, cause),
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(string(key.vmID)),
		errs.WithDetails(details),
	)
}

func namespaceCreateDetails(existing map[string]any, outcome namespaceCreateOutcome) map[string]any {
	details := maps.Clone(existing)
	if details == nil {
		details = make(map[string]any, 6)
	}
	details["cleanup_required"] = outcome.cleanupRequired
	details["namespace_created"] = outcome.created
	details["namespace_pinned"] = outcome.pinned
	details["namespace_published"] = outcome.published
	details["publication_verified"] = outcome.verified
	details["host_namespace_restored"] = outcome.restored
	return details
}

func namespaceEffectRemoveError(
	vmID string,
	message string,
	cause error,
	outcome namespaceRemoveOutcome,
) *errs.DomainError {
	domainErr := errs.AsDomainError(cause)
	if domainErr != nil {
		result := *domainErr
		result.Details = namespaceRemoveDetails(domainErr.Details, outcome)
		result.Err = cause
		return &result
	}
	options := []errs.ErrorOption{
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(namespaceRemoveDetails(nil, outcome)),
	}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	if cause == nil {
		return errs.New(errs.CodeNetworkRemoveFailed, message, options...)
	}
	return errs.WrapMsg(errs.CodeNetworkRemoveFailed, message, cause, options...)
}

func namespaceRemoveDetails(existing map[string]any, outcome namespaceRemoveOutcome) map[string]any {
	details := maps.Clone(existing)
	if details == nil {
		details = make(map[string]any, 5)
	}
	details["cleanup_required"] = outcome.cleanupRequired
	details["namespace_published"] = outcome.published
	details["namespace_unmounted"] = outcome.unmounted
	details["authority_target_removed"] = outcome.authorityTargetRemoved
	details["unmount_outcome_unknown"] = outcome.unmountOutcomeUnknown
	return details
}
