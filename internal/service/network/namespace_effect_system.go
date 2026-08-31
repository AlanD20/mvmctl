package network

import (
	"context"
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

type namespaceEffectDeps struct {
	runWorker          func(context.Context, func(context.Context) error) error
	lockThread         func(context.Context) error
	unlockThread       func(context.Context) error
	open               func(context.Context, string, int, uint32) (int, error)
	openAt2            func(context.Context, int, string, *unix.OpenHow) (int, error)
	fstat              func(context.Context, int, *unix.Stat_t) error
	statx              func(context.Context, int, *unix.Statx_t) error
	fstatfs            func(context.Context, int, *unix.Statfs_t) error
	fsync              func(context.Context, int) error
	ioctlNamespaceType func(context.Context, int) (int, error)
	unshare            func(context.Context, int) error
	setns              func(context.Context, int, int) error
	openTree           func(context.Context, int, string, uint) (int, error)
	moveMount          func(context.Context, int, string, int, string, int) error
	unmount            func(context.Context, string, int) error
	close              func(context.Context, int) error
}

func validateNamespaceEffectDeps(deps namespaceEffectDeps) error {
	if deps.runWorker == nil || deps.lockThread == nil || deps.unlockThread == nil || deps.open == nil ||
		deps.openAt2 == nil ||
		deps.fstat == nil || deps.statx == nil || deps.fstatfs == nil || deps.fsync == nil ||
		deps.ioctlNamespaceType == nil ||
		deps.unshare == nil || deps.setns == nil || deps.openTree == nil || deps.moveMount == nil ||
		deps.unmount == nil || deps.close == nil {
		return errors.New("network namespace dependencies are incomplete")
	}
	return nil
}

func systemNamespaceEffectDeps() namespaceEffectDeps {
	return namespaceEffectDeps{
		runWorker: func(ctx context.Context, worker func(context.Context) error) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			if worker == nil {
				return errors.New("network namespace worker is required")
			}
			var workerErr error
			done := make(chan struct{})
			// CRITICAL: Network-namespace switching is confined to this disposable goroutine. If its callback returns
			// while still locked after an unsafe restoration, the runtime retires that OS thread as the goroutine exits.
			go func() {
				defer close(done)
				workerErr = worker(ctx)
			}()
			<-done
			return workerErr
		},
		lockThread: func(ctx context.Context) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			runtime.LockOSThread()
			if err := ctx.Err(); err != nil {
				runtime.UnlockOSThread()
				return err
			}
			return nil
		},
		unlockThread: func(ctx context.Context) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			runtime.UnlockOSThread()
			return nil
		},
		open: func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return -1, err
			}
			return unix.Open(path, flags, mode)
		},
		openAt2: func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return -1, err
			}
			return unix.Openat2(parentFD, name, how)
		},
		fstat: func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Fstat(fd, stat)
		},
		statx: func(ctx context.Context, fd int, stat *unix.Statx_t) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Statx(fd, "", namespaceTargetStatxFlags, namespaceTargetStatxMask, stat)
		},
		fstatfs: func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Fstatfs(fd, stat)
		},
		fsync: func(ctx context.Context, fd int) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Fsync(fd)
		},
		ioctlNamespaceType: func(ctx context.Context, fd int) (int, error) {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return 0, err
			}
			return unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
		},
		unshare: func(ctx context.Context, flags int) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Unshare(flags)
		},
		setns: func(ctx context.Context, fd, namespaceType int) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Setns(fd, namespaceType)
		},
		openTree: func(ctx context.Context, fd int, path string, flags uint) (int, error) {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return -1, err
			}
			return unix.OpenTree(fd, path, flags)
		},
		moveMount: func(
			ctx context.Context,
			fromFD int,
			fromPath string,
			toFD int,
			toPath string,
			flags int,
		) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.MoveMount(fromFD, fromPath, toFD, toPath, flags)
		},
		unmount: func(ctx context.Context, path string, flags int) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Unmount(path, flags)
		},
		close: func(ctx context.Context, fd int) error {
			if err := namespaceEffectContextErr(ctx); err != nil {
				return err
			}
			return unix.Close(fd)
		},
	}
}

func namespaceEffectContextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("network namespace context is required")
	}
	return ctx.Err()
}
