package firewall

import (
	"context"
	"errors"
	"sync"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	kernelObservationCurrentNetworkNamespacePath = "/proc/self/ns/net"
	kernelObservationNamespaceOpenFlags          = unix.O_RDONLY | unix.O_CLOEXEC
	kernelObservationNamespaceMinimumFD          = 3
)

type kernelObservationNamespaceAuthority interface {
	retainAfterGeneration(
		context.Context,
		generationNetworkAuthority,
		generationPersistentLease,
	) (*kernelObservationNamespaceLease, error)
}

type kernelObservationNamespaceDeps struct {
	open               func(context.Context, string, int, uint32) (int, error)
	duplicateCloexec   func(context.Context, int, int) (int, error)
	fstat              func(context.Context, int, *unix.Stat_t) error
	fstatfs            func(context.Context, int, *unix.Statfs_t) error
	ioctlNamespaceType func(context.Context, int) (int, error)
	getFDFlags         func(context.Context, int) (int, error)
	close              func(context.Context, int) error
	dial               func(context.Context, int, *netlink.Config) (kernelObservationNetlinkConnection, error)
}

type retainedKernelObservationNamespaceAuthority struct {
	deps            kernelObservationNamespaceDeps
	applicationDeps kernelApplicationNamespaceDeps
}

type kernelObservationNamespaceLease struct {
	state *kernelObservationNamespaceLeaseState
}

type kernelObservationNamespaceLeaseState struct {
	// mu guards the retained namespace descriptor and every lifecycle field below.
	mu              sync.Mutex
	deps            kernelObservationNamespaceDeps
	applicationDeps kernelApplicationNamespaceDeps
	fd              int
	identity        generationNamespaceIdentity
	active          bool
	released        bool
	releaseErr      error
}

type retainedKernelObservationTransportFactory struct {
	state *kernelObservationNamespaceLeaseState
}

func newKernelObservationNamespaceAuthority() kernelObservationNamespaceAuthority {
	deps := realKernelObservationNamespaceDeps()
	return newKernelObservationNamespaceAuthorityWithAllDeps(deps, realKernelApplicationNamespaceDeps(deps))
}

func newKernelObservationNamespaceAuthorityWithDeps(
	deps kernelObservationNamespaceDeps,
) *retainedKernelObservationNamespaceAuthority {
	return newKernelObservationNamespaceAuthorityWithAllDeps(deps, realKernelApplicationNamespaceDeps(deps))
}

func newKernelObservationNamespaceAuthorityWithAllDeps(
	deps kernelObservationNamespaceDeps,
	applicationDeps kernelApplicationNamespaceDeps,
) *retainedKernelObservationNamespaceAuthority {
	return &retainedKernelObservationNamespaceAuthority{deps: deps, applicationDeps: applicationDeps}
}

func (authority *retainedKernelObservationNamespaceAuthority) retainAfterGeneration(
	ctx context.Context,
	network generationNetworkAuthority,
	generation generationPersistentLease,
) (_ *kernelObservationNamespaceLease, returnErr error) {
	if authority == nil || network == nil || generation == nil {
		return nil, generationAuthorityError(
			"network and generation authority are required before kernel observation",
			nil,
			nil,
		)
	}
	if ctx == nil {
		return nil, generationAuthorityError("kernel observation namespace context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("retain kernel observation namespace", err, nil)
	}
	if err := validateKernelObservationNamespaceDeps(authority.deps); err != nil {
		return nil, err
	}

	// CRITICAL: Both earlier lock-order capabilities are resolved before opening namespace state. Neither one exposes
	// a descriptor, and their independently retained identities must agree.
	networkIdentity, err := network.firewallGenerationNamespace(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("verify network authority for kernel observation", err)
	}
	if networkIdentity.device == 0 || networkIdentity.inode == 0 {
		return nil, generationAuthorityError("network authority namespace identity is invalid", nil, nil)
	}
	record, err := generation.admittedRecord()
	if err != nil {
		return nil, preserveGenerationAuthorityError("read admitted generation for kernel observation", err)
	}
	if err := validateGenerationRecord(record); err != nil {
		return nil, err
	}
	if record.binding.namespace != networkIdentity {
		return nil, generationAuthorityError(
			"network and generation authority namespace identities differ",
			nil,
			nil,
		)
	}
	return authority.retainExpectedNamespace(ctx, networkIdentity)
}

// retainExpectedNamespace pins the root service's exact host namespace for bootstrap inspection.
// CRITICAL: The caller must hold real network mutation authority before invoking this method.
func (authority *retainedKernelObservationNamespaceAuthority) retainExpectedNamespace(
	ctx context.Context,
	networkIdentity generationNamespaceIdentity,
) (_ *kernelObservationNamespaceLease, returnErr error) {
	if err := validateKernelObservationNamespaceDeps(authority.deps); err != nil {
		return nil, err
	}

	openedFD := -1
	retainedFD := -1
	var err error
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		returnErr = closeRejectedKernelObservationNamespace(
			cleanupCtx,
			authority.deps,
			&retainedFD,
			returnErr,
			"close rejected retained kernel observation namespace",
		)
		returnErr = closeRejectedKernelObservationNamespace(
			cleanupCtx,
			authority.deps,
			&openedFD,
			returnErr,
			"close rejected current kernel observation namespace",
		)
	}()

	openedFD, err = authority.deps.open(
		ctx,
		kernelObservationCurrentNetworkNamespacePath,
		kernelObservationNamespaceOpenFlags,
		0,
	)
	if err != nil {
		return nil, preserveGenerationAuthorityError("open current kernel observation namespace", err)
	}
	if openedFD < kernelObservationNamespaceMinimumFD {
		return nil, generationAuthorityError("current kernel observation namespace descriptor is unsafe", nil, nil)
	}
	openedIdentity, err := inspectKernelObservationNamespace(ctx, authority.deps, openedFD)
	if err != nil {
		return nil, preserveGenerationAuthorityError("inspect current kernel observation namespace", err)
	}
	if openedIdentity != networkIdentity {
		return nil, generationAuthorityError(
			"current kernel observation namespace does not match retained authority",
			nil,
			nil,
		)
	}

	retainedFD, err = authority.deps.duplicateCloexec(
		ctx,
		openedFD,
		kernelObservationNamespaceMinimumFD,
	)
	if err != nil {
		return nil, preserveGenerationAuthorityError("duplicate kernel observation namespace", err)
	}
	if retainedFD < kernelObservationNamespaceMinimumFD || retainedFD == openedFD {
		return nil, generationAuthorityError("retained kernel observation namespace descriptor is unsafe", nil, nil)
	}
	retainedIdentity, err := inspectKernelObservationNamespace(ctx, authority.deps, retainedFD)
	if err != nil {
		return nil, preserveGenerationAuthorityError("inspect retained kernel observation namespace", err)
	}
	if retainedIdentity != networkIdentity {
		return nil, generationAuthorityError(
			"retained kernel observation namespace identity changed",
			nil,
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("retain kernel observation namespace", err, nil)
	}

	if err := closeKernelObservationNamespaceFD(context.WithoutCancel(ctx), authority.deps, &openedFD); err != nil {
		return nil, generationCleanupError("close current kernel observation namespace", err)
	}
	lease := &kernelObservationNamespaceLease{state: &kernelObservationNamespaceLeaseState{
		deps: authority.deps, applicationDeps: authority.applicationDeps,
		fd: retainedFD, identity: retainedIdentity, active: true,
	}}
	retainedFD = -1
	return lease, nil
}

func validateKernelObservationNamespaceDeps(deps kernelObservationNamespaceDeps) error {
	if deps.open == nil || deps.duplicateCloexec == nil || deps.fstat == nil || deps.fstatfs == nil ||
		deps.ioctlNamespaceType == nil || deps.getFDFlags == nil || deps.close == nil || deps.dial == nil {
		return generationAuthorityError("kernel observation namespace dependencies are incomplete", nil, nil)
	}
	return nil
}

func inspectKernelObservationNamespace(
	ctx context.Context,
	deps kernelObservationNamespaceDeps,
	fd int,
) (generationNamespaceIdentity, error) {
	if fd < kernelObservationNamespaceMinimumFD {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"kernel observation namespace descriptor is unsafe",
			nil,
			nil,
		)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return generationNamespaceIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Dev == 0 || stat.Ino == 0 {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"kernel observation namespace descriptor has invalid file identity",
			nil,
			nil,
		)
	}
	var filesystem unix.Statfs_t
	if err := deps.fstatfs(ctx, fd, &filesystem); err != nil {
		return generationNamespaceIdentity{}, err
	}
	if filesystem.Type != unix.NSFS_MAGIC {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"kernel observation namespace descriptor is not on nsfs",
			nil,
			nil,
		)
	}
	namespaceType, err := deps.ioctlNamespaceType(ctx, fd)
	if err != nil {
		return generationNamespaceIdentity{}, err
	}
	if namespaceType != unix.CLONE_NEWNET {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"kernel observation namespace descriptor is not a network namespace",
			nil,
			nil,
		)
	}
	flags, err := deps.getFDFlags(ctx, fd)
	if err != nil {
		return generationNamespaceIdentity{}, err
	}
	if flags != unix.FD_CLOEXEC {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"kernel observation namespace descriptor is not close-on-exec",
			nil,
			nil,
		)
	}
	return generationNamespaceIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func (lease *kernelObservationNamespaceLease) snapshotTransportFactory() (
	kernelObservationSnapshotTransportFactory,
	error,
) {
	if lease == nil || lease.state == nil {
		return nil, generationAuthorityError("kernel observation namespace lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released || state.fd < kernelObservationNamespaceMinimumFD ||
		state.identity.device == 0 || state.identity.inode == 0 {
		return nil, generationAuthorityError("kernel observation namespace lease is released", nil, nil)
	}
	return &retainedKernelObservationTransportFactory{state: state}, nil
}

func (factory *retainedKernelObservationTransportFactory) newTransport(
	ctx context.Context,
	budget *kernelObservationReceiveBudget,
) (kernelObservationSnapshotTransport, error) {
	if factory == nil || factory.state == nil || budget == nil {
		return nil, generationAuthorityError("retained kernel observation transport factory is unavailable", nil, nil)
	}
	if ctx == nil {
		return nil, generationAuthorityError("kernel observation transport context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("open namespace-bound kernel observation transport", err, nil)
	}
	state := factory.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released || state.fd < kernelObservationNamespaceMinimumFD {
		return nil, generationAuthorityError("kernel observation namespace lease is released", nil, nil)
	}
	identity, err := inspectKernelObservationNamespace(ctx, state.deps, state.fd)
	if err != nil {
		return nil, preserveGenerationAuthorityError("recheck retained kernel observation namespace", err)
	}
	if identity != state.identity {
		return nil, generationAuthorityError("retained kernel observation namespace identity changed", nil, nil)
	}
	connection, err := state.deps.dial(ctx, unix.NETLINK_NETFILTER, &netlink.Config{
		NetNS:  state.fd,
		Strict: true,
	})
	var transport *kernelObservationTransport
	if connection != nil {
		transport = newKernelObservationTransport(connection, nil)
		transport.budget = budget
	}
	if err != nil {
		return transport, preserveGenerationAuthorityError("open namespace-bound kernel observation transport", err)
	}
	if transport == nil {
		return nil, generationAuthorityError(
			"namespace-bound kernel observation dial returned no connection",
			nil,
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return transport, generationAuthorityError("open namespace-bound kernel observation transport", err, nil)
	}
	return transport, nil
}

// Release closes the retained namespace descriptor exactly once, ignores cancellation, and caches the first result
// across copied lease values.
func (lease *kernelObservationNamespaceLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	var primary error
	if ctx == nil {
		primary = generationAuthorityError("kernel observation namespace release context is required", nil, nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if state.active {
		closeErr := closeKernelObservationNamespaceFD(cleanupCtx, state.deps, &state.fd)
		if closeErr != nil {
			primary = appendGenerationCleanupError(
				primary,
				"close retained kernel observation namespace",
				closeErr,
			)
		}
	}
	state.identity = generationNamespaceIdentity{}
	state.active = false
	state.released = true
	state.releaseErr = primary
	return state.releaseErr
}

func closeRejectedKernelObservationNamespace(
	ctx context.Context,
	deps kernelObservationNamespaceDeps,
	fd *int,
	primary error,
	stage string,
) error {
	if closeErr := closeKernelObservationNamespaceFD(ctx, deps, fd); closeErr != nil {
		return appendGenerationCleanupError(primary, stage, closeErr)
	}
	return primary
}

func closeKernelObservationNamespaceFD(
	ctx context.Context,
	deps kernelObservationNamespaceDeps,
	fd *int,
) error {
	if fd == nil || *fd < 0 {
		return nil
	}
	value := *fd
	*fd = -1
	return deps.close(context.WithoutCancel(ctx), value)
}

func realKernelObservationNamespaceDeps() kernelObservationNamespaceDeps {
	return kernelObservationNamespaceDeps{
		open: func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return -1, err
			}
			return unix.Open(path, flags, mode)
		},
		duplicateCloexec: func(ctx context.Context, fd int, minimum int) (int, error) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return -1, err
			}
			return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, minimum)
		},
		fstat: func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return err
			}
			return unix.Fstat(fd, stat)
		},
		fstatfs: func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return err
			}
			return unix.Fstatfs(fd, stat)
		},
		ioctlNamespaceType: func(ctx context.Context, fd int) (int, error) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return 0, err
			}
			return unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
		},
		getFDFlags: func(ctx context.Context, fd int) (int, error) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return 0, err
			}
			return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		},
		close: func(ctx context.Context, fd int) error {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return err
			}
			return unix.Close(fd)
		},
		dial: func(
			ctx context.Context,
			family int,
			config *netlink.Config,
		) (kernelObservationNetlinkConnection, error) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return nil, err
			}
			return netlink.Dial(family, config)
		},
	}
}

func kernelObservationNamespaceContextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("kernel observation namespace context is required")
	}
	return ctx.Err()
}
