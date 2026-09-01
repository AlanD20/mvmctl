package jailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const (
	vmExecSessionSourceID         = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	vmExecSessionDestinationID    = "11111111111111111111111111111111"
	vmExecSessionDestinationPID   = 5252
	vmExecSessionDestinationTicks = uint64(765432)
)

func TestVMExecSessionAuthorityRetainsBothProcessAuthoritiesUntilRelease(t *testing.T) {
	t.Parallel()

	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)

	lease, err := fixture.authority.reserve(
		t.Context(),
		testInstanceCaller(),
		vmExecSessionSourceID,
		vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Equal(t, []string{
		"index:acquire",
		"vm:" + vmExecSessionDestinationID + ":acquire",
		"vm:" + vmExecSessionSourceID + ":acquire",
		"index:release",
		"pidfd:" + vmExecSessionSourceID + ":open",
		"pidfd:" + vmExecSessionDestinationID + ":open",
		"capacity:reserve",
	}, fixture.events.snapshot())

	// Rationale: closing either pidfd before capacity admission completes would reduce the active-record proof to
	// replayable numeric PID data. Both process capabilities remain retained until the combined lease is released.
	assert.Zero(t, fixture.processCloseCount())
	require.NoError(t, lease.Release(context.Background()))
	assert.Equal(t, int32(2), fixture.processCloseCount())
}

type vmExecSessionAuthorityFixture struct {
	authority         *vmExecSessionAuthority
	events            *vmExecSessionEventLog
	tree              *testInstanceAuthorityTree
	root              string
	capacity          *vmExecCapacityFixture
	identity          *fakeProcessIdentityDeps
	sourceRecord      instanceRecord
	destinationRecord instanceRecord
	processCloses     atomic.Int32
	sourceKey         authorityfs.VMKey
	destinationKey    authorityfs.VMKey
	aliveMu           sync.Mutex
	aliveCalls        map[int]int
	failAliveCall     map[int]int
}

func newVMExecSessionAuthorityFixture(
	t *testing.T,
	sourceVMID string,
	destinationVMID string,
) *vmExecSessionAuthorityFixture {
	t.Helper()

	record, fake := activeRecoveryTestRecord(t)
	sourceRecord := cloneInstanceRecord(record)
	sourceRecord.vmID = sourceVMID
	destinationRecord := cloneInstanceRecord(record)
	destinationRecord.vmID = destinationVMID
	destinationRecord.launch.process = instanceProcessLocator{
		pid: vmExecSessionDestinationPID, startTimeTicks: vmExecSessionDestinationTicks,
	}

	root := prepareInstanceAuthorityTestRoot(t)
	writeTestAuthorityRecord(t, root, sourceRecord)
	writeTestAuthorityRecord(t, root, destinationRecord)
	events := &vmExecSessionEventLog{}
	tree := newTestInstanceAuthorityTree()
	sourceKey, err := authorityfs.NewVMKey(sourceVMID)
	require.NoError(t, err)
	destinationKey, err := authorityfs.NewVMKey(destinationVMID)
	require.NoError(t, err)
	tree.onAcquire = func(_ context.Context, description string) {
		if description == "index" {
			events.add("index:acquire")
		}
	}
	tree.onRelease = func(_ context.Context, description string) error {
		if description == "index" {
			events.add("index:release")
		}
		return nil
	}
	tree.onVMAcquire = func(_ context.Context, key authorityfs.VMKey) {
		events.add(
			"vm:" + vmExecSessionVMIDForKey(key, sourceKey, sourceVMID, destinationKey, destinationVMID) + ":acquire",
		)
	}
	tree.onVMRelease = func(_ context.Context, key authorityfs.VMKey) error {
		events.add(
			"vm:" + vmExecSessionVMIDForKey(key, sourceKey, sourceVMID, destinationKey, destinationVMID) + ":release",
		)
		return nil
	}
	instances := newInstanceAuthorityWithPolicy(realInstanceAuthorityDeps(), testInstanceAuthorityPolicy(root), tree)

	for key, values := range fake.metadata {
		if key.directoryFD == processTestFirecrackerDirFD && key.kind == processStatMetadata {
			fake.metadata[key] = [][]byte{
				[]byte(testProcessStat(record.launch.process.pid, record.launch.process.startTimeTicks, "S")),
				[]byte(testProcessStat(
					vmExecSessionDestinationPID,
					vmExecSessionDestinationTicks,
					"S",
				)),
			}
			continue
		}
		if key.directoryFD == processTestFirecrackerDirFD && key.kind == processCgroupMetadata {
			fake.metadata[key] = [][]byte{
				[]byte("0::/" + infra.JailerCgroupParent + "/" + sourceVMID + "\n"),
				[]byte("0::/" + infra.JailerCgroupParent + "/" + destinationVMID + "\n"),
			}
			continue
		}
		if len(values) > 0 {
			fake.metadata[key] = [][]byte{append([]byte{}, values[0]...), append([]byte{}, values[0]...)}
		}
	}
	fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processExecutableObject,
	}] = []int{113, 116, 119, 116}
	fake.stats[116] = repeatProcessStat(fake.stats[116][0], 6)
	fake.bootIDs = []string{processTestHostBootID, processTestHostBootID, processTestHostBootID}
	fake.bootIndex = 0
	fake.processDirectories[vmExecSessionDestinationPID] = processTestFirecrackerDirFD

	fixture := &vmExecSessionAuthorityFixture{
		events: events, tree: tree, root: root, identity: fake,
		sourceRecord: sourceRecord, destinationRecord: destinationRecord,
		sourceKey: sourceKey, destinationKey: destinationKey,
		aliveCalls: make(map[int]int), failAliveCall: make(map[int]int),
	}
	pidfdVMs := map[int]string{700: sourceVMID, 701: destinationVMID}
	locators := map[int]instanceProcessLocator{
		700: sourceRecord.launch.process,
		701: destinationRecord.launch.process,
	}
	recoveryDeps := processRecoveryDeps{
		pidfdOpen: func(_ context.Context, pid int) (int, error) {
			fd := -1
			switch pid {
			case sourceRecord.launch.process.pid:
				fd = 700
			case destinationRecord.launch.process.pid:
				fd = 701
			default:
				return -1, fmt.Errorf("unexpected recovered PID %d", pid)
			}
			events.add("pidfd:" + pidfdVMs[fd] + ":open")
			return fd, nil
		},
		pidfdAlive: func(_ context.Context, fd int) (bool, error) {
			fixture.aliveMu.Lock()
			defer fixture.aliveMu.Unlock()
			fixture.aliveCalls[fd]++
			return fixture.failAliveCall[fd] != fixture.aliveCalls[fd], nil
		},
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator: func(_ context.Context, fd int) (instanceProcessLocator, error) {
			locator, ok := locators[fd]
			if !ok {
				return instanceProcessLocator{}, fmt.Errorf("unexpected recovered pidfd %d", fd)
			}
			return locator, nil
		},
		terminatePIDFD: func(context.Context, int) error { return nil },
		waitReapPIDFD:  func(context.Context, int) error { return nil },
		close: func(_ context.Context, fd int) error {
			fixture.processCloses.Add(1)
			events.add("pidfd:" + pidfdVMs[fd] + ":close")
			return nil
		},
	}
	capacityFixture := newVMExecCapacityFixture(t, 8)
	fixture.capacity = capacityFixture
	capacityFixture.bootID = processTestHostBootID
	capacityFixture.onOpenStore = func(call int32) {
		if call == 1 {
			events.add("capacity:reserve")
		} else {
			events.add("capacity:release")
		}
	}
	authority, err := newVMExecSessionAuthorityWithDeps(
		instances,
		capacityFixture.authority(t),
		recoveryDeps,
		fake.deps(),
	)
	require.NoError(t, err)
	fixture.authority = authority
	return fixture
}

func (fixture *vmExecSessionAuthorityFixture) failProcessLiveness(fd int, call int) {
	fixture.aliveMu.Lock()
	defer fixture.aliveMu.Unlock()
	fixture.failAliveCall[fd] = call
}

func vmExecSessionVMIDForKey(
	key authorityfs.VMKey,
	sourceKey authorityfs.VMKey,
	sourceVMID string,
	destinationKey authorityfs.VMKey,
	destinationVMID string,
) string {
	if key == sourceKey {
		return sourceVMID
	}
	if key == destinationKey {
		return destinationVMID
	}
	return "unknown"
}

func (fixture *vmExecSessionAuthorityFixture) processCloseCount() int32 {
	return fixture.processCloses.Load()
}

func (fixture *vmExecSessionAuthorityFixture) vmAcquireEvents() []string {
	all := fixture.events.snapshot()
	result := make([]string, 0, 2)
	for _, event := range all {
		if strings.HasPrefix(event, "vm:") && strings.HasSuffix(event, ":acquire") {
			result = append(result, event)
		}
	}
	return result
}

func (fixture *vmExecSessionAuthorityFixture) vmReleaseEvents() []string {
	all := fixture.events.snapshot()
	result := make([]string, 0, 2)
	for _, event := range all {
		if strings.HasPrefix(event, "vm:") && strings.HasSuffix(event, ":release") {
			result = append(result, event)
		}
	}
	return result
}

func (fixture *vmExecSessionAuthorityFixture) indexAcquireCount() int {
	count := 0
	for _, event := range fixture.events.snapshot() {
		if event == "index:acquire" {
			count++
		}
	}
	return count
}

type vmExecSessionEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *vmExecSessionEventLog) add(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *vmExecSessionEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string{}, log.events...)
}

func (log *vmExecSessionEventLog) reset() {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = nil
}

func TestVMExecSessionAuthorityAcquiresVMLocksLexicallyForEitherDirection(t *testing.T) {
	t.Parallel()

	for _, direction := range []struct {
		name        string
		source      string
		destination string
	}{
		{name: "source_sorts_first", source: vmExecSessionDestinationID, destination: vmExecSessionSourceID},
		{name: "destination_sorts_first", source: vmExecSessionSourceID, destination: vmExecSessionDestinationID},
	} {
		t.Run(direction.name, func(t *testing.T) {
			fixture := newVMExecSessionAuthorityFixture(t, direction.source, direction.destination)

			lease, err := fixture.authority.reserve(
				t.Context(),
				testInstanceCaller(),
				direction.source,
				direction.destination,
			)
			require.NoError(t, err)
			require.NotNil(t, lease)
			require.NoError(t, lease.Release(t.Context()))

			assert.Equal(t, []string{
				"vm:" + vmExecSessionDestinationID + ":acquire",
				"vm:" + vmExecSessionSourceID + ":acquire",
			}, fixture.vmAcquireEvents())
			assert.Equal(t, 1, fixture.indexAcquireCount())
		})
	}
}

func TestVMExecSessionAuthorityReturnsExactImmutableAdmission(t *testing.T) {
	t.Parallel()

	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	lease, err := fixture.authority.reserve(
		t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	admission, err := lease.admission()
	require.NoError(t, err)
	want := vmExecSessionAdmission{
		reservationID: vmExecReservationID{value: testVMExecReservationID},
		ownerUID:      testAuthorityUID,
		source: vmExecSessionVMAdmission{
			vmID: vmExecSessionSourceID, launchGeneration: fixture.sourceRecord.launchGeneration,
		},
		destination: vmExecSessionVMAdmission{
			vmID: vmExecSessionDestinationID, launchGeneration: fixture.destinationRecord.launchGeneration,
		},
		hostBootID:          processTestHostBootID,
		relayStopBoottimeNS: fixture.capacity.boottimeNS + uint64(vmexecwire.MaxExecutionDuration),
	}
	if diff := cmp.Diff(
		want,
		admission,
		cmp.AllowUnexported(vmExecSessionAdmission{}, vmExecSessionVMAdmission{}, vmExecReservationID{}),
	); diff != "" {
		t.Errorf("VM-exec session admission mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.Release(t.Context()))
}

// Rationale: failure on the second identity must drop every earlier directory/lock capability without ever reaching
// process or global-capacity authority.
func TestVMExecSessionAuthoritySecondIdentityFailuresUnwindEarlierAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate         func(*testing.T, *vmExecSessionAuthorityFixture)
		wantCode       string
		wantVMReleases []string
	}{
		"second_lookup_missing": {
			mutate: func(t *testing.T, fixture *vmExecSessionAuthorityFixture) {
				require.NoError(t, os.Remove(filepath.Join(
					fixture.root,
					"var/lib/mvmctl/instances/1000",
					vmExecSessionDestinationID+".json",
				)))
			},
			wantCode: string(errs.CodeVMNotFound),
		},
		"second_owner_foreign": {
			mutate: func(t *testing.T, fixture *vmExecSessionAuthorityFixture) {
				require.NoError(t, os.Remove(filepath.Join(
					fixture.root,
					"var/lib/mvmctl/instances/1000",
					vmExecSessionDestinationID+".json",
				)))
				foreign := cloneInstanceRecord(fixture.destinationRecord)
				foreign.ownerUID = testAuthorityUID + 1
				foreign.launch.ownerGID = testAuthorityGID + 1
				foreign.active.userIDs = uniformProcessCredentials(testAuthorityUID + 1)
				foreign.active.groupIDs = uniformProcessCredentials(testAuthorityGID + 1)
				writeTestAuthorityRecord(t, fixture.root, foreign)
			},
			wantCode: string(errs.CodeUnauthorized),
		},
		"second_lifecycle_inactive": {
			mutate: func(t *testing.T, fixture *vmExecSessionAuthorityFixture) {
				inactive := cloneInstanceRecord(fixture.destinationRecord)
				inactive.lifecycle = instanceLifecycleLaunching
				inactive.active = nil
				writeTestAuthorityRecord(t, fixture.root, inactive)
			},
			wantCode: string(errs.CodeVMNotRunning),
			wantVMReleases: []string{
				"vm:" + vmExecSessionSourceID + ":release",
				"vm:" + vmExecSessionDestinationID + ":release",
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
			test.mutate(t, fixture)
			lease, err := fixture.authority.reserve(
				t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
			)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Equal(t, test.wantCode, string(errs.AsDomainError(err).Code))
			assert.Equal(t, int32(0), fixture.capacity.openStoreCalls.Load())
			assert.Equal(t, int32(0), fixture.processCloseCount())
			assert.ElementsMatch(t, test.wantVMReleases, fixture.vmReleaseEvents())
			assert.Equal(t, 1, fixture.indexAcquireCount())
		})
	}
}

func TestVMExecSessionAuthorityDeniesSameVMAndForeignCaller(t *testing.T) {
	t.Parallel()

	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	lease, err := fixture.authority.reserve(
		t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionSourceID,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	assert.Zero(t, fixture.indexAcquireCount())

	foreign := instanceCaller{uid: testAuthorityUID + 1, gid: testAuthorityGID + 1}
	lease, err = fixture.authority.reserve(
		t.Context(), foreign, vmExecSessionSourceID, vmExecSessionDestinationID,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
	assert.Equal(t, int32(0), fixture.capacity.openStoreCalls.Load())
}

func TestVMExecSessionAuthorityProcessFailuresNeverBecomePositiveAdmission(t *testing.T) {
	t.Parallel()

	t.Run("pre_capacity_process_failure", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		fixture.failProcessLiveness(700, 1)
		lease, err := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		assert.Nil(t, lease)
		require.Error(t, err)
		assert.Equal(t, int32(0), fixture.capacity.openStoreCalls.Load())
		assert.Equal(t, int32(1), fixture.processCloseCount())
	})

	t.Run("second_process_failure_unwinds_first", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		fixture.failProcessLiveness(701, 1)
		lease, err := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		assert.Nil(t, lease)
		require.Error(t, err)
		assert.Equal(t, int32(0), fixture.capacity.openStoreCalls.Load())
		assert.Equal(t, int32(2), fixture.processCloseCount())
	})

	// Rationale: a process may exit during the capacity-lock wait. The created capacity record makes cleanup ownership
	// partial, so admission returns the combined lease with an error instead of dropping or authorizing it.
	t.Run("post_capacity_process_failure_returns_cleanup_authority", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		fixture.failProcessLiveness(701, 5)
		lease, err := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		require.NotNil(t, lease)
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeProcessError, domainErr.Code)
		assert.Equal(t, vmExecSessionDestinationID, domainErr.Entity)
		_, admissionErr := lease.admission()
		require.Error(t, admissionErr)
		require.NoError(t, lease.Release(t.Context()))
		assert.Equal(t, int32(2), fixture.processCloseCount())
		assert.Equal(t, int32(1), fixture.capacity.recordUnlinkCalls.Load())
	})
}

func TestVMExecSessionAuthorityBootChangesNeverBecomePositiveAdmission(t *testing.T) {
	t.Parallel()

	t.Run("before_capacity", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		fixture.identity.bootIDs = []string{processTestHostBootID, processOtherBootID}
		lease, err := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		assert.Nil(t, lease)
		require.Error(t, err)
		assert.Equal(t, int32(0), fixture.capacity.openStoreCalls.Load())
	})

	t.Run("after_capacity", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		fixture.identity.bootIDs = []string{processTestHostBootID, processTestHostBootID, processOtherBootID}
		lease, err := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		require.NotNil(t, lease)
		require.Error(t, err)
		_, admissionErr := lease.admission()
		require.Error(t, admissionErr)
		require.NoError(t, lease.Release(t.Context()))
	})
}

func TestVMExecSessionCapacityProjectionRequiresEveryProvenField(t *testing.T) {
	t.Parallel()

	intent := vmExecCapacityIntent{
		caller:     vmExecCapacityCaller{ownerUID: testAuthorityUID},
		sourceVMID: vmExecSessionSourceID, sourceLaunchGeneration: 7,
		destinationVMID: vmExecSessionDestinationID, destinationLaunchGeneration: 9,
	}
	record, err := newVMExecCapacityRecord(
		intent,
		vmExecReservationID{value: testVMExecReservationID},
		processTestHostBootID,
		1_000_000_000,
		1_000_000_000+vmExecCapacityLifetimeNS,
	)
	require.NoError(t, err)
	require.NoError(t, validateVMExecSessionCapacityProjection(record, intent, processTestHostBootID))

	mutations := map[string]func(*vmExecCapacityRecord){
		"owner":                  func(record *vmExecCapacityRecord) { record.ownerUID++ },
		"source_id":              func(record *vmExecCapacityRecord) { record.sourceVMID = testVMID },
		"source_generation":      func(record *vmExecCapacityRecord) { record.sourceLaunchGeneration++ },
		"destination_id":         func(record *vmExecCapacityRecord) { record.destinationVMID = testVMExecDestinationID },
		"destination_generation": func(record *vmExecCapacityRecord) { record.destinationLaunchGeneration++ },
		"boot":                   func(record *vmExecCapacityRecord) { record.hostBootID = processOtherBootID },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := record
			mutate(&changed)
			require.Error(t, validateVMExecSessionCapacityProjection(changed, intent, processTestHostBootID))
		})
	}
}

func TestVMExecSessionAuthorityCapacityFailuresPreserveCleanupOwnership(t *testing.T) {
	t.Parallel()

	t.Run("failure_before_record_returns_no_lease", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		deps := fixture.capacity.deps()
		capacityErr := errors.New("injected capacity boot failure")
		deps.currentBootID = func(context.Context) (string, error) { return "", capacityErr }
		capacity, err := newVMExecCapacityAuthorityWithDeps(vmExecCapacityPolicy{maxReservations: 8}, deps)
		require.NoError(t, err)
		fixture.authority.capacity = capacity
		lease, reserveErr := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		assert.Nil(t, lease)
		require.Error(t, reserveErr)
		assert.ErrorIs(t, reserveErr, capacityErr)
		assert.Equal(t, int32(2), fixture.processCloseCount())
	})

	t.Run("created_record_error_returns_combined_lease", func(t *testing.T) {
		fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
		fixture.capacity.failDirectorySync = errors.New("injected capacity durability failure")
		lease, err := fixture.authority.reserve(
			t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
		)
		require.NotNil(t, lease)
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
		assert.Equal(t, true, domainErr.Details["record_created"])
		releaseErr := lease.Release(t.Context())
		require.Error(t, releaseErr)
		assert.Equal(t, true, errs.AsDomainError(releaseErr).Details["vm_exec_session_cleanup_failed"])
		assert.Equal(t, int32(1), fixture.capacity.recordUnlinkCalls.Load())
	})
}

func TestVMExecSessionLeaseReleasesInCanonicalReverseOrderWithCanceledContext(t *testing.T) {
	t.Parallel()

	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	lease, err := fixture.authority.reserve(
		t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	fixture.events.reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, lease.Release(ctx))
	assert.Equal(t, []string{
		"capacity:release",
		"pidfd:" + vmExecSessionDestinationID + ":close",
		"vm:" + vmExecSessionDestinationID + ":release",
		"pidfd:" + vmExecSessionSourceID + ":close",
		"vm:" + vmExecSessionSourceID + ":release",
	}, fixture.events.snapshot())
}

func TestVMExecSessionLeaseCopiesConcurrentlyReleaseOnce(t *testing.T) {
	t.Parallel()

	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	lease, err := fixture.authority.reserve(
		t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	copied := *lease
	const callers = 16
	results := make(chan error, callers)
	for index := 0; index < callers; index++ {
		candidate := lease
		if index%2 == 1 {
			candidate = &copied
		}
		go func() { results <- candidate.Release(context.Background()) }()
	}
	for range callers {
		require.NoError(t, <-results)
	}
	assert.Equal(t, int32(2), fixture.processCloseCount())
	assert.Equal(t, int32(1), fixture.capacity.recordUnlinkCalls.Load())
	assert.Len(t, fixture.vmReleaseEvents(), 2)
}

func uniformProcessCredentials(value uint32) processCredentialIdentity {
	return processCredentialIdentity{real: value, effective: value, saved: value, filesystem: value}
}
