package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseCandidateReplacesInstalledReleaseAtomically(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	slotLease, err := authority.lockReleaseSlot(t.Context(), releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
	fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	writeDifferentInstalledReleaseForTest(t, fixture)
	oldInodes := installedReleaseInodesForTest(t, fixture.store.slotPath)
	reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
	wantNewInodes := installedReleaseInodesForTest(t, reservedPath)

	realExchange := candidate.architecture.deps.exchangeCandidate
	exchangeCalls := 0
	exchangeParentFD := -1
	exchangeSource := ""
	exchangeTarget := ""
	exchanged := false
	candidate.architecture.deps.exchangeCandidate = func(
		ctx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		exchangeCalls++
		exchangeParentFD = parentFD
		exchangeSource = source
		exchangeTarget = target
		err := realExchange(ctx, parentFD, source, target)
		exchanged = err == nil
		return err
	}
	realFsync := candidate.architecture.deps.fsync
	firstParentSyncObserved := false
	replacedBeforeParentSync := false
	candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
		if exchanged && fd == candidate.architecture.fd && !firstParentSyncObserved {
			firstParentSyncObserved = true
			replacedBeforeParentSync = candidate.state == trustedReleaseCandidateReplaced
		}
		return realFsync(ctx, fd)
	}
	architectureFD := candidate.architecture.fd

	changed, err := candidate.replaceInstalled(t.Context())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, exchangeCalls)
	assert.Equal(t, architectureFD, exchangeParentFD)
	assert.Equal(t, fixture.candidateName, exchangeSource)
	assert.Equal(t, fixture.store.slot.version, exchangeTarget)
	assert.True(t, firstParentSyncObserved)
	assert.True(t, replacedBeforeParentSync)
	assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
	assert.NoDirExists(t, reservedPath)
	if diff := cmp.Diff(wantNewInodes, installedReleaseInodesForTest(t, fixture.store.slotPath)); diff != "" {
		t.Errorf("replacement canonical release mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(oldInodes, wantNewInodes); diff == "" {
		t.Fatal("test setup did not create distinct old and new release identities")
	}

	require.NoError(t, candidate.Release(t.Context()))
	assert.DirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateReplacementRequiresExistingTarget(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	exchangeCalled := false
	candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
		exchangeCalled = true
		return nil
	}

	changed, err := candidate.replaceInstalled(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, errs.CodeBinaryNotFound, errs.AsDomainError(err).Code)
	assert.False(t, exchangeCalled)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateIdenticalReplacementIsIdempotentWithoutReferenceScan(t *testing.T) {
	t.Parallel()

	authority, authorityRoot := newTestInstanceAuthority(t)
	slotLease, err := authority.lockReleaseSlot(t.Context(), releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
	fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	copyCandidateToInstalledReleaseForTest(t, fixture, candidate)
	recordDir := filepath.Join(authorityRoot, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(recordDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(recordDir, testVMID+".json"), []byte(`{"broken":true}`), 0600))
	exchangeCalled := false
	candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
		exchangeCalled = true
		return nil
	}
	before := installedReleaseInodesForTest(t, fixture.store.slotPath)

	changed, err := candidate.replaceInstalled(t.Context())
	require.NoError(t, err)
	assert.False(t, changed)
	assert.False(t, exchangeCalled)
	if diff := cmp.Diff(before, installedReleaseInodesForTest(t, fixture.store.slotPath)); diff != "" {
		t.Errorf("idempotent replacement mutated installed release (-before +after):\n%s", diff)
	}
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateReplacementScansExactOldIdentity(t *testing.T) {
	t.Parallel()

	authority, authorityRoot := newTestInstanceAuthority(t)
	slotLease, err := authority.lockReleaseSlot(t.Context(), releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
	fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	writeDifferentInstalledReleaseForTest(t, fixture)
	record := testRegisteredInstanceRecord()
	record.release = candidate.admission.identity
	writeTestAuthorityRecord(t, authorityRoot, record)

	changed, err := candidate.replaceInstalled(t.Context())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateReferencedOldReleaseCannotBeReplaced(t *testing.T) {
	t.Parallel()

	authority, authorityRoot := newTestInstanceAuthority(t)
	slotLease, err := authority.lockReleaseSlot(t.Context(), releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
	fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	writeDifferentInstalledReleaseForTest(t, fixture)
	manifestRaw, err := os.ReadFile(filepath.Join(fixture.store.slotPath, trustedReleaseManifestLeaf))
	require.NoError(t, err)
	manifest, err := decodeTrustedReleaseManifest(manifestRaw)
	require.NoError(t, err)
	oldIdentity, err := manifest.releaseIdentity()
	require.NoError(t, err)
	record := testRegisteredInstanceRecord()
	record.release = oldIdentity
	writeTestAuthorityRecord(t, authorityRoot, record)
	exchangeCalled := false
	candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
		exchangeCalled = true
		return nil
	}

	changed, err := candidate.replaceInstalled(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMStateInvalid, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.False(t, exchangeCalled)
	assert.DirExists(t, fixture.store.slotPath)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateExchangeFailuresKeepOldReleaseCanonical(t *testing.T) {
	t.Parallel()

	tests := []error{unix.ENOSYS, unix.EOPNOTSUPP, unix.EINVAL, unix.EXDEV, unix.EIO}
	for _, cause := range tests {
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()

			authority, _ := newTestInstanceAuthority(t)
			slotLease, err := authority.lockReleaseSlot(
				t.Context(),
				releaseSlot{version: "1.16.1", architecture: "x86_64"},
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
			fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			writeDifferentInstalledReleaseForTest(t, fixture)
			before := installedReleaseInodesForTest(t, fixture.store.slotPath)
			candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
				return cause
			}

			changed, err := candidate.replaceInstalled(t.Context())
			require.Error(t, err)
			assert.False(t, changed)
			assert.ErrorIs(t, err, cause)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			if diff := cmp.Diff(before, installedReleaseInodesForTest(t, fixture.store.slotPath)); diff != "" {
				t.Errorf("failed exchange changed canonical release (-before +after):\n%s", diff)
			}
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
		})
	}
}

func TestTrustedReleaseCandidateReplacementCancellationAfterExchangeDoesNotRollBack(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	slotLease, err := authority.lockReleaseSlot(t.Context(), releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
	fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	writeDifferentInstalledReleaseForTest(t, fixture)
	ctx, cancel := context.WithCancel(t.Context())
	realExchange := candidate.architecture.deps.exchangeCandidate
	candidate.architecture.deps.exchangeCandidate = func(
		exchangeCtx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		err := realExchange(exchangeCtx, parentFD, source, target)
		cancel()
		return err
	}
	cleanupCanceled := false
	realFsync := candidate.architecture.deps.fsync
	candidate.architecture.deps.fsync = func(cleanupCtx context.Context, fd int) error {
		cleanupCanceled = cleanupCanceled || cleanupCtx.Err() != nil
		return realFsync(cleanupCtx, fd)
	}

	changed, err := candidate.replaceInstalled(ctx)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, cleanupCanceled)
	assert.DirExists(t, fixture.store.slotPath)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateReplacementPostCommitFailuresReportState(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject func(*testing.T, *trustedReleaseCandidate, string)
		want   map[string]any
	}{
		"first architecture sync retains old release": {
			inject: func(_ *testing.T, candidate *trustedReleaseCandidate, _ string) {
				realExchange := candidate.architecture.deps.exchangeCandidate
				exchanged := false
				candidate.architecture.deps.exchangeCandidate = func(
					ctx context.Context,
					parentFD int,
					source string,
					target string,
				) error {
					err := realExchange(ctx, parentFD, source, target)
					exchanged = err == nil
					return err
				}
				realFsync := candidate.architecture.deps.fsync
				candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
					if exchanged && fd == candidate.architecture.fd {
						return unix.ENOSPC
					}
					return realFsync(ctx, fd)
				}
			},
			want: map[string]any{
				"release_replaced":         true,
				"durability_uncertain":     true,
				"retired_release_retained": true,
			},
		},
		"old executable close still retires old release": {
			inject: func(_ *testing.T, candidate *trustedReleaseCandidate, _ string) {
				realExchange := candidate.architecture.deps.exchangeCandidate
				exchanged := false
				candidate.architecture.deps.exchangeCandidate = func(
					ctx context.Context,
					parentFD int,
					source string,
					target string,
				) error {
					err := realExchange(ctx, parentFD, source, target)
					exchanged = err == nil
					return err
				}
				realClose := candidate.architecture.deps.close
				failed := false
				candidate.architecture.deps.close = func(ctx context.Context, fd int) error {
					err := realClose(ctx, fd)
					if exchanged && !failed {
						failed = true
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			},
			want: map[string]any{"release_replaced": true},
		},
		"final architecture sync reports uncertain durability": {
			inject: func(_ *testing.T, candidate *trustedReleaseCandidate, _ string) {
				realExchange := candidate.architecture.deps.exchangeCandidate
				exchanged := false
				candidate.architecture.deps.exchangeCandidate = func(
					ctx context.Context,
					parentFD int,
					source string,
					target string,
				) error {
					err := realExchange(ctx, parentFD, source, target)
					exchanged = err == nil
					return err
				}
				realFsync := candidate.architecture.deps.fsync
				architectureSyncs := 0
				candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
					if exchanged && fd == candidate.architecture.fd {
						architectureSyncs++
						if architectureSyncs == 2 {
							return unix.ENOSPC
						}
					}
					return realFsync(ctx, fd)
				}
			},
			want: map[string]any{"release_replaced": true, "durability_uncertain": true},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			authority, _ := newTestInstanceAuthority(t)
			slotLease, err := authority.lockReleaseSlot(
				t.Context(),
				releaseSlot{version: "1.16.1", architecture: "x86_64"},
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, slotLease.Release(context.Background())) })
			fixture := newTrustedReleaseCandidateFixtureWithSlotLease(t, slotLease)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			writeDifferentInstalledReleaseForTest(t, fixture)
			reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
			test.inject(t, candidate, reservedPath)

			changed, err := candidate.replaceInstalled(t.Context())
			require.Error(t, err)
			assert.True(t, changed)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			if diff := cmp.Diff(test.want, domainErr.Details); diff != "" {
				t.Errorf("replacement failure details mismatch (-want +got):\n%s", diff)
			}
			if _, retained := test.want["retired_release_retained"]; retained {
				assert.DirExists(t, reservedPath)
			} else {
				assert.NoDirExists(t, reservedPath)
			}
			assert.DirExists(t, fixture.store.slotPath)
			require.NoError(t, candidate.Release(t.Context()))
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateProductionExchangeDependencyIsCancellationAware(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, exchangeTrustedReleaseCandidate(canceled, -1, "source", "target"), context.Canceled)
}
