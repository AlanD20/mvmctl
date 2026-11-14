package network

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// openRecoveryNamespacePin takes an independent nsfs pin through procfs. A
// descriptor opened on the published bind mount would keep that exact mount
// busy when recovered cleanup tries a non-lazy unmount.
func openRecoveryNamespacePin(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
	mountedFD int,
	want namespaceIdentity,
) (int, error) {
	if ctx == nil || mountedFD < 0 || !validNamespaceIdentity(want) {
		return -1, errors.New("recovered namespace pin requires an exact mounted identity")
	}
	independentFD := -1
	err := deps.runWorker(ctx, func(workerCtx context.Context) (workerErr error) {
		if err := deps.lockThread(workerCtx); err != nil {
			return err
		}
		hostFD := -1
		var hostIdentity namespaceIdentity
		switched := false
		defer func() {
			cleanup := context.WithoutCancel(workerCtx)
			safeToUnlock := true
			if switched {
				var restoreErr error
				safeToUnlock, restoreErr = restoreHostNetworkNamespace(cleanup, deps, policy, hostFD, hostIdentity)
				workerErr = errors.Join(workerErr, restoreErr)
				if !safeToUnlock {
					workerErr = errors.Join(
						workerErr,
						errors.New("recovery worker did not restore host network namespace"),
					)
				}
			}
			workerErr = errors.Join(workerErr, closeConsumedNamespaceFD(cleanup, deps, &hostFD))
			if safeToUnlock {
				workerErr = errors.Join(workerErr, deps.unlockThread(cleanup))
			}
			// An unsafe worker remains locked; exiting its disposable goroutine
			// retires that OS thread instead of returning it to Go's host pool.
		}()
		var err error
		hostFD, err = deps.open(workerCtx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
		if err != nil {
			return err
		}
		hostIdentity, err = inspectNetworkNamespaceFD(workerCtx, deps, policy, hostFD)
		if err != nil {
			return err
		}
		if err := deps.setns(workerCtx, mountedFD, unix.CLONE_NEWNET); err != nil {
			return err
		}
		switched = true
		independentFD, err = deps.open(workerCtx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
		if err != nil {
			return err
		}
		got, err := inspectNetworkNamespaceFD(workerCtx, deps, policy, independentFD)
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("independent recovery namespace pin differs from published mount")
		}
		return nil
	})
	if err != nil {
		return -1, errors.Join(err, closeConsumedNamespaceFD(context.WithoutCancel(ctx), deps, &independentFD))
	}
	if independentFD < 0 {
		return -1, errors.New("recovery worker returned no independent namespace pin")
	}
	return independentFD, nil
}
