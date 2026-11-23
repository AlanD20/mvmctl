package network

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type networkStoreFixture struct {
	root       string
	policy     networkStorePolicy
	storeRoot  *networkStoreRoot
	uid        *networkStoreUID
	record     networkAuthorityRecord
	recordPath string
}

// Rationale: only an active ENOENT is absence; cancellation, I/O failure, and corruption must remain distinguishable.
func TestNetworkStoreReadDistinguishesMissingCancellationIOAndCorruption(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		record, found, err := fixture.uid.readRecord(t.Context(), NetworkID(testNetworkID))
		require.NoError(t, err)
		assert.False(t, found)
		assertNetworkAuthorityRecordEqual(t, networkAuthorityRecord{}, record)
	})

	t.Run("already canceled", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		record, found, err := fixture.uid.readRecord(ctx, NetworkID(testNetworkID))
		assertNetworkAuthorityRecordEqual(t, networkAuthorityRecord{}, record)
		assert.False(t, found)
		assert.ErrorIs(t, err, context.Canceled)
		assertNetworkAuthorityErrorClass(t, err, errs.ClassRetryable)
	})

	t.Run("canceled open returning missing", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		fixture.uid.deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) {
			cancel()
			return -1, unix.ENOENT
		}
		record, found, err := fixture.uid.readRecord(ctx, NetworkID(testNetworkID))
		assertNetworkAuthorityRecordEqual(t, networkAuthorityRecord{}, record)
		assert.False(t, found)
		assert.ErrorIs(t, err, context.Canceled)
		assertNetworkAuthorityErrorClass(t, err, errs.ClassRetryable)
	})

	t.Run("open IO", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		fixture.uid.deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) {
			return -1, unix.EIO
		}
		_, found, err := fixture.uid.readRecord(t.Context(), NetworkID(testNetworkID))
		assert.False(t, found)
		assert.ErrorIs(t, err, unix.EIO)
		assertNetworkAuthorityErrorClass(t, err, errs.ClassInternal)
	})

	t.Run("read IO", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		fixture.writeRaw(t, []byte(canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID)), 0600)
		fixture.uid.deps.read = func(context.Context, int, []byte) (int, error) { return 0, unix.EIO }
		_, found, err := fixture.uid.readRecord(t.Context(), NetworkID(testNetworkID))
		assert.False(t, found)
		assert.ErrorIs(t, err, unix.EIO)
		assertNetworkAuthorityErrorClass(t, err, errs.ClassInternal)
	})

	t.Run("corrupt JSON", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		fixture.writeRaw(t, []byte("{"), 0600)
		_, found, err := fixture.uid.readRecord(t.Context(), NetworkID(testNetworkID))
		assert.False(t, found)
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeNetworkError, domainErr.Code)
		assert.Equal(t, errs.ClassInternal, domainErr.Class)
	})
}

// Rationale: descriptor cleanup evidence must be retained without erasing a successful read or its primary failure.
func TestNetworkStoreReadReturnsRecordWithCloseFaultAndPreservesPrimary(t *testing.T) {
	t.Parallel()

	t.Run("successful read plus close fault", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		fixture.writeRaw(t, []byte(canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID)), 0600)
		realOpen := fixture.uid.deps.openAt2
		realClose := fixture.uid.deps.close
		fault := errors.New("close failed")
		var recordFD atomic.Int64
		var closeCalls atomic.Int32
		fixture.uid.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			fd, err := realOpen(ctx, parentFD, name, how)
			if err == nil {
				recordFD.Store(int64(fd))
			}
			return fd, err
		}
		fixture.uid.deps.close = func(ctx context.Context, fd int) error {
			closeErr := realClose(ctx, fd)
			if int64(fd) == recordFD.Load() {
				closeCalls.Add(1)
				return errors.Join(closeErr, fault)
			}
			return closeErr
		}
		record, found, err := fixture.uid.readRecord(t.Context(), NetworkID(testNetworkID))
		assert.True(t, found)
		assertNetworkAuthorityRecordEqual(t, fixture.record, record)
		assert.ErrorIs(t, err, fault)
		assert.Equal(t, int32(1), closeCalls.Load())
	})

	t.Run("read and close faults", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		fixture.writeRaw(t, []byte(canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID)), 0600)
		realOpen := fixture.uid.deps.openAt2
		realClose := fixture.uid.deps.close
		readFault := errors.New("read failed")
		closeFault := errors.New("close failed")
		var recordFD atomic.Int64
		var closeCalls atomic.Int32
		fixture.uid.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			fd, err := realOpen(ctx, parentFD, name, how)
			if err == nil {
				recordFD.Store(int64(fd))
			}
			return fd, err
		}
		fixture.uid.deps.read = func(context.Context, int, []byte) (int, error) { return 0, readFault }
		fixture.uid.deps.close = func(ctx context.Context, fd int) error {
			closeErr := realClose(ctx, fd)
			if int64(fd) == recordFD.Load() {
				closeCalls.Add(1)
				return errors.Join(closeErr, closeFault)
			}
			return closeErr
		}
		_, found, err := fixture.uid.readRecord(t.Context(), NetworkID(testNetworkID))
		assert.False(t, found)
		assert.ErrorIs(t, err, readFault)
		assert.ErrorIs(t, err, closeFault)
		assert.Equal(t, int32(1), closeCalls.Load())
	})
}

// Rationale: no precommit stage may expose a final record or strand a temporary artifact after a partial transaction.
func TestNetworkStoreAtomicWriteCleansEveryPrecommitFault(t *testing.T) {
	t.Parallel()

	stages := []string{
		"random", "open", "write", "chown", "chmod", "stat_before", "sync", "stat_after", "close", "rename",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			fixture := newNetworkStoreFixture(t)
			fault := errors.New(stage + " failed")
			observation := installNetworkStoreWriteFault(t, fixture.uid, stage, fault)
			err := fixture.uid.writeRecord(t.Context(), fixture.record)
			require.Error(t, err)
			assert.ErrorIs(t, err, fault)
			_, statErr := os.Lstat(fixture.recordPath)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
			entries, readErr := os.ReadDir(filepath.Dir(fixture.recordPath))
			require.NoError(t, readErr)
			assert.Empty(t, entries)
			if stage == "random" || stage == "open" {
				assert.Zero(t, observation.tempCloseCalls.Load())
				assert.Zero(t, observation.unlinkCalls.Load())
			} else {
				assert.Equal(t, int32(1), observation.tempCloseCalls.Load())
				assert.Equal(t, int32(1), observation.unlinkCalls.Load())
			}
		})
	}
}

func TestNetworkStoreAtomicWriteChecksCancellationBeforePublish(t *testing.T) {
	t.Parallel()

	fixture := newNetworkStoreFixture(t)
	realOpen := fixture.uid.deps.openAt2
	realClose := fixture.uid.deps.close
	realUnlink := fixture.uid.deps.unlinkAt
	ctx, cancel := context.WithCancel(t.Context())
	var tempFD atomic.Int64
	var closeCalls atomic.Int32
	var unlinkCalls atomic.Int32
	fixture.uid.deps.openAt2 = func(callCtx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpen(callCtx, parentFD, name, how)
		if err == nil && networkStoreTempNamePattern.MatchString(name) {
			tempFD.Store(int64(fd))
		}
		return fd, err
	}
	fixture.uid.deps.close = func(callCtx context.Context, fd int) error {
		if int64(fd) == tempFD.Load() {
			closeCalls.Add(1)
			closeErr := realClose(callCtx, fd)
			cancel()
			return closeErr
		}
		return realClose(callCtx, fd)
	}
	fixture.uid.deps.unlinkAt = func(callCtx context.Context, parentFD int, name string, flags int) error {
		unlinkCalls.Add(1)
		return realUnlink(callCtx, parentFD, name, flags)
	}
	err := fixture.uid.writeRecord(ctx, fixture.record)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(1), closeCalls.Load())
	assert.Equal(t, int32(1), unlinkCalls.Load())
	assert.Empty(t, mustReadNetworkStoreEntries(t, filepath.Dir(fixture.recordPath)))
}

func TestNetworkStoreAtomicWritePreservesAbortCloseAndUnlinkFaults(t *testing.T) {
	t.Parallel()

	fixture := newNetworkStoreFixture(t)
	realOpen := fixture.uid.deps.openAt2
	realClose := fixture.uid.deps.close
	writeFault := errors.New("write failed")
	closeFault := errors.New("close failed")
	unlinkFault := errors.New("unlink failed")
	var tempFD atomic.Int64
	var closeCalls atomic.Int32
	var unlinkCalls atomic.Int32
	fixture.uid.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpen(ctx, parentFD, name, how)
		if err == nil && networkStoreTempNamePattern.MatchString(name) {
			tempFD.Store(int64(fd))
		}
		return fd, err
	}
	fixture.uid.deps.write = func(context.Context, int, []byte) (int, error) { return 0, writeFault }
	fixture.uid.deps.close = func(ctx context.Context, fd int) error {
		if int64(fd) == tempFD.Load() {
			closeCalls.Add(1)
			return errors.Join(realClose(ctx, fd), closeFault)
		}
		return realClose(ctx, fd)
	}
	fixture.uid.deps.unlinkAt = func(context.Context, int, string, int) error {
		unlinkCalls.Add(1)
		return unlinkFault
	}
	err := fixture.uid.writeRecord(t.Context(), fixture.record)
	assert.ErrorIs(t, err, writeFault)
	assert.ErrorIs(t, err, closeFault)
	assert.ErrorIs(t, err, unlinkFault)
	assert.Equal(t, int32(1), closeCalls.Load())
	assert.Equal(t, int32(1), unlinkCalls.Load())
	entries, readErr := os.ReadDir(filepath.Dir(fixture.recordPath))
	require.NoError(t, readErr)
	require.Len(t, entries, 1)
	assert.True(t, networkStoreTempNamePattern.MatchString(entries[0].Name()))
}

func TestNetworkStoreAtomicWriteReportsPostcommitDurabilityUncertainty(t *testing.T) {
	t.Parallel()

	fixture := newNetworkStoreFixture(t)
	realFsync := fixture.uid.deps.fsync
	fault := errors.New("directory sync failed")
	fixture.uid.deps.fsync = func(ctx context.Context, fd int) error {
		if fd == fixture.uid.stateFD {
			return fault
		}
		return realFsync(ctx, fd)
	}
	err := fixture.uid.writeRecord(t.Context(), fixture.record)
	require.ErrorIs(t, err, fault)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_created"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	raw, readErr := os.ReadFile(fixture.recordPath)
	require.NoError(t, readErr)
	assert.Equal(t, canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID), string(raw))
}

// Rationale: publication is create-only so an attacker-controlled or concurrently-created identity is never replaced.
func TestNetworkStoreAtomicWriteNeverReplacesExistingEntry(t *testing.T) {
	t.Parallel()

	fixture := newNetworkStoreFixture(t)
	require.NoError(t, os.Symlink("attacker", fixture.recordPath))
	realUnlink := fixture.uid.deps.unlinkAt
	var unlinkCalls atomic.Int32
	fixture.uid.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		unlinkCalls.Add(1)
		return realUnlink(ctx, parentFD, name, flags)
	}
	err := fixture.uid.writeRecord(t.Context(), fixture.record)
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EEXIST)
	assert.Equal(t, int32(1), unlinkCalls.Load())
	info, statErr := os.Lstat(fixture.recordPath)
	require.NoError(t, statErr)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	target, readErr := os.Readlink(fixture.recordPath)
	require.NoError(t, readErr)
	assert.Equal(t, "attacker", target)
	entries, readErr := os.ReadDir(filepath.Dir(fixture.recordPath))
	require.NoError(t, readErr)
	assert.Len(t, entries, 1)
}

func TestNetworkStoreWriteHandlesPartialWritesAndRejectsInvalidCounts(t *testing.T) {
	t.Parallel()

	t.Run("partial writes", func(t *testing.T) {
		fixture := newNetworkStoreFixture(t)
		realWrite := fixture.uid.deps.write
		var calls atomic.Int32
		fixture.uid.deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
			calls.Add(1)
			return realWrite(ctx, fd, value[:min(7, len(value))])
		}
		require.NoError(t, fixture.uid.writeRecord(t.Context(), fixture.record))
		assert.Greater(t, calls.Load(), int32(1))
		raw, err := os.ReadFile(fixture.recordPath)
		require.NoError(t, err)
		assert.Equal(t, canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID), string(raw))
	})

	for name, write := range map[string]func(context.Context, int, []byte) (int, error){
		"zero":     func(context.Context, int, []byte) (int, error) { return 0, nil },
		"negative": func(context.Context, int, []byte) (int, error) { return -1, nil },
		"too large": func(_ context.Context, _ int, value []byte) (int, error) {
			return len(value) + 1, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newNetworkStoreFixture(t)
			fixture.uid.deps.write = write
			err := fixture.uid.writeRecord(t.Context(), fixture.record)
			require.Error(t, err)
			entries, readErr := os.ReadDir(filepath.Dir(fixture.recordPath))
			require.NoError(t, readErr)
			assert.Empty(t, entries)
		})
	}
}

func TestNetworkStoreTemporaryNameCollisionRetryIsBounded(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		collisions  int
		wantSuccess bool
	}{
		{name: "last attempt succeeds", collisions: networkStoreTempAttempts - 1, wantSuccess: true},
		{name: "all attempts collide", collisions: networkStoreTempAttempts, wantSuccess: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newNetworkStoreFixture(t)
			names := make([]string, networkStoreTempAttempts)
			for index := range names {
				names[index] = fmt.Sprintf(".mvm-network-%032x.tmp", index+1)
			}
			for _, name := range names[:tc.collisions] {
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(fixture.recordPath), name), nil, 0600))
			}
			var calls atomic.Int32
			fixture.uid.deps.randomName = func(context.Context) (string, error) {
				index := int(calls.Add(1)) - 1
				return names[index], nil
			}
			err := fixture.uid.writeRecord(t.Context(), fixture.record)
			assert.Equal(t, int32(networkStoreTempAttempts), calls.Load())
			if tc.wantSuccess {
				require.NoError(t, err)
				_, statErr := os.Stat(fixture.recordPath)
				require.NoError(t, statErr)
				return
			}
			require.Error(t, err)
			_, statErr := os.Stat(fixture.recordPath)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}

	fixture := newNetworkStoreFixture(t)
	fixture.uid.deps.randomName = func(context.Context) (string, error) { return "../escape", nil }
	err := fixture.uid.writeRecord(t.Context(), fixture.record)
	require.Error(t, err)
	assert.Empty(t, mustReadNetworkStoreEntries(t, filepath.Dir(fixture.recordPath)))
}

func TestNetworkStoreRejectsUnsafeManagedDirectoryWithoutRepair(t *testing.T) {
	t.Parallel()

	root := secureNetworkTestRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib/mvmctl/networks"), 0755))
	policy := networkStorePolicy{
		rootPath: root, stateRoot: "/var/lib/mvmctl/networks",
		expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
	}
	store, err := openNetworkStoreRoot(t.Context(), realNetworkStoreDeps(), policy)
	assert.Nil(t, store)
	require.Error(t, err)
	info, statErr := os.Stat(filepath.Join(root, "var/lib/mvmctl/networks"))
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

// Rationale: guest completion markers share the state base, while network authority remains private.
func TestNetworkStoreSharedStateBaseAndPrivateDirectories(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		baseMode    os.FileMode
		privateMode os.FileMode
		wrongOwner  bool
		wantErr     bool
	}{
		"writable shared base":         {baseMode: 0775, wantErr: true},
		"unexpected shared owner":      {baseMode: 0755, wrongOwner: true, wantErr: true},
		"permissive private directory": {baseMode: 0755, privateMode: 0755, wantErr: true},
		"existing shared base":         {baseMode: 0755},
		"missing shared base":          {},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := secureNetworkTestRoot(t)
			require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
			base := filepath.Join(root, "var/lib/mvmctl")
			private := filepath.Join(base, "networks")
			var before unix.Stat_t
			if tc.baseMode != 0 {
				require.NoError(t, os.Mkdir(base, tc.baseMode))
				require.NoError(t, os.Chmod(base, tc.baseMode))
				require.NoError(t, unix.Lstat(base, &before))
			}
			if tc.privateMode != 0 {
				require.NoError(t, os.Mkdir(private, tc.privateMode))
				require.NoError(t, os.Chmod(private, tc.privateMode))
			}
			deps := realNetworkStoreDeps()
			realFstat := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if tc.wrongOwner && stat.Ino == before.Ino && stat.Dev == before.Dev {
					stat.Uid++
				}
				return nil
			}
			store, err := openNetworkStoreRoot(t.Context(), deps, networkStorePolicy{
				rootPath: root, stateRoot: "/var/lib/mvmctl/networks",
				expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
			})
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, store)
			} else {
				require.NoError(t, err)
				require.NoError(t, store.Release(t.Context()))
				info, statErr := os.Lstat(private)
				require.NoError(t, statErr)
				assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
			}
			var after unix.Stat_t
			require.NoError(t, unix.Lstat(base, &after))
			if tc.baseMode != 0 {
				assert.Equal(t, before.Ino, after.Ino)
				assert.Equal(t, before.Mode, after.Mode)
				assert.Equal(t, before.Uid, after.Uid)
				assert.Equal(t, before.Gid, after.Gid)
			} else {
				assert.Equal(t, uint32(0700), after.Mode&07777)
				assert.Equal(t, uint32(os.Geteuid()), after.Uid)
				assert.Equal(t, uint32(os.Getegid()), after.Gid)
			}
			if tc.wantErr && tc.privateMode == 0 {
				assert.NoDirExists(t, private)
			}
			if tc.privateMode != 0 {
				info, statErr := os.Lstat(private)
				require.NoError(t, statErr)
				assert.Equal(t, tc.privateMode, info.Mode().Perm())
			}
		})
	}
}

func TestHostResetNetworkStorePreservesSharedBaseAndAbsence(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"missing base", "missing private", "existing private", "unsafe private"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			root := secureNetworkTestRoot(t)
			require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
			base := filepath.Join(root, "var/lib/mvmctl")
			private := filepath.Join(base, "networks")
			if state != "missing base" {
				require.NoError(t, os.Mkdir(base, 0755))
			}
			if state == "existing private" {
				require.NoError(t, os.Mkdir(private, 0700))
			}
			if state == "unsafe private" {
				require.NoError(t, os.Mkdir(private, 0755))
			}
			store, err := openHostResetNetworkStoreWithDeps(t.Context(), realNetworkStoreDeps(), networkStorePolicy{
				rootPath: root, stateRoot: "/var/lib/mvmctl/networks",
				expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
			})
			if state == "unsafe private" {
				require.Error(t, err)
				assert.Nil(t, store)
				info, statErr := os.Lstat(private)
				require.NoError(t, statErr)
				assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
				return
			}
			require.NoError(t, err)
			if state == "existing private" {
				require.NotNil(t, store)
				require.NoError(t, store.Release(t.Context()))
			} else {
				assert.Nil(t, store)
				assert.NoDirExists(t, private)
			}
			if state == "missing base" {
				assert.NoDirExists(t, base)
			} else {
				info, statErr := os.Lstat(base)
				require.NoError(t, statErr)
				assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
			}
		})
	}
}

// Rationale: release owns descriptor lifetime and must cache one exact result while closing in reverse acquisition order.
func TestNetworkStoreRootReleaseClosesReverseOnceWithCleanupContext(t *testing.T) {
	t.Parallel()

	rootPath := secureNetworkTestRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(rootPath, "var/lib"), 0755))
	deps := realNetworkStoreDeps()
	realOpen := deps.open
	realOpenAt2 := deps.openAt2
	realClose := deps.close
	accepted := make([]int, 0, 5)
	closed := make([]int, 0, 5)
	contextsSafe := make([]bool, 0, 5)
	deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, path, flags, mode)
		if err == nil {
			accepted = append(accepted, fd)
		}
		return fd, err
	}
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil {
			accepted = append(accepted, fd)
		}
		return fd, err
	}
	closeFault := errors.New("root close failed")
	deps.close = func(ctx context.Context, fd int) error {
		closed = append(closed, fd)
		contextsSafe = append(contextsSafe,
			ctx.Err() == nil && ctx.Value(testAuthorityContextKey{}) == "retained")
		closeErr := realClose(ctx, fd)
		if len(accepted) > 0 && fd == accepted[len(accepted)-1] {
			return errors.Join(closeErr, closeFault)
		}
		return closeErr
	}
	root, err := openNetworkStoreRoot(t.Context(), deps, networkStorePolicy{
		rootPath: rootPath, stateRoot: "/var/lib/mvmctl/networks",
		expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
	})
	require.NoError(t, err)
	require.Len(t, accepted, 5)
	releaseCtx, cancel := context.WithCancel(context.WithValue(
		t.Context(), testAuthorityContextKey{}, "retained",
	))
	cancel()
	first := root.Release(releaseCtx)
	second := root.Release(context.Background())
	require.ErrorIs(t, first, closeFault)
	assert.Same(t, first, second)
	wantClosed := slices.Clone(accepted)
	slices.Reverse(wantClosed)
	if diff := cmp.Diff(wantClosed, closed); diff != "" {
		t.Errorf("network store root close order mismatch (-want +got):\n%s", diff)
	}
	assert.NotEmpty(t, contextsSafe)
	assert.NotContains(t, contextsSafe, false)
}

// Rationale: a UID descriptor close failure remains the stable release result and must never trigger a second close.
func TestNetworkStoreUIDReleaseClosesOnceWithCleanupContext(t *testing.T) {
	t.Parallel()

	fixture := newNetworkStoreFixtureWithoutCleanup(t)
	realClose := fixture.uid.deps.close
	closeFault := errors.New("UID close failed")
	var closeCalls atomic.Int32
	var contextSafe atomic.Bool
	fixture.uid.deps.close = func(ctx context.Context, fd int) error {
		closeCalls.Add(1)
		contextSafe.Store(ctx.Err() == nil && ctx.Value(testAuthorityContextKey{}) == "retained")
		return errors.Join(realClose(ctx, fd), closeFault)
	}
	releaseCtx, cancel := context.WithCancel(context.WithValue(
		t.Context(), testAuthorityContextKey{}, "retained",
	))
	cancel()
	first := fixture.uid.Release(releaseCtx)
	second := fixture.uid.Release(context.Background())
	require.ErrorIs(t, first, closeFault)
	assert.Same(t, first, second)
	assert.Equal(t, int32(1), closeCalls.Load())
	assert.True(t, contextSafe.Load())
	require.NoError(t, fixture.storeRoot.Release(context.Background()))
}

type networkStoreWriteObservation struct {
	tempCloseCalls atomic.Int32
	unlinkCalls    atomic.Int32
}

func installNetworkStoreWriteFault(
	t *testing.T,
	uid *networkStoreUID,
	stage string,
	fault error,
) *networkStoreWriteObservation {
	t.Helper()
	observation := &networkStoreWriteObservation{}
	realOpen := uid.deps.openAt2
	realWrite := uid.deps.write
	realFstat := uid.deps.fstat
	realClose := uid.deps.close
	realUnlink := uid.deps.unlinkAt
	var tempFD atomic.Int64
	var tempStats atomic.Int32
	if stage == "random" {
		uid.deps.randomName = func(context.Context) (string, error) { return "", fault }
	}
	uid.deps.openAt2 = func(callCtx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		if stage == "open" && networkStoreTempNamePattern.MatchString(name) {
			return -1, fault
		}
		fd, err := realOpen(callCtx, parentFD, name, how)
		if err == nil && networkStoreTempNamePattern.MatchString(name) {
			tempFD.Store(int64(fd))
		}
		return fd, err
	}
	uid.deps.write = func(callCtx context.Context, fd int, value []byte) (int, error) {
		if stage == "write" && int64(fd) == tempFD.Load() {
			return 0, fault
		}
		return realWrite(callCtx, fd, value)
	}
	uid.deps.fchown = wrapNetworkStoreFDOwnerFault(uid.deps.fchown, &tempFD, stage == "chown", fault)
	uid.deps.fchmod = wrapNetworkStoreFDModeFault(uid.deps.fchmod, &tempFD, stage == "chmod", fault)
	uid.deps.fstat = func(callCtx context.Context, fd int, stat *unix.Stat_t) error {
		if int64(fd) == tempFD.Load() {
			call := tempStats.Add(1)
			if stage == "stat_before" && call == 1 || stage == "stat_after" && call == 2 {
				return fault
			}
		}
		return realFstat(callCtx, fd, stat)
	}
	uid.deps.fsync = func(callCtx context.Context, fd int) error {
		if stage == "sync" && int64(fd) == tempFD.Load() {
			return fault
		}
		return unix.Fsync(fd)
	}
	uid.deps.close = func(callCtx context.Context, fd int) error {
		if int64(fd) != tempFD.Load() {
			return realClose(callCtx, fd)
		}
		observation.tempCloseCalls.Add(1)
		closeErr := realClose(callCtx, fd)
		if stage == "close" {
			return errors.Join(closeErr, fault)
		}
		return closeErr
	}
	uid.deps.renameNoReplace = func(callCtx context.Context, oldFD int, oldName string, newFD int, newName string) error {
		if stage == "rename" {
			return fault
		}
		return unix.Renameat2(oldFD, oldName, newFD, newName, unix.RENAME_NOREPLACE)
	}
	uid.deps.unlinkAt = func(callCtx context.Context, parentFD int, name string, flags int) error {
		observation.unlinkCalls.Add(1)
		return realUnlink(callCtx, parentFD, name, flags)
	}
	return observation
}

func wrapNetworkStoreFDOwnerFault(
	real func(context.Context, int, int, int) error,
	tempFD *atomic.Int64,
	fail bool,
	fault error,
) func(context.Context, int, int, int) error {
	return func(ctx context.Context, fd, uid, gid int) error {
		if fail && int64(fd) == tempFD.Load() {
			return fault
		}
		return real(ctx, fd, uid, gid)
	}
}

func wrapNetworkStoreFDModeFault(
	real func(context.Context, int, uint32) error,
	tempFD *atomic.Int64,
	fail bool,
	fault error,
) func(context.Context, int, uint32) error {
	return func(ctx context.Context, fd int, mode uint32) error {
		if fail && int64(fd) == tempFD.Load() {
			return fault
		}
		return real(ctx, fd, mode)
	}
}

func newNetworkStoreFixture(t *testing.T) *networkStoreFixture {
	t.Helper()
	fixture := newNetworkStoreFixtureWithoutCleanup(t)
	t.Cleanup(func() { require.NoError(t, fixture.storeRoot.Release(context.Background())) })
	t.Cleanup(func() { require.NoError(t, fixture.uid.Release(context.Background())) })
	return fixture
}

func newNetworkStoreFixtureWithoutCleanup(t *testing.T) *networkStoreFixture {
	t.Helper()
	root := secureNetworkTestRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
	policy := networkStorePolicy{
		rootPath: root, stateRoot: "/var/lib/mvmctl/networks",
		expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
	}
	storeRoot, err := openNetworkStoreRoot(t.Context(), realNetworkStoreDeps(), policy)
	require.NoError(t, err)
	uid, err := storeRoot.openUID(t.Context(), testOwnerUID, true)
	require.NoError(t, err)
	record := testNetworkAuthorityRecord(t, testOwnerUID, testNetworkID, testBootID)
	return &networkStoreFixture{
		root: root, policy: policy, storeRoot: storeRoot, uid: uid, record: record,
		recordPath: filepath.Join(root, "var/lib/mvmctl/networks/1000", testNetworkID+".json"),
	}
}

func secureNetworkTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	return root
}

func (fixture *networkStoreFixture) writeRaw(t *testing.T, raw []byte, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(fixture.recordPath, raw, mode))
}

func assertNetworkAuthorityErrorClass(t *testing.T, err error, class errs.Class) {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeNetworkError, domainErr.Code)
	assert.Equal(t, class, domainErr.Class)
}

func mustReadNetworkStoreEntries(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	return entries
}
