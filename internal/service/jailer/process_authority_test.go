package jailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

func TestBlockedLaunchProcessesAdmitExactCapabilitiesAndTransferOnce(t *testing.T) {
	t.Parallel()

	fixture := newBlockedProcessFixture(true)
	processes, err := fixture.admit(t.Context())
	require.NoError(t, err)

	state, got, err := processes.lockForRegistration(t.Context(), testVMID)
	require.NoError(t, err)
	if diff := cmp.Diff(testLaunchProcessLocators(true), got, cmp.AllowUnexported(
		launchProcessLocators{}, instanceProcessLocator{}, instanceLaunchRelay{},
	)); diff != "" {
		t.Errorf("launch locators mismatch (-want +got):\n%s", diff)
	}
	target := transferBlockedLaunchProcessesLocked(state)
	state.mu.Unlock()

	_, _, err = processes.lockForRegistration(t.Context(), testVMID)
	require.Error(t, err)
	assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
	require.NoError(t, processes.Release(t.Context()))
	require.NoError(t, target.Release(t.Context()))
	if diff := cmp.Diff([]string{"receiver", "relay:101", "firecracker:100"}, fixture.eventsSnapshot()); diff != "" {
		t.Errorf("cleanup order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: descriptor-owning values are routinely copied into commit results. Concurrent release must never retry
// a close against a descriptor number that the kernel could already have reused.
func TestBlockedLaunchProcessCopiesConcurrentlyReleaseOnceAndCacheFirstError(t *testing.T) {
	t.Parallel()

	fixture := newBlockedProcessFixture(true)
	closeErr := errors.New("injected relay pidfd close failure")
	fixture.closeErr[101] = closeErr
	processes, err := fixture.admit(t.Context())
	require.NoError(t, err)
	copied := *processes

	const callers = 16
	start := make(chan struct{})
	results := make(chan error, callers)
	for index := range callers {
		candidate := processes
		if index%2 == 1 {
			candidate = &copied
		}
		go func() {
			<-start
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			results <- candidate.Release(ctx)
		}()
	}
	close(start)
	first := <-results
	require.Error(t, first)
	assert.ErrorIs(t, first, closeErr)
	for range callers - 1 {
		assert.Same(t, first, <-results)
	}
	domainErr := errs.AsDomainError(first)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["blocked_launch_process_cleanup_failed"])
	if diff := cmp.Diff([]string{"receiver", "relay:101", "firecracker:100"}, fixture.eventsSnapshot()); diff != "" {
		t.Errorf("cleanup order mismatch (-want +got):\n%s", diff)
	}
}

func TestBlockedLaunchProcessesRejectAmbiguousOrDeadSourcesAndCloseOwnedAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*blockedProcessFixture){
		"shared PID": func(fixture *blockedProcessFixture) {
			fixture.locators[101] = fixture.locators[100]
		},
		"shared pidfd": func(fixture *blockedProcessFixture) {
			fixture.relay.pidfd = fixture.firecracker.pidfd
		},
		"missing CLOEXEC": func(fixture *blockedProcessFixture) {
			fixture.flags[fixture.firecracker.pidfd] = 0
		},
		"exited process": func(fixture *blockedProcessFixture) {
			fixture.alive[fixture.firecracker.pidfd] = false
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newBlockedProcessFixture(true)
			mutate(fixture)

			processes, err := fixture.admit(t.Context())
			assert.Nil(t, processes)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.NotEmpty(t, domainErr.Code)
			assert.NotEmpty(t, fixture.eventsSnapshot())
		})
	}
}

func TestBlockedLaunchProcessAdmissionClosesPIDFDOnEveryRejectedPath(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*blockedProcessFixture){
		"flags": func(fixture *blockedProcessFixture) {
			fixture.flags[fixture.firecracker.pidfd] = 0
		},
		"dead": func(fixture *blockedProcessFixture) {
			fixture.alive[fixture.firecracker.pidfd] = false
		},
		"challenge": func(fixture *blockedProcessFixture) {
			fixture.firecracker.gate.state.deps.writeFull = func(context.Context, int, []byte) error {
				return errors.New("challenge failed")
			}
		},
		"locator": func(fixture *blockedProcessFixture) {
			delete(fixture.locators, fixture.firecracker.pidfd)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newBlockedProcessFixture(false)
			mutate(fixture)
			processes, err := fixture.admit(t.Context())
			assert.Nil(t, processes)
			require.Error(t, err)
			assert.Equal(t, 1, countProcessFixtureEvent(fixture.eventsSnapshot(), "firecracker:100"))
		})
	}
}

func TestBlockedLaunchProcessesTerminateWaitReapAndVerifyBeforeRelease(t *testing.T) {
	t.Parallel()

	fixture := newBlockedProcessFixture(true)
	processes, err := fixture.admit(t.Context())
	require.NoError(t, err)
	require.NoError(t, processes.terminateAndReap(t.Context()))
	require.NoError(t, processes.Release(t.Context()))

	assert.Equal(t, []string{
		"signal:101", "reap:101", "signal:100", "reap:100",
		"receiver", "relay:101", "firecracker:100",
	}, fixture.eventsSnapshot())
}

func TestBlockedLaunchProcessWaitFailureRetainsPIDFDAuthorityForRetry(t *testing.T) {
	t.Parallel()

	fixture := newBlockedProcessFixture(false)
	processes, err := fixture.admit(t.Context())
	require.NoError(t, err)
	waitErr := context.DeadlineExceeded
	calls := 0
	processes.state.deps.waitReapPIDFD = func(_ context.Context, fd int) error {
		calls++
		fixture.record(fmt.Sprintf("reap:%d", fd))
		if calls == 1 {
			return waitErr
		}
		fixture.alive[fd] = false
		return nil
	}

	first := processes.terminateAndReap(t.Context())
	require.Error(t, first)
	assert.ErrorIs(t, first, waitErr)
	domain := errs.AsDomainError(first)
	require.NotNil(t, domain)
	assert.Equal(t, errs.ClassRetryable, domain.Class)
	assert.Equal(t, []string{"signal:100", "reap:100"}, fixture.eventsSnapshot())

	require.NoError(t, processes.terminateAndReap(t.Context()))
	require.NoError(t, processes.Release(t.Context()))
	assert.Equal(t, 1, countProcessFixtureEvent(fixture.eventsSnapshot(), "signal:100"))
	assert.Equal(t, 1, countProcessFixtureEvent(fixture.eventsSnapshot(), "firecracker:100"))
}

func countProcessFixtureEvent(events []string, target string) int {
	count := 0
	for _, event := range events {
		if event == target {
			count++
		}
	}
	return count
}

type blockedProcessFixture struct {
	mu          sync.Mutex
	firecracker blockedLaunchProcessSource
	relay       *blockedLaunchProcessSource
	locators    map[int]instanceProcessLocator
	flags       map[int]int
	alive       map[int]bool
	closeErr    map[int]error
	events      []string
}

func newBlockedProcessFixture(relayPresent bool) *blockedProcessFixture {
	fixture := &blockedProcessFixture{
		firecracker: blockedLaunchProcessSource{pidfd: 100},
		locators: map[int]instanceProcessLocator{
			100: testLaunchProcessLocators(false).process,
			101: testLaunchProcessLocators(true).relay.locator,
		},
		flags:    map[int]int{100: unix.FD_CLOEXEC, 101: unix.FD_CLOEXEC},
		alive:    map[int]bool{100: true, 101: true},
		closeErr: make(map[int]error),
	}
	fixture.firecracker.gate = newTestBlockedLaunchGate(200)
	if relayPresent {
		relay := blockedLaunchProcessSource{pidfd: 101, gate: newTestBlockedLaunchGate(201)}
		fixture.relay = &relay
	}
	return fixture
}

func (fixture *blockedProcessFixture) admit(ctx context.Context) (*blockedLaunchProcesses, error) {
	return newBlockedLaunchProcesses(
		ctx,
		blockedLaunchProcessDeps{
			pidfdAlive:      func(_ context.Context, fd int) (bool, error) { return fixture.alive[fd], nil },
			descriptorFlags: func(_ context.Context, fd int) (int, error) { return fixture.flags[fd], nil },
			deriveLocator: func(_ context.Context, fd int) (instanceProcessLocator, error) {
				return fixture.locators[fd], nil
			},
			pinnedExecutableReinspect: func(
				context.Context,
				*system.PinnedExecutable,
			) (system.PinnedExecutableIdentity, error) {
				return system.PinnedExecutableIdentity{
					Device: 1, Inode: 2, SizeBytes: 3, Mode: unix.S_IFREG | 0755,
					UID: 0, GID: 0, Links: 1, SHA256: testRelayHash,
				}, nil
			},
			releasePinnedExecutable: func(context.Context, *system.PinnedExecutable) error {
				fixture.record("receiver")
				return nil
			},
			terminatePIDFD: func(_ context.Context, fd int) error {
				fixture.record(fmt.Sprintf("signal:%d", fd))
				return nil
			},
			waitReapPIDFD: func(_ context.Context, fd int) error {
				fixture.record(fmt.Sprintf("reap:%d", fd))
				fixture.alive[fd] = false
				return nil
			},
			close: func(_ context.Context, fd int) error {
				role := "firecracker"
				if fd == 101 {
					role = "relay"
				}
				fixture.record(fmt.Sprintf("%s:%d", role, fd))
				return fixture.closeErr[fd]
			},
		},
		testVMID,
		fixture.firecracker,
		fixture.relay,
		&system.PinnedExecutable{},
	)
}

func newTestBlockedLaunchGate(fd int) *blockedLaunchGate {
	var challenge []byte
	gate, err := newBlockedLaunchGate(
		context.Background(),
		blockedLaunchGateDeps{
			random: func(_ context.Context, target []byte) error {
				for index := range target {
					target[index] = byte(index + 1)
				}
				return nil
			},
			descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
			writeFull: func(_ context.Context, _ int, source []byte) error {
				if len(source) == blockedLaunchChallengeBytes {
					challenge = append([]byte{}, source...)
				}
				return nil
			},
			readFull: func(_ context.Context, _ int, target []byte) error {
				copy(target, challenge)
				return nil
			},
			shutdown: func(context.Context, int) error { return nil },
			close:    func(context.Context, int) error { return nil },
		},
		fd,
	)
	if err != nil {
		panic(err)
	}
	return gate
}

func (fixture *blockedProcessFixture) record(event string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.events = append(fixture.events, event)
}

func (fixture *blockedProcessFixture) eventsSnapshot() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string{}, fixture.events...)
}
