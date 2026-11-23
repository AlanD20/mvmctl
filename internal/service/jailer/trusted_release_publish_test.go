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

// Rationale: the no-replace rename is the only absent-install commit point; after it succeeds, cleanup must close
// capabilities without ever unlinking the now-canonical release.
func TestTrustedReleaseCandidatePublishesAbsentReleaseAtomically(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
	wantLeaves := directoryNamesForTest(t, reservedPath)
	wantRaw, err := os.ReadFile(filepath.Join(reservedPath, trustedReleaseManifestLeaf))
	require.NoError(t, err)

	realRename := candidate.architecture.deps.renameCandidateNoReplace
	renameCalls := 0
	renameParentFD := -1
	renameSource := ""
	renameTarget := ""
	renamed := false
	candidate.architecture.deps.renameCandidateNoReplace = func(
		ctx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		renameCalls++
		renameParentFD = parentFD
		renameSource = source
		renameTarget = target
		err := realRename(ctx, parentFD, source, target)
		renamed = err == nil
		return err
	}
	realFsync := candidate.architecture.deps.fsync
	parentSyncedAfterRename := false
	installedStateBeforeParentSync := false
	candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
		if renamed && fd == candidate.architecture.fd {
			parentSyncedAfterRename = true
			installedStateBeforeParentSync = candidate.state == trustedReleaseCandidateInstalled
		}
		return realFsync(ctx, fd)
	}
	architectureFD := candidate.architecture.fd

	changed, err := candidate.publishAbsent(t.Context())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, renameCalls)
	assert.Equal(t, architectureFD, renameParentFD)
	assert.Equal(t, fixture.candidateName, renameSource)
	assert.Equal(t, fixture.store.slot.version, renameTarget)
	assert.True(t, parentSyncedAfterRename)
	assert.True(t, installedStateBeforeParentSync)
	assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
	assert.NoDirExists(t, reservedPath)
	assert.DirExists(t, fixture.store.slotPath)
	if diff := cmp.Diff(wantLeaves, directoryNamesForTest(t, fixture.store.slotPath)); diff != "" {
		t.Errorf("published trusted release leaves mismatch (-want +got):\n%s", diff)
	}
	gotRaw, err := os.ReadFile(filepath.Join(fixture.store.slotPath, trustedReleaseManifestLeaf))
	require.NoError(t, err)
	if diff := cmp.Diff(wantRaw, gotRaw); diff != "" {
		t.Errorf("published trusted release manifest mismatch (-want +got):\n%s", diff)
	}

	require.NoError(t, candidate.Release(t.Context()))
	require.NoError(t, candidate.Release(t.Context()))
	assert.DirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateIdenticalInstalledReleaseIsIdempotent(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	copyCandidateToInstalledReleaseForTest(t, fixture, candidate)
	before := installedReleaseInodesForTest(t, fixture.store.slotPath)
	renameCalled := false
	candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
		renameCalled = true
		return nil
	}

	changed, err := candidate.publishAbsent(t.Context())
	require.NoError(t, err)
	assert.False(t, changed)
	assert.False(t, renameCalled)
	assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	if diff := cmp.Diff(before, installedReleaseInodesForTest(t, fixture.store.slotPath)); diff != "" {
		t.Errorf("idempotent trusted release publication replaced installed files (-before +after):\n%s", diff)
	}
}

func TestTrustedReleaseCandidateIdempotencyRequiresFullInstalledAdmission(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseCandidateFixture){
		"missing Jailer": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			require.NoError(t, os.Remove(filepath.Join(fixture.store.slotPath, trustedReleaseJailerLeaf)))
		},
		"wrong Firecracker hash": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			path := filepath.Join(fixture.store.slotPath, trustedReleaseFirecrackerLeaf)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			raw[len(raw)-1]++
			require.NoError(t, os.WriteFile(path, raw, os.FileMode(trustedReleaseStoreExecutableMode)))
		},
		"unexpected leaf": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(fixture.store.slotPath, "unexpected"), []byte("value"), 0600))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			copyCandidateToInstalledReleaseForTest(t, fixture, candidate)
			mutate(t, fixture)
			renameCalled := false
			candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
				renameCalled = true
				return nil
			}

			changed, err := candidate.publishAbsent(t.Context())
			require.Error(t, err)
			assert.False(t, changed)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.False(t, renameCalled)
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateDifferentInstalledReleaseConflicts(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	writeDifferentInstalledReleaseForTest(t, fixture)
	renameCalled := false
	candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
		renameCalled = true
		return nil
	}

	changed, err := candidate.publishAbsent(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, errs.CodeBinaryAlreadyExists, errs.AsDomainError(err).Code)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	assert.False(t, renameCalled)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	assert.DirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateUnsafeInstalledVersionFailsClosed(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseCandidateFixture, *trustedReleaseCandidate){
		"symlink": func(t *testing.T, fixture *trustedReleaseCandidateFixture, candidate *trustedReleaseCandidate) {
			target := filepath.Join(fixture.store.architecturePath, "target")
			require.NoError(t, os.Mkdir(target, 0700))
			require.NoError(t, os.Symlink(target, fixture.store.slotPath))
		},
		"unsafe directory mode": func(
			t *testing.T,
			fixture *trustedReleaseCandidateFixture,
			candidate *trustedReleaseCandidate,
		) {
			copyCandidateToInstalledReleaseForTest(t, fixture, candidate)
			require.NoError(t, os.Chmod(fixture.store.slotPath, 0755))
		},
		"malformed manifest": func(
			t *testing.T,
			fixture *trustedReleaseCandidateFixture,
			candidate *trustedReleaseCandidate,
		) {
			copyCandidateToInstalledReleaseForTest(t, fixture, candidate)
			require.NoError(t, os.WriteFile(
				filepath.Join(fixture.store.slotPath, trustedReleaseManifestLeaf),
				[]byte("{}"),
				0600,
			))
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			arrange(t, fixture, candidate)
			renameCalled := false
			candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
				renameCalled = true
				return nil
			}

			changed, err := candidate.publishAbsent(t.Context())
			require.Error(t, err)
			assert.False(t, changed)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.False(t, renameCalled)
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
		})
	}
}

func TestTrustedReleaseCandidateRejectsInvalidPublicationAuthorityWithoutRename(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseCandidate){
		"mismatched directory slot": func(candidate *trustedReleaseCandidate) {
			candidate.directory.slot.version = "1.16.0"
		},
		"changed canonical manifest": func(candidate *trustedReleaseCandidate) {
			candidate.canonicalManifest[0]++
		},
		"inactive admission": func(candidate *trustedReleaseCandidate) {
			candidate.admission.executables.firecrackerFD = -1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			mutate(candidate)
			renameCalled := false
			candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
				renameCalled = true
				return nil
			}

			changed, err := candidate.publishAbsent(t.Context())
			require.Error(t, err)
			assert.False(t, changed)
			assert.False(t, renameCalled)
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateReleasedStateCannotPublish(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	renameCalled := false
	candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
		renameCalled = true
		return nil
	}
	require.NoError(t, candidate.Release(t.Context()))

	changed, err := candidate.publishAbsent(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	assert.False(t, renameCalled)
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateReservedNameReplacementIsNotPublishedOrRemoved(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
	detachedPath := reservedPath + ".detached"
	require.NoError(t, os.Rename(reservedPath, detachedPath))
	require.NoError(t, os.Mkdir(reservedPath, 0700))
	renameCalled := false
	candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
		renameCalled = true
		return nil
	}

	changed, err := candidate.publishAbsent(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	assert.False(t, renameCalled)
	assert.DirExists(t, reservedPath)
	assert.DirExists(t, detachedPath)
	assert.Empty(t, directoryNamesForTest(t, detachedPath))
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateNoReplaceFailuresNeverPublishPartialRelease(t *testing.T) {
	t.Parallel()

	tests := []error{unix.ENOSYS, unix.EOPNOTSUPP, unix.EINVAL, unix.EXDEV, unix.EIO}
	for _, cause := range tests {
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
				return cause
			}

			changed, err := candidate.publishAbsent(t.Context())
			require.Error(t, err)
			assert.False(t, changed)
			assert.ErrorIs(t, err, cause)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateNoReplaceCollisionDoesNotClobberTarget(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	markerPath := filepath.Join(fixture.store.slotPath, "attacker-marker")
	realRename := candidate.architecture.deps.renameCandidateNoReplace
	candidate.architecture.deps.renameCandidateNoReplace = func(
		ctx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		require.NoError(t, os.Mkdir(fixture.store.slotPath, 0700))
		require.NoError(t, os.WriteFile(markerPath, []byte("retained"), 0600))
		return realRename(ctx, parentFD, source, target)
	}

	changed, err := candidate.publishAbsent(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, unix.EEXIST)
	assert.FileExists(t, markerPath)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateCancellationImmediatelyBeforeCommitDiscardsCandidate(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	realOpenAt := candidate.architecture.deps.openAt
	candidate.architecture.deps.openAt = func(
		openCtx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, openErr := realOpenAt(openCtx, parentFD, name, flags, mode)
		if parentFD == candidate.architecture.fd && name == fixture.store.slot.version &&
			errors.Is(openErr, unix.ENOENT) {
			cancel()
		}
		return fd, openErr
	}
	renameCalled := false
	candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
		renameCalled = true
		return nil
	}

	changed, err := candidate.publishAbsent(ctx)
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, renameCalled)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidatePreCanceledPublicationNeverRenames(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	renameCalled := false
	candidate.architecture.deps.renameCandidateNoReplace = func(context.Context, int, string, string) error {
		renameCalled = true
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	changed, err := candidate.publishAbsent(ctx)
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, renameCalled)
	assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateCancellationAfterCommitCannotRollBackRelease(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	realRename := candidate.architecture.deps.renameCandidateNoReplace
	candidate.architecture.deps.renameCandidateNoReplace = func(
		renameCtx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		err := realRename(renameCtx, parentFD, source, target)
		cancel()
		return err
	}
	cleanupWasCanceled := false
	realFsync := candidate.architecture.deps.fsync
	candidate.architecture.deps.fsync = func(cleanupCtx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || cleanupCtx.Err() != nil
		return realFsync(cleanupCtx, fd)
	}

	changed, err := candidate.publishAbsent(ctx)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, cleanupWasCanceled)
	assert.DirExists(t, fixture.store.slotPath)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidatePostCommitFailuresReportInstalledState(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject              func(*trustedReleaseCandidate)
		durabilityUncertain bool
	}{
		"architecture sync": {
			inject: func(candidate *trustedReleaseCandidate) {
				realRename := candidate.architecture.deps.renameCandidateNoReplace
				renamed := false
				candidate.architecture.deps.renameCandidateNoReplace = func(
					ctx context.Context,
					parentFD int,
					source string,
					target string,
				) error {
					err := realRename(ctx, parentFD, source, target)
					renamed = err == nil
					return err
				}
				realFsync := candidate.architecture.deps.fsync
				candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
					if renamed && fd == candidate.architecture.fd {
						return unix.ENOSPC
					}
					return realFsync(ctx, fd)
				}
			},
			durabilityUncertain: true,
		},
		"admitted descriptor close": {
			inject: func(candidate *trustedReleaseCandidate) {
				realClose := candidate.admission.executables.deps.close
				candidate.admission.executables.deps.close = func(ctx context.Context, fd int) error {
					return errors.Join(realClose(ctx, fd), unix.EIO)
				}
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			test.inject(candidate)

			changed, err := candidate.publishAbsent(t.Context())
			require.Error(t, err)
			assert.True(t, changed)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["release_installed"])
			if test.durabilityUncertain {
				assert.Equal(t, true, domainErr.Details["durability_uncertain"])
			} else {
				assert.NotContains(t, domainErr.Details, "durability_uncertain")
			}
			assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
			assert.DirExists(t, fixture.store.slotPath)
			require.NoError(t, candidate.Release(t.Context()))
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateCombinedPostCommitFailuresPreservePrimaryMetadata(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	primary := errs.New(
		errs.CodeValidationFailed,
		"distinctive committed release failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("trusted-release"),
		errs.WithDetails(map[string]any{"existing_detail": true}),
	)
	realRename := candidate.architecture.deps.renameCandidateNoReplace
	renamed := false
	candidate.architecture.deps.renameCandidateNoReplace = func(
		ctx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		err := realRename(ctx, parentFD, source, target)
		renamed = err == nil
		return err
	}
	realFsync := candidate.architecture.deps.fsync
	candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
		if renamed && fd == candidate.architecture.fd {
			return primary
		}
		return realFsync(ctx, fd)
	}
	realClose := candidate.admission.executables.deps.close
	candidate.admission.executables.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	changed, err := candidate.publishAbsent(t.Context())
	require.Error(t, err)
	assert.True(t, changed)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, "trusted-release", domainErr.Entity)
	assert.Equal(t, true, domainErr.Details["existing_detail"])
	assert.Equal(t, true, domainErr.Details["release_installed"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.ErrorIs(t, err, unix.EIO)
	assert.DirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateProductionNoReplaceDependencyIsCancellationAware(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, renameTrustedReleaseCandidateNoReplace(canceled, -1, "source", "target"), context.Canceled)
}

func copyCandidateToInstalledReleaseForTest(
	t *testing.T,
	fixture *trustedReleaseCandidateFixture,
	candidate *trustedReleaseCandidate,
) {
	t.Helper()

	require.NoError(t, os.Mkdir(fixture.store.slotPath, os.FileMode(trustedReleaseStoreDirectoryMode)))
	source := filepath.Join(fixture.store.architecturePath, candidate.name)
	for _, leaf := range []string{
		trustedReleaseFirecrackerLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseManifestLeaf,
	} {
		raw, err := os.ReadFile(filepath.Join(source, leaf))
		require.NoError(t, err)
		mode := trustedReleaseStoreExecutableMode
		if leaf == trustedReleaseManifestLeaf {
			mode = trustedReleaseStoreManifestMode
		}
		require.NoError(t, os.WriteFile(filepath.Join(fixture.store.slotPath, leaf), raw, os.FileMode(mode)))
	}
}

func writeDifferentInstalledReleaseForTest(t *testing.T, fixture *trustedReleaseCandidateFixture) {
	t.Helper()

	require.NoError(t, os.Mkdir(fixture.store.slotPath, os.FileMode(trustedReleaseStoreDirectoryMode)))
	manifest := testTrustedReleaseManifest(fixture.store.slot)
	writeTrustedReleaseTestExecutable(
		t,
		filepath.Join(fixture.store.slotPath, trustedReleaseFirecrackerLeaf),
		trustedReleaseTestELF("different Firecracker"),
		&manifest.firecracker,
	)
	writeTrustedReleaseTestExecutable(
		t,
		filepath.Join(fixture.store.slotPath, trustedReleaseJailerLeaf),
		trustedReleaseTestELF("different Jailer"),
		&manifest.jailer,
	)
	raw, err := encodeTrustedReleaseManifest(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.store.slotPath, trustedReleaseManifestLeaf),
		raw,
		os.FileMode(trustedReleaseStoreManifestMode),
	))
}

func installedReleaseInodesForTest(t *testing.T, slotPath string) map[string]uint64 {
	t.Helper()

	result := make(map[string]uint64, 3)
	for _, leaf := range []string{
		trustedReleaseFirecrackerLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseManifestLeaf,
	} {
		result[leaf] = fileInode(t, filepath.Join(slotPath, leaf))
	}
	return result
}
