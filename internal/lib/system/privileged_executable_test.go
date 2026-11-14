package system

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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
		Links:     fixture.stats[testPinnedCanonicalFD].Nlink,
		SHA256:    fmt.Sprintf("%x", wantDigest),
	}
	if diff := cmp.Diff(want, identity); diff != "" {
		t.Fatalf("identity mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, fixture.closedFDs)

	openCallCount := len(fixture.openCalls)
	inspected, err := capability.Reinspect(t.Context())
	require.NoError(t, err)
	if diff := cmp.Diff(want, inspected); diff != "" {
		t.Fatalf("reinspection mismatch (-want +got):\n%s", diff)
	}
	assert.Len(t, fixture.openCalls, openCallCount)

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
	if diff := cmp.Diff(map[string]any{"pinned_executable_cleanup_failed": true}, domainErr.Details); diff != "" {
		t.Fatalf("cleanup details mismatch (-want +got):\n%s", diff)
	}

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
	assertPinnedExecutableDomainError(t, identityErr, errs.CodeInternal, errs.ClassInternal)
	_, reinspectErr := capability.Reinspect(t.Context())
	assertPinnedExecutableDomainError(t, reinspectErr, errs.CodeInternal, errs.ClassInternal)
}

func TestPinnedExecutableSerializesReinspectionAndRelease(t *testing.T) {
	t.Parallel()

	fixture := newPinnedExecutableFixture()
	originalPread := fixture.deps.pread
	reinspectionStarted := make(chan struct{})
	allowReinspection := make(chan struct{})
	hashPass := 0
	fixture.deps.pread = func(ctx context.Context, fd int, target []byte, offset int64) (int, error) {
		if offset == 0 {
			hashPass++
			if hashPass == 2 {
				close(reinspectionStarted)
				<-allowReinspection
			}
		}
		return originalPread(ctx, fd, target, offset)
	}
	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	require.NoError(t, err)

	reinspectResult := make(chan error, 1)
	go func() {
		_, reinspectErr := capability.Reinspect(t.Context())
		reinspectResult <- reinspectErr
	}()
	<-reinspectionStarted
	releaseResult := make(chan error, 1)
	go func() {
		releaseResult <- capability.Release(t.Context())
	}()
	close(allowReinspection)
	require.NoError(t, <-reinspectResult)
	require.NoError(t, <-releaseResult)

	_, err = capability.Identity(t.Context())
	assertPinnedExecutableDomainError(t, err, errs.CodeInternal, errs.ClassInternal)
}

func TestPinnedExecutableReinspectRejectsContentAndMetadataDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*pinnedExecutableFixture){
		"content": func(fixture *pinnedExecutableFixture) {
			fixture.content = bytes.Repeat([]byte{'x'}, len(fixture.content))
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
		"mtime": func(fixture *pinnedExecutableFixture) {
			stat := fixture.stats[testPinnedCanonicalFD]
			stat.Mtim.Nsec++
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
		mutate    func(*pinnedExecutableFixture)
		ctx       func() context.Context
		wantCode  errs.Code
		wantClass errs.Class
	}{
		"cancelled_before_effects": {
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantCode:  errs.CodeInternal,
			wantClass: errs.ClassRetryable,
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
			wantCode := testCase.wantCode
			wantClass := testCase.wantClass
			if wantCode == "" {
				wantCode = errs.CodePrivilegeRequired
				wantClass = errs.ClassNeedsInteraction
			}
			assertPinnedExecutableDomainError(t, err, wantCode, wantClass)
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
	fstatFailure := errors.New("inspect executable failed")
	openFailure := errors.New("component replaced")
	resolveFailure := errors.New("resolve executable failed")
	tests := map[string]struct {
		mutate    func(*pinnedExecutableFixture)
		wantCause error
		wantCode  errs.Code
		wantClass errs.Class
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
			wantCode:  errs.CodeInternal,
			wantClass: errs.ClassInternal,
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
		"non_regular_executable": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedCanonicalFD]
				stat.Mode = unix.S_IFIFO | 0755
				fixture.stats[testPinnedCanonicalFD] = stat
			},
		},
		"setuid_executable": {
			mutate: func(fixture *pinnedExecutableFixture) {
				stat := fixture.stats[testPinnedCanonicalFD]
				stat.Mode = unix.S_IFREG | unix.S_ISUID | 0755
				fixture.stats[testPinnedCanonicalFD] = stat
			},
		},
		"canonical_fstat_failure": {
			mutate: func(fixture *pinnedExecutableFixture) {
				original := fixture.deps.fstat
				fixture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					if fd == testPinnedCanonicalFD {
						return fstatFailure
					}
					return original(ctx, fd, stat)
				}
			},
			wantCause: fstatFailure,
			wantCode:  errs.CodeInternal,
			wantClass: errs.ClassInternal,
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
			wantCode:  errs.CodeInternal,
			wantClass: errs.ClassInternal,
		},
		"executable_read_failure": {
			mutate: func(fixture *pinnedExecutableFixture) {
				fixture.deps.pread = func(context.Context, int, []byte, int64) (int, error) {
					return 0, readFailure
				}
			},
			wantCause: readFailure,
			wantCode:  errs.CodeInternal,
			wantClass: errs.ClassInternal,
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPinnedExecutableFixture()
			testCase.mutate(fixture)
			capability, err := pinSystemExecutable(t.Context(), fixture.deps)
			assert.Nil(t, capability)
			wantCode := testCase.wantCode
			wantClass := testCase.wantClass
			if wantCode == "" {
				wantCode = errs.CodePrivilegeRequired
				wantClass = errs.ClassNeedsInteraction
			}
			assertPinnedExecutableDomainError(t, err, wantCode, wantClass)
			if testCase.wantCause != nil {
				assert.ErrorIs(t, err, testCase.wantCause)
			}
		})
	}
}

func TestPinSystemExecutableClosesPartialAdmissionAndPreservesCleanupFailure(t *testing.T) {
	t.Parallel()

	fixture := newPinnedExecutableFixture()
	primaryCause := errors.New("canonical fstat failed")
	cleanupCause := errors.New("canonical close failed")
	originalFstat := fixture.deps.fstat
	fixture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if fd == testPinnedCanonicalFD {
			return primaryCause
		}
		return originalFstat(ctx, fd, stat)
	}
	fixture.closeErrors[testPinnedCanonicalFD] = cleanupCause

	capability, err := pinSystemExecutable(t.Context(), fixture.deps)
	assert.Nil(t, capability)
	assert.ErrorIs(t, err, primaryCause)
	assert.ErrorIs(t, err, cleanupCause)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeInternal, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, true, domainErr.Details["pinned_executable_cleanup_failed"])
	wantClosed := []int{
		testPinnedCanonicalFD,
		testPinnedBinFD,
		testPinnedLocalFD,
		testPinnedUsrFD,
		testPinnedRootFD,
	}
	if diff := cmp.Diff(wantClosed, fixture.closedFDs); diff != "" {
		t.Fatalf("partial-admission close order mismatch (-want +got):\n%s", diff)
	}
}

func TestHashPinnedExecutableEnforcesReadBoundaries(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		content []byte
		pread   func([]byte, int64) (int, error)
		wantErr string
	}{
		"negative_count": {
			content: []byte("x"),
			pread:   func([]byte, int64) (int, error) { return -1, nil },
			wantErr: "invalid byte count",
		},
		"oversized_count": {
			content: []byte("x"),
			pread:   func(target []byte, _ int64) (int, error) { return len(target) + 1, nil },
			wantErr: "invalid byte count",
		},
		"premature_zero": {
			content: []byte("x"),
			pread:   func([]byte, int64) (int, error) { return 0, nil },
			wantErr: "size changed",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPinnedExecutableFixture()
			fixture.content = testCase.content
			stat := fixture.stats[testPinnedCanonicalFD]
			stat.Size = int64(len(testCase.content))
			fixture.stats[testPinnedCanonicalFD] = stat
			fixture.deps.pread = func(_ context.Context, _ int, target []byte, offset int64) (int, error) {
				return testCase.pread(target, offset)
			}
			_, err := hashPinnedExecutable(t.Context(), fixture.deps, testPinnedCanonicalFD, stat)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.ErrorContains(t, domainErr.Err, testCase.wantErr)
		})
	}

	t.Run("multi_buffer_and_eof_with_bytes", func(t *testing.T) {
		fixture := newPinnedExecutableFixture()
		fixture.content = bytes.Repeat([]byte("z"), pinnedExecutableHashBufferBytes+17)
		stat := fixture.stats[testPinnedCanonicalFD]
		stat.Size = int64(len(fixture.content))
		fixture.stats[testPinnedCanonicalFD] = stat
		fixture.deps.pread = func(_ context.Context, _ int, target []byte, offset int64) (int, error) {
			if offset >= int64(len(fixture.content)) {
				return 0, io.EOF
			}
			count := copy(target, fixture.content[offset:])
			if offset+int64(count) == int64(len(fixture.content)) {
				return count, io.EOF
			}
			return count, nil
		}
		identity, err := hashPinnedExecutable(t.Context(), fixture.deps, testPinnedCanonicalFD, stat)
		require.NoError(t, err)
		wantDigest := sha256.Sum256(fixture.content)
		assert.Equal(t, fmt.Sprintf("%x", wantDigest), identity.SHA256)
	})
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
			testPinnedCanonicalFD: {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 41, Ino: 53, Nlink: 7},
			testPinnedRunningFD:   {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 41, Ino: 53, Nlink: 7},
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
