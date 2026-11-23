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

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	testVMExecSourceConnectionFD      = 50
	testVMExecDestinationConnectionFD = 51
	testVMExecSourcePeerPIDFD         = 900
	testVMExecDestinationPeerPIDFD    = 901
)

type vmExecSessionPeerFixture struct {
	// mu guards injected liveness state and peer close observations shared with authority effect callbacks.
	mu          sync.Mutex
	records     map[int]instanceRecord
	peerPIDFDs  map[int]int
	locators    map[int]instanceProcessLocator
	aliveCalls  map[int]int
	failAlive   map[int]int
	closeErrors map[int]error
	closed      []int

	exitSourceOnDestinationAliveCall int
	sourceExited                     bool
}

// Rationale: directional peer state is monotonic. Reordering or rebinding either capability would make readiness
// ambiguous and could replace an already-proven Firecracker peer.
func TestVMExecSessionRejectsOutOfOrderAndDuplicatePeerBinding(t *testing.T) {
	tests := []struct {
		name        string
		exercise    func(*testing.T, *vmExecSessionLease) error
		wantMessage string
	}{
		{
			name: "destination_before_source",
			exercise: func(t *testing.T, lease *vmExecSessionLease) error {
				return lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD)
			},
			wantMessage: "source connection must be bound first",
		},
		{
			name: "duplicate_source",
			exercise: func(t *testing.T, lease *vmExecSessionLease) error {
				require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))
				return lease.bindSource(t.Context(), testVMExecSourceConnectionFD)
			},
			wantMessage: "source connection is already bound",
		},
		{
			name: "duplicate_destination",
			exercise: func(t *testing.T, lease *vmExecSessionLease) error {
				require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))
				require.NoError(t, lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD))
				return lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD)
			},
			wantMessage: "destination connection is already bound",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
			prepareVMExecSessionBindingIdentity(fixture)
			peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
			fixture.authority.peerDeps = peers.deps()
			lease, err := fixture.authority.reserve(
				t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
			)
			require.NoError(t, err)
			require.NotNil(t, lease)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

			err = test.exercise(t, lease)

			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantMessage)
		})
	}
}

// Rationale: connection setup consumes the same fixed execution budget as the relay and cannot cross a host reboot.
// Either boundary invalidates the retained admission before a directional binding becomes usable.
func TestVMExecSessionBindingRejectsBootDriftAndExhaustedRelayBudget(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *vmExecSessionAuthorityFixture, *vmExecSessionLease)
		wantMessage string
	}{
		{
			name: "boot_drift",
			mutate: func(t *testing.T, fixture *vmExecSessionAuthorityFixture, _ *vmExecSessionLease) {
				fixture.identity.mu.Lock()
				defer fixture.identity.mu.Unlock()
				require.Less(t, fixture.identity.bootIndex, len(fixture.identity.bootIDs))
				fixture.identity.bootIDs[fixture.identity.bootIndex] = processOtherBootID
			},
			wantMessage: "host boot ID changed during VM-exec peer proof",
		},
		{
			name: "relay_budget_exhausted",
			mutate: func(_ *testing.T, fixture *vmExecSessionAuthorityFixture, lease *vmExecSessionLease) {
				fixture.capacity.boottimeNS = lease.state.admission.relayStopBoottimeNS
			},
			wantMessage: "vm-exec relay CLOCK_BOOTTIME budget is exhausted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
			prepareVMExecSessionBindingIdentity(fixture)
			peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
			fixture.authority.peerDeps = peers.deps()
			lease, err := fixture.authority.reserve(
				t.Context(), testInstanceCaller(), vmExecSessionSourceID, vmExecSessionDestinationID,
			)
			require.NoError(t, err)
			require.NotNil(t, lease)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
			test.mutate(t, fixture, lease)

			err = lease.bindSource(t.Context(), testVMExecSourceConnectionFD)

			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantMessage)
		})
	}
}

func TestVMExecSessionBindsAndRetainsBothExactFirecrackerPeers(t *testing.T) {
	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	prepareVMExecSessionBindingIdentity(fixture)
	peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
	fixture.authority.peerDeps = peers.deps()

	lease, err := fixture.authority.reserve(
		t.Context(),
		testInstanceCaller(),
		vmExecSessionSourceID,
		vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { assert.NoError(t, lease.Release(context.Background())) })
	require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))
	require.NoError(t, lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD))

	assert.Empty(t, peers.closedSnapshot())
	assert.Equal(t, 5, peers.aliveCallCount(testVMExecSourcePeerPIDFD))
	assert.Equal(t, 5, peers.aliveCallCount(testVMExecDestinationPeerPIDFD))
	require.NoError(t, lease.Release(t.Context()))
	if diff := cmp.Diff(
		[]int{testVMExecDestinationPeerPIDFD, testVMExecSourcePeerPIDFD},
		peers.closedSnapshot(),
	); diff != "" {
		t.Errorf("released VM-exec peer pidfds mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, int32(1), fixture.capacity.recordUnlinkCalls.Load())
}

func TestVMExecSessionDestinationBindingRechecksRetainedSourcePeer(t *testing.T) {
	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	prepareVMExecSessionBindingIdentity(fixture)
	peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
	peers.failAlive[testVMExecSourcePeerPIDFD] = 3
	fixture.authority.peerDeps = peers.deps()

	lease, err := fixture.authority.reserve(
		t.Context(),
		testInstanceCaller(),
		vmExecSessionSourceID,
		vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { assert.NoError(t, lease.Release(context.Background())) })
	require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))

	err = lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")
	assert.Contains(t, err.Error(), "not live")
	require.NoError(t, lease.Release(t.Context()))
	if diff := cmp.Diff(
		[]int{testVMExecDestinationPeerPIDFD, testVMExecSourcePeerPIDFD},
		peers.closedSnapshot(),
	); diff != "" {
		t.Errorf("failed binding peer cleanup mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, int32(1), fixture.capacity.recordUnlinkCalls.Load())
}

// Rationale: the source can exit while the destination's expensive full-identity proof is in progress. Both retained
// peer and process pidfd sets must receive one final bounded sweep after those proofs and before readiness.
func TestVMExecSessionDestinationBindingRejectsSourceExitDuringDestinationIdentityVerification(t *testing.T) {
	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	prepareVMExecSessionBindingIdentity(fixture)
	peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
	peers.exitSourceOnDestinationAliveCall = 3
	fixture.authority.peerDeps = peers.deps()

	lease, err := fixture.authority.reserve(
		t.Context(),
		testInstanceCaller(),
		vmExecSessionSourceID,
		vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { assert.NoError(t, lease.Release(context.Background())) })
	require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))

	err = lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")
	assert.Contains(t, err.Error(), "not live")
	lease.state.mu.Lock()
	destinationBound := lease.state.destinationBound
	lease.state.mu.Unlock()
	assert.False(t, destinationBound)
}

// Rationale: PID/start and pidfd liveness do not prove that the retained task remains the admitted Firecracker. The
// complete active credentials, cgroup, namespaces, root, executable metadata, and digest are rechecked before ready.
func TestVMExecSessionDestinationBindingRejectsRetainedSourceIdentityDrift(t *testing.T) {
	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	prepareVMExecSessionBindingIdentity(fixture)
	cgroupKey := processMetadataTestKey{
		directoryFD: processTestFirecrackerDirFD,
		kind:        processCgroupMetadata,
	}
	fixture.identity.metadata[cgroupKey][6] = []byte("0::/foreign.slice\n")
	peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
	fixture.authority.peerDeps = peers.deps()

	lease, err := fixture.authority.reserve(
		t.Context(),
		testInstanceCaller(),
		vmExecSessionSourceID,
		vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { assert.NoError(t, lease.Release(context.Background())) })
	require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))

	err = lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD)

	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, vmExecSessionSourceID, domainErr.Entity)
	assert.Contains(t, err.Error(), "full active identity")
	require.NoError(t, lease.Release(t.Context()))
	if diff := cmp.Diff(
		[]int{testVMExecDestinationPeerPIDFD, testVMExecSourcePeerPIDFD},
		peers.closedSnapshot(),
	); diff != "" {
		t.Errorf("identity-drift peer cleanup mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecSessionPeerReleaseFailurePreservesEvidenceAndContinuesCleanup(t *testing.T) {
	fixture := newVMExecSessionAuthorityFixture(t, vmExecSessionSourceID, vmExecSessionDestinationID)
	prepareVMExecSessionBindingIdentity(fixture)
	peers := newVMExecSessionPeerFixture(fixture.sourceRecord, fixture.destinationRecord)
	closeErr := errors.New("injected destination peer close failure")
	peers.closeErrors[testVMExecDestinationPeerPIDFD] = closeErr
	fixture.authority.peerDeps = peers.deps()

	lease, err := fixture.authority.reserve(
		t.Context(),
		testInstanceCaller(),
		vmExecSessionSourceID,
		vmExecSessionDestinationID,
	)
	require.NoError(t, err)
	require.NoError(t, lease.bindSource(t.Context(), testVMExecSourceConnectionFD))
	require.NoError(t, lease.bindDestination(t.Context(), testVMExecDestinationConnectionFD))

	releaseErr := lease.Release(t.Context())

	require.Error(t, releaseErr)
	assert.ErrorIs(t, releaseErr, closeErr)
	domainErr := errs.AsDomainError(releaseErr)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["vm_exec_session_cleanup_failed"])
	if diff := cmp.Diff(
		[]int{testVMExecDestinationPeerPIDFD, testVMExecSourcePeerPIDFD},
		peers.closedSnapshot(),
	); diff != "" {
		t.Errorf("peer-close failure cleanup mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, int32(1), fixture.capacity.recordUnlinkCalls.Load())
	assert.Equal(t, int32(2), fixture.processCloseCount())
}

func TestProductionVMExecSessionAuthorityUsesFixedGlobalCeiling(t *testing.T) {
	authority, err := newVMExecSessionAuthority()

	require.NoError(t, err)
	require.NotNil(t, authority)
	require.NotNil(t, authority.instances)
	require.NotNil(t, authority.capacity)
	assert.Equal(t, uint32(8), productionVMExecMaxSessions)
	assert.Equal(t, uint32(8), authority.capacity.policy.maxReservations)
	require.NoError(t, validateProcessRecoveryDeps(authority.recoveryDeps))
	require.NoError(t, validateProcessIdentityVerifierDeps(authority.identityDeps))
	require.NoError(t, validateVMExecPeerAuthorityDeps(authority.peerDeps))
}

// newVMExecSessionPeerFixture maps each directional connection to its exact Firecracker credentials, pidfd, and
// launch locator while retaining observable liveness and close state.
func newVMExecSessionPeerFixture(
	source instanceRecord,
	destination instanceRecord,
) *vmExecSessionPeerFixture {
	return &vmExecSessionPeerFixture{
		records: map[int]instanceRecord{
			testVMExecSourceConnectionFD:      source,
			testVMExecDestinationConnectionFD: destination,
		},
		peerPIDFDs: map[int]int{
			testVMExecSourceConnectionFD:      testVMExecSourcePeerPIDFD,
			testVMExecDestinationConnectionFD: testVMExecDestinationPeerPIDFD,
		},
		locators: map[int]instanceProcessLocator{
			testVMExecSourcePeerPIDFD:      source.launch.process,
			testVMExecDestinationPeerPIDFD: destination.launch.process,
		},
		aliveCalls:  make(map[int]int),
		failAlive:   make(map[int]int),
		closeErrors: make(map[int]error),
	}
}

// deps exposes the exact, deterministic peer evidence for both directional connections and records retained-pidfd
// liveness and cleanup observations under the fixture mutex.
func (fixture *vmExecSessionPeerFixture) deps() vmExecPeerAuthorityDeps {
	return vmExecPeerAuthorityDeps{
		peerCredentials: func(_ context.Context, connectionFD int) (*unix.Ucred, error) {
			record, found := fixture.records[connectionFD]
			if !found {
				return nil, fmt.Errorf("unexpected VM-exec connection descriptor %d", connectionFD)
			}
			return &unix.Ucred{
				Pid: int32(record.launch.process.pid),
				Uid: record.active.userIDs.effective,
				Gid: record.active.groupIDs.effective,
			}, nil
		},
		peerPIDFD: func(_ context.Context, connectionFD int) (int, error) {
			pidfd, found := fixture.peerPIDFDs[connectionFD]
			if !found {
				return -1, fmt.Errorf("unexpected VM-exec connection descriptor %d", connectionFD)
			}
			return pidfd, nil
		},
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator: func(_ context.Context, pidfd int) (instanceProcessLocator, error) {
			locator, found := fixture.locators[pidfd]
			if !found {
				return instanceProcessLocator{}, fmt.Errorf("unexpected VM-exec peer pidfd %d", pidfd)
			}
			return locator, nil
		},
		pidfdAlive: func(_ context.Context, pidfd int) (bool, error) {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.aliveCalls[pidfd]++
			if pidfd == testVMExecDestinationPeerPIDFD &&
				fixture.aliveCalls[pidfd] == fixture.exitSourceOnDestinationAliveCall {
				fixture.sourceExited = true
			}
			if pidfd == testVMExecSourcePeerPIDFD && fixture.sourceExited {
				return false, nil
			}
			return fixture.aliveCalls[pidfd] != fixture.failAlive[pidfd], nil
		},
		close: func(_ context.Context, pidfd int) error {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.closed = append(fixture.closed, pidfd)
			return fixture.closeErrors[pidfd]
		},
	}
}

func (fixture *vmExecSessionPeerFixture) aliveCallCount(pidfd int) int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.aliveCalls[pidfd]
}

func (fixture *vmExecSessionPeerFixture) closedSnapshot() []int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]int{}, fixture.closed...)
}

// prepareVMExecSessionBindingIdentity extends the admission fixture with the ordered complete-identity observations
// consumed by source admission and the later source/destination readiness rechecks.
func prepareVMExecSessionBindingIdentity(fixture *vmExecSessionAuthorityFixture) {
	fake := fixture.identity
	sequence := []vmExecSessionRole{
		vmExecSessionSourceRole,
		vmExecSessionDestinationRole,
		vmExecSessionSourceRole,
		vmExecSessionDestinationRole,
	}
	statKey := processMetadataTestKey{directoryFD: processTestFirecrackerDirFD, kind: processStatMetadata}
	cgroupKey := processMetadataTestKey{directoryFD: processTestFirecrackerDirFD, kind: processCgroupMetadata}
	for _, role := range sequence {
		pid := fixture.sourceRecord.launch.process.pid
		ticks := fixture.sourceRecord.launch.process.startTimeTicks
		vmID := fixture.sourceRecord.vmID
		if role == vmExecSessionDestinationRole {
			pid = fixture.destinationRecord.launch.process.pid
			ticks = fixture.destinationRecord.launch.process.startTimeTicks
			vmID = fixture.destinationRecord.vmID
		}
		fake.metadata[statKey] = append(fake.metadata[statKey], []byte(testProcessStat(pid, ticks, "S")))
		fake.metadata[cgroupKey] = append(
			fake.metadata[cgroupKey],
			[]byte("0::/"+infra.JailerCgroupParent+"/"+vmID+"\n"),
		)
		for key, values := range fake.metadata {
			if key == statKey || key == cgroupKey || len(values) == 0 {
				continue
			}
			fake.metadata[key] = append(fake.metadata[key], append([]byte{}, values[0]...))
		}
		appendVMExecSessionBindingObjects(fake, role)
	}
	for fd, values := range fake.stats {
		if len(values) == 0 {
			continue
		}
		fake.stats[fd] = append(fake.stats[fd], repeatProcessStat(values[0], 32)...)
	}
	for range 8 {
		fake.bootIDs = append(fake.bootIDs, processTestHostBootID)
	}
}

// appendVMExecSessionBindingObjects adds one role's pinned namespace, root, and executable observations in verifier
// call order; executable checks consume an extra stable descriptor for digest comparison.
func appendVMExecSessionBindingObjects(fake *fakeProcessIdentityDeps, role vmExecSessionRole) {
	mountFD := 111
	networkFD := 141
	rootFD := 112
	executableFD := 113
	if role == vmExecSessionDestinationRole {
		mountFD = 114
		networkFD = 142
		rootFD = 115
		executableFD = 119
	}
	fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processMountNamespaceObject,
	}] = append(fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processMountNamespaceObject,
	}], mountFD)
	fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processNetworkNamespaceObject,
	}] = append(fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processNetworkNamespaceObject,
	}], networkFD)
	fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processRootObject,
	}] = append(fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processRootObject,
	}], rootFD)
	fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processExecutableObject,
	}] = append(fake.processObjects[processObjectTestKey{
		directoryFD: processTestFirecrackerDirFD,
		object:      processExecutableObject,
	}], executableFD, 116)
}
