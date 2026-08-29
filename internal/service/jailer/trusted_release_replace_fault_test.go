package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseCandidateReplacementRejectsInvalidAuthorityBeforeExchange(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseCandidate) context.Context{
		"released candidate": func(t *testing.T, candidate *trustedReleaseCandidate) context.Context {
			require.NoError(t, candidate.Release(t.Context()))
			return t.Context()
		},
		"inactive release slot lease": func(t *testing.T, candidate *trustedReleaseCandidate) context.Context {
			candidate.architecture.slotLease.releaseLock.fd = -1
			return t.Context()
		},
		"pre-canceled context": func(t *testing.T, _ *trustedReleaseCandidate) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			writeDifferentInstalledReleaseForTest(t, fixture)
			exchangeCalled := false
			candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
				exchangeCalled = true
				return nil
			}
			ctx := arrange(t, candidate)

			changed, err := candidate.replaceInstalled(ctx)
			require.Error(t, err)
			assert.False(t, changed)
			assert.False(t, exchangeCalled)
			_, statErr := os.Lstat(fixture.store.slotPath)
			assert.NoError(t, statErr)
		})
	}
}

func TestTrustedReleaseCandidateReplacementRequiresFullOldReleaseAdmission(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseCandidateFixture){
		"symlinked version": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			detached := fixture.store.slotPath + ".detached"
			require.NoError(t, os.Rename(fixture.store.slotPath, detached))
			require.NoError(t, os.Symlink(detached, fixture.store.slotPath))
		},
		"unsafe version mode": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			require.NoError(t, os.Chmod(fixture.store.slotPath, 0755))
		},
		"missing Jailer": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			require.NoError(t, os.Remove(filepath.Join(fixture.store.slotPath, trustedReleaseJailerLeaf)))
		},
		"wrong Firecracker hash": func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
			name := filepath.Join(fixture.store.slotPath, trustedReleaseFirecrackerLeaf)
			raw, err := os.ReadFile(name)
			require.NoError(t, err)
			raw[len(raw)-1]++
			require.NoError(t, os.WriteFile(name, raw, os.FileMode(trustedReleaseStoreExecutableMode)))
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
			writeDifferentInstalledReleaseForTest(t, fixture)
			mutate(t, fixture)
			exchangeCalled := false
			candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
				exchangeCalled = true
				return nil
			}

			changed, err := candidate.replaceInstalled(t.Context())
			require.Error(t, err)
			assert.False(t, changed)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.False(t, exchangeCalled)
			_, statErr := os.Lstat(fixture.store.slotPath)
			assert.NoError(t, statErr)
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
		})
	}
}

func TestTrustedReleaseCandidateCancellationImmediatelyBeforeExchangeDiscardsCandidate(t *testing.T) {
	t.Parallel()

	fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
	ctx, cancel := context.WithCancel(t.Context())
	realOpenAt := candidate.architecture.deps.openAt
	versionOpens := 0
	bindingFD := -1
	candidate.architecture.deps.openAt = func(
		openCtx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(openCtx, parentFD, name, flags, mode)
		if err == nil && parentFD == candidate.architecture.fd && name == fixture.store.slot.version {
			versionOpens++
			if versionOpens == 2 {
				bindingFD = fd
			}
		}
		return fd, err
	}
	realClose := candidate.architecture.deps.close
	candidate.architecture.deps.close = func(closeCtx context.Context, fd int) error {
		err := realClose(closeCtx, fd)
		if fd == bindingFD {
			cancel()
		}
		return err
	}
	exchangeCalled := false
	candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
		exchangeCalled = true
		return nil
	}

	changed, err := candidate.replaceInstalled(ctx)
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, exchangeCalled)
	assert.DirExists(t, fixture.store.slotPath)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateCorruptReferenceAuthorityPreventsExchange(t *testing.T) {
	t.Parallel()

	fixture, candidate, _, authorityRoot := newTrustedReleaseReplacementCandidate(t)
	recordDir := filepath.Join(authorityRoot, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(recordDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(recordDir, testVMID+".json"), []byte(`{"broken":true}`), 0600))
	exchangeCalled := false
	candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
		exchangeCalled = true
		return nil
	}

	changed, err := candidate.replaceInstalled(t.Context())
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	assert.False(t, exchangeCalled)
	assert.DirExists(t, fixture.store.slotPath)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateReplacementDetectsNameBindingChanges(t *testing.T) {
	t.Parallel()

	t.Run("reserved candidate binding", func(t *testing.T) {
		t.Parallel()

		fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
		reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
		detachedPath := reservedPath + ".detached"
		require.NoError(t, os.Rename(reservedPath, detachedPath))
		require.NoError(t, os.Mkdir(reservedPath, 0700))
		exchangeCalled := false
		candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
			exchangeCalled = true
			return nil
		}

		changed, err := candidate.replaceInstalled(t.Context())
		require.Error(t, err)
		assert.False(t, changed)
		assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		assert.False(t, exchangeCalled)
		assert.DirExists(t, reservedPath)
		assert.DirExists(t, detachedPath)
		assert.Empty(t, directoryNamesForTest(t, detachedPath))
		assert.DirExists(t, fixture.store.slotPath)
	})

	t.Run("canonical target binding", func(t *testing.T) {
		t.Parallel()

		fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
		detachedPath := fixture.store.slotPath + ".detached"
		realOpenAt := candidate.architecture.deps.openAt
		versionOpens := 0
		candidate.architecture.deps.openAt = func(
			ctx context.Context,
			parentFD int,
			name string,
			flags int,
			mode uint32,
		) (int, error) {
			if parentFD == candidate.architecture.fd && name == fixture.store.slot.version {
				versionOpens++
				if versionOpens == 2 {
					require.NoError(t, os.Rename(fixture.store.slotPath, detachedPath))
					require.NoError(t, os.Mkdir(fixture.store.slotPath, 0700))
				}
			}
			return realOpenAt(ctx, parentFD, name, flags, mode)
		}
		exchangeCalled := false
		candidate.architecture.deps.exchangeCandidate = func(context.Context, int, string, string) error {
			exchangeCalled = true
			return nil
		}

		changed, err := candidate.replaceInstalled(t.Context())
		require.Error(t, err)
		assert.False(t, changed)
		assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		assert.False(t, exchangeCalled)
		assert.DirExists(t, fixture.store.slotPath)
		assert.Empty(t, directoryNamesForTest(t, fixture.store.slotPath))
		assert.DirExists(t, detachedPath)
		assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	})
}

func TestTrustedReleaseCandidateRetirementFailuresRetainRecoverableOldDirectory(t *testing.T) {
	t.Parallel()

	tests := map[string]func(
		*trustedReleaseCandidate,
		*int,
		*bool,
	){
		"manifest unlink": func(candidate *trustedReleaseCandidate, targetFD *int, exchanged *bool) {
			realUnlinkAt := candidate.architecture.deps.unlinkAt
			candidate.architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
				if *exchanged && parentFD == *targetFD && name == trustedReleaseManifestLeaf {
					return unix.EIO
				}
				return realUnlinkAt(ctx, parentFD, name, flags)
			}
		},
		"Jailer unlink": func(candidate *trustedReleaseCandidate, targetFD *int, exchanged *bool) {
			realUnlinkAt := candidate.architecture.deps.unlinkAt
			candidate.architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
				if *exchanged && parentFD == *targetFD && name == trustedReleaseJailerLeaf {
					return unix.EIO
				}
				return realUnlinkAt(ctx, parentFD, name, flags)
			}
		},
		"Firecracker unlink": func(candidate *trustedReleaseCandidate, targetFD *int, exchanged *bool) {
			realUnlinkAt := candidate.architecture.deps.unlinkAt
			candidate.architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
				if *exchanged && parentFD == *targetFD && name == trustedReleaseFirecrackerLeaf {
					return unix.EIO
				}
				return realUnlinkAt(ctx, parentFD, name, flags)
			}
		},
		"retired directory sync": func(candidate *trustedReleaseCandidate, targetFD *int, exchanged *bool) {
			realFsync := candidate.architecture.deps.fsync
			candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
				if *exchanged && fd == *targetFD {
					return unix.EIO
				}
				return realFsync(ctx, fd)
			}
		},
		"retired directory removal": func(candidate *trustedReleaseCandidate, _ *int, exchanged *bool) {
			realUnlinkAt := candidate.architecture.deps.unlinkAt
			candidate.architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
				if *exchanged && parentFD == candidate.architecture.fd && name == candidate.name &&
					flags == unix.AT_REMOVEDIR {
					return unix.EBUSY
				}
				return realUnlinkAt(ctx, parentFD, name, flags)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
			reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
			targetFD := -1
			exchanged := false
			realOpenAt := candidate.architecture.deps.openAt
			candidate.architecture.deps.openAt = func(
				ctx context.Context,
				parentFD int,
				leaf string,
				flags int,
				mode uint32,
			) (int, error) {
				fd, err := realOpenAt(ctx, parentFD, leaf, flags, mode)
				if err == nil && parentFD == candidate.architecture.fd && leaf == fixture.store.slot.version &&
					targetFD < 0 {
					targetFD = fd
				}
				return fd, err
			}
			realExchange := candidate.architecture.deps.exchangeCandidate
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
			inject(candidate, &targetFD, &exchanged)

			changed, err := candidate.replaceInstalled(t.Context())
			require.Error(t, err)
			assert.True(t, changed)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["release_replaced"])
			assert.Equal(t, true, domainErr.Details["retired_release_retained"])
			assert.NotContains(t, domainErr.Details, "durability_uncertain")
			assert.DirExists(t, fixture.store.slotPath)
			assert.DirExists(t, reservedPath)
		})
	}
}

func TestTrustedReleaseCandidateReplacementCloseFailuresNeverUnlinkCanonicalRelease(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseCandidate, *int, *bool){
		"retired directory descriptor": func(candidate *trustedReleaseCandidate, targetFD *int, exchanged *bool) {
			realClose := candidate.architecture.deps.close
			candidate.architecture.deps.close = func(ctx context.Context, fd int) error {
				err := realClose(ctx, fd)
				if *exchanged && fd == *targetFD {
					return errors.Join(err, unix.EIO)
				}
				return err
			}
		},
		"new canonical executable descriptor": func(candidate *trustedReleaseCandidate, _ *int, _ *bool) {
			realClose := candidate.admission.executables.deps.close
			failed := false
			candidate.admission.executables.deps.close = func(ctx context.Context, fd int) error {
				err := realClose(ctx, fd)
				if !failed {
					failed = true
					return errors.Join(err, unix.EIO)
				}
				return err
			}
		},
		"new canonical architecture descriptor": func(candidate *trustedReleaseCandidate, _ *int, exchanged *bool) {
			realClose := candidate.architecture.deps.close
			candidate.architecture.deps.close = func(ctx context.Context, fd int) error {
				err := realClose(ctx, fd)
				if *exchanged && fd == candidate.architecture.fd {
					return errors.Join(err, unix.EIO)
				}
				return err
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
			reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
			targetFD := -1
			exchanged := false
			realOpenAt := candidate.architecture.deps.openAt
			candidate.architecture.deps.openAt = func(
				ctx context.Context,
				parentFD int,
				leaf string,
				flags int,
				mode uint32,
			) (int, error) {
				fd, err := realOpenAt(ctx, parentFD, leaf, flags, mode)
				if err == nil && parentFD == candidate.architecture.fd && leaf == fixture.store.slot.version &&
					targetFD < 0 {
					targetFD = fd
				}
				return fd, err
			}
			realExchange := candidate.architecture.deps.exchangeCandidate
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
			inject(candidate, &targetFD, &exchanged)

			changed, err := candidate.replaceInstalled(t.Context())
			require.Error(t, err)
			assert.True(t, changed)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["release_replaced"])
			assert.NotContains(t, domainErr.Details, "retired_release_retained")
			assert.NotContains(t, domainErr.Details, "durability_uncertain")
			assert.NoDirExists(t, reservedPath)
			assert.DirExists(t, fixture.store.slotPath)
			require.NoError(t, candidate.Release(t.Context()))
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateRetirementBindingMismatchNeverRemovesReplacementDirectory(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"before retired leaf cleanup":      false,
		"before retired directory removal": true,
	}
	for name, mutateAfterRetiredSync := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
			reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
			detachedPath := reservedPath + ".detached"
			markerPath := filepath.Join(reservedPath, "unrelated")
			targetFD := -1
			exchanged := false
			mutated := false
			realOpenAt := candidate.architecture.deps.openAt
			candidate.architecture.deps.openAt = func(
				ctx context.Context,
				parentFD int,
				leaf string,
				flags int,
				mode uint32,
			) (int, error) {
				fd, err := realOpenAt(ctx, parentFD, leaf, flags, mode)
				if err == nil && parentFD == candidate.architecture.fd && leaf == fixture.store.slot.version &&
					targetFD < 0 {
					targetFD = fd
				}
				return fd, err
			}
			realExchange := candidate.architecture.deps.exchangeCandidate
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
				err := realFsync(ctx, fd)
				shouldMutate := exchanged && !mutated &&
					((mutateAfterRetiredSync && fd == targetFD) ||
						(!mutateAfterRetiredSync && fd == candidate.architecture.fd))
				if err == nil && shouldMutate {
					mutated = true
					require.NoError(t, os.Rename(reservedPath, detachedPath))
					require.NoError(t, os.Mkdir(reservedPath, 0700))
					require.NoError(t, os.WriteFile(markerPath, []byte("unrelated"), 0600))
				}
				return err
			}

			changed, err := candidate.replaceInstalled(t.Context())
			require.Error(t, err)
			assert.True(t, changed)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, true, errs.AsDomainError(err).Details["release_replaced"])
			assert.Equal(t, true, errs.AsDomainError(err).Details["retired_release_retained"])
			assert.FileExists(t, markerPath)
			assert.DirExists(t, detachedPath)
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateRetainedOldReleaseIsRecoveredByExistingRecoveryPath(t *testing.T) {
	t.Parallel()

	fixture, candidate, slotLease, _ := newTrustedReleaseReplacementCandidate(t)
	reservedPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
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
	realUnlinkAt := candidate.architecture.deps.unlinkAt
	candidate.architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if exchanged && parentFD == candidate.architecture.fd && name == candidate.name &&
			flags == unix.AT_REMOVEDIR {
			return unix.EBUSY
		}
		return realUnlinkAt(ctx, parentFD, name, flags)
	}

	changed, err := candidate.replaceInstalled(t.Context())
	require.Error(t, err)
	assert.True(t, changed)
	assert.DirExists(t, reservedPath)
	assert.Empty(t, directoryNamesForTest(t, reservedPath))

	architecture, err := fixture.writer.openArchitectureForWrite(t.Context(), slotLease)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, architecture.Release(context.Background())) })
	require.NoError(t, architecture.recoverCandidates(t.Context()))
	assert.NoDirExists(t, reservedPath)
	assert.DirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateReplacementCombinedErrorsPreservePrimaryMetadata(t *testing.T) {
	t.Parallel()

	fixture, candidate, _, _ := newTrustedReleaseReplacementCandidate(t)
	primary := errs.New(
		errs.CodeValidationFailed,
		"distinctive committed replacement failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("trusted-release"),
		errs.WithDetails(map[string]any{"existing_detail": true}),
	)
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
			return primary
		}
		return realFsync(ctx, fd)
	}
	realClose := candidate.architecture.deps.close
	closeFailed := false
	candidate.architecture.deps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if exchanged && !closeFailed {
			closeFailed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}

	changed, err := candidate.replaceInstalled(t.Context())
	require.Error(t, err)
	assert.True(t, changed)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, "trusted-release", domainErr.Entity)
	assert.Equal(t, true, domainErr.Details["existing_detail"])
	assert.Equal(t, true, domainErr.Details["release_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.Equal(t, true, domainErr.Details["retired_release_retained"])
	assert.ErrorIs(t, err, unix.EIO)
	assert.DirExists(t, fixture.store.slotPath)
	assert.DirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

// newTrustedReleaseReplacementCandidate creates distinct old and new complete releases under one real active slot
// lease. The caller receives the external lease because candidate cleanup deliberately borrows rather than releases it.
func newTrustedReleaseReplacementCandidate(
	t *testing.T,
) (*trustedReleaseCandidateFixture, *trustedReleaseCandidate, *releaseSlotLease, string) {
	t.Helper()

	authority, authorityRoot := newTestInstanceAuthority(t)
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
	return fixture, candidate, slotLease, authorityRoot
}
