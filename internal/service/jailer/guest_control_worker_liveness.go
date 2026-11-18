package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// VerifyRunning observes only the exact root-captured process, never a PID display mirror.
func (lease *GuestControlWorkerLease) VerifyRunning(ctx context.Context) (_ bool, returnErr error) {
	if lease == nil || lease.state == nil {
		return false, instanceValidationError("worker authority is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return false, err
	}
	if !state.store.found || state.store.record.Process == nil || state.store.record.Phase == "stopped" {
		return false, nil
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, state.store.record)
	if err != nil {
		return false, err
	}
	if absent {
		return false, nil
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	return true, nil
}
