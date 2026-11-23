package jailer

import (
	"context"
	"crypto/sha256"
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

const trustedReleaseTestExecutableBytes = 64*1024 + 17

// Rationale: launch must use the exact objects whose metadata, complete bytes, and ELF headers were admitted. Path
// replacement after verification must not redirect either retained descriptor, and positioned reads must not alter the
// offsets later inherited by the launch boundary.
func TestTrustedReleaseDirectoryPinsVerifiedExecutables(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	directory := openTrustedReleaseTestDirectory(t, fixture.store)
	executables, err := directory.openExecutables(t.Context(), fixture.manifest)
	require.NoError(t, err)
	retainedFDs := []int{executables.firecrackerFD, executables.jailerFD}

	before := executableDescriptorInodes(t, executables)
	for _, fd := range retainedFDs {
		flags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		require.NoError(t, flagErr)
		assert.NotZero(t, flags&unix.FD_CLOEXEC)
		offset, seekErr := unix.Seek(fd, 0, unix.SEEK_CUR)
		require.NoError(t, seekErr)
		assert.Equal(t, int64(0), offset)
	}

	for _, leaf := range []string{trustedReleaseFirecrackerLeaf, trustedReleaseJailerLeaf} {
		candidatePath := filepath.Join(fixture.store.slotPath, leaf)
		require.NoError(t, os.Rename(candidatePath, candidatePath+".verified"))
		require.NoError(t, os.WriteFile(candidatePath, trustedReleaseTestELF(leaf+" replacement"), 0755))
	}
	after := executableDescriptorInodes(t, executables)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("retained executable inode mismatch (-before +after):\n%s", diff)
	}
	assert.NotEqual(t, before[0], fileInode(t, fixture.firecrackerPath))
	assert.NotEqual(t, before[1], fileInode(t, fixture.jailerPath))

	realClose := executables.deps.close
	closed := make(map[int]int, len(retainedFDs))
	executables.deps.close = func(ctx context.Context, fd int) error {
		closed[fd]++
		return realClose(ctx, fd)
	}
	require.NoError(t, executables.Release(t.Context()))
	require.NoError(t, executables.Release(t.Context()))
	wantClosed := make(map[int]int, len(retainedFDs))
	for _, fd := range retainedFDs {
		wantClosed[fd] = 1
	}
	if diff := cmp.Diff(wantClosed, closed); diff != "" {
		t.Errorf("retained executable close count mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, -1, executables.firecrackerFD)
	assert.Equal(t, -1, executables.jailerFD)
	assert.Empty(t, executables.retained)
}

// Rationale: an existing slot is a complete authority record. Either fixed leaf being absent, redirected, incorrectly
// attributed, structurally unsafe, or inconsistent with the manifest must invalidate the whole release.
func TestTrustedReleaseDirectoryRejectsUnsafeExecutableLeaves(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string, string, *trustedReleaseExecutable){
		"missing": func(t *testing.T, path, _ string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Remove(path))
		},
		"symlink": func(t *testing.T, path, other string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Symlink(other, path))
		},
		"directory": func(t *testing.T, path, _ string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0755))
		},
		"FIFO": func(t *testing.T, path, _ string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, unix.Mkfifo(path, 0755))
		},
		"wrong mode": func(t *testing.T, path, _ string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Chmod(path, 0700))
		},
		"multiple links": func(t *testing.T, path, _ string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Link(path, path+".link"))
		},
		"manifest size mismatch": func(t *testing.T, path, _ string, _ *trustedReleaseExecutable) {
			require.NoError(t, os.Truncate(path, trustedReleaseTestExecutableBytes-1))
		},
		"manifest digest mismatch": func(_ *testing.T, _, _ string, entry *trustedReleaseExecutable) {
			entry.digest[0] ^= 0xff
		},
		"invalid ELF": func(t *testing.T, path, _ string, entry *trustedReleaseExecutable) {
			raw := trustedReleaseTestELF("invalid ELF")
			raw[0] = 0
			writeTrustedReleaseTestExecutable(t, path, raw, entry)
		},
		"wrong ELF architecture": func(t *testing.T, path, _ string, entry *trustedReleaseExecutable) {
			raw := trustedReleaseTestELF("wrong architecture")
			raw[18] = 183
			writeTrustedReleaseTestExecutable(t, path, raw, entry)
		},
	}

	for _, leaf := range []string{trustedReleaseFirecrackerLeaf, trustedReleaseJailerLeaf} {
		leaf := leaf
		for name, mutate := range tests {
			mutate := mutate
			t.Run(leaf+"/"+name, func(t *testing.T) {
				t.Parallel()

				fixture := newTrustedReleaseExecutableFixture(t)
				path, other, entry := fixture.leaf(leaf)
				mutate(t, path, other, entry)
				directory := openTrustedReleaseTestDirectory(t, fixture.store)

				executables, err := directory.openExecutables(t.Context(), fixture.manifest)
				assert.Nil(t, executables)
				require.Error(t, err)
				domainErr := errs.AsDomainError(err)
				require.NotNil(t, domainErr)
				assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
			})
		}
	}
}

func TestTrustedReleaseDirectoryRejectsExecutableAuthorityMismatch(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseDirectory, *trustedReleaseManifest){
		"manifest slot": func(_ *trustedReleaseDirectory, manifest *trustedReleaseManifest) {
			manifest.slot.version = "1.16.0"
		},
		"owner UID": func(directory *trustedReleaseDirectory, _ *trustedReleaseManifest) {
			directory.policy.expectedUID++
		},
		"owner GID": func(directory *trustedReleaseDirectory, _ *trustedReleaseManifest) {
			directory.policy.expectedGID++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseExecutableFixture(t)
			directory := openTrustedReleaseTestDirectory(t, fixture.store)
			mutate(directory, &fixture.manifest)

			executables, err := directory.openExecutables(t.Context(), fixture.manifest)
			assert.Nil(t, executables)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestTrustedReleaseDirectoryHashesPartialPositionedReads(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	realPread := fixture.store.deps.pread
	readCalls := 0
	fixture.store.deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		readCalls++
		if len(value) > 7 {
			value = value[:7]
		}
		return realPread(ctx, fd, value, offset)
	}
	directory := openTrustedReleaseTestDirectory(t, fixture.store)

	executables, err := directory.openExecutables(t.Context(), fixture.manifest)
	require.NoError(t, err)
	assert.Greater(t, readCalls, 2*trustedReleaseTestExecutableBytes/7)
	for _, fd := range []int{executables.firecrackerFD, executables.jailerFD} {
		offset, seekErr := unix.Seek(fd, 0, unix.SEEK_CUR)
		require.NoError(t, seekErr)
		assert.Equal(t, int64(0), offset)
	}
	require.NoError(t, executables.Release(t.Context()))
}

func TestTrustedReleaseDirectoryRejectsExecutableMutationDuringRead(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	realPread := fixture.store.deps.pread
	mutated := false
	fixture.store.deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		count, err := realPread(ctx, fd, value, offset)
		if !mutated {
			mutated = true
			require.NoError(t, os.Chmod(fixture.firecrackerPath, 0700))
		}
		return count, err
	}
	directory := openTrustedReleaseTestDirectory(t, fixture.store)

	executables, err := directory.openExecutables(t.Context(), fixture.manifest)
	assert.Nil(t, executables)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
}

func TestTrustedReleaseDirectoryExecutableReadHonorsCancellation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	realPread := fixture.store.deps.pread
	fixture.store.deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		count, err := realPread(ctx, fd, value, offset)
		cancel()
		return count, err
	}
	directory := openTrustedReleaseTestDirectory(t, fixture.store)
	realClose := directory.deps.close
	closeCount := 0
	cleanupWasCanceled := false
	directory.deps.close = func(closeCtx context.Context, fd int) error {
		closeCount++
		cleanupWasCanceled = cleanupWasCanceled || closeCtx.Err() != nil
		return realClose(closeCtx, fd)
	}

	executables, err := directory.openExecutables(ctx, fixture.manifest)
	assert.Nil(t, executables)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 1, closeCount)
	assert.False(t, cleanupWasCanceled)
	directory.deps.close = realClose
}

func TestTrustedReleaseDirectoryRejectsInvalidExecutableReadCount(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	fixture.store.deps.pread = func(_ context.Context, _ int, value []byte, _ int64) (int, error) {
		return len(value) + 1, nil
	}
	directory := openTrustedReleaseTestDirectory(t, fixture.store)

	executables, err := directory.openExecutables(t.Context(), fixture.manifest)
	assert.Nil(t, executables)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
}

func TestTrustedReleaseDirectoryRejectsExecutableReadBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject   func(context.Context, int, []byte, int64) (int, error)
		wantCode errs.Code
		wantErr  error
	}{
		"premature EOF": {
			inject: func(_ context.Context, _ int, _ []byte, _ int64) (int, error) {
				return 0, nil
			},
			wantCode: errs.CodeBinaryUntrusted,
		},
		"I/O failure": {
			inject: func(_ context.Context, _ int, _ []byte, _ int64) (int, error) {
				return 0, unix.EIO
			},
			wantCode: errs.CodeBinaryTrustedInstallFailed,
			wantErr:  unix.EIO,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseExecutableFixture(t)
			fixture.store.deps.pread = tc.inject
			directory := openTrustedReleaseTestDirectory(t, fixture.store)

			executables, err := directory.openExecutables(t.Context(), fixture.manifest)
			assert.Nil(t, executables)
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, errs.AsDomainError(err).Code)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestTrustedReleaseDirectoryRejectsExecutableGrowthAfterInitialStat(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	realPread := fixture.store.deps.pread
	fixture.store.deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		if offset == int64(fixture.manifest.firecracker.sizeBytes) {
			value[0] = 1
			return 1, nil
		}
		return realPread(ctx, fd, value, offset)
	}
	directory := openTrustedReleaseTestDirectory(t, fixture.store)

	executables, err := directory.openExecutables(t.Context(), fixture.manifest)
	assert.Nil(t, executables)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
}

// Rationale: rejecting Jailer must unwind it and the already verified Firecracker in reverse order. A cleanup failure
// is diagnostic context and must not replace the security classification that rejected the release.
func TestTrustedReleaseDirectoryPreservesExecutableErrorWhenCleanupFails(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	require.NoError(t, os.Chmod(fixture.jailerPath, 0700))
	directory := openTrustedReleaseTestDirectory(t, fixture.store)
	realOpenAt := directory.deps.openAt
	opened := make(map[int]string)
	directory.deps.openAt = func(ctx context.Context, parentFD int, name string, flags int, mode uint32) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil {
			opened[fd] = name
		}
		return fd, err
	}
	realClose := directory.deps.close
	closed := make([]string, 0, 2)
	directory.deps.close = func(ctx context.Context, fd int) error {
		closed = append(closed, opened[fd])
		closeErr := realClose(ctx, fd)
		if opened[fd] == trustedReleaseFirecrackerLeaf {
			return errors.Join(closeErr, unix.EIO)
		}
		return closeErr
	}

	executables, err := directory.openExecutables(t.Context(), fixture.manifest)
	assert.Nil(t, executables)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.Contains(t, domainErr.Message, "close rejected trusted release executables")
	assert.ErrorIs(t, err, unix.EIO)
	if diff := cmp.Diff([]string{trustedReleaseJailerLeaf, trustedReleaseFirecrackerLeaf}, closed); diff != "" {
		t.Errorf("executable cleanup order mismatch (-want +got):\n%s", diff)
	}
	directory.deps.openAt = realOpenAt
	directory.deps.close = realClose
}

type trustedReleaseExecutableFixture struct {
	store           *trustedReleaseStoreFixture
	manifest        trustedReleaseManifest
	firecrackerPath string
	jailerPath      string
}

// newTrustedReleaseExecutableFixture creates both fixed executable leaves and synchronizes their manifest authority.
func newTrustedReleaseExecutableFixture(t *testing.T) *trustedReleaseExecutableFixture {
	t.Helper()

	store := newTrustedReleaseStoreFixture(t)
	manifest := testTrustedReleaseManifest(store.slot)
	firecrackerPath := filepath.Join(store.slotPath, trustedReleaseFirecrackerLeaf)
	jailerPath := filepath.Join(store.slotPath, trustedReleaseJailerLeaf)
	writeTrustedReleaseTestExecutable(t, firecrackerPath, trustedReleaseTestELF("firecracker"), &manifest.firecracker)
	writeTrustedReleaseTestExecutable(t, jailerPath, trustedReleaseTestELF("jailer"), &manifest.jailer)

	return &trustedReleaseExecutableFixture{
		store:           store,
		manifest:        manifest,
		firecrackerPath: firecrackerPath,
		jailerPath:      jailerPath,
	}
}

// leaf selects one fixed candidate path, its peer path, and its corresponding manifest entry for symmetric cases.
func (fixture *trustedReleaseExecutableFixture) leaf(
	name string,
) (string, string, *trustedReleaseExecutable) {
	if name == trustedReleaseFirecrackerLeaf {
		return fixture.firecrackerPath, fixture.jailerPath, &fixture.manifest.firecracker
	}
	return fixture.jailerPath, fixture.firecrackerPath, &fixture.manifest.jailer
}

// openTrustedReleaseTestDirectory pins the complete synthetic store chain and registers reverse-order test cleanup.
func openTrustedReleaseTestDirectory(
	t *testing.T,
	fixture *trustedReleaseStoreFixture,
) *trustedReleaseDirectory {
	t.Helper()

	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, directory.Release(context.Background())) })
	return directory
}

// writeTrustedReleaseTestExecutable updates one fixed file and its exact typed manifest size and digest together.
func writeTrustedReleaseTestExecutable(
	t *testing.T,
	path string,
	raw []byte,
	entry *trustedReleaseExecutable,
) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, raw, 0755))
	digest := sha256.Sum256(raw)
	entry.digest = trustedReleaseExecutableDigest(digest)
	entry.sizeBytes = uint64(len(raw))
}

// trustedReleaseTestELF returns a bounded x86_64 candidate with the independently audited test header.
func trustedReleaseTestELF(label string) []byte {
	raw := make([]byte, trustedReleaseTestExecutableBytes)
	copy(raw, auditedTrustedReleaseELFHeader())
	copy(raw[trustedReleaseELFHeaderBytes:], label)
	return raw
}

// executableDescriptorInodes captures the two retained identities without consulting their replaceable pathnames.
func executableDescriptorInodes(t *testing.T, executables *trustedReleaseExecutables) []uint64 {
	t.Helper()

	result := make([]uint64, 0, 2)
	for _, fd := range []int{executables.firecrackerFD, executables.jailerFD} {
		var stat unix.Stat_t
		require.NoError(t, unix.Fstat(fd, &stat))
		result = append(result, stat.Ino)
	}
	return result
}
