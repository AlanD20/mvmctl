// Package hostadmission serializes administrator changes against privileged requests.
// Layer: Lib. It owns only the fixed root-controlled host admission lock and state.
package hostadmission

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const lockWaitLimit = 30 * time.Second

// OperationLease retains shared admission throughout one privileged request and cleanup.
// THREAD-SAFE: copies share the same release state.
type OperationLease struct{ state *leaseState }

// AdministratorLease excludes every privileged request while host state is changed.
// THREAD-SAFE: copies share the same release and publication state.
type AdministratorLease struct{ state *leaseState }

type leaseState struct {
	// mu serializes publication with release and caches the first release result.
	mu         sync.Mutex
	directory  *os.File
	lock       *os.File
	released   bool
	releaseErr error
}

// AcquireOperation waits for shared admission and then requires enabled durable state.
// CRITICAL: caller authorization must be rechecked after this acquisition.
func AcquireOperation(ctx context.Context) (*OperationLease, error) {
	state, err := acquire(ctx, false)
	if err != nil {
		return nil, err
	}
	enabled, err := readEnabled(state.directory)
	if err != nil || !enabled {
		if err == nil {
			err = errors.New("host admission is disabled; explicit administrator initialization is required")
		}
		return nil, admissionError("admit privileged operation", errors.Join(err, state.release()))
	}
	return &OperationLease{state: state}, nil
}

// AcquireAdministrator obtains exclusive admission without enabling operator requests.
// Only this explicit administrator route may create the fixed host state directory.
func AcquireAdministrator(ctx context.Context) (*AdministratorLease, error) {
	state, err := acquire(ctx, true)
	if err != nil {
		return nil, err
	}
	return &AdministratorLease{state: state}, nil
}

func acquire(ctx context.Context, administrator bool) (*leaseState, error) {
	if ctx == nil || os.Geteuid() != 0 {
		return nil, admissionError("acquire host admission", errors.New("root identity and context are required"))
	}
	directory, err := openDirectory(ctx, administrator)
	if err != nil {
		return nil, admissionError("open host admission directory", err)
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if administrator {
		flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CREAT
	}
	fd, err := unix.Openat(int(directory.Fd()), "lock", flags, 0600)
	if err != nil {
		return nil, admissionError("open host admission lock", errors.Join(err, directory.Close()))
	}
	lock := os.NewFile(uintptr(fd), "host admission lock")
	state := &leaseState{directory: directory, lock: lock}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&07777 != 0600 ||
		stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size != 0) {
		err = errors.New("unsafe host admission lock metadata")
	}
	if err != nil {
		return nil, admissionError("inspect host admission lock", errors.Join(err, state.release()))
	}
	waitCtx, cancel := context.WithTimeout(ctx, lockWaitLimit)
	defer cancel()
	mode := unix.LOCK_SH
	if administrator {
		mode = unix.LOCK_EX
	}
	for {
		if err := waitCtx.Err(); err != nil {
			return nil, admissionError("wait for host admission", errors.Join(err, state.release()))
		}
		err := unix.Flock(fd, mode|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, admissionError("lock host admission", errors.Join(err, state.release()))
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return state, nil
}

// Verify proves the exclusive administrator lease has not been released.
func (lease *AdministratorLease) Verify(ctx context.Context) error {
	if ctx == nil {
		return admissionError("verify host admission", errors.New("context is required"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease == nil || lease.state == nil {
		return admissionError("verify host admission", errors.New("administrator lease is required"))
	}
	lease.state.mu.Lock()
	defer lease.state.mu.Unlock()
	if lease.state.released {
		return admissionError("verify host admission", errors.New("administrator lease is released"))
	}
	return nil
}

// Enable publishes enabled state only while exclusive administrator authority is held.
func (lease *AdministratorLease) Enable(ctx context.Context) error { return lease.publish(ctx, true) }

// Disable durably refuses queued and subsequent operations before reset mutations.
func (lease *AdministratorLease) Disable(ctx context.Context) error { return lease.publish(ctx, false) }

func (lease *AdministratorLease) publish(ctx context.Context, enabled bool) error {
	if lease == nil || lease.state == nil {
		return admissionError("publish host admission", errors.New("administrator lease is required"))
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return admissionError("publish host admission", errors.New("administrator lease is released"))
	}
	return writeEnabled(ctx, state.directory, enabled)
}

// Release relinquishes shared admission once, including after cancellation.
func (lease *OperationLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	return lease.state.release()
}

// Release relinquishes exclusive admission once without changing the durable gate.
func (lease *AdministratorLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	return lease.state.release()
}

func (state *leaseState) release() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.released = true
	state.releaseErr = errors.Join(state.lock.Close(), state.directory.Close())
	if state.releaseErr != nil {
		state.releaseErr = admissionError("release host admission", state.releaseErr)
	}
	return state.releaseErr
}

func admissionError(operation string, cause error) error {
	return errs.WrapMsg(errs.CodePrivilegeRequired, operation, cause)
}
