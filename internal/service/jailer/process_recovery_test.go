package jailer

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestBeginFreshRecoveryCleanupVerifiesFullIdentityAndTerminatesThroughPIDFDs(t *testing.T) {
	t.Parallel()

	record, fake := activeRecoveryTestRecord(t)
	authority, recoveryDeps, identityDeps, events := newActiveRecoveryTestAuthority(t, record, fake)

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		testInstanceCaller(),
		testVMID,
		recoveryDeps,
		identityDeps,
	)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.Equal(t, []string{"open:4242", "signal:700", "reap:700", "close:700"}, events.snapshot())
}

func TestBeginFreshRecoveryCleanupRejectsFullIdentityMismatchWithoutSignal(t *testing.T) {
	t.Parallel()

	record, fake := activeRecoveryTestRecord(t)
	wrongNamespace := fake.stats[111][0]
	wrongNamespace.Ino++
	fake.stats[111] = []unix.Stat_t{wrongNamespace}
	authority, recoveryDeps, identityDeps, events := newActiveRecoveryTestAuthority(t, record, fake)

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		testInstanceCaller(),
		testVMID,
		recoveryDeps,
		identityDeps,
	)
	assert.Nil(t, cleanup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "full active identity")
	assert.Equal(t, []string{"open:4242", "close:700"}, events.snapshot())
}

func TestBeginFreshRecoveryCleanupBootMismatchCleansWithoutOpeningOrSignalingPIDFD(t *testing.T) {
	t.Parallel()

	record, fake := activeRecoveryTestRecord(t)
	authority, recoveryDeps, identityDeps, events := newActiveRecoveryTestAuthority(t, record, fake)
	identityDeps.readBootID = func(context.Context) (string, error) { return processOtherBootID, nil }

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		testInstanceCaller(),
		testVMID,
		recoveryDeps,
		identityDeps,
	)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.Empty(t, events.snapshot())
}

func activeRecoveryTestRecord(t *testing.T) (instanceRecord, *fakeProcessIdentityDeps) {
	t.Helper()
	fixture := newProcessIdentityFixture(t, false)
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	record := active.snapshot()
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))

	fake := fixture.fake
	fake.bootIndex = 0
	fake.bootIDs = []string{processTestHostBootID, processTestHostBootID}
	fake.stats[113] = repeatProcessStat(executableStat(
		int32(record.active.executable.device),
		record.active.executable.inode,
		fake.contents[203],
	), 2)
	fake.stats[116] = repeatProcessStat(fake.stats[113][0], 3)
	fake.contents[116] = append([]byte{}, fake.contents[203]...)
	return record, fake
}

func newActiveRecoveryTestAuthority(
	t *testing.T,
	record instanceRecord,
	fake *fakeProcessIdentityDeps,
) (*instanceAuthority, processRecoveryDeps, processIdentityVerifierDeps, *recoveryEventLog) {
	t.Helper()
	root := prepareInstanceAuthorityTestRoot(t)
	writeTestAuthorityRecord(t, root, record)
	authority := newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	)
	events := &recoveryEventLog{}
	alive := true
	recoveryDeps := processRecoveryDeps{
		pidfdOpen: func(_ context.Context, pid int) (int, error) {
			events.add(fmt.Sprintf("open:%d", pid))
			if pid != processTestFirecrackerPID {
				return -1, fmt.Errorf("unexpected PID %d", pid)
			}
			return 700, nil
		},
		pidfdAlive:      func(context.Context, int) (bool, error) { return alive, nil },
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator: func(context.Context, int) (instanceProcessLocator, error) {
			return record.launch.process, nil
		},
		terminatePIDFD: func(_ context.Context, fd int) error {
			events.add(fmt.Sprintf("signal:%d", fd))
			return nil
		},
		waitReapPIDFD: func(_ context.Context, fd int) error {
			events.add(fmt.Sprintf("reap:%d", fd))
			alive = false
			return nil
		},
		close: func(_ context.Context, fd int) error {
			events.add(fmt.Sprintf("close:%d", fd))
			return nil
		},
	}
	identityDeps := fake.deps()
	return authority, recoveryDeps, identityDeps, events
}

type recoveryEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *recoveryEventLog) add(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *recoveryEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string{}, log.events...)
}
