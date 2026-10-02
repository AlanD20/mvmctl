package jailer

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	blockedLaunchChallengeBytes = 32
	blockedLaunchReleaseByte    = byte(0xa5)
)

type blockedLaunchGate struct {
	state *blockedLaunchGateState
}

type blockedLaunchGateState struct {
	// mu guards the descriptor and the complete challenge/release lifecycle below.
	mu         sync.Mutex
	deps       blockedLaunchGateDeps
	fd         int
	status     blockedLaunchGateStatus
	releaseErr error
}

type blockedLaunchGateStatus uint8

const (
	blockedLaunchGateOpen blockedLaunchGateStatus = iota + 1
	blockedLaunchGateChallenged
	blockedLaunchGateReleased
	blockedLaunchGateClosed
)

type blockedLaunchGateDeps struct {
	random          func(context.Context, []byte) error
	descriptorFlags func(context.Context, int) (int, error)
	readFull        func(context.Context, int, []byte) error
	writeFull       func(context.Context, int, []byte) error
	shutdown        func(context.Context, int) error
	close           func(context.Context, int) error
}

func newBlockedLaunchGate(
	ctx context.Context,
	deps blockedLaunchGateDeps,
	fd int,
) (*blockedLaunchGate, error) {
	if ctx == nil {
		return nil, fmt.Errorf("blocked launch gate context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deps.random == nil || deps.descriptorFlags == nil || deps.readFull == nil || deps.writeFull == nil ||
		deps.shutdown == nil || deps.close == nil {
		return nil, fmt.Errorf("blocked launch gate dependencies are incomplete")
	}
	if fd < 0 {
		return nil, fmt.Errorf("blocked launch gate descriptor is invalid")
	}
	flags, err := deps.descriptorFlags(ctx, fd)
	if err != nil {
		return nil, fmt.Errorf("inspect blocked launch gate flags: %w", err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return nil, fmt.Errorf("blocked launch gate does not have close-on-exec")
	}
	return &blockedLaunchGate{state: &blockedLaunchGateState{
		deps: deps, fd: fd, status: blockedLaunchGateOpen,
	}}, nil
}

func (gate *blockedLaunchGate) challengeAndAcknowledge(ctx context.Context) error {
	if gate == nil || gate.state == nil {
		return fmt.Errorf("blocked launch gate is not active")
	}
	state := gate.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status != blockedLaunchGateOpen || state.fd < 0 {
		return fmt.Errorf("blocked launch gate is not awaiting a challenge")
	}
	challenge := make([]byte, blockedLaunchChallengeBytes)
	if err := state.deps.random(ctx, challenge); err != nil {
		return fmt.Errorf("create blocked launch challenge: %w", err)
	}
	if err := state.deps.writeFull(ctx, state.fd, challenge); err != nil {
		return fmt.Errorf("send blocked launch challenge: %w", err)
	}
	acknowledgement := make([]byte, blockedLaunchChallengeBytes)
	if err := state.deps.readFull(ctx, state.fd, acknowledgement); err != nil {
		return fmt.Errorf("read blocked launch acknowledgement: %w", err)
	}
	if subtle.ConstantTimeCompare(challenge, acknowledgement) != 1 {
		return fmt.Errorf("blocked launch acknowledgement does not match its challenge")
	}
	state.status = blockedLaunchGateChallenged
	return nil
}

func (gate *blockedLaunchGate) release(ctx context.Context) error {
	if gate == nil || gate.state == nil {
		return fmt.Errorf("blocked launch gate is not active")
	}
	state := gate.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status != blockedLaunchGateChallenged || state.fd < 0 {
		return fmt.Errorf("blocked launch gate is not ready for release")
	}
	if err := state.deps.writeFull(ctx, state.fd, []byte{blockedLaunchReleaseByte}); err != nil {
		return fmt.Errorf("release blocked launch child: %w", err)
	}
	state.status = blockedLaunchGateReleased
	return nil
}

func (gate *blockedLaunchGate) isReleased() bool {
	if gate == nil || gate.state == nil {
		return false
	}
	gate.state.mu.Lock()
	defer gate.state.mu.Unlock()
	return gate.state.status == blockedLaunchGateReleased
}

func (gate *blockedLaunchGate) Release(ctx context.Context) error {
	if gate == nil || gate.state == nil {
		return nil
	}
	state := gate.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status == blockedLaunchGateClosed {
		return state.releaseErr
	}
	cleanupCtx := processCleanupContext(ctx)
	var cleanupErr error
	if state.status != blockedLaunchGateReleased {
		cleanupErr = errors.Join(cleanupErr, state.deps.shutdown(cleanupCtx, state.fd))
	}
	cleanupErr = errors.Join(cleanupErr, state.deps.close(cleanupCtx, state.fd))
	state.fd = -1
	state.status = blockedLaunchGateClosed
	state.releaseErr = cleanupErr
	return state.releaseErr
}

// awaitBlockedLaunchRelease is the child-side EOF-fails-closed protocol. It returns false unless the parent proves
// liveness with a fresh challenge and then sends the one exact release byte.
// Authentication is bounded separately; preparation waits only while the authenticated parent owns the gate.
func awaitBlockedLaunchRelease(
	ctx context.Context,
	authenticationCtx context.Context,
	deps blockedLaunchGateDeps,
	fd int,
) (bool, error) {
	challenge := make([]byte, blockedLaunchChallengeBytes)
	if err := deps.readFull(authenticationCtx, fd, challenge); err != nil {
		if errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ECONNRESET) || errors.Is(err, unix.EBADF) {
			return false, nil
		}
		return false, err
	}
	if err := deps.writeFull(authenticationCtx, fd, challenge); err != nil {
		return false, err
	}
	release := []byte{0}
	if err := deps.readFull(ctx, fd, release); err != nil {
		if errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ECONNRESET) || errors.Is(err, unix.EBADF) {
			return false, nil
		}
		return false, err
	}
	if release[0] != blockedLaunchReleaseByte {
		return false, fmt.Errorf("blocked launch release byte is invalid")
	}
	return true, nil
}
