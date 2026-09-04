package firewall

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const kernelApplicationCurrentNetworkNamespacePath = "/proc/thread-self/ns/net"

type kernelApplicationNamespaceWorker func(context.Context) (kernelApplicationConnection, error)

type kernelApplicationNamespaceDeps struct {
	runWorker            func(context.Context, kernelApplicationNamespaceWorker) (kernelApplicationConnection, error)
	lockThread           func(context.Context) error
	unlockThread         func(context.Context) error
	openCurrentNamespace func(context.Context) (int, error)
	inspectNamespace     func(context.Context, int) (generationNamespaceIdentity, error)
	setNamespace         func(context.Context, int) error
	newConnection        func(context.Context) (kernelApplicationConnection, error)
	closeNamespace       func(context.Context, int) error
}

type googleNFTablesApplicationConnection struct {
	connection *nftables.Conn
}

func (connection *googleNFTablesApplicationConnection) deleteTable(table *nftables.Table) {
	connection.connection.DelTable(table)
}

func (connection *googleNFTablesApplicationConnection) addTable(table *nftables.Table) {
	connection.connection.AddTable(table)
}

func (connection *googleNFTablesApplicationConnection) addChain(chain *nftables.Chain) {
	connection.connection.AddChain(chain)
}

func (connection *googleNFTablesApplicationConnection) addRule(rule compiledRule) {
	connection.connection.AddRule(rule.rule)
}

func (connection *googleNFTablesApplicationConnection) flush() error {
	return connection.connection.Flush()
}

func (connection *googleNFTablesApplicationConnection) close() error {
	return connection.connection.CloseLasting()
}

func (lease *kernelObservationNamespaceLease) newApplicationConnection(
	ctx context.Context,
) (kernelApplicationConnection, error) {
	if lease == nil || lease.state == nil {
		return nil, generationAuthorityError("kernel application namespace lease is unavailable", nil, nil)
	}
	if ctx == nil {
		return nil, generationAuthorityError("kernel application context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("open kernel application connection", err, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released || state.fd < kernelObservationNamespaceMinimumFD ||
		state.identity.device == 0 || state.identity.inode == 0 {
		return nil, generationAuthorityError("kernel application namespace lease is released", nil, nil)
	}
	if err := validateKernelApplicationNamespaceDeps(state.applicationDeps); err != nil {
		return nil, err
	}
	identity, err := state.applicationDeps.inspectNamespace(ctx, state.fd)
	if err != nil {
		return nil, preserveGenerationAuthorityError("recheck retained kernel application namespace", err)
	}
	if identity != state.identity {
		return nil, generationAuthorityError("retained kernel application namespace identity changed", nil, nil)
	}
	connection, err := state.applicationDeps.runWorker(ctx, func(workerCtx context.Context) (
		kernelApplicationConnection,
		error,
	) {
		return openKernelApplicationConnectionOnWorker(workerCtx, state.applicationDeps, state.fd, state.identity)
	})
	if err != nil {
		return nil, preserveGenerationAuthorityError("open exact namespace-bound kernel application connection", err)
	}
	if connection == nil {
		return nil, generationAuthorityError("kernel application connection is unavailable", nil, nil)
	}
	return connection, nil
}

func openKernelApplicationConnectionOnWorker(
	ctx context.Context,
	deps kernelApplicationNamespaceDeps,
	targetFD int,
	targetIdentity generationNamespaceIdentity,
) (returnConnection kernelApplicationConnection, returnErr error) {
	threadLocked := false
	mustRestore := false
	restoreSafe := false
	hostFD := -1
	currentFD := -1
	hostIdentity := generationNamespaceIdentity{}
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if mustRestore {
			var restoreErr error
			restoreSafe, restoreErr = restoreKernelApplicationNamespace(cleanupCtx, deps, hostFD, hostIdentity)
			returnErr = errors.Join(returnErr, restoreErr)
		}
		returnErr = errors.Join(
			returnErr,
			closeKernelApplicationNamespace(cleanupCtx, deps, &currentFD),
			closeKernelApplicationNamespace(cleanupCtx, deps, &hostFD),
		)
		if threadLocked && (!mustRestore || restoreSafe) {
			returnErr = errors.Join(returnErr, deps.unlockThread(cleanupCtx))
		}
		if returnErr != nil && returnConnection != nil {
			returnErr = errors.Join(returnErr, returnConnection.close())
			returnConnection = nil
		}
	}()

	if err := deps.lockThread(ctx); err != nil {
		return nil, fmt.Errorf("lock kernel application namespace worker: %w", err)
	}
	threadLocked = true
	var err error
	hostFD, err = deps.openCurrentNamespace(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin current kernel application namespace: %w", err)
	}
	hostIdentity, err = deps.inspectNamespace(ctx, hostFD)
	if err != nil {
		return nil, fmt.Errorf("inspect current kernel application namespace: %w", err)
	}
	if hostIdentity != targetIdentity {
		return nil, errors.New("current kernel application namespace does not match retained authority")
	}
	targetBefore, err := deps.inspectNamespace(ctx, targetFD)
	if err != nil {
		return nil, fmt.Errorf("inspect target kernel application namespace: %w", err)
	}
	if targetBefore != targetIdentity {
		return nil, errors.New("target kernel application namespace identity changed")
	}
	if err := deps.setNamespace(ctx, targetFD); err != nil {
		return nil, fmt.Errorf("enter retained kernel application namespace: %w", err)
	}
	mustRestore = true
	currentFD, err = deps.openCurrentNamespace(ctx)
	if err != nil {
		return nil, fmt.Errorf("open entered kernel application namespace: %w", err)
	}
	currentIdentity, err := deps.inspectNamespace(ctx, currentFD)
	if err != nil {
		return nil, fmt.Errorf("inspect entered kernel application namespace: %w", err)
	}
	if currentIdentity != targetIdentity {
		return nil, errors.New("kernel application worker entered the wrong network namespace")
	}
	returnConnection, err = deps.newConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("create lasting kernel application connection: %w", err)
	}
	if returnConnection == nil {
		return nil, errors.New("lasting kernel application constructor returned no connection")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return returnConnection, nil
}

func restoreKernelApplicationNamespace(
	ctx context.Context,
	deps kernelApplicationNamespaceDeps,
	hostFD int,
	want generationNamespaceIdentity,
) (safe bool, returnErr error) {
	if hostFD < kernelObservationNamespaceMinimumFD || want.device == 0 || want.inode == 0 {
		return false, errors.New("original kernel application namespace pin is incomplete")
	}
	if err := deps.setNamespace(ctx, hostFD); err != nil {
		return false, fmt.Errorf("restore original kernel application namespace: %w", err)
	}
	currentFD, err := deps.openCurrentNamespace(ctx)
	if err != nil {
		return false, fmt.Errorf("verify restored kernel application namespace: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeKernelApplicationNamespace(ctx, deps, &currentFD))
	}()
	got, err := deps.inspectNamespace(ctx, currentFD)
	if err != nil {
		return false, fmt.Errorf("verify restored kernel application namespace: %w", err)
	}
	if got != want {
		return false, errors.New("restored kernel application namespace does not match pinned host namespace")
	}
	return true, nil
}

func validateKernelApplicationNamespaceDeps(deps kernelApplicationNamespaceDeps) error {
	if deps.runWorker == nil || deps.lockThread == nil || deps.unlockThread == nil ||
		deps.openCurrentNamespace == nil || deps.inspectNamespace == nil || deps.setNamespace == nil ||
		deps.newConnection == nil || deps.closeNamespace == nil {
		return generationAuthorityError("kernel application namespace dependencies are incomplete", nil, nil)
	}
	return nil
}

func closeKernelApplicationNamespace(
	ctx context.Context,
	deps kernelApplicationNamespaceDeps,
	fd *int,
) error {
	if fd == nil || *fd < 0 {
		return nil
	}
	value := *fd
	*fd = -1
	return deps.closeNamespace(context.WithoutCancel(ctx), value)
}

func realKernelApplicationNamespaceDeps(
	observation kernelObservationNamespaceDeps,
) kernelApplicationNamespaceDeps {
	return kernelApplicationNamespaceDeps{
		runWorker: func(ctx context.Context, worker kernelApplicationNamespaceWorker) (
			kernelApplicationConnection,
			error,
		) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return nil, err
			}
			if worker == nil {
				return nil, errors.New("kernel application namespace worker is required")
			}
			type workerResult struct {
				connection kernelApplicationConnection
				err        error
			}
			result := make(chan workerResult, 1)
			// CRITICAL: A worker whose namespace restoration cannot be proven exits while still locked, causing the Go
			// runtime to retire that contaminated OS thread instead of returning it to the scheduler.
			go func() {
				connection, err := worker(ctx)
				result <- workerResult{connection: connection, err: err}
			}()
			completed := <-result
			return completed.connection, completed.err
		},
		lockThread: func(ctx context.Context) error {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return err
			}
			runtime.LockOSThread()
			if err := ctx.Err(); err != nil {
				runtime.UnlockOSThread()
				return err
			}
			return nil
		},
		unlockThread: func(context.Context) error {
			runtime.UnlockOSThread()
			return nil
		},
		openCurrentNamespace: func(ctx context.Context) (int, error) {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return -1, err
			}
			return unix.Open(
				kernelApplicationCurrentNetworkNamespacePath,
				kernelObservationNamespaceOpenFlags,
				0,
			)
		},
		inspectNamespace: func(ctx context.Context, fd int) (generationNamespaceIdentity, error) {
			return inspectKernelObservationNamespace(ctx, observation, fd)
		},
		setNamespace: func(ctx context.Context, fd int) error {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return err
			}
			return unix.Setns(fd, unix.CLONE_NEWNET)
		},
		newConnection: newGoogleNFTablesApplicationConnection,
		closeNamespace: func(ctx context.Context, fd int) error {
			if err := kernelObservationNamespaceContextErr(ctx); err != nil {
				return err
			}
			return unix.Close(fd)
		},
	}
}

func newGoogleNFTablesApplicationConnection(ctx context.Context) (kernelApplicationConnection, error) {
	if err := kernelObservationNamespaceContextErr(ctx); err != nil {
		return nil, err
	}
	options := []nftables.ConnOption{nftables.AsLasting()}
	if deadline, ok := ctx.Deadline(); ok {
		options = append(options, nftables.WithSockOptions(func(connection *netlink.Conn) error {
			return connection.SetDeadline(deadline)
		}))
	}
	connection, err := nftables.New(options...)
	if err != nil {
		return nil, err
	}
	return &googleNFTablesApplicationConnection{connection: connection}, nil
}

var _ kernelApplicationConnection = (*googleNFTablesApplicationConnection)(nil)
