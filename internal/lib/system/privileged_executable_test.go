package system

import (
	"context"
	"crypto/sha256"
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
	testPinnedRootFD      = 210
	testPinnedUsrFD       = 211
	testPinnedLocalFD     = 212
	testPinnedBinFD       = 213
	testPinnedCanonicalFD = 214
	testPinnedRunningFD   = 215
)

func TestPinSystemExecutableRetainsExactRunningImageAndDigest(t *testing.T) {
	t.Parallel()

	fixture := newPinnedExecutableFixture()
	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	require.NoError(t, err)

	identity, err := capability.Identity(t.Context())
	require.NoError(t, err)
	wantDigest := sha256.Sum256(fixture.content)
	want := PinnedExecutableIdentity{
		Device:    fixture.stats[testPinnedCanonicalFD].Dev,
		Inode:     fixture.stats[testPinnedCanonicalFD].Ino,
		SizeBytes: uint64(len(fixture.content)),
		Mode:      unix.S_IFREG | 0755,
		UID:       0,
		GID:       0,
		Links:     1,
		SHA256:    fmt.Sprintf("%x", wantDigest),
	}
	assert.Equal(t, want, identity)
	assert.Empty(t, fixture.closedFDs)

	inspected, err := capability.Reinspect(t.Context())
	require.NoError(t, err)
	assert.Equal(t, want, inspected)

	require.NoError(t, capability.Release(t.Context()))
	wantClosed := []int{
		testPinnedRunningFD,
		testPinnedCanonicalFD,
		testPinnedBinFD,
		testPinnedLocalFD,
		testPinnedUsrFD,
		testPinnedRootFD,
	}
	if diff := cmp.Diff(wantClosed, fixture.closedFDs); diff != "" {
		t.Fatalf("descriptor close order mismatch (-want +got):\n%s", diff)
	}
}

func TestPinnedExecutableReleaseIsSharedCloseOnceAndCachesCleanupFailure(t *testing.T) {
	t.Parallel()

	fixture := newPinnedExecutableFixture()
	closeFailure := errors.New("close canonical failed")
	fixture.closeErrors[testPinnedCanonicalFD] = closeFailure
	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	require.NoError(t, err)
	copyOfCapability := *capability

	const callers = 16
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- copyOfCapability.Release(t.Context())
		}()
	}
	wait.Wait()
	close(results)

	var first error
	for result := range results {
		require.Error(t, result)
		assert.ErrorIs(t, result, closeFailure)
		if first == nil {
			first = result
		} else {
			assert.Same(t, first, result)
		}
	}
	domainErr := errs.AsDomainError(first)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeInternal, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, infra.SystemBinaryPath, domainErr.Entity)
	assert.Equal(t, map[string]any{"pinned_executable_cleanup_failed": true}, domainErr.Details)

	wantClosed := []int{
		testPinnedRunningFD,
		testPinnedCanonicalFD,
		testPinnedBinFD,
		testPinnedLocalFD,
		testPinnedUsrFD,
		testPinnedRootFD,
	}
	if diff := cmp.Diff(wantClosed, fixture.closedFDs); diff != "" {
		t.Fatalf("descriptor close order mismatch (-want +got):\n%s", diff)
	}

	_, identityErr := capability.Identity(t.Context())
	assertPinnedExecutableDomainError(t, identityErr, errs.CodePrivilegeRequired, errs.ClassNeedsInteraction)
	_, reinspectErr := capability.Reinspect(t.Context())
	assertPinnedExecutableDomainError(t, reinspectErr, errs.CodePrivilegeRequired, errs.ClassNeedsInteraction)
}

func TestPinnedExecutableReinspectRejectsContentAndMetadataDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*pinnedExecutableFixture){
		"content": func(fixture *pinnedExecutableFixture) {
			fixture.content = []byte("different executable content")
			stat := fixture.stats[testPinnedCanonicalFD]
			stat.Size = int64(len(fixture.content))
			fixture.stats[testPinnedCanonicalFD] = stat
		},
		"owner": func(fixture *pinnedExecutableFixture) {
			stat := fixture.stats[testPinnedCanonicalFD]
			stat.Uid = 1000
			fixture.stats[testPinnedCanonicalFD] = stat
		},
		"inode": func(fixture *pinnedExecutableFixture) {
			stat := fixture.stats[testPinnedCanonicalFD]
			stat.Ino++
			fixture.stats[testPinnedCanonicalFD] = stat
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPinnedExecutableFixture()
			capability, err := pinSystemExecutable(t.Context(), fixture.deps)
			require.NoError(t, err)
			defer func() { _ = capability.Release(t.Context()) }()

			mutate(fixture)
			_, err = capability.Reinspect(t.Context())
			assertPinnedExecutableDomainError(t, err, errs.CodePrivilegeRequired, errs.ClassNeedsInteraction)
		})
	}
}

func TestPinSystemExecutableRejectsInvalidAuthorityAndCancellation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*pinnedExecutableFixture)
		ctx    func() context.Context
	}{
		"cancelled_before_effects": {
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		"running_image_mismatch": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedRunningFD]
				stat.Ino++
				fixture.stats[testPinnedRunningFD] = stat
			},
		},
		"unsafe_mode": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedCanonicalFD]
				stat.Mode = unix.S_IFREG | 0777
				fixture.stats[testPinnedCanonicalFD] = stat
			},
		},
		"foreign_owner": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedCanonicalFD]
				stat.Gid = 1000
				fixture.stats[testPinnedCanonicalFD] = stat
			},
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPinnedExecutableFixture()
			if testCase.mutate != nil {
				testCase.mutate(fixture)
			}
			ctx := t.Context()
			if testCase.ctx != nil {
				ctx = testCase.ctx()
			}
			capability, err := pinSystemExecutable(ctx, fixture.deps)
			assert.Nil(t, capability)
			assertPinnedExecutableDomainError(t, err, errs.CodePrivilegeRequired, errs.ClassNeedsInteraction)
			if name == "cancelled_before_effects" {
				assert.Empty(t, fixture.openCalls)
			}
		})
	}
}

func TestPinSystemExecutableUsesDescriptorRelativeNoFollowTraversal(t *testing.T) {
	t.Parallel()

	fixture := newPinnedExecutableFixture()
	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	require.NoError(t, err)
	defer func() { require.NoError(t, capability.Release(t.Context())) }()

	require.Len(t, fixture.openCalls, 6)
	for _, call := range fixture.openCalls[:4] {
		assert.NotZero(t, call.flags&unix.O_PATH)
		assert.NotZero(t, call.flags&unix.O_DIRECTORY)
		assert.NotZero(t, call.flags&unix.O_CLOEXEC)
		assert.NotZero(t, call.flags&unix.O_NOFOLLOW)
	}
	canonical := fixture.openCalls[4]
	assert.Equal(t, testPinnedBinFD, canonical.directoryFD)
	assert.Equal(t, "mvm", canonical.name)
	assert.Zero(t, canonical.flags&unix.O_PATH)
	assert.Zero(t, canonical.flags&unix.O_DIRECTORY)
	assert.NotZero(t, canonical.flags&unix.O_CLOEXEC)
	assert.NotZero(t, canonical.flags&unix.O_NOFOLLOW)
	running := fixture.openCalls[5]
	assert.Equal(t, "/proc/self/exe", running.name)
	assert.NotZero(t, running.flags&unix.O_PATH)
	assert.Zero(t, running.flags&unix.O_NOFOLLOW)
}

func TestPinSystemExecutableRejectsUnsafeTraversalAndReadFailures(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("read executable failed")
	openFailure := errors.New("component replaced")
	resolveFailure := errors.New("resolve executable failed")
	tests := map[string]struct {
		mutate    func(*pinnedExecutableFixture)
		wantCause error
	}{
		"unexpected_running_path": {
			mutate: func(fixture *pinnedExecutableFixture) {
				fixture.deps.executable = func(context.Context) (string, error) { return "/tmp/mvm", nil }
			},
		},
		"executable_resolution_failure": {
			mutate: func(fixture *pinnedExecutableFixture) {
				fixture.deps.executable = func(context.Context) (string, error) { return "", resolveFailure }
			},
			wantCause: resolveFailure,
		},
		"non_directory_ancestor": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedBinFD]
				stat.Mode = unix.S_IFREG | 0755
				fixture.stats[testPinnedBinFD] = stat
			},
		},
		"non_root_ancestor": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedBinFD]
				stat.Uid = 1000
				fixture.stats[testPinnedBinFD] = stat
			},
		},
		"writable_ancestor": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedBinFD]
				stat.Mode = unix.S_IFDIR | 0775
				fixture.stats[testPinnedBinFD] = stat
			},
		},
		"component_replacement": {
			mutate: func(fixture *pinnedExecutableFixture) {
				original := fixture.deps.openAt
				fixture.deps.openAt = func(
					ctx context.Context,
					directoryFD int,
					name string,
					flags int,
					mode uint32,
				) (int, error) {
					if name == "local" {
						return -1, openFailure
					}
					return original(ctx, directoryFD, name, flags, mode)
				}
			},
			wantCause: openFailure,
		},
		"executable_read_failure": {
			mutate: func(fixture *pinnedExecutableFixture) {
				fixture.deps.pread = func(context.Context, int, []byte, int64) (int, error) {
					return 0, readFailure
				}
			},
			wantCause: readFailure,
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPinnedExecutableFixture()
			testCase.mutate(fixture)
			capability, err := pinSystemExecutable(t.Context(), fixture.deps)
			assert.Nil(t, capability)
			assertPinnedExecutableDomainError(t, err, errs.CodePrivilegeRequired, errs.ClassNeedsInteraction)
			if testCase.wantCause != nil {
				assert.ErrorIs(t, err, testCase.wantCause)
			}
		})
	}
}

func TestPinSystemExecutableRejectsMetadataChangeDuringHash(t *testing.T) {
	t.Parallel()

	fixture := newPinnedExecutableFixture()
	originalFstat := fixture.deps.fstat
	canonicalObservations := 0
	fixture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := originalFstat(ctx, fd, stat); err != nil {
			return err
		}
		if fd == testPinnedCanonicalFD {
			canonicalObservations++
			if canonicalObservations == 2 {
				stat.Ctim.Nsec++
			}
		}
		return nil
	}

	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	assert.Nil(t, capability)
	assertPinnedExecutableDomainError(t, err, errs.CodePrivilegeRequired, errs.ClassNeedsInteraction)
}

func TestPinnedExecutableReleaseIgnoresCancellationAndPreservesValues(t *testing.T) {
	t.Parallel()

	type contextKey string
	const key contextKey = "cleanup"
	fixture := newPinnedExecutableFixture()
	originalClose := fixture.deps.close
	fixture.deps.close = func(ctx context.Context, fd int) error {
		assert.NoError(t, ctx.Err())
		assert.Equal(t, "retained", ctx.Value(key))
		return originalClose(ctx, fd)
	}
	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	require.NoError(t, err)
	cleanupCtx, cancel := context.WithCancel(context.WithValue(t.Context(), key, "retained"))
	cancel()
	require.NoError(t, capability.Release(cleanupCtx))
}

func assertPinnedExecutableDomainError(
	t *testing.T,
	err error,
	wantCode errs.Code,
	wantClass errs.Class,
) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, wantCode, domainErr.Code)
	assert.Equal(t, wantClass, domainErr.Class)
	assert.Equal(t, infra.SystemBinaryPath, domainErr.Entity)
}

type pinnedExecutableOpenCall struct {
	directoryFD int
	name        string
	flags       int
}

type pinnedExecutableFixture struct {
	deps        pinnedExecutableDeps
	content     []byte
	stats       map[int]unix.Stat_t
	openCalls   []pinnedExecutableOpenCall
	closedFDs   []int
	closeErrors map[int]error
}

func newPinnedExecutableFixture() *pinnedExecutableFixture {
	fixture := &pinnedExecutableFixture{
		content: []byte("trusted mvm executable"),
		stats: map[int]unix.Stat_t{
			testPinnedRootFD:      {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 1, Nlink: 1},
			testPinnedUsrFD:       {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 2, Nlink: 1},
			testPinnedLocalFD:     {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 100, Dev: 1, Ino: 3, Nlink: 1},
			testPinnedBinFD:       {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 4, Nlink: 1},
			testPinnedCanonicalFD: {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5, Nlink: 1},
			testPinnedRunningFD:   {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5, Nlink: 1},
		},
		closeErrors: make(map[int]error),
	}
	canonical := fixture.stats[testPinnedCanonicalFD]
	canonical.Size = int64(len(fixture.content))
	fixture.stats[testPinnedCanonicalFD] = canonical
	running := fixture.stats[testPinnedRunningFD]
	running.Size = int64(len(fixture.content))
	fixture.stats[testPinnedRunningFD] = running

	fixture.deps = pinnedExecutableDeps{
		executable: func(context.Context) (string, error) { return infra.SystemBinaryPath, nil },
		open: func(_ context.Context, name string, flags int, _ uint32) (int, error) {
			fixture.openCalls = append(fixture.openCalls, pinnedExecutableOpenCall{name: name, flags: flags})
			switch name {
			case "/":
				return testPinnedRootFD, nil
			case "/proc/self/exe":
				return testPinnedRunningFD, nil
			default:
				return -1, fmt.Errorf("unexpected open target %q", name)
			}
		},
		openAt: func(_ context.Context, directoryFD int, name string, flags int, _ uint32) (int, error) {
			fixture.openCalls = append(fixture.openCalls, pinnedExecutableOpenCall{
				directoryFD: directoryFD,
				name:        name,
				flags:       flags,
			})
			switch {
			case directoryFD == testPinnedRootFD && name == "usr":
				return testPinnedUsrFD, nil
			case directoryFD == testPinnedUsrFD && name == "local":
				return testPinnedLocalFD, nil
			case directoryFD == testPinnedLocalFD && name == "bin":
				return testPinnedBinFD, nil
			case directoryFD == testPinnedBinFD && name == "mvm":
				return testPinnedCanonicalFD, nil
			default:
				return -1, fmt.Errorf("unexpected openat target %d/%s", directoryFD, name)
			}
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			value, ok := fixture.stats[fd]
			if !ok {
				return unix.EBADF
			}
			*stat = value
			return nil
		},
		pread: func(_ context.Context, fd int, target []byte, offset int64) (int, error) {
			if fd != testPinnedCanonicalFD {
				return 0, unix.EBADF
			}
			if offset >= int64(len(fixture.content)) {
				return 0, nil
			}
			return copy(target, fixture.content[offset:]), nil
		},
		close: func(_ context.Context, fd int) error {
			fixture.closedFDs = append(fixture.closedFDs, fd)
			return fixture.closeErrors[fd]
		},
	}
	return fixture
}
