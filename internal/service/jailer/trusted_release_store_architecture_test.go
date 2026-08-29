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

func TestOpenTrustedReleaseArchitectureForWriteCreatesAndPinsFixedDirectory(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.architecturePath))
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })

	slotLease := trustedReleaseSlotLeaseForWriteTest(fixture.slot)
	architecture, err := writer.openArchitectureForWrite(t.Context(), slotLease)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, architecture.Release(context.Background())) })
	assertExactMode(t, fixture.architecturePath, 0700)
	assert.NoDirExists(t, fixture.slotPath)
	var pinned unix.Stat_t
	require.NoError(t, unix.Fstat(architecture.fd, &pinned))
	assert.Equal(t, fileInode(t, fixture.architecturePath), pinned.Ino)
	if diff := cmp.Diff(fixture.slot, architecture.slot, cmp.AllowUnexported(releaseSlot{})); diff != "" {
		t.Errorf("trusted release architecture slot mismatch (-want +got):\n%s", diff)
	}

	require.NoError(t, architecture.Release(t.Context()))
	assert.DirExists(t, fixture.architecturePath)
	require.NoError(t, architecture.Release(t.Context()))
}

func TestOpenTrustedReleaseArchitectureForWriteRejectsUnsafeOrInvalidInput(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*testing.T, *trustedReleaseStoreFixture)
		slot   func(releaseSlot) releaseSlot
		code   errs.Code
	}{
		"invalid slot": {
			slot: func(slot releaseSlot) releaseSlot {
				slot.architecture = "../x86_64"
				return slot
			},
			code: errs.CodeValidationFailed,
		},
		"architecture symlink": {
			mutate: func(t *testing.T, fixture *trustedReleaseStoreFixture) {
				require.NoError(t, os.RemoveAll(fixture.architecturePath))
				target := filepath.Join(fixture.root, "attacker")
				require.NoError(t, os.Mkdir(target, 0700))
				require.NoError(t, os.Symlink(target, fixture.architecturePath))
			},
			code: errs.CodeBinaryUntrusted,
		},
		"architecture wrong mode": {
			mutate: func(t *testing.T, fixture *trustedReleaseStoreFixture) {
				require.NoError(t, os.Chmod(fixture.architecturePath, 0755))
			},
			code: errs.CodeBinaryUntrusted,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			if test.mutate != nil {
				test.mutate(t, fixture)
			}
			writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
			slot := fixture.slot
			if test.slot != nil {
				slot = test.slot(slot)
			}

			architecture, err := writer.openArchitectureForWrite(
				t.Context(),
				trustedReleaseSlotLeaseForWriteTest(slot),
			)
			require.Error(t, err)
			assert.Nil(t, architecture)
			assert.Equal(t, test.code, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenTrustedReleaseArchitectureForWriteRequiresActiveSlotLeaseBeforeMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]func(releaseSlot) *releaseSlotLease{
		"nil": func(releaseSlot) *releaseSlotLease { return nil },
		"missing roots": func(slot releaseSlot) *releaseSlotLease {
			lease := trustedReleaseSlotLeaseForWriteTest(slot)
			lease.roots = nil
			return lease
		},
		"missing lock": func(slot releaseSlot) *releaseSlotLease {
			lease := trustedReleaseSlotLeaseForWriteTest(slot)
			lease.releaseLock = nil
			return lease
		},
		"released lock": func(slot releaseSlot) *releaseSlotLease {
			lease := trustedReleaseSlotLeaseForWriteTest(slot)
			lease.releaseLock.fd = -1
			return lease
		},
	}
	for name, makeLease := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			require.NoError(t, os.RemoveAll(fixture.architecturePath))
			writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })

			architecture, err := writer.openArchitectureForWrite(t.Context(), makeLease(fixture.slot))
			require.Error(t, err)
			assert.Nil(t, architecture)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.NoDirExists(t, fixture.architecturePath)
		})
	}
}

func TestOpenTrustedReleaseArchitectureForWriteSyncsCreatedChildAndParent(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.architecturePath))
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	fdNames := map[int]string{writer.store.binariesFD: "binaries"}
	realOpenAt := writer.store.deps.openAt
	writer.store.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, openErr := realOpenAt(ctx, parentFD, name, flags, mode)
		if openErr == nil {
			fdNames[fd] = name
		}
		return fd, openErr
	}
	realFsync := writer.store.deps.fsync
	var synced []string
	writer.store.deps.fsync = func(ctx context.Context, fd int) error {
		synced = append(synced, fdNames[fd])
		return realFsync(ctx, fd)
	}

	architecture, err := writer.openArchitectureForWrite(
		t.Context(),
		trustedReleaseSlotLeaseForWriteTest(fixture.slot),
	)
	require.NoError(t, err)
	require.NoError(t, architecture.Release(t.Context()))
	if diff := cmp.Diff([]string{"x86_64", "binaries"}, synced); diff != "" {
		t.Errorf("trusted release architecture creation sync order mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseArchitectureReleaseClosesOnceWithoutCallerCancellation(t *testing.T) {
	t.Parallel()

	_, architecture := newTrustedReleaseArchitectureFixture(t)
	realClose := architecture.deps.close
	closeCalls := 0
	cleanupWasCanceled := false
	architecture.deps.close = func(ctx context.Context, fd int) error {
		closeCalls++
		cleanupWasCanceled = cleanupWasCanceled || ctx.Err() != nil
		return realClose(ctx, fd)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, architecture.Release(ctx))
	assert.Equal(t, 1, closeCalls)
	assert.False(t, cleanupWasCanceled)
	assert.Equal(t, -1, architecture.fd)
	require.NoError(t, architecture.Release(t.Context()))
	assert.Equal(t, 1, closeCalls)
}

// Rationale: crash recovery may touch only this slot's reserved candidate namespace and the three fixed admitted
// leaves. Official versions, other slots' candidates, and arbitrary architecture entries are never cleanup targets.
func TestTrustedReleaseArchitectureRecoversOnlySlotCandidates(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	otherSlot := releaseSlot{version: "1.16.0", architecture: fixture.slot.architecture}
	otherPrefix, err := trustedReleaseCandidatePrefix(otherSlot)
	require.NoError(t, err)
	names := []string{
		prefix + "00000000000000000000000000000000.tmp",
		prefix + "11111111111111111111111111111111.tmp",
		prefix + "22222222222222222222222222222222.tmp",
	}
	writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, names[0], nil)
	writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, names[1], []string{
		trustedReleaseFirecrackerLeaf,
	})
	writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, names[2], []string{
		trustedReleaseFirecrackerLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseManifestLeaf,
	})
	otherCandidate := otherPrefix + "33333333333333333333333333333333.tmp"
	writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, otherCandidate, nil)
	official := filepath.Join(fixture.architecturePath, "1.15.1")
	require.NoError(t, os.Mkdir(official, 0700))

	realUnlinkAt := architecture.deps.unlinkAt
	var removed []string
	architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		removed = append(removed, name)
		return realUnlinkAt(ctx, parentFD, name, flags)
	}
	realOpenAt := architecture.deps.openAt
	var leafFlags []int
	architecture.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		if name == trustedReleaseFirecrackerLeaf || name == trustedReleaseJailerLeaf ||
			name == trustedReleaseManifestLeaf {
			leafFlags = append(leafFlags, flags)
		}
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}
	require.NoError(t, architecture.recoverCandidates(t.Context()))

	for _, name := range names {
		assert.NoDirExists(t, filepath.Join(fixture.architecturePath, name))
	}
	assert.DirExists(t, filepath.Join(fixture.architecturePath, otherCandidate))
	assert.DirExists(t, official)
	wantRemoved := []string{
		names[0],
		trustedReleaseFirecrackerLeaf,
		names[1],
		trustedReleaseManifestLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseFirecrackerLeaf,
		names[2],
	}
	if diff := cmp.Diff(wantRemoved, removed); diff != "" {
		t.Errorf("trusted release recovery removals mismatch (-want +got):\n%s", diff)
	}
	for _, flags := range leafFlags {
		assert.NotZero(t, flags&unix.O_PATH)
		assert.NotZero(t, flags&unix.O_NOFOLLOW)
		assert.NotZero(t, flags&unix.O_CLOEXEC)
		assert.Zero(t, flags&(unix.O_RDONLY|unix.O_RDWR|unix.O_WRONLY))
	}
}

func TestTrustedReleaseArchitectureRecoveryAdmitsInclusiveLeafSizeBounds(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		leaf string
		size int64
	}{
		"executable minimum": {
			leaf: trustedReleaseFirecrackerLeaf,
			size: int64(trustedReleaseExecutableMinBytes),
		},
		"executable maximum": {
			leaf: trustedReleaseFirecrackerLeaf,
			size: int64(trustedReleaseExecutableMaxBytes),
		},
		"manifest minimum": {
			leaf: trustedReleaseManifestLeaf,
			size: 1,
		},
		"manifest maximum": {
			leaf: trustedReleaseManifestLeaf,
			size: maxTrustedReleaseManifestBytes,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, architecture := newTrustedReleaseArchitectureFixture(t)
			prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
			require.NoError(t, err)
			candidateName := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, candidateName, []string{
				test.leaf,
			})
			require.NoError(t, os.Truncate(filepath.Join(candidate, test.leaf), test.size))

			require.NoError(t, architecture.recoverCandidates(t.Context()))
			assert.NoDirExists(t, candidate)
		})
	}
}

func TestTrustedReleaseArchitectureRecoveryRejectsUnsafeCandidate(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string, string){
		"malformed reserved name": func(t *testing.T, architecturePath, prefix string) {
			require.NoError(t, os.Mkdir(filepath.Join(architecturePath, prefix+"bad.tmp"), 0700))
		},
		"reserved name missing suffix": func(t *testing.T, architecturePath, prefix string) {
			require.NoError(t, os.Mkdir(
				filepath.Join(architecturePath, prefix+"00000000000000000000000000000000"),
				0700,
			))
		},
		"reserved name uppercase nonce": func(t *testing.T, architecturePath, prefix string) {
			require.NoError(t, os.Mkdir(
				filepath.Join(architecturePath, prefix+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA.tmp"),
				0700,
			))
		},
		"reserved name short nonce": func(t *testing.T, architecturePath, prefix string) {
			require.NoError(t, os.Mkdir(
				filepath.Join(architecturePath, prefix+"0000000000000000000000000000000.tmp"),
				0700,
			))
		},
		"reserved name long nonce": func(t *testing.T, architecturePath, prefix string) {
			require.NoError(t, os.Mkdir(
				filepath.Join(architecturePath, prefix+"000000000000000000000000000000000.tmp"),
				0700,
			))
		},
		"candidate symlink": func(t *testing.T, architecturePath, prefix string) {
			target := filepath.Join(architecturePath, "target")
			require.NoError(t, os.Mkdir(target, 0700))
			require.NoError(t, os.Symlink(target, filepath.Join(
				architecturePath,
				prefix+"00000000000000000000000000000000.tmp",
			)))
		},
		"candidate wrong mode": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			require.NoError(t, os.Mkdir(filepath.Join(architecturePath, name), 0755))
		},
		"unexpected leaf": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, nil)
			require.NoError(t, os.WriteFile(filepath.Join(candidate, "attacker"), []byte("value"), 0600))
		},
		"leaf symlink": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, nil)
			require.NoError(t, os.Symlink("target", filepath.Join(candidate, trustedReleaseManifestLeaf)))
		},
		"leaf directory": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, nil)
			require.NoError(t, os.Mkdir(filepath.Join(candidate, trustedReleaseManifestLeaf), 0600))
		},
		"leaf FIFO": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, nil)
			require.NoError(t, unix.Mkfifo(filepath.Join(candidate, trustedReleaseManifestLeaf), 0600))
		},
		"leaf wrong mode": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, []string{
				trustedReleaseManifestLeaf,
			})
			require.NoError(t, os.Chmod(filepath.Join(candidate, trustedReleaseManifestLeaf), 0644))
		},
		"leaf multiple links": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, []string{
				trustedReleaseFirecrackerLeaf,
			})
			require.NoError(t, os.Link(
				filepath.Join(candidate, trustedReleaseFirecrackerLeaf),
				filepath.Join(architecturePath, "attacker-link"),
			))
		},
		"executable below size bound": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, []string{
				trustedReleaseFirecrackerLeaf,
			})
			require.NoError(t, os.WriteFile(filepath.Join(candidate, trustedReleaseFirecrackerLeaf), []byte{1}, 0755))
		},
		"executable above size bound": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, []string{
				trustedReleaseFirecrackerLeaf,
			})
			require.NoError(t, os.Truncate(
				filepath.Join(candidate, trustedReleaseFirecrackerLeaf),
				int64(trustedReleaseExecutableMaxBytes+1),
			))
		},
		"manifest below size bound": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, []string{
				trustedReleaseManifestLeaf,
			})
			require.NoError(t, os.Truncate(filepath.Join(candidate, trustedReleaseManifestLeaf), 0))
		},
		"manifest above size bound": func(t *testing.T, architecturePath, prefix string) {
			name := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, architecturePath, name, []string{
				trustedReleaseManifestLeaf,
			})
			require.NoError(t, os.Truncate(
				filepath.Join(candidate, trustedReleaseManifestLeaf),
				maxTrustedReleaseManifestBytes+1,
			))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, architecture := newTrustedReleaseArchitectureFixture(t)
			prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
			require.NoError(t, err)
			mutate(t, fixture.architecturePath, prefix)
			before := directoryNamesForTest(t, fixture.architecturePath)

			err = architecture.recoverCandidates(t.Context())
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.architecturePath)); diff != "" {
				t.Errorf("rejected recovery changed architecture entries (-before +after):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseArchitectureRecoveryRejectsWrongLeafOwnership(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Stat_t){
		"owner UID": func(stat *unix.Stat_t) { stat.Uid++ },
		"owner GID": func(stat *unix.Stat_t) { stat.Gid++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, architecture := newTrustedReleaseArchitectureFixture(t)
			prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
			require.NoError(t, err)
			candidateName := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, candidateName, []string{
				trustedReleaseManifestLeaf,
			})
			realFstat := architecture.deps.fstat
			architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if stat.Mode&unix.S_IFMT == unix.S_IFREG {
					mutate(stat)
				}
				return nil
			}

			err = architecture.recoverCandidates(t.Context())
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.DirExists(t, candidate)
			assert.FileExists(t, filepath.Join(candidate, trustedReleaseManifestLeaf))
		})
	}
}

func TestTrustedReleaseArchitectureRecoveryRequiresSameActiveSlotLease(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseArchitectureWriteLease){
		"missing lease": func(architecture *trustedReleaseArchitectureWriteLease) {
			architecture.slotLease = nil
		},
		"released lease": func(architecture *trustedReleaseArchitectureWriteLease) {
			architecture.slotLease.releaseLock.fd = -1
		},
		"mismatched lease": func(architecture *trustedReleaseArchitectureWriteLease) {
			architecture.slotLease.slot.version = "1.16.0"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, architecture := newTrustedReleaseArchitectureFixture(t)
			prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
			require.NoError(t, err)
			candidateName := prefix + "00000000000000000000000000000000.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, candidateName, nil)
			mutate(architecture)

			err = architecture.recoverCandidates(t.Context())
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.DirExists(t, candidate)
		})
	}
}

func TestTrustedReleaseArchitectureRecoveryFinishesStartedCleanupWithoutCancellation(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name := prefix + "00000000000000000000000000000000.tmp"
	writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, name, []string{
		trustedReleaseFirecrackerLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseManifestLeaf,
	})
	ctx, cancel := context.WithCancel(t.Context())
	realUnlinkAt := architecture.deps.unlinkAt
	cleanupWasCanceled := false
	architecture.deps.unlinkAt = func(cleanupCtx context.Context, parentFD int, leaf string, flags int) error {
		cleanupWasCanceled = cleanupWasCanceled || cleanupCtx.Err() != nil
		cancel()
		return realUnlinkAt(cleanupCtx, parentFD, leaf, flags)
	}

	require.NoError(t, architecture.recoverCandidates(ctx))
	assert.False(t, cleanupWasCanceled)
	assert.NoDirExists(t, filepath.Join(fixture.architecturePath, name))
}

func TestTrustedReleaseArchitectureRecoveryHonorsCancellationBeforeFirstUnlink(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name := prefix + "00000000000000000000000000000000.tmp"
	candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, name, nil)
	ctx, cancel := context.WithCancel(t.Context())
	realReadDirNames := architecture.deps.readDirNames
	readCalls := 0
	architecture.deps.readDirNames = func(readCtx context.Context, fd int) ([]string, error) {
		readCalls++
		names, readErr := realReadDirNames(readCtx, fd)
		if readCalls == 2 {
			cancel()
		}
		return names, readErr
	}
	unlinkCalled := false
	fsyncCalled := false
	architecture.deps.unlinkAt = func(context.Context, int, string, int) error {
		unlinkCalled = true
		return unix.EIO
	}
	architecture.deps.fsync = func(context.Context, int) error {
		fsyncCalled = true
		return unix.EIO
	}

	err = architecture.recoverCandidates(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, unlinkCalled)
	assert.False(t, fsyncCalled)
	assert.DirExists(t, candidate)
}

func TestTrustedReleaseArchitectureRecoveryAdmitsAllCandidatesBeforeFirstUnlink(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	validName := prefix + "00000000000000000000000000000000.tmp"
	unsafeName := prefix + "11111111111111111111111111111111.tmp"
	valid := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, validName, []string{
		trustedReleaseManifestLeaf,
	})
	unsafe := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, unsafeName, nil)
	require.NoError(t, os.WriteFile(filepath.Join(unsafe, "unexpected"), []byte("value"), 0600))
	unlinks := 0
	architecture.deps.unlinkAt = func(context.Context, int, string, int) error {
		unlinks++
		return unix.EIO
	}

	err = architecture.recoverCandidates(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	assert.Zero(t, unlinks)
	assert.DirExists(t, valid)
	assert.FileExists(t, filepath.Join(valid, trustedReleaseManifestLeaf))
	assert.DirExists(t, unsafe)
	assert.FileExists(t, filepath.Join(unsafe, "unexpected"))
}

func TestTrustedReleaseArchitectureRecoverySyncsEachNamespaceMutationInOrder(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name := prefix + "00000000000000000000000000000000.tmp"
	writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, name, []string{
		trustedReleaseManifestLeaf,
	})
	realUnlinkAt := architecture.deps.unlinkAt
	realFsync := architecture.deps.fsync
	var candidateFD int
	var events []string
	architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, leaf string, flags int) error {
		switch {
		case flags == 0:
			candidateFD = parentFD
			events = append(events, "unlink "+leaf)
		case parentFD == architecture.fd && leaf == name && flags == unix.AT_REMOVEDIR:
			events = append(events, "remove candidate")
		default:
			t.Fatalf("unexpected recovery unlink parent=%d leaf=%q flags=%d", parentFD, leaf, flags)
		}
		return realUnlinkAt(ctx, parentFD, leaf, flags)
	}
	architecture.deps.fsync = func(ctx context.Context, fd int) error {
		switch fd {
		case candidateFD:
			events = append(events, "sync candidate")
		case architecture.fd:
			events = append(events, "sync architecture")
		default:
			t.Fatalf("unexpected recovery fsync fd=%d", fd)
		}
		return realFsync(ctx, fd)
	}

	require.NoError(t, architecture.recoverCandidates(t.Context()))
	want := []string{
		"unlink " + trustedReleaseManifestLeaf,
		"sync candidate",
		"remove candidate",
		"sync architecture",
		"sync architecture",
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("trusted release recovery durability order mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseArchitectureRecoveryResumesAfterCandidateSyncFailure(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name := prefix + "00000000000000000000000000000000.tmp"
	candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, name, []string{
		trustedReleaseManifestLeaf,
	})
	realFsync := architecture.deps.fsync
	fsyncCalls := 0
	architecture.deps.fsync = func(context.Context, int) error {
		fsyncCalls++
		return unix.EIO
	}

	err = architecture.recoverCandidates(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EIO)
	assert.Equal(t, 1, fsyncCalls)
	assert.DirExists(t, candidate)
	assert.NoFileExists(t, filepath.Join(candidate, trustedReleaseManifestLeaf))

	architecture.deps.fsync = realFsync
	require.NoError(t, architecture.recoverCandidates(t.Context()))
	assert.NoDirExists(t, candidate)
}

func TestTrustedReleaseArchitectureRecoveryResumesAfterPartialFixedLeafCleanup(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name := prefix + "00000000000000000000000000000000.tmp"
	candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, name, []string{
		trustedReleaseFirecrackerLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseManifestLeaf,
	})
	realUnlinkAt := architecture.deps.unlinkAt
	unlinks := 0
	architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, leaf string, flags int) error {
		unlinks++
		if unlinks == 2 {
			return unix.EIO
		}
		return realUnlinkAt(ctx, parentFD, leaf, flags)
	}

	err = architecture.recoverCandidates(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EIO)
	assert.DirExists(t, candidate)
	assert.NoFileExists(t, filepath.Join(candidate, trustedReleaseManifestLeaf))
	assert.FileExists(t, filepath.Join(candidate, trustedReleaseJailerLeaf))
	assert.FileExists(t, filepath.Join(candidate, trustedReleaseFirecrackerLeaf))

	architecture.deps.unlinkAt = realUnlinkAt
	require.NoError(t, architecture.recoverCandidates(t.Context()))
	assert.NoDirExists(t, candidate)
}

func TestTrustedReleaseArchitectureRecoveryPreservesPrimaryErrorOnCloseFailure(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name := prefix + "00000000000000000000000000000000.tmp"
	candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, name, nil)
	require.NoError(t, os.WriteFile(filepath.Join(candidate, "unexpected"), []byte("value"), 0600))
	realClose := architecture.deps.close
	architecture.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	err = architecture.recoverCandidates(t.Context())
	architecture.deps.close = realClose
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.ErrorIs(t, err, unix.EIO)
}

func TestAppendTrustedReleaseStoreErrorPreservesPrimaryDomainMetadata(t *testing.T) {
	t.Parallel()

	primary := errs.New(
		errs.CodeValidationFailed,
		"distinctive recovery failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("trusted-release-candidate"),
		errs.WithDetails(map[string]any{"candidate_retained": true}),
	)

	combined := appendTrustedReleaseStoreError(primary, "close candidate directory", unix.EIO)
	domainErr := errs.AsDomainError(combined)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, "trusted-release-candidate", domainErr.Entity)
	if diff := cmp.Diff(map[string]any{"candidate_retained": true}, domainErr.Details); diff != "" {
		t.Errorf("cleanup error changed primary details (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, combined, unix.EIO)
}

func newTrustedReleaseArchitectureFixture(
	t *testing.T,
) (*trustedReleaseStoreFixture, *trustedReleaseArchitectureWriteLease) {
	t.Helper()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	architecture, err := writer.openArchitectureForWrite(
		t.Context(),
		trustedReleaseSlotLeaseForWriteTest(fixture.slot),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, architecture.Release(context.Background())) })
	return fixture, architecture
}

func writeTrustedReleaseRecoveryCandidate(
	t *testing.T,
	architecturePath string,
	name string,
	leaves []string,
) string {
	t.Helper()

	candidate := filepath.Join(architecturePath, name)
	require.NoError(t, os.Mkdir(candidate, 0700))
	for _, leaf := range leaves {
		switch leaf {
		case trustedReleaseFirecrackerLeaf, trustedReleaseJailerLeaf:
			require.NoError(t, os.WriteFile(filepath.Join(candidate, leaf), trustedReleaseTestELF(leaf), 0755))
		case trustedReleaseManifestLeaf:
			require.NoError(t, os.WriteFile(filepath.Join(candidate, leaf), []byte("{}"), 0600))
		default:
			t.Fatalf("unsupported trusted release recovery leaf %q", leaf)
		}
	}
	return candidate
}

func trustedReleaseSlotLeaseForWriteTest(slot releaseSlot) *releaseSlotLease {
	return &releaseSlotLease{
		roots:       &instanceAuthorityRoots{},
		releaseLock: &authorityLock{fd: 123},
		slot:        slot,
	}
}
