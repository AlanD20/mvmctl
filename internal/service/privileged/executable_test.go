package privileged

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	testRootFD      = 10
	testUsrFD       = 11
	testLocalFD     = 12
	testBinFD       = 13
	testCanonicalFD = 14
	testRunningFD   = 15
)

func TestVerifySystemExecutable_AcceptsAndPinsCanonicalRootOwnedImage(t *testing.T) {
	fixture := newExecutableFixture()
	pinned, err := verifySystemExecutable(fixture.deps)
	require.NoError(t, err)
	require.NotNil(t, pinned)
	assert.Empty(t, fixture.closedFDs)

	require.NoError(t, pinned.close())
	assert.ElementsMatch(t, []int{
		testRootFD, testUsrFD, testLocalFD, testBinFD, testCanonicalFD, testRunningFD,
	}, fixture.closedFDs)
}

// Rationale: Path components must be pinned by no-follow descriptor traversal;
// checking a pathname and reopening it would leave a symlink-substitution race.
func TestVerifySystemExecutable_UsesDescriptorRelativeNoFollowTraversal(t *testing.T) {
	fixture := newExecutableFixture()
	pinned, err := verifySystemExecutable(fixture.deps)
	require.NoError(t, err)
	defer func() { require.NoError(t, pinned.close()) }()

	require.Len(t, fixture.openAtCalls, 4)
	for _, call := range fixture.openAtCalls[:3] {
		assert.NotZero(t, call.flags&unix.O_PATH)
		assert.NotZero(t, call.flags&unix.O_CLOEXEC)
		assert.NotZero(t, call.flags&unix.O_NOFOLLOW)
		assert.NotZero(t, call.flags&unix.O_DIRECTORY)
	}
	canonicalCall := fixture.openAtCalls[3]
	assert.Equal(t, testBinFD, canonicalCall.dirFD)
	assert.Equal(t, "mvm", canonicalCall.path)
	assert.NotZero(t, canonicalCall.flags&unix.O_PATH)
	assert.NotZero(t, canonicalCall.flags&unix.O_CLOEXEC)
	assert.NotZero(t, canonicalCall.flags&unix.O_NOFOLLOW)
	assert.Zero(t, canonicalCall.flags&unix.O_DIRECTORY)

	require.Len(t, fixture.openCalls, 2)
	assert.Equal(t, "/", fixture.openCalls[0].path)
	assert.NotZero(t, fixture.openCalls[0].flags&unix.O_NOFOLLOW)
	assert.Equal(t, "/proc/self/exe", fixture.openCalls[1].path)
	assert.Zero(t, fixture.openCalls[1].flags&unix.O_NOFOLLOW)
}

// Rationale: A root-owned file appearing at the canonical pathname is not
// enough; it must be the same device and inode as the image executing as root.
func TestVerifySystemExecutable_RejectsRunningImageMismatch(t *testing.T) {
	fixture := newExecutableFixture()
	stat := fixture.stats[testRunningFD]
	stat.Ino++
	fixture.stats[testRunningFD] = stat

	pinned, err := verifySystemExecutable(fixture.deps)
	require.Error(t, err)
	assert.Nil(t, pinned)
	assert.ErrorContains(t, err, "does not match the running process image")
	assert.Equal(t, errs.CodePrivilegeRequired, errs.AsDomainError(err).Code)
	assert.ElementsMatch(t, []int{
		testRootFD, testUsrFD, testLocalFD, testBinFD, testCanonicalFD, testRunningFD,
	}, fixture.closedFDs)
}

// Rationale: O_NOFOLLOW must turn a path-component swap into a closed failure,
// even if an earlier metadata observation would have described a safe directory.
func TestVerifySystemExecutable_RejectsSymlinkSwapDuringTraversal(t *testing.T) {
	fixture := newExecutableFixture()
	original := fixture.deps.openAt
	fixture.deps.openAt = func(dirFD int, path string, flags int, mode uint32) (int, error) {
		if path == "local" {
			return -1, unix.ELOOP
		}
		return original(dirFD, path, flags, mode)
	}

	pinned, err := verifySystemExecutable(fixture.deps)
	require.Error(t, err)
	assert.Nil(t, pinned)
	assert.ErrorContains(t, err, "open trusted executable path component local")
	assert.ElementsMatch(t, []int{testRootFD, testUsrFD}, fixture.closedFDs)
}

func TestVerifySystemExecutable_RejectsUnsafeDescriptorState(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*executableFixture)
		wantErr string
	}{
		"different_executable_path": {
			mutate: func(fixture *executableFixture) {
				fixture.deps.executable = func() (string, error) { return "/tmp/mvm", nil }
			},
			wantErr: "must run from " + infra.SystemBinaryPath,
		},
		"executable_lookup_error": {
			mutate: func(fixture *executableFixture) {
				fixture.deps.executable = func() (string, error) { return "", errors.New("proc unavailable") }
			},
			wantErr: "resolve running executable",
		},
		"non_root_owned_directory": {
			mutate: replaceExecutableStat(testBinFD, unix.Stat_t{
				Mode: unix.S_IFDIR | 0755, Uid: 1000, Gid: 0, Dev: 1, Ino: 4,
			}),
			wantErr: "must be owned by root",
		},
		"group_writable_directory": {
			mutate: replaceExecutableStat(testBinFD, unix.Stat_t{
				Mode: unix.S_IFDIR | 0775, Uid: 0, Gid: 0, Dev: 1, Ino: 4,
			}),
			wantErr: "must not be group/world writable",
		},
		"non_regular_executable": {
			mutate: replaceExecutableStat(testCanonicalFD, unix.Stat_t{
				Mode: unix.S_IFIFO | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5,
			}),
			wantErr: "must be a regular file",
		},
		"wrong_executable_mode": {
			mutate: replaceExecutableStat(testCanonicalFD, unix.Stat_t{
				Mode: unix.S_IFREG | 0777, Uid: 0, Gid: 0, Dev: 1, Ino: 5,
			}),
			wantErr: "must have mode 0755",
		},
		"non_root_executable_group": {
			mutate: replaceExecutableStat(testCanonicalFD, unix.Stat_t{
				Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 1000, Dev: 1, Ino: 5,
			}),
			wantErr: "must be owned by root:root",
		},
		"setuid_executable": {
			mutate: replaceExecutableStat(testCanonicalFD, unix.Stat_t{
				Mode: unix.S_IFREG | unix.S_ISUID | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5,
			}),
			wantErr: "must have mode 0755",
		},
		"missing_component": {
			mutate: func(fixture *executableFixture) {
				fixture.deps.openAt = func(int, string, int, uint32) (int, error) {
					return -1, unix.ENOENT
				}
			},
			wantErr: "open trusted executable path component",
		},
		"fstat_failure": {
			mutate: func(fixture *executableFixture) {
				fixture.deps.fstat = func(int, *unix.Stat_t) error { return unix.EIO }
			},
			wantErr: "inspect trusted executable descriptor",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newExecutableFixture()
			tc.mutate(fixture)
			pinned, err := verifySystemExecutable(fixture.deps)
			require.Error(t, err)
			assert.Nil(t, pinned)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodePrivilegeRequired, errs.AsDomainError(err).Code)
		})
	}
}

func trustedExecutableDeps() executableDeps {
	return newExecutableFixture().deps
}

func replaceExecutableStat(fd int, replacement unix.Stat_t) func(*executableFixture) {
	return func(fixture *executableFixture) {
		fixture.stats[fd] = replacement
	}
}

type executableOpenCall struct {
	dirFD int
	path  string
	flags int
}

type executableFixture struct {
	deps        executableDeps
	stats       map[int]unix.Stat_t
	openCalls   []executableOpenCall
	openAtCalls []executableOpenCall
	closedFDs   []int
}

func newExecutableFixture() *executableFixture {
	fixture := &executableFixture{
		stats: map[int]unix.Stat_t{
			testRootFD:      {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 1},
			testUsrFD:       {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 2},
			testLocalFD:     {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 100, Dev: 1, Ino: 3},
			testBinFD:       {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 4},
			testCanonicalFD: {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5},
			testRunningFD:   {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5},
		},
	}
	fixture.deps = executableDeps{
		executable: func() (string, error) { return infra.SystemBinaryPath, nil },
		open: func(path string, flags int, _ uint32) (int, error) {
			fixture.openCalls = append(fixture.openCalls, executableOpenCall{path: path, flags: flags})
			switch path {
			case "/":
				return testRootFD, nil
			case "/proc/self/exe":
				return testRunningFD, nil
			default:
				return -1, fmt.Errorf("unexpected open path %s", path)
			}
		},
		openAt: func(dirFD int, path string, flags int, _ uint32) (int, error) {
			fixture.openAtCalls = append(
				fixture.openAtCalls,
				executableOpenCall{dirFD: dirFD, path: path, flags: flags},
			)
			switch {
			case dirFD == testRootFD && path == "usr":
				return testUsrFD, nil
			case dirFD == testUsrFD && path == "local":
				return testLocalFD, nil
			case dirFD == testLocalFD && path == "bin":
				return testBinFD, nil
			case dirFD == testBinFD && path == "mvm":
				return testCanonicalFD, nil
			default:
				return -1, fmt.Errorf("unexpected openat target %d/%s", dirFD, path)
			}
		},
		fstat: func(fd int, stat *unix.Stat_t) error {
			value, ok := fixture.stats[fd]
			if !ok {
				return unix.EBADF
			}
			*stat = value
			return nil
		},
		close: func(fd int) error {
			fixture.closedFDs = append(fixture.closedFDs, fd)
			return nil
		},
	}
	return fixture
}
