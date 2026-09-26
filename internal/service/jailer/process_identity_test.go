package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestActivateVerifiedProcessUsesRetainedPIDFDsAndPinnedExecutable(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, true)
	execution := fixture.launch.snapshot().record.launch.executionIdentity
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	require.NotNil(t, active)
	activeRecord := active.snapshot()
	assert.Equal(t, instanceLifecycleActive, activeRecord.lifecycle)
	require.NotNil(t, activeRecord.active)
	want := instanceActiveIdentity{
		userIDs:             repeatedProcessCredentials(execution.uid),
		groupIDs:            repeatedProcessCredentials(execution.gid),
		supplementaryGroups: []uint32{},
		mountNamespace:      fixture.firecrackerNamespace,
		jailRoot:            fixture.jailRoot,
		executable: instanceFirecrackerExecutableIdentity{
			device: fixture.firecrackerExecutable.device, inode: fixture.firecrackerExecutable.inode,
			sizeBytes: fixture.firecrackerExecutable.sizeBytes, mode: fixture.firecrackerExecutable.mode,
			uid: fixture.firecrackerExecutable.uid, gid: fixture.firecrackerExecutable.gid,
			links: fixture.firecrackerExecutable.links, manifestSHA256: fixture.firecrackerDigest,
		},
		relay: instanceActiveRelay{
			kind:                instanceRelayPresent,
			userIDs:             repeatedProcessCredentials(testAuthorityUID),
			groupIDs:            repeatedProcessCredentials(testAuthorityGID),
			supplementaryGroups: []uint32{},
			mountNamespace:      fixture.hostNamespace,
			root:                fixture.hostRoot,
			executable: instanceRelayExecutableIdentity{
				device: fixture.receiverIdentity.Device, inode: fixture.receiverIdentity.Inode,
				sizeBytes: fixture.receiverIdentity.SizeBytes, mode: fixture.receiverIdentity.Mode,
				uid: fixture.receiverIdentity.UID, gid: fixture.receiverIdentity.GID,
				links: fixture.receiverIdentity.Links, sha256: fixture.receiverIdentity.SHA256,
			},
		},
	}
	if diff := cmp.Diff(
		want,
		*activeRecord.active,
		cmp.AllowUnexported(
			instanceActiveIdentity{}, instanceActiveRelay{}, processCredentialIdentity{}, instanceObjectIdentity{},
			instanceFirecrackerExecutableIdentity{}, instanceRelayExecutableIdentity{},
		),
	); diff != "" {
		t.Errorf("active identity mismatch (-want +got):\n%s", diff)
	}
	assert.True(t, fixture.launch.snapshot().released)
	assert.False(t, fixture.launch.snapshot().hasPreparedManagedVMCgroup)
	assert.False(t, fixture.launch.snapshot().hasManagedVMCgroup)
	assert.Equal(t, blockedLaunchProcessesTransferred, fixture.processSource.state.status)
	require.NotNil(t, active.state.processes)
	boundCgroup := active.state.managedVMCgroup
	require.NotNil(t, boundCgroup)
	require.Error(t, active.Release(t.Context()))
	assert.Equal(t, blockedLaunchProcessesReleasedToJailer, active.state.processes.state.status)
	require.NoError(t, fixture.launch.Release(t.Context()))
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	assert.Nil(t, active.state.managedVMCgroup)
	assert.Same(t, boundCgroup, cleanup.state.managedVMCgroup)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.True(t, boundCgroup.snapshot().released)
	assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
	require.NoError(t, cleanup.Release(t.Context()))
	if diff := cmp.Diff([]int{203, 202, 201, 200}, fixture.fake.destinationCloseOrder()); diff != "" {
		t.Errorf("destination cleanup order mismatch (-want +got):\n%s", diff)
	}
}

func TestActivateVerifiedProcessRetriesLimitReadbackOnSameBoundCgroup(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	pidsPath := filepath.Join(fixture.cgroup.vmLeafPath(testVMID), "pids.max")
	require.NoError(t, os.WriteFile(pidsPath, []byte("129\n"), 0o644))
	leafOpens := 0
	pidsOpens := 0
	prepared := fixture.launch.state.preparedManagedVMCgroup
	realOpenAt2 := prepared.state.deps.openAt2
	prepared.state.deps.openAt2 = func(
		ctx context.Context,
		fd int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		if name == testVMID {
			leafOpens++
		}
		if name == "pids.max" {
			pidsOpens++
		}
		return realOpenAt2(ctx, fd, name, how)
	}

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	assert.Nil(t, active)
	require.Error(t, err)
	bound := fixture.launch.state.managedVMCgroup
	require.NotNil(t, bound)
	assert.Nil(t, fixture.launch.state.preparedManagedVMCgroup)
	assert.False(t, fixture.launch.snapshot().hasPreparedManagedVMCgroup)
	assert.True(t, fixture.launch.snapshot().hasManagedVMCgroup)
	assert.Equal(t, instanceLifecycleLaunching, fixture.launch.state.record.lifecycle)
	assert.Equal(t, 1, leafOpens)
	assert.Equal(t, 1, pidsOpens)

	require.NoError(t, os.WriteFile(pidsPath, []byte("128\n"), 0o644))
	fixture.fake.retainPostExecFirecracker()
	active, err = activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Same(t, bound, active.state.managedVMCgroup)
	assert.Equal(t, 1, leafOpens)
	assert.Equal(t, 2, pidsOpens)
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestActivateVerifiedProcessPreAdmissionCgroupFailureRetainsPreparation(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	prepared := fixture.launch.state.preparedManagedVMCgroup
	require.NoError(t, unix.Chmod(fixture.cgroup.vmLeafPath(testVMID), 0o755))

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Same(t, prepared, fixture.launch.state.preparedManagedVMCgroup)
	assert.Nil(t, fixture.launch.state.managedVMCgroup)
	assert.True(t, fixture.launch.snapshot().hasPreparedManagedVMCgroup)
	assert.False(t, fixture.launch.snapshot().hasManagedVMCgroup)

	require.NoError(t, unix.Chmod(fixture.cgroup.vmLeafPath(testVMID), managedVMCgroupLeafMode))
	fixture.fake.retainPostExecFirecracker()
	active, err = activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	require.NotNil(t, active)
	bound := active.state.managedVMCgroup
	require.NotNil(t, bound)
	assert.True(t, prepared.snapshot().released)
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.True(t, bound.snapshot().released)
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestActiveCleanupNonemptyManagedVMCgroupRetainsLeaseForRetry(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	bound := active.state.managedVMCgroup
	require.NotNil(t, bound)
	originalWait := active.state.processes.state.deps.waitReapPIDFD
	active.state.processes.state.deps.waitReapPIDFD = func(ctx context.Context, fd int) error {
		if err := originalWait(ctx, fd); err != nil {
			return err
		}
		if fd == processTestFirecrackerPIDFD {
			fixture.cgroup.setMembership(testVMID, "4242\n")
		}
		return nil
	}
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)

	require.Error(t, cleanup.Complete(t.Context()))
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	assert.Same(t, bound, cleanup.state.managedVMCgroup)
	assert.True(t, fixture.cgroup.vmLeafPresent(testVMID))
	fixture.cgroup.setMembership(testVMID, "")
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.True(t, bound.snapshot().released)
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestBoundJailDestinationUsesOperableRootDescriptor(t *testing.T) {
	t.Parallel()

	fake := newPostHandoffJailDestinationFixture()
	destination, err := fake.destination.bindAfterHandoff(
		t.Context(),
		testAuthorityUID,
		testAuthorityGID,
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		fake.destinationOpenFlags[202],
	)
	assert.Zero(t, fake.destinationOpenFlags[202]&unix.O_PATH)
	require.NoError(t, destination.Release(t.Context()))
}

func TestCopiedLaunchExecutableReadyAdmitsJailerPrivateCopy(t *testing.T) {
	fake := newPostHandoffJailDestinationFixture()
	ready, err := copiedLaunchExecutableReady(
		t.Context(), fake.destination.state, testAuthorityGID, testAuthorityUID,
	)
	require.NoError(t, err)
	assert.True(t, ready, "Jailer-owned 0700 copy must be ready for root sealing")
}

func TestCopiedLaunchExecutableReadyWaitsForPrivateJailRoot(t *testing.T) {
	fake := newPostHandoffJailDestinationFixture()
	root := fake.stats[202][0]
	root.Mode = unix.S_IFDIR | 0755
	root.Uid = 0
	root.Gid = 0
	fake.stats[202][0] = root

	ready, err := copiedLaunchExecutableReady(
		t.Context(), fake.destination.state, testAuthorityGID, testAuthorityUID,
	)
	require.NoError(t, err)
	assert.False(t, ready, "a copied executable alone must not race Jailer's private-root transition")
}

func TestBindAfterHandoffTransfersParentCloseOnceOnFailure(t *testing.T) {
	t.Parallel()

	fake := newPostHandoffJailDestinationFixture()
	invalid := fake.stats[203][0]
	invalid.Nlink = 2
	fake.stats[203][0] = invalid

	destination, err := fake.destination.bindAfterHandoff(
		t.Context(),
		testAuthorityUID,
		testAuthorityGID,
	)
	assert.Nil(t, destination)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-seal metadata")
	require.NoError(t, fake.destination.Release(t.Context()))

	counts := make(map[int]int)
	for _, fd := range fake.destinationCloseOrder() {
		counts[fd]++
	}
	assert.Equal(t, 1, counts[200])
	assert.Equal(t, 1, counts[201])
	assert.Equal(t, 1, counts[202])
	assert.Equal(t, 1, counts[203])
}

func TestActivateVerifiedProcessWaitsForBoundedExecutableTransition(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	makeFirecrackerObservationTransient(fixture, 112, 113)
	fixture.fake.bootIDs = append(fixture.fake.bootIDs, processTestHostBootID)

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, []time.Duration{processExecObservationInterval}, fixture.fake.waits)
	assert.Equal(t, 4, fixture.fake.destinationMutations)

	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestActivateVerifiedProcessExecutableTransitionHonorsEarlierCallerDeadline(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	makeFirecrackerObservationTransient(fixture, 112, 113)
	makeFirecrackerObservationTransient(fixture, 115, 116)
	ctx, cancel := context.WithDeadline(t.Context(), fixture.fake.clock.Add(2*processExecObservationInterval))
	defer cancel()

	active, err := activateVerifiedProcessWithDeps(ctx, fixture.launch, fixture.fake.deps())
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadline")
	assert.Equal(t, []time.Duration{processExecObservationInterval, processExecObservationInterval}, fixture.fake.waits)
	assert.Zero(t, fixture.fake.destinationMutations)
	releaseTestLaunch(t, fixture.launch)
}

func TestActivateVerifiedProcessExecutableTransitionEnforcesTenSecondBudget(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	makeFirecrackerObservationTransient(fixture, 112, 113)
	deps := fixture.fake.deps()
	deps.wait = func(ctx context.Context, wait time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fixture.fake.mu.Lock()
		defer fixture.fake.mu.Unlock()
		fixture.fake.waits = append(fixture.fake.waits, wait)
		fixture.fake.clock = fixture.fake.clock.Add(processExecObservationBudget)
		return nil
	}

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, deps)
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded its deadline")
	assert.Equal(t, []time.Duration{processExecObservationInterval}, fixture.fake.waits)
	assert.Zero(t, fixture.fake.destinationMutations)
	releaseTestLaunch(t, fixture.launch)
}

func TestActivateVerifiedProcessExecutableTransitionChecksPIDFDExitEachInterval(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	makeFirecrackerObservationTransient(fixture, 112, 113)
	fixture.fake.alive[processTestFirecrackerPIDFD] = []bool{true, false}

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "process exited")
	assert.Equal(t, []time.Duration{processExecObservationInterval}, fixture.fake.waits)
	assert.Zero(t, fixture.fake.destinationMutations)
	releaseTestLaunch(t, fixture.launch)
}

func TestActivateVerifiedProcessRejectsTopologyNamespaceMismatch(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	fixture.launch.state.networkNamespace = &topologySelectedNetworkNamespace{
		identity: instanceObjectIdentity{device: fixture.firecrackerNetworkNamespace.device, inode: 999999},
	}

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topology-selected network namespace")
	releaseTestLaunch(t, fixture.launch)
}

func makeFirecrackerObservationTransient(fixture *processIdentityFixture, rootFD, executableFD int) {
	if executableFD == 113 {
		// A second pre-seal poll still observes Jailer's private 0700 copy;
		// the third observation follows the controller's sealing mutation.
		fixture.fake.stats[116][0] = fixture.fake.stats[203][0]
	}
	root := fixture.fake.stats[rootFD][0]
	root.Ino++
	fixture.fake.stats[rootFD][0] = root
	executable := fixture.fake.stats[executableFD][0]
	executable.Ino++
	fixture.fake.stats[executableFD][0] = executable
}

func TestActivateVerifiedProcessOmitsAbsentRelay(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	activeRecord := active.snapshot()
	require.NotNil(t, activeRecord.active)
	assert.Equal(t, instanceRelayAbsent, activeRecord.active.relay.kind)
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestActivateVerifiedProcessFailsClosedAtEveryRecheckBoundary(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		relay   bool
		mutate  func(*processIdentityFixture)
		wantErr string
	}{
		"initial boot mismatch": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.bootIDs[0] = processOtherBootID },
			wantErr: "host boot ID does not match",
		},
		"final boot drift": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.bootIDs[2] = processOtherBootID },
			wantErr: "host boot ID changed",
		},
		"malformed process stat": {
			mutate: func(fixture *processIdentityFixture) {
				fixture.fake.metadata[processMetadataTestKey{processTestFirecrackerDirFD, processStatMetadata}][0] =
					[]byte("4242 (truncated) S\n")
			},
			wantErr: "incomplete",
		},
		"start ticks drift": {
			mutate: func(fixture *processIdentityFixture) {
				fixture.fake.metadata[processMetadataTestKey{processTestFirecrackerDirFD, processStatMetadata}][1] =
					[]byte(testProcessStat(processTestFirecrackerPID, processTestFirecrackerTicks+1, "S"))
			},
			wantErr: "launch locator",
		},
		"credentials mismatch": {
			mutate: func(fixture *processIdentityFixture) {
				fixture.fake.replaceProcessStatus(processTestFirecrackerDirFD, testAuthorityUID+1, testAuthorityGID, "")
			},
			wantErr: "credentials do not match",
		},
		"supplementary groups": {
			mutate: func(fixture *processIdentityFixture) {
				execution := fixture.launch.snapshot().record.launch.executionIdentity
				fixture.fake.replaceProcessStatus(
					processTestFirecrackerDirFD,
					execution.uid,
					execution.gid,
					"2000",
				)
			},
			wantErr: "supplementary groups are not empty",
		},
		"cgroup mismatch": {
			mutate: func(fixture *processIdentityFixture) {
				fixture.fake.replaceProcessCgroup(processTestFirecrackerDirFD, "/other")
			},
			wantErr: "cgroup does not match",
		},
		"root mismatch": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[115][0].Ino++ },
			wantErr: "root or executable",
		},
		"executable mismatch": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[116][0].Ino++ },
			wantErr: "root or executable",
		},
		"copied executable digest": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.contents[203][0] ^= 0xff },
			wantErr: "release manifest",
		},
		"mount namespace drift": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[114][0].Ino++ },
			wantErr: "identity changed while sealing",
		},
		"network namespace mismatch": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[141][0].Ino++ },
			wantErr: "topology-selected network namespace",
		},
		"jail root drift": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[202][2].Ino++ },
			wantErr: "binding changed",
		},
		"receiver executable reinspect drift": {
			relay: true,
			mutate: func(fixture *processIdentityFixture) {
				drifted := fixture.receiverIdentity
				drifted.Inode++
				fixture.fake.reinspectIdentity = &drifted
			},
			wantErr: "pinned receiver executable identity changed",
		},
		"relay credentials mismatch": {
			relay: true,
			mutate: func(fixture *processIdentityFixture) {
				fixture.fake.replaceProcessStatus(processTestRelayDirFD, testAuthorityUID+1, testAuthorityGID, "")
			},
			wantErr: "relay credentials do not match",
		},
		"relay namespace mismatch": {
			relay:   true,
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[121][0].Ino++ },
			wantErr: "relay namespace, root, or executable",
		},
		"relay root mismatch": {
			relay:   true,
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[122][0].Ino++ },
			wantErr: "relay namespace, root, or executable",
		},
		"relay executable mismatch": {
			relay:   true,
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[123][0].Ino++ },
			wantErr: "relay namespace, root, or executable",
		},
		"final Firecracker exit": {
			mutate: func(fixture *processIdentityFixture) {
				calls := 0
				fixture.fake.deriveLocatorOverride = func(context.Context, int) (instanceProcessLocator, error) {
					calls++
					if calls > 1 {
						return instanceProcessLocator{}, errors.New("pidfd process exited")
					}
					return instanceProcessLocator{
						pid: processTestFirecrackerPID, startTimeTicks: processTestFirecrackerTicks,
					}, nil
				}
			},
			wantErr: "pidfd process exited",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newProcessIdentityFixture(t, tc.relay)
			deps := fixture.fake.deps()
			tc.mutate(fixture)
			active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, deps)
			assert.Nil(t, active)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.Equal(t, testVMID, domainErr.Entity)
			assert.False(t, fixture.launch.snapshot().released)
			releaseTestLaunch(t, fixture.launch)
		})
	}
}

func TestActivateVerifiedProcessPreservesPrimaryContractAndCleanupEvidence(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	deps := fixture.fake.deps()
	fixture.fake.metadata[processMetadataTestKey{processTestFirecrackerDirFD, processStatMetadata}][0] =
		[]byte("4242 (truncated) S\n")
	closeErr := errors.New(strings.Repeat("descriptor close failure ", 80))
	fixture.fake.closeErrors[processTestFirecrackerDirFD] = closeErr

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, deps)
	assert.Nil(t, active)
	require.Error(t, err)
	assert.ErrorIs(t, err, closeErr)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["process_identity_cleanup_failed"])
	detail, ok := domainErr.Details["process_identity_cleanup_detail"].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(detail), maxInstanceAuthorityCleanupDetailBytes)
	releaseTestLaunch(t, fixture.launch)
}

func TestActivateVerifiedProcessHonorsCancellationDuringCopiedExecutableHash(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	deps := fixture.fake.deps()
	ctx, cancel := context.WithCancel(t.Context())
	fixture.fake.onPread = func(call int) {
		if call == 1 {
			cancel()
		}
	}

	active, err := activateVerifiedProcessWithDeps(ctx, fixture.launch, deps)
	assert.Nil(t, active)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	releaseTestLaunch(t, fixture.launch)
}

func TestActivateVerifiedProcessReturnsTargetAfterActiveRecordRename(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	deps := fixture.fake.deps()
	fault := errors.New("injected active directory fsync failure")
	stateFD := fixture.launch.state.directories.stateFD
	realFsync := fixture.launch.state.directories.deps.fsync
	fixture.launch.state.directories.deps.fsync = func(ctx context.Context, fd int) error {
		if fd == stateFD {
			return fault
		}
		return realFsync(ctx, fd)
	}

	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, deps)
	require.NotNil(t, active)
	require.Error(t, err)
	assert.ErrorIs(t, err, fault)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.True(t, fixture.launch.snapshot().released)
	assert.False(t, fixture.launch.snapshot().hasManagedVMCgroup)
	bound := active.state.managedVMCgroup
	require.NotNil(t, bound)
	cleanup, cleanupErr := active.BeginCleanup(t.Context())
	require.ErrorIs(t, cleanupErr, fault)
	require.NotNil(t, cleanup)
	assert.Nil(t, active.state.managedVMCgroup)
	assert.Same(t, bound, cleanup.state.managedVMCgroup)
	require.ErrorIs(t, cleanup.Complete(t.Context()), fault)
	assert.True(t, bound.snapshot().released)
	require.NoError(t, cleanup.Release(t.Context()))
}
