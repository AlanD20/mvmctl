package network

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

const testOtherBootID = "11111111-2222-4333-8444-555555555555"

type networkAuthorityFixture struct {
	root      string
	bootID    string
	tree      *testNetworkAuthorityTree
	authority *networkAuthority
}

func TestNetworkAuthorityRegisterPersistsExactOwnerBootAndBridge(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	want := testNetworkAuthorityRecord(t, testOwnerUID, testNetworkID, testBootID)
	got, err := fixture.authority.registerManagedNetwork(
		t.Context(), networkCaller{uid: testOwnerUID}, NetworkID(testNetworkID), want.managedBridge,
	)
	require.NoError(t, err)
	assertNetworkAuthorityRecordEqual(t, want, got)

	recordPath := fixture.recordPath(testOwnerUID, testNetworkID)
	raw, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	assert.Equal(t, canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID), string(raw))
	assertNetworkAuthorityMetadata(t, recordPath, 0600)
	for _, path := range []string{
		filepath.Join(fixture.root, "var/lib/mvmctl"),
		filepath.Join(fixture.root, "var/lib/mvmctl/networks"),
		filepath.Join(fixture.root, "var/lib/mvmctl/networks", fmt.Sprint(testOwnerUID)),
	} {
		assertNetworkAuthorityMetadata(t, path, 0700)
	}
	assert.Equal(t, []string{"acquire_index", "acquire_network", "release_network", "release_lock_uid",
		"release_lock_root", "release_index"}, fixture.tree.eventsSnapshot())
}

// Rationale: identity availability is globally serialized, but authorization discloses only the invoking UID's claim.
func TestNetworkAuthorityRegistrationIsIdempotentAndUIDOwned(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	bridge := testManagedBridge(t, testOwnerUID, testNetworkID)
	first, err := fixture.authority.registerManagedNetwork(
		t.Context(), networkCaller{uid: testOwnerUID}, NetworkID(testNetworkID), bridge,
	)
	require.NoError(t, err)
	firstInfo, err := os.Stat(fixture.recordPath(testOwnerUID, testNetworkID))
	require.NoError(t, err)
	second, err := fixture.authority.registerManagedNetwork(
		t.Context(), networkCaller{uid: testOwnerUID}, NetworkID(testNetworkID), bridge,
	)
	require.NoError(t, err)
	secondInfo, err := os.Stat(fixture.recordPath(testOwnerUID, testNetworkID))
	require.NoError(t, err)
	assertNetworkAuthorityRecordEqual(t, first, second)
	assert.True(t, os.SameFile(firstInfo, secondInfo), "idempotent registration must not replace the record")

	foreign, err := fixture.authority.registerManagedNetwork(
		t.Context(),
		networkCaller{uid: testOwnerUID + 1},
		NetworkID(testNetworkID),
		testManagedBridge(t, testOwnerUID+1, testNetworkID),
	)
	assert.Equal(t, networkAuthorityRecord{}, foreign)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeUnauthorized, domainErr.Code)
	assert.NotContains(t, err.Error(), fmt.Sprint(testOwnerUID))
	_, statErr := os.Stat(fixture.recordPath(testOwnerUID+1, testNetworkID))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestNetworkAuthorityRegistrationRejectsStaleBootAndDifferentExactBridge(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutateFixture func(*networkAuthorityFixture)
		mutateBridge  func(linkIdentity) linkIdentity
		wantCode      errs.Code
	}{
		"stale boot": {
			mutateFixture: func(fixture *networkAuthorityFixture) { fixture.bootID = testOtherBootID },
			mutateBridge:  func(bridge linkIdentity) linkIdentity { return bridge },
			wantCode:      errs.CodeNetworkError,
		},
		"different ifindex": {
			mutateFixture: func(*networkAuthorityFixture) {},
			mutateBridge: func(bridge linkIdentity) linkIdentity {
				bridge.ifIndex++
				return bridge
			},
			wantCode: errs.CodeNetworkAlreadyExists,
		},
		"different namespace": {
			mutateFixture: func(*networkAuthorityFixture) {},
			mutateBridge: func(bridge linkIdentity) linkIdentity {
				bridge.namespace.inode++
				return bridge
			},
			wantCode: errs.CodeNetworkAlreadyExists,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newNetworkAuthorityFixture(t, testBootID)
			bridge := testManagedBridge(t, testOwnerUID, testNetworkID)
			_, err := fixture.authority.registerManagedNetwork(
				t.Context(), networkCaller{uid: testOwnerUID}, NetworkID(testNetworkID), bridge,
			)
			require.NoError(t, err)
			tc.mutateFixture(fixture)
			got, err := fixture.authority.registerManagedNetwork(
				t.Context(),
				networkCaller{uid: testOwnerUID},
				NetworkID(testNetworkID),
				tc.mutateBridge(bridge),
			)
			assert.Equal(t, networkAuthorityRecord{}, got)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, tc.wantCode, domainErr.Code)
			assert.Equal(t, errs.ClassConflict, domainErr.Class)
			assert.NotContains(t, err.Error(), testBootID)
		})
	}
}

func TestNetworkAuthorityRegistrationRejectsCorruptOrDuplicateIndex(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *networkAuthorityFixture){
		"non-canonical owner directory": func(t *testing.T, fixture *networkAuthorityFixture) {
			require.NoError(t, os.MkdirAll(filepath.Join(fixture.stateRoot(), "01000"), 0700))
		},
		"corrupt foreign claim": func(t *testing.T, fixture *networkAuthorityFixture) {
			uidDir := fixture.prepareUIDDirectory(t, testOwnerUID)
			require.NoError(t, os.WriteFile(filepath.Join(uidDir, testNetworkID+".json"), []byte("{"), 0600))
		},
		"duplicate claims": func(t *testing.T, fixture *networkAuthorityFixture) {
			fixture.writeRecord(t, testNetworkAuthorityRecord(t, testOwnerUID, testNetworkID, testBootID))
			fixture.writeRecord(t, testNetworkAuthorityRecord(t, testOwnerUID+1, testNetworkID, testBootID))
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newNetworkAuthorityFixture(t, testBootID)
			arrange(t, fixture)
			got, err := fixture.authority.registerManagedNetwork(
				t.Context(),
				networkCaller{uid: testOwnerUID + 2},
				NetworkID(testNetworkID),
				testManagedBridge(t, testOwnerUID+2, testNetworkID),
			)
			assert.Equal(t, networkAuthorityRecord{}, got)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeNetworkError, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.NotContains(t, err.Error(), fmt.Sprint(testOwnerUID))
		})
	}
}

func TestNetworkAuthorityRegistrationRejectsUnsafeRecordWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, target string) {
			require.NoError(t, os.Symlink("attacker", target))
		},
		"wrong mode": func(t *testing.T, target string) {
			require.NoError(t, os.WriteFile(target, []byte(canonicalNetworkAuthorityJSON(
				testOwnerUID, testNetworkID, testBootID,
			)), 0644))
		},
		"hard link": func(t *testing.T, target string) {
			other := target + ".other"
			require.NoError(t, os.WriteFile(other, []byte(canonicalNetworkAuthorityJSON(
				testOwnerUID, testNetworkID, testBootID,
			)), 0600))
			require.NoError(t, os.Link(other, target))
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newNetworkAuthorityFixture(t, testBootID)
			fixture.prepareUIDDirectory(t, testOwnerUID)
			target := fixture.recordPath(testOwnerUID, testNetworkID)
			arrange(t, target)
			before, statErr := os.Lstat(target)
			require.NoError(t, statErr)
			got, err := fixture.authority.registerManagedNetwork(
				t.Context(),
				networkCaller{uid: testOwnerUID},
				NetworkID(testNetworkID),
				testManagedBridge(t, testOwnerUID, testNetworkID),
			)
			assert.Equal(t, networkAuthorityRecord{}, got)
			require.Error(t, err)
			after, statErr := os.Lstat(target)
			require.NoError(t, statErr)
			assert.Equal(t, before.Mode(), after.Mode())
			assert.Equal(t, before.Size(), after.Size())
		})
	}
}

func TestNetworkAuthorityValidatesBeforeEffects(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	var bootCalls atomic.Int32
	fixture.authority.storeDeps.currentBootID = func(context.Context) (string, error) {
		bootCalls.Add(1)
		return testBootID, nil
	}
	invalidBridge := testManagedBridge(t, testOwnerUID, testNetworkID)
	invalidBridge.name = "mvb000000000000"
	tests := []struct {
		caller    networkCaller
		networkID NetworkID
		bridge    linkIdentity
	}{
		{caller: networkCaller{}, networkID: NetworkID(testNetworkID), bridge: invalidBridge},
		{caller: networkCaller{uid: testOwnerUID}, networkID: NetworkID("bad"), bridge: invalidBridge},
		{caller: networkCaller{uid: testOwnerUID}, networkID: NetworkID(testNetworkID), bridge: invalidBridge},
	}
	for _, tc := range tests {
		_, err := fixture.authority.registerManagedNetwork(t.Context(), tc.caller, tc.networkID, tc.bridge)
		require.Error(t, err)
	}
	assert.Zero(t, bootCalls.Load())
	assert.Zero(t, fixture.tree.openCalls.Load())
}

func TestNetworkAuthorityObservesBootBeforeLockEffectsAndPreservesFailure(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	fault := errors.New("read boot identity failed")
	fixture.authority.storeDeps.currentBootID = func(context.Context) (string, error) { return "", fault }
	got, err := fixture.authority.registerManagedNetwork(
		t.Context(),
		networkCaller{uid: testOwnerUID},
		NetworkID(testNetworkID),
		testManagedBridge(t, testOwnerUID, testNetworkID),
	)
	assert.Equal(t, networkAuthorityRecord{}, got)
	assert.ErrorIs(t, err, fault)
	assert.Zero(t, fixture.tree.openCalls.Load())
}

func TestNetworkAuthorityReturnsCommittedRecordWithDurabilityUncertainty(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	fixture.prepareUIDDirectory(t, testOwnerUID)
	realOpenAt2 := fixture.authority.storeDeps.openAt2
	realFsync := fixture.authority.storeDeps.fsync
	var uidFD atomic.Int64
	fixture.authority.storeDeps.openAt2 = func(
		ctx context.Context, parentFD int, name string, how *unix.OpenHow,
	) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == fmt.Sprint(testOwnerUID) {
			uidFD.Store(int64(fd))
		}
		return fd, err
	}
	fault := errors.New("directory sync failed")
	fixture.authority.storeDeps.fsync = func(ctx context.Context, fd int) error {
		if int64(fd) == uidFD.Load() {
			return fault
		}
		return realFsync(ctx, fd)
	}
	want := testNetworkAuthorityRecord(t, testOwnerUID, testNetworkID, testBootID)
	got, err := fixture.authority.registerManagedNetwork(
		t.Context(), networkCaller{uid: testOwnerUID}, NetworkID(testNetworkID), want.managedBridge,
	)
	assertNetworkAuthorityRecordEqual(t, want, got)
	require.ErrorIs(t, err, fault)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_created"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	_, statErr := os.Stat(fixture.recordPath(testOwnerUID, testNetworkID))
	require.NoError(t, statErr)
}

func TestNetworkAuthorityRegistrationIndexWaitIsCancellable(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	heldRoot, err := fixture.tree.openNetwork(t.Context())
	require.NoError(t, err)
	held, err := heldRoot.acquireIndex(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, held.Release(context.Background())) })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	got, err := fixture.authority.registerManagedNetwork(
		ctx,
		networkCaller{uid: testOwnerUID},
		NetworkID(testNetworkID),
		testManagedBridge(t, testOwnerUID, testNetworkID),
	)
	assert.Equal(t, networkAuthorityRecord{}, got)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.ClassRetryable, domainErr.Class)
	_, statErr := os.Stat(fixture.recordPath(testOwnerUID, testNetworkID))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestNetworkAuthorityRegistrationPreservesLockStageFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject     func(*testNetworkAuthorityTree, error)
		wantEvents []string
	}{
		"open root": {
			inject:     func(tree *testNetworkAuthorityTree, fault error) { tree.openErr = fault },
			wantEvents: nil,
		},
		"acquire index": {
			inject:     func(tree *testNetworkAuthorityTree, fault error) { tree.acquireIndexErr = fault },
			wantEvents: []string{"release_lock_root"},
		},
		"open UID": {
			inject:     func(tree *testNetworkAuthorityTree, fault error) { tree.openUIDErr = fault },
			wantEvents: []string{"acquire_index", "release_lock_root", "release_index"},
		},
		"acquire network": {
			inject: func(tree *testNetworkAuthorityTree, fault error) {
				tree.acquireNetworkErr = fault
			},
			wantEvents: []string{
				"acquire_index", "release_lock_uid", "release_lock_root", "release_index",
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newNetworkAuthorityFixture(t, testBootID)
			fault := errors.New(name + " failed")
			tc.inject(fixture.tree, fault)
			got, err := fixture.authority.registerManagedNetwork(
				t.Context(),
				networkCaller{uid: testOwnerUID},
				NetworkID(testNetworkID),
				testManagedBridge(t, testOwnerUID, testNetworkID),
			)
			assert.Equal(t, networkAuthorityRecord{}, got)
			assert.ErrorIs(t, err, fault)
			assert.Equal(t, tc.wantEvents, fixture.tree.eventsSnapshot())
			_, statErr := os.Stat(fixture.recordPath(testOwnerUID, testNetworkID))
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

// Rationale: cancellation must not skip cleanup or invert the global-to-resource authority acquisition order.
func TestNetworkAuthorityRegistrationReleasesInReverseOrderWithCleanupContext(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	recorder := fixture.tree.recorder
	realOpenAt2 := fixture.authority.storeDeps.openAt2
	realClose := fixture.authority.storeDeps.close
	realWrite := fixture.authority.storeDeps.write
	var storeRootFD, storeUIDFD atomic.Int64
	fixture.authority.storeDeps.openAt2 = func(
		ctx context.Context, parentFD int, name string, how *unix.OpenHow,
	) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == "networks" {
			storeRootFD.Store(int64(fd))
		}
		if err == nil && name == fmt.Sprint(testOwnerUID) {
			storeUIDFD.Store(int64(fd))
		}
		return fd, err
	}
	fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
		switch int64(fd) {
		case storeUIDFD.Load():
			recorder.addContext(ctx, "release_store_uid")
		case storeRootFD.Load():
			recorder.addContext(ctx, "release_store_root")
		}
		return realClose(ctx, fd)
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), testAuthorityContextKey{}, "retained"))
	writeFault := errors.New("write failed")
	fixture.authority.storeDeps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
		cancel()
		_, _ = realWrite(ctx, fd, value[:min(len(value), 1)])
		return 0, writeFault
	}
	got, err := fixture.authority.registerManagedNetwork(
		ctx,
		networkCaller{uid: testOwnerUID},
		NetworkID(testNetworkID),
		testManagedBridge(t, testOwnerUID, testNetworkID),
	)
	assert.Equal(t, networkAuthorityRecord{}, got)
	require.ErrorIs(t, err, writeFault)
	assert.Equal(t, []string{
		"acquire_index", "acquire_network", "release_network", "release_lock_uid", "release_store_uid",
		"release_store_root", "release_lock_root", "release_index",
	}, recorder.snapshot())
	assert.True(t, recorder.allCleanupContextsSafe())
}

func TestNetworkAuthorityRegistrationPreservesPrimaryAndEveryCleanupFault(t *testing.T) {
	t.Parallel()

	fixture := newNetworkAuthorityFixture(t, testBootID)
	primary := errors.New("record write failed")
	fixture.authority.storeDeps.write = func(context.Context, int, []byte) (int, error) { return 0, primary }
	fixture.tree.releaseNetworkErr = errors.New("release network failed")
	fixture.tree.releaseUIDErr = errors.New("release UID failed")
	fixture.tree.releaseRootErr = errors.New("release root failed")
	fixture.tree.releaseIndexErr = errors.New("release index failed")
	_, err := fixture.authority.registerManagedNetwork(
		t.Context(),
		networkCaller{uid: testOwnerUID},
		NetworkID(testNetworkID),
		testManagedBridge(t, testOwnerUID, testNetworkID),
	)
	for _, want := range []error{
		primary, fixture.tree.releaseNetworkErr, fixture.tree.releaseUIDErr,
		fixture.tree.releaseRootErr, fixture.tree.releaseIndexErr,
	} {
		assert.ErrorIs(t, err, want)
	}
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeNetworkError, domainErr.Code)
}

// Rationale: concurrent registrants must converge on one immutable record without duplicate cross-UID ownership.
func TestNetworkAuthorityConcurrentRegistrationSerializesClaims(t *testing.T) {
	t.Parallel()

	t.Run("same owner is idempotent", func(t *testing.T) {
		fixture := newNetworkAuthorityFixture(t, testBootID)
		const workers = 24
		results := make(chan networkAuthorityRecord, workers)
		errorsFound := make(chan error, workers)
		var wait sync.WaitGroup
		bridge := testManagedBridge(t, testOwnerUID, testNetworkID)
		for range workers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				record, err := fixture.authority.registerManagedNetwork(
					t.Context(),
					networkCaller{uid: testOwnerUID},
					NetworkID(testNetworkID),
					bridge,
				)
				if err != nil {
					errorsFound <- err
					return
				}
				results <- record
			}()
		}
		wait.Wait()
		close(errorsFound)
		close(results)
		assert.Empty(t, errorsFound)
		assert.Len(t, results, workers)
		entries, err := os.ReadDir(filepath.Dir(fixture.recordPath(testOwnerUID, testNetworkID)))
		require.NoError(t, err)
		assert.Len(t, entries, 1)
	})

	t.Run("different owners produce exactly one claim", func(t *testing.T) {
		fixture := newNetworkAuthorityFixture(t, testBootID)
		const workers = 20
		var successes atomic.Int32
		var unauthorized atomic.Int32
		var unexpected atomic.Int32
		var wait sync.WaitGroup
		bridges := make([]linkIdentity, workers)
		for index := range workers {
			bridges[index] = testManagedBridge(t, testOwnerUID+uint32(index), testNetworkID)
		}
		for index := range workers {
			wait.Add(1)
			go func(uid uint32) {
				defer wait.Done()
				_, err := fixture.authority.registerManagedNetwork(
					t.Context(),
					networkCaller{uid: uid},
					NetworkID(testNetworkID),
					bridges[index],
				)
				if err == nil {
					successes.Add(1)
					return
				}
				domainErr := errs.AsDomainError(err)
				if domainErr != nil && domainErr.Code == errs.CodeUnauthorized {
					unauthorized.Add(1)
					return
				}
				unexpected.Add(1)
			}(testOwnerUID + uint32(index))
		}
		wait.Wait()
		assert.Equal(t, int32(1), successes.Load())
		assert.Equal(t, int32(workers-1), unauthorized.Load())
		assert.Zero(t, unexpected.Load())
		matches, err := filepath.Glob(filepath.Join(fixture.stateRoot(), "*", testNetworkID+".json"))
		require.NoError(t, err)
		assert.Len(t, matches, 1)
	})
}

func newNetworkAuthorityFixture(t *testing.T, bootID string) *networkAuthorityFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
	tree := newTestNetworkAuthorityTree()
	fixture := &networkAuthorityFixture{root: root, bootID: bootID, tree: tree}
	deps := realNetworkStoreDeps()
	deps.currentBootID = func(context.Context) (string, error) { return fixture.bootID, nil }
	fixture.authority = newNetworkAuthorityWithPolicy(
		deps,
		networkStorePolicy{
			rootPath: root, stateRoot: "/var/lib/mvmctl/networks",
			expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
		},
		tree,
	)
	return fixture
}

func (fixture *networkAuthorityFixture) stateRoot() string {
	return filepath.Join(fixture.root, "var/lib/mvmctl/networks")
}

func (fixture *networkAuthorityFixture) recordPath(uid uint32, networkID string) string {
	return filepath.Join(fixture.stateRoot(), fmt.Sprint(uid), networkID+".json")
}

func (fixture *networkAuthorityFixture) prepareUIDDirectory(t *testing.T, uid uint32) string {
	t.Helper()
	uidDir := filepath.Join(fixture.stateRoot(), fmt.Sprint(uid))
	require.NoError(t, os.MkdirAll(uidDir, 0700))
	for _, path := range []string{
		filepath.Join(fixture.root, "var/lib/mvmctl"), fixture.stateRoot(), uidDir,
	} {
		require.NoError(t, os.Chmod(path, 0700))
	}
	return uidDir
}

func (fixture *networkAuthorityFixture) writeRecord(t *testing.T, record networkAuthorityRecord) {
	t.Helper()
	uidDir := fixture.prepareUIDDirectory(t, record.ownerUID)
	raw, err := encodeNetworkAuthorityRecord(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(uidDir, string(record.networkID)+".json"), raw, 0600))
}

func assertNetworkAuthorityMetadata(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, mode, info.Mode().Perm())
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	assert.Equal(t, uint32(os.Geteuid()), stat.Uid)
	assert.Equal(t, uint32(os.Getegid()), stat.Gid)
	if mode == 0600 {
		assert.Equal(t, uint64(1), stat.Nlink)
	}
}

func assertNetworkAuthorityRecordEqual(t *testing.T, want, got networkAuthorityRecord) {
	t.Helper()
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(
		networkAuthorityRecord{}, linkIdentity{}, namespaceIdentity{},
	)); diff != "" {
		t.Errorf("network authority record mismatch (-want +got):\n%s", diff)
	}
}

type testAuthorityContextKey struct{}

type testAuthorityEventRecorder struct {
	mu       sync.Mutex
	events   []string
	contexts []bool
}

func (recorder *testAuthorityEventRecorder) add(event string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
}

func (recorder *testAuthorityEventRecorder) addContext(ctx context.Context, event string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	recorder.contexts = append(recorder.contexts,
		ctx.Err() == nil && ctx.Value(testAuthorityContextKey{}) == "retained")
}

func (recorder *testAuthorityEventRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.events...)
}

func (recorder *testAuthorityEventRecorder) allCleanupContextsSafe() bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(recorder.contexts) > 0 && !slices.Contains(recorder.contexts, false)
}

type testNetworkAuthorityTree struct {
	mu                sync.Mutex
	index             chan struct{}
	networks          map[string]chan struct{}
	recorder          *testAuthorityEventRecorder
	openCalls         atomic.Int32
	openErr           error
	acquireIndexErr   error
	openUIDErr        error
	acquireNetworkErr error
	releaseNetworkErr error
	releaseUIDErr     error
	releaseRootErr    error
	releaseIndexErr   error
}

func newTestNetworkAuthorityTree() *testNetworkAuthorityTree {
	index := make(chan struct{}, 1)
	index <- struct{}{}
	return &testNetworkAuthorityTree{
		index: index, networks: make(map[string]chan struct{}), recorder: &testAuthorityEventRecorder{},
	}
}

func (tree *testNetworkAuthorityTree) openNetwork(context.Context) (networkLockRoot, error) {
	tree.openCalls.Add(1)
	if tree.openErr != nil {
		return nil, tree.openErr
	}
	return &testNetworkLockRoot{tree: tree}, nil
}

func (tree *testNetworkAuthorityTree) eventsSnapshot() []string { return tree.recorder.snapshot() }

type testNetworkLockRoot struct{ tree *testNetworkAuthorityTree }

func (root *testNetworkLockRoot) acquireIndex(ctx context.Context) (networkAuthorityLock, error) {
	if root.tree.acquireIndexErr != nil {
		return nil, root.tree.acquireIndexErr
	}
	return root.tree.acquire(ctx, root.tree.index, "index", root.tree.releaseIndexErr)
}

func (root *testNetworkLockRoot) openUID(_ context.Context, uid uint32) (networkLockUID, error) {
	if root.tree.openUIDErr != nil {
		return nil, root.tree.openUIDErr
	}
	return &testNetworkLockUID{tree: root.tree, uid: uid}, nil
}

func (root *testNetworkLockRoot) Release(ctx context.Context) error {
	root.tree.recorder.addContext(ctx, "release_lock_root")
	return root.tree.releaseRootErr
}

type testNetworkLockUID struct {
	tree *testNetworkAuthorityTree
	uid  uint32
}

func (uid *testNetworkLockUID) acquireNetwork(
	ctx context.Context,
	key authorityfs.NetworkKey,
) (networkAuthorityLock, error) {
	if uid.tree.acquireNetworkErr != nil {
		return nil, uid.tree.acquireNetworkErr
	}
	mapKey := fmt.Sprintf("%d:%#v", uid.uid, key)
	uid.tree.mu.Lock()
	gate := uid.tree.networks[mapKey]
	if gate == nil {
		gate = make(chan struct{}, 1)
		gate <- struct{}{}
		uid.tree.networks[mapKey] = gate
	}
	uid.tree.mu.Unlock()
	return uid.tree.acquire(ctx, gate, "network", uid.tree.releaseNetworkErr)
}

func (uid *testNetworkLockUID) Release(ctx context.Context) error {
	uid.tree.recorder.addContext(ctx, "release_lock_uid")
	return uid.tree.releaseUIDErr
}

func (tree *testNetworkAuthorityTree) acquire(
	ctx context.Context,
	gate chan struct{},
	description string,
	releaseErr error,
) (networkAuthorityLock, error) {
	select {
	case <-ctx.Done():
		return nil, mapNetworkAuthorityLockError(description, ctx.Err())
	case <-gate:
	}
	tree.recorder.add("acquire_" + description)
	return &testNetworkLock{tree: tree, gate: gate, description: description, releaseErr: releaseErr}, nil
}

type testNetworkLock struct {
	tree        *testNetworkAuthorityTree
	gate        chan struct{}
	description string
	releaseErr  error
	once        sync.Once
}

func (lock *testNetworkLock) Release(ctx context.Context) error {
	lock.once.Do(func() {
		lock.tree.recorder.addContext(ctx, "release_"+lock.description)
		lock.gate <- struct{}{}
	})
	return lock.releaseErr
}
