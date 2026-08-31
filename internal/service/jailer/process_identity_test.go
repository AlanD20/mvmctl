package jailer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestActivateVerifiedProcessUsesRetainedPIDFDsAndPinnedExecutable(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, true)
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	require.NotNil(t, active)
	activeRecord := active.snapshot()
	assert.Equal(t, instanceLifecycleActive, activeRecord.lifecycle)
	require.NotNil(t, activeRecord.active)
	want := instanceActiveIdentity{
		userIDs:             repeatedProcessCredentials(testAuthorityUID),
		groupIDs:            repeatedProcessCredentials(testAuthorityGID),
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
	assert.Equal(t, blockedLaunchProcessesTransferred, fixture.processSource.state.status)
	require.NotNil(t, active.state.processes)
	assert.Equal(t, blockedLaunchProcessesReleasedToJailer, active.state.processes.state.status)
	require.NoError(t, fixture.launch.Release(t.Context()))
	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Release(t.Context()))
	if diff := cmp.Diff([]int{203, 202, 201, 200}, fixture.fake.destinationCloseOrder()); diff != "" {
		t.Errorf("destination cleanup order mismatch (-want +got):\n%s", diff)
	}
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
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.bootIDs[1] = processOtherBootID },
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
				fixture.fake.replaceProcessStatus(
					processTestFirecrackerDirFD,
					testAuthorityUID,
					testAuthorityGID,
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
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[112][0].Ino++ },
			wantErr: "root or executable",
		},
		"executable mismatch": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[113][0].Ino++ },
			wantErr: "root or executable",
		},
		"copied executable digest": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.contents[203][0] ^= 0xff },
			wantErr: "release manifest",
		},
		"mount namespace drift": {
			mutate:  func(fixture *processIdentityFixture) { fixture.fake.stats[114][0].Ino++ },
			wantErr: "identity changed",
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
				fixture.launch.state.processes.state.deps.pidfdAlive = func(context.Context, int) (bool, error) {
					calls++
					return calls == 1, nil
				}
			},
			wantErr: "Firecracker process exited",
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
	cleanup, cleanupErr := active.BeginCleanup(t.Context())
	require.ErrorIs(t, cleanupErr, fault)
	require.NotNil(t, cleanup)
	require.NoError(t, cleanup.Release(t.Context()))
}
