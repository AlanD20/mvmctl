package jailer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

func TestVerifyRecoveredActiveIdentityReturnsFullyVerifiedFirecrackerObservation(t *testing.T) {
	t.Parallel()

	record, fake := activeRecoveryTestRecord(t)
	observed, err := verifyRecoveredActiveIdentity(t.Context(), fake.deps(), record)
	require.NoError(t, err)
	want := processObservation{
		startTimeTicks:      record.launch.process.startTimeTicks,
		userIDs:             record.active.userIDs,
		groupIDs:            record.active.groupIDs,
		supplementaryGroups: append([]uint32{}, record.active.supplementaryGroups...),
		cgroupPath:          infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + record.vmID,
	}
	if diff := cmp.Diff(
		want,
		observed,
		cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
	); diff != "" {
		t.Errorf("recovered active Firecracker observation mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Returning metadata before the recorded executable digest is proved would let cgroup recovery reuse an
// observation from an incompletely admitted active identity.
func TestVerifyRecoveredActiveIdentityReturnsNoObservationUntilDigestProofCompletes(t *testing.T) {
	t.Parallel()

	record, fake := activeRecoveryTestRecord(t)
	record.active.executable.manifestSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	observed, err := verifyRecoveredActiveIdentity(t.Context(), fake.deps(), record)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "digest does not match active identity")
	if diff := cmp.Diff(
		processObservation{},
		observed,
		cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
	); diff != "" {
		t.Errorf("rejected recovered observation mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: The returned Firecracker metadata is reusable only after a present relay's complete recorded identity
// and executable digest have also been verified.
func TestVerifyRecoveredActiveIdentityReturnsNoObservationUntilRelayProofCompletes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*instanceRecord, *fakeProcessIdentityDeps)
		want   string
	}{
		"identity": {
			mutate: func(_ *instanceRecord, fake *fakeProcessIdentityDeps) {
				wrongNamespace := fake.stats[121][0]
				wrongNamespace.Ino++
				fake.stats[121] = []unix.Stat_t{wrongNamespace}
			},
			want: "recovered relay does not match full active identity",
		},
		"digest": {
			mutate: func(record *instanceRecord, _ *fakeProcessIdentityDeps) {
				record.active.relay.executable.sha256 =
					"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			},
			want: "recovered relay digest does not match active identity",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			record, fake := activeRecoveryTestRecordWithRelay(t, true)
			tc.mutate(&record, fake)

			observed, err := verifyRecoveredActiveIdentity(t.Context(), fake.deps(), record)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			if diff := cmp.Diff(
				processObservation{},
				observed,
				cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
			); diff != "" {
				t.Errorf("relay-rejected recovered observation mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReopenedRecordedProcessesObserveLaunchingFirecrackerMetadataOnly(t *testing.T) {
	t.Parallel()

	processes, recovery := newLaunchingRecoveryProcessTestAuthority()
	observationDeps := launchingRecoveryObservationDeps(recovery)

	observed, err := processes.observeLaunchingFirecracker(t.Context(), observationDeps)
	require.NoError(t, err)
	if diff := cmp.Diff(
		launchingRecoveryProcessObservation(),
		observed,
		cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
	); diff != "" {
		t.Errorf("launching Firecracker observation mismatch (-want +got):\n%s", diff)
	}
	wantEvents := []string{
		"alive:100",
		"locator:100",
		"metadata:stat",
		"metadata:status",
		"metadata:cgroup",
		"alive:100",
		"locator:100",
		"proc-close:110",
	}
	if diff := cmp.Diff(wantEvents, recovery.events); diff != "" {
		t.Errorf("launching Firecracker proof order mismatch (-want +got):\n%s", diff)
	}
	assert.NotContains(t, recovery.aliveFDs, processTestRelayPIDFD)
	assert.NotContains(t, recovery.locatorFDs, processTestRelayPIDFD)
	require.NoError(t, processes.Release(t.Context()))
}

// Rationale: A process-directory close failure is part of the observation operation. The caller must not receive
// reusable metadata together with that error, and the original close cause must remain discoverable.
func TestReopenedRecordedProcessesObserveLaunchingFirecrackerCloseFailureReturnsNoObservation(t *testing.T) {
	t.Parallel()

	processes, probe := newLaunchingRecoveryProcessTestAuthority()
	closeErr := errors.New("injected process directory close failure")
	probe.observationCloseErr = closeErr

	observed, err := processes.observeLaunchingFirecracker(
		t.Context(),
		launchingRecoveryObservationDeps(probe),
	)
	require.ErrorIs(t, err, closeErr)
	assert.Contains(t, err.Error(), "close temporary process identity descriptor")
	if diff := cmp.Diff(
		processObservation{},
		observed,
		cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
	); diff != "" {
		t.Errorf("close-rejected launching observation mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{processTestFirecrackerDirFD}, probe.observationClosedFDs); diff != "" {
		t.Errorf("closed process observation descriptors mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, processes.Release(t.Context()))
}

// Rationale: A /proc directory reopened by numeric PID is not authority. The retained pidfd proof must bracket the
// metadata read, and the stat start tick must independently match the recorded locator.
func TestReopenedRecordedProcessesObserveLaunchingFirecrackerRejectsIdentityDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*launchingRecoveryProcessProbe)
		want      string
	}{
		"start tick mismatch": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.observedStartTimeTicks++
			},
			want: "does not match its launch locator",
		},
		"pidfd dead before observation": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.alive = []bool{false}
			},
			want: "exited during identity verification",
		},
		"pidfd locator drift before observation": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.locators = []instanceProcessLocator{{pid: 9999, startTimeTicks: 1}}
			},
			want: "pidfd locator does not match",
		},
		"pidfd dead after observation": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.alive = []bool{true, false}
			},
			want: "exited during identity verification",
		},
		"pidfd locator drift after observation": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.locators = []instanceProcessLocator{
					processTestLocator(),
					{pid: 9999, startTimeTicks: 1},
				}
			},
			want: "pidfd locator does not match",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			processes, probe := newLaunchingRecoveryProcessTestAuthority()
			tc.configure(probe)

			observed, err := processes.observeLaunchingFirecracker(
				t.Context(),
				launchingRecoveryObservationDeps(probe),
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			if diff := cmp.Diff(
				processObservation{},
				observed,
				cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
			); diff != "" {
				t.Errorf("rejected launching observation mismatch (-want +got):\n%s", diff)
			}
			assert.NotContains(t, probe.aliveFDs, processTestRelayPIDFD)
			assert.NotContains(t, probe.locatorFDs, processTestRelayPIDFD)
			require.NoError(t, processes.Release(t.Context()))
		})
	}
}

// Rationale: A retained pidfd must not be closed while an unlocked observation callback is using it. The explicit
// verification gate makes Release and another verification fail closed instead of deadlocking or racing descriptor reuse.
func TestReopenedRecordedProcessesObservationCallbacksRunOutsideLockAndRejectConcurrentRelease(t *testing.T) {
	t.Parallel()

	processes, probe := newLaunchingRecoveryProcessTestAuthority()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	callbackEntered := make(chan struct{})
	resumeCallback := make(chan struct{})
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(resumeCallback) }) }
	defer resume()
	probe.onAlive = func() {
		close(callbackEntered)
		select {
		case <-resumeCallback:
		case <-ctx.Done():
		}
	}
	type observationResult struct {
		observation processObservation
		err         error
	}
	result := make(chan observationResult, 1)
	go func() {
		observed, err := processes.observeLaunchingFirecracker(
			ctx,
			launchingRecoveryObservationDeps(probe),
		)
		result <- observationResult{observation: observed, err: err}
	}()
	select {
	case <-callbackEntered:
	case <-ctx.Done():
		t.Fatalf("wait for observation callback: %v", ctx.Err())
	}
	if !processes.state.mu.TryLock() {
		t.Fatal("pidfd callback ran while recovery mutex was held")
	}
	processes.state.mu.Unlock()
	_, nestedObservationErr := processes.observeLaunchingFirecracker(
		ctx,
		launchingRecoveryObservationDeps(probe),
	)
	releaseErr := processes.Release(ctx)
	resume()
	var completed observationResult
	select {
	case completed = <-result:
	case <-ctx.Done():
		t.Fatalf("wait for completed observation: %v", ctx.Err())
	}

	observed, err := completed.observation, completed.err
	require.NoError(t, err)
	if diff := cmp.Diff(
		launchingRecoveryProcessObservation(),
		observed,
		cmp.AllowUnexported(processObservation{}, processCredentialIdentity{}),
	); diff != "" {
		t.Errorf("launching Firecracker observation mismatch (-want +got):\n%s", diff)
	}
	require.Error(t, nestedObservationErr)
	assert.Contains(t, nestedObservationErr.Error(), "verification is in progress")
	require.Error(t, releaseErr)
	assert.Contains(t, releaseErr.Error(), "verification is in progress")
	assert.Empty(t, probe.closedFDs)
	require.NoError(t, processes.Release(t.Context()))
	if diff := cmp.Diff([]int{processTestRelayPIDFD, processTestFirecrackerPIDFD}, probe.closedFDs); diff != "" {
		t.Errorf("released recovered process descriptors mismatch (-want +got):\n%s", diff)
	}
}

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
	if diff := cmp.Diff([]string{"open:4242", "signal:700", "reap:700", "close:700"}, events.snapshot()); diff != "" {
		t.Errorf("fresh recovery event order mismatch (-want +got):\n%s", diff)
	}
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
	if diff := cmp.Diff([]string{"open:4242", "close:700"}, events.snapshot()); diff != "" {
		t.Errorf("rejected fresh recovery event order mismatch (-want +got):\n%s", diff)
	}
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
	return activeRecoveryTestRecordWithRelay(t, false)
}

func activeRecoveryTestRecordWithRelay(
	t *testing.T,
	relayPresent bool,
) (instanceRecord, *fakeProcessIdentityDeps) {
	t.Helper()
	fixture := newProcessIdentityFixture(t, relayPresent)
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
	if relayPresent {
		relayExecutable := fake.stats[126][0]
		fake.stats[126] = repeatProcessStat(relayExecutable, 3)
		fake.contents[126] = []byte("exact pinned receiver executable")
	}
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

type launchingRecoveryProcessProbe struct {
	alive                  []bool
	locators               []instanceProcessLocator
	events                 []string
	aliveFDs               []int
	locatorFDs             []int
	closedFDs              []int
	observationClosedFDs   []int
	aliveIndex             int
	locatorIndex           int
	observedStartTimeTicks uint64
	observationCloseErr    error
	onAlive                func()
}

// newLaunchingRecoveryProcessTestAuthority retains both process roles while making any accidental relay proof visible.
func newLaunchingRecoveryProcessTestAuthority() (*reopenedRecordedProcesses, *launchingRecoveryProcessProbe) {
	probe := &launchingRecoveryProcessProbe{
		alive:                  []bool{true, true},
		locators:               []instanceProcessLocator{processTestLocator(), processTestLocator()},
		observedStartTimeTicks: processTestFirecrackerTicks,
	}
	state := &reopenedRecordedProcessesState{vmID: testVMID}
	state.deps = processRecoveryDeps{
		pidfdAlive: func(_ context.Context, fd int) (bool, error) {
			probe.events = append(probe.events, fmt.Sprintf("alive:%d", fd))
			probe.aliveFDs = append(probe.aliveFDs, fd)
			if probe.onAlive != nil {
				onAlive := probe.onAlive
				probe.onAlive = nil
				onAlive()
			}
			if probe.aliveIndex >= len(probe.alive) {
				return false, io.EOF
			}
			alive := probe.alive[probe.aliveIndex]
			probe.aliveIndex++
			return alive, nil
		},
		deriveLocator: func(_ context.Context, fd int) (instanceProcessLocator, error) {
			probe.events = append(probe.events, fmt.Sprintf("locator:%d", fd))
			probe.locatorFDs = append(probe.locatorFDs, fd)
			if probe.locatorIndex >= len(probe.locators) {
				return instanceProcessLocator{}, io.EOF
			}
			locator := probe.locators[probe.locatorIndex]
			probe.locatorIndex++
			return locator, nil
		},
		close: func(_ context.Context, fd int) error {
			probe.closedFDs = append(probe.closedFDs, fd)
			return nil
		},
	}
	state.retained = []recoveredRecordedProcess{
		{role: "Firecracker", locator: processTestLocator(), pidfd: processTestFirecrackerPIDFD},
		{
			role: "relay",
			locator: instanceProcessLocator{
				pid:            processTestRelayPID,
				startTimeTicks: processTestRelayTicks,
			},
			pidfd: processTestRelayPIDFD,
		},
	}
	return &reopenedRecordedProcesses{state: state}, probe
}

func launchingRecoveryObservationDeps(probe *launchingRecoveryProcessProbe) processIdentityVerifierDeps {
	return processIdentityVerifierDeps{
		openProcessDirectory: func(_ context.Context, pid int) (int, error) {
			if pid != processTestFirecrackerPID {
				return -1, fmt.Errorf("unexpected process directory PID %d", pid)
			}
			return processTestFirecrackerDirFD, nil
		},
		readProcessMetadata: func(
			_ context.Context,
			directoryFD int,
			kind processMetadataKind,
			_ int,
		) ([]byte, error) {
			if directoryFD != processTestFirecrackerDirFD {
				return nil, fmt.Errorf("unexpected process directory fd %d", directoryFD)
			}
			switch kind {
			case processStatMetadata:
				probe.events = append(probe.events, "metadata:stat")
				return []byte(testProcessStat(
					processTestFirecrackerPID,
					probe.observedStartTimeTicks,
					"S",
				)), nil
			case processStatusMetadata:
				probe.events = append(probe.events, "metadata:status")
				return []byte(testProcessStatus(testAuthorityUID, testAuthorityGID, "")), nil
			case processCgroupMetadata:
				probe.events = append(probe.events, "metadata:cgroup")
				return []byte("0::/" + infra.JailerCgroupParent + "/" + testVMID + "\n"), nil
			default:
				return nil, fmt.Errorf("unexpected process metadata kind %d", kind)
			}
		},
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		close: func(_ context.Context, fd int) error {
			probe.events = append(probe.events, fmt.Sprintf("proc-close:%d", fd))
			probe.observationClosedFDs = append(probe.observationClosedFDs, fd)
			return probe.observationCloseErr
		},
	}
}

func launchingRecoveryProcessObservation() processObservation {
	return processObservation{
		startTimeTicks: processTestFirecrackerTicks,
		userIDs: processCredentialIdentity{
			real: testAuthorityUID, effective: testAuthorityUID,
			saved: testAuthorityUID, filesystem: testAuthorityUID,
		},
		groupIDs: processCredentialIdentity{
			real: testAuthorityGID, effective: testAuthorityGID,
			saved: testAuthorityGID, filesystem: testAuthorityGID,
		},
		supplementaryGroups: []uint32{},
		cgroupPath:          infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + testVMID,
	}
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
