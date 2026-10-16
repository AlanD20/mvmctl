package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

const lineageTestKernelID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
const lineageTestBytes = "independent immutable kernel fixture bytes"

// These tests own real root-private metadata and caller-owned files beneath t.TempDir.
// Import, kernel retirement and store retirement use production effects; no receipt is fabricated.
type kernelLineageFixture struct {
	root, owner string
	caches      [2]*managedCacheLease
	paths       [2]string
	anchors     [2]string
}

func newKernelLineageFixture(t *testing.T) *kernelLineageFixture {
	t.Helper()
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("protected kernel lineage requires the disposable root-test VM")
	}
	f := &kernelLineageFixture{root: t.TempDir()}
	f.owner = filepath.Join(f.root, "var", "lib", "mvmctl", "protected-backing", "1000")
	require.NoError(t, os.MkdirAll(f.owner, 0700))
	for i := range f.caches {
		f.paths[i] = filepath.Join(f.root, []string{"first-cache", "second-cache"}[i])
		require.NoError(t, os.Mkdir(f.paths[i], 0700))
		require.NoError(t, os.Chown(f.paths[i], 1000, 1000))
		f.caches[i] = openKernelLineageCache(t, f.paths[i])
		f.anchors[i] = filepath.Join(f.owner, protectedCacheKey(f.caches[i].identity))
		require.NoError(t, os.Mkdir(f.anchors[i], 0700))
		f.source(t, i, []byte(lineageTestBytes))
	}
	return f
}

func openKernelLineageCache(t *testing.T, path string) *managedCacheLease {
	t.Helper()
	fd := openProtectedBranchTestFD(t, path)
	t.Cleanup(func() { require.NoError(t, unix.Close(fd)) })
	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &stat))
	locator, err := parseManagedCacheLocator(path)
	require.NoError(t, err)
	return &managedCacheLease{cacheFD: fd, ownerUID: 1000, ownerGID: 1000, locator: locator,
		identity: managedCacheIdentity{deviceMajor: unix.Major(uint64(stat.Dev)),
			deviceMinor: unix.Minor(uint64(stat.Dev)), inode: stat.Ino},
		deps: realManagedCacheDeps(), configDeps: realManagedFirecrackerConfigDeps(), retained: []int{fd}}
}

func (f *kernelLineageFixture) source(t *testing.T, i int, content []byte) {
	t.Helper()
	directory := filepath.Join(f.paths[i], "kernels")
	require.NoError(t, os.MkdirAll(directory, 0700))
	require.NoError(t, os.Chown(directory, 1000, 1000))
	path := filepath.Join(directory, lineageTestKernelID)
	require.NoError(t, os.WriteFile(path, content, 0700))
	require.NoError(t, os.Chown(path, 1000, 1000))
}

func (f *kernelLineageFixture) importKernel(
	t *testing.T, ctx context.Context, i int, explicit bool,
) (*protectedBackingLease, error) {
	t.Helper()
	owner := openProtectedBranchTestFD(t, f.owner)
	anchor := openProtectedBranchTestFD(t, f.anchors[i])
	return protectBackingInAnchor(ctx, f.caches[i], protectedBackingKernel, lineageTestKernelID, 0,
		true, -1, explicit, anchor, []int{owner, anchor})
}

func (f *kernelLineageFixture) retireKernel(t *testing.T) {
	t.Helper()
	lease, err := f.importKernel(t, t.Context(), 0, true)
	require.NoError(t, err)
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func (f *kernelLineageFixture) retireCache(t *testing.T) {
	t.Helper()
	owner := openProtectedBranchTestFD(t, f.owner)
	anchor := openProtectedBranchTestFD(t, f.anchors[0])
	require.NoError(t, retireProtectedCache(t.Context(), f.caches[0], anchor, owner))
	require.NoError(t, closeProtectedFDs([]int{owner, anchor}))
}

func (f *kernelLineageFixture) logicalPath() string {
	return filepath.Join(f.owner, "logical", protectedBackingKernel, lineageTestKernelID)
}

func (f *kernelLineageFixture) logical(t *testing.T) protectedLogicalRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.logicalPath(), "logical.json"))
	require.NoError(t, err)
	var record protectedLogicalRecord
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

func (f *kernelLineageFixture) disk(i int, leaf string) string {
	return filepath.Join(f.paths[i], protectedBackingDirectory, protectedBackingKernel, lineageTestKernelID, leaf)
}

func (f *kernelLineageFixture) assertCurrent(t *testing.T, i int, generation uint64) {
	t.Helper()
	content, err := os.ReadFile(f.disk(i, "disk"))
	require.NoError(t, err)
	assertKernelLineageDiff(t, []byte(lineageTestBytes), content)
	logical := f.logical(t)
	assert.Equal(t, uint32(2), logical.Version)
	require.NotNil(t, logical.Kernel)
	assert.Nil(t, logical.Kernel.Pending)
	assert.Equal(t, generation, logical.Kernel.LastGeneration)
	require.NotNil(t, logical.Kernel.Current)
	digest := sha256.Sum256([]byte(lineageTestBytes)) // Independent fixed-input digest, not production hashing.
	assert.Equal(t, hex.EncodeToString(digest[:]), logical.Kernel.Current.Digest)
	assert.Equal(t, generation, logical.Kernel.Current.Generation)
	assertKernelLineageDiff(t, protectedCacheIdentity(f.caches[i].identity), logical.Kernel.Current.Cache)
	assert.Equal(t, "ready", logical.Kernel.Current.Phase)
	var stat unix.Stat_t
	require.NoError(t, unix.Stat(f.disk(i, "disk"), &stat))
	assertKernelLineageDiff(t, protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size},
		logical.Kernel.Current.Backing)
	require.NoError(t, unix.Stat(filepath.Join(f.paths[i], protectedBackingDirectory), &stat))
	assertKernelLineageDiff(
		t,
		protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino},
		protectedBackingIdentity{
			Device: logical.Kernel.Current.Store.Device,
			Inode:  logical.Kernel.Current.Store.Inode,
		},
	)
}

func assertKernelLineageDiff(t *testing.T, want, got any) {
	t.Helper()
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(managedCacheIdentity{})); diff != "" {
		t.Errorf("kernel lineage contract (-want +got):\n%s", diff)
	}
}

func assertKernelLineageTree(t *testing.T, want map[string]protectedCacheTestNode, path string) {
	t.Helper()
	if diff := cmp.Diff(want, protectedCacheTestTree(t, path)); diff != "" {
		t.Errorf("retained authority changed (-want +got):\n%s", diff)
	}
}

func TestProtectedKernelSuccessorAcrossRetiredCachePreservesAuthority(t *testing.T) {
	// Primary regression: schema-1's permanent cache binding refuses the second actual import.
	f := newKernelLineageFixture(t)
	f.retireKernel(t)
	f.retireCache(t)
	old := protectedCacheTestTree(t, f.anchors[0])
	// The old cache is not required for admission. Remove only fixture-owned, known-empty leaves.
	require.NoError(t, os.Remove(filepath.Join(f.paths[0], ProtectedCacheQuarantineDirectory)))
	require.NoError(t, os.Remove(filepath.Join(f.paths[0], "kernels", lineageTestKernelID)))
	require.NoError(t, os.Remove(filepath.Join(f.paths[0], "kernels")))
	require.NoError(t, os.Remove(f.paths[0]))
	lease, err := f.importKernel(t, t.Context(), 1, true)
	require.NoError(t, err)
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 1, 2)
	// VM grant inventory must follow the new current cache, not the immutable origin anchor.
	owner := openProtectedBranchTestFD(t, f.owner)
	kind := openProtectedBranchTestFD(t, filepath.Dir(f.logicalPath()))
	entry, present, err := readProtectedInventoryEntry(t.Context(), owner, kind, 1000,
		protectedBackingKernel, lineageTestKernelID)
	require.NoError(t, err)
	require.True(t, present)
	assertKernelLineageDiff(t, f.caches[1].identity, entry.binding.cacheIdentity)
	assert.Equal(t, uint64(2), entry.binding.generation)
	require.NoError(t, closeProtectedFDs([]int{owner, kind}))
	assertKernelLineageTree(t, old, f.anchors[0])
	var receipt protectedKernelReceipt
	data, err := os.ReadFile(filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000001.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &receipt))
	assert.Equal(t, uint32(2), receipt.Version)
	assert.Equal(t, uint32(1000), receipt.Owner)
	assert.Equal(t, lineageTestKernelID, receipt.ID)
	assert.Equal(t, "removed", receipt.Binding.Phase)
	assert.Equal(t, uint64(1), receipt.Binding.Generation)
	assertKernelLineageDiff(t, protectedCacheIdentity(f.caches[0].identity), receipt.Binding.Cache)
	digest := sha256.Sum256([]byte(lineageTestBytes))
	assert.Equal(t, hex.EncodeToString(digest[:]), receipt.Binding.Digest)
}

func TestProtectedKernelSuccessorRequiresBothRetirements(t *testing.T) {
	f := newKernelLineageFixture(t)
	f.retireKernel(t)
	old := protectedCacheTestTree(t, f.anchors[0])
	logical := protectedCacheTestTree(t, f.logicalPath())
	lease, err := f.importKernel(t, t.Context(), 1, true)
	require.Nil(t, lease)
	require.Error(t, err)
	var domain *errs.DomainError
	require.True(t, errors.As(err, &domain))
	assert.Equal(t, errs.CodeKernelImportFailed, domain.Code)
	assert.Equal(t, errs.ClassConflict, domain.Class)
	assert.Equal(t, true, domain.Details["predecessor_store_retirement_required"])
	assertKernelLineageTree(t, old, f.anchors[0])
	assertKernelLineageTree(t, logical, f.logicalPath())
	_, err = os.Stat(f.disk(1, "disk"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	f.retireCache(t)
	lease, err = f.importKernel(t, t.Context(), 1, true)
	require.NoError(t, err)
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 1, 2)
}

func TestProtectedKernelSuccessorCannotMoveLiveKernel(t *testing.T) {
	f := newKernelLineageFixture(t)
	first, err := f.importKernel(t, t.Context(), 0, true)
	require.NoError(t, err)
	require.NoError(t, first.Release(t.Context()))
	logical := protectedCacheTestTree(t, f.logicalPath())
	old := protectedCacheTestTree(t, f.anchors[0])
	lease, err := f.importKernel(t, t.Context(), 1, true)
	require.Nil(t, lease)
	require.Error(t, err)
	assertKernelLineageTree(t, logical, f.logicalPath())
	assertKernelLineageTree(t, old, f.anchors[0])
	f.assertCurrent(t, 0, 1)
	_, err = os.Stat(f.disk(1, "disk"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestProtectedKernelSuccessorChangedBytesAbortConsumesGeneration(t *testing.T) {
	f := newKernelLineageFixture(t)
	f.retireKernel(t)
	f.retireCache(t)
	old := protectedCacheTestTree(t, f.anchors[0])
	receipt, err := os.ReadFile(filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000001.json"))
	require.NoError(t, err)
	f.source(t, 1, []byte("different same-ID bytes"))
	caller := protectedCacheTestTree(t, filepath.Join(f.paths[1], "kernels"))
	lease, err := f.importKernel(t, t.Context(), 1, true)
	require.Nil(t, lease)
	require.Error(t, err)
	logical := f.logical(t)
	assert.Nil(t, logical.Kernel.Pending)
	assert.Equal(t, uint64(2), logical.Kernel.LastGeneration)
	assert.Equal(t, uint64(1), logical.Kernel.Current.Generation)
	assert.Equal(t, "removed", logical.Kernel.Current.Phase)
	assertKernelLineageTree(t, caller, filepath.Join(f.paths[1], "kernels"))
	assertKernelLineageTree(t, old, f.anchors[0])
	for _, leaf := range []string{"disk", "pending"} {
		_, err = os.Stat(f.disk(1, leaf))
		assert.ErrorIs(t, err, os.ErrNotExist)
	}
	var aborted protectedKernelReceipt
	data, err := os.ReadFile(filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000002.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &aborted))
	assert.Equal(t, "aborted", aborted.Binding.Phase)
	assert.Equal(t, uint64(2), aborted.Binding.Generation)
	assertKernelLineageDiff(t, protectedCacheIdentity(f.caches[1].identity), aborted.Binding.Cache)
	f.source(t, 1, []byte(lineageTestBytes))
	lease, err = f.importKernel(t, t.Context(), 1, true)
	require.NoError(t, err)
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 1, 3)
	data, err = os.ReadFile(filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000001.json"))
	require.NoError(t, err)
	assertKernelLineageDiff(t, receipt, data)
	assertKernelLineageTree(t, old, f.anchors[0])
}

func TestProtectedKernelSuccessorRejectsLegacyAndExhaustedAuthority(t *testing.T) {
	for _, failure := range []string{"legacy-schema", "generation-exhausted"} {
		t.Run(failure, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			f.retireKernel(t)
			f.retireCache(t)
			logical := f.logical(t)
			if failure == "legacy-schema" {
				logical.Version, logical.Kernel = 1, nil
			} else {
				logical.Kernel.LastGeneration = math.MaxUint64
			}
			fd := openProtectedBranchTestFD(t, f.logicalPath())
			require.NoError(t, writeProtectedRecord(t.Context(), fd, "logical.json", logical))
			require.NoError(t, unix.Close(fd))
			before := protectedCacheTestTree(t, f.logicalPath())
			old := protectedCacheTestTree(t, f.anchors[0])
			lease, err := f.importKernel(t, t.Context(), 1, true)
			require.Nil(t, lease)
			require.Error(t, err)
			assertKernelLineageTree(t, before, f.logicalPath())
			assertKernelLineageTree(t, old, f.anchors[0])
			_, err = os.Stat(f.disk(1, "disk"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

// A test-process receiver exits only after observing a real production-written protocol stage.
// No production fault seam or fabricated pending record is needed. This proves receiver-death
// replay, not power-loss durability. All effects remain in the parent's private filesystem.
type kernelLineageCrashContext struct {
	context.Context
	logical, metadata, stage string
}

func (ctx kernelLineageCrashContext) Err() error {
	logicalBytes, _ := os.ReadFile(ctx.logical)
	var logical protectedLogicalRecord
	_ = json.Unmarshal(logicalBytes, &logical)
	if logical.Kernel != nil && logical.Kernel.Pending != nil {
		pending := logical.Kernel.Pending
		metadataBytes, _ := os.ReadFile(ctx.metadata)
		var backing protectedBackingRecord
		_ = json.Unmarshal(metadataBytes, &backing)
		matched := ctx.stage == "intent" && pending.Phase == "preparing" ||
			ctx.stage == "directory" && pending.Phase == "copying" && pending.Directory.Inode != 0 &&
				len(metadataBytes) == 0 ||
			ctx.stage == "candidate" && backing.Backing.Inode != 0 && backing.Phase == "kernel-reimport-copying" ||
			ctx.stage == "copied" && backing.Phase == "kernel-reimport-copied" ||
			ctx.stage == "published" && backing.Phase == "ready"
		if matched {
			os.Exit(73) // Abrupt death: no deferred import abort or descriptor release runs.
		}
	}
	return ctx.Context.Err()
}

func TestProtectedKernelSuccessorCrashWorker(t *testing.T) {
	root := os.Getenv("MVM_KERNEL_LINEAGE_TEST_ROOT")
	if root == "" {
		t.Skip("private receiver-death subprocess only")
	}
	cachePath := filepath.Join(root, "second-cache")
	cache := openKernelLineageCache(t, cachePath)
	ownerPath := filepath.Join(root, "var", "lib", "mvmctl", "protected-backing", "1000")
	anchorPath := filepath.Join(ownerPath, protectedCacheKey(cache.identity))
	ctx := kernelLineageCrashContext{Context: t.Context(), stage: os.Getenv("MVM_KERNEL_LINEAGE_TEST_STAGE"),
		logical:  filepath.Join(ownerPath, "logical", protectedBackingKernel, lineageTestKernelID, "logical.json"),
		metadata: filepath.Join(anchorPath, protectedBackingKernel, lineageTestKernelID, "record.json")}
	owner, anchor := openProtectedBranchTestFD(t, ownerPath), openProtectedBranchTestFD(t, anchorPath)
	lease, err := protectBackingInAnchor(ctx, cache, protectedBackingKernel, lineageTestKernelID, 0, true,
		-1, true, anchor, []int{owner, anchor})
	if lease != nil {
		require.NoError(t, lease.Release(t.Context()))
	}
	t.Fatalf("receiver never reached requested crash stage: %v", err)
}

func (f *kernelLineageFixture) crash(t *testing.T, stage string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	result, err := system.DefaultRunner.Run(t.Context(), []string{executable,
		"-test.run=^TestProtectedKernelSuccessorCrashWorker$"}, system.RunCmdOpts{
		Capture: true, Timeout: 20 * time.Second, MaxStdoutBytes: 4096, MaxStderrBytes: 4096,
		Env: map[string]string{"MVM_KERNEL_LINEAGE_TEST_ROOT": f.root, "MVM_KERNEL_LINEAGE_TEST_STAGE": stage},
	})
	require.NotNil(t, result, "%v", err)
	require.Equal(t, 73, result.ExitCode, "receiver stdout=%s stderr=%s error=%v", result.Stdout, result.Stderr, err)
}

func TestProtectedKernelSuccessorReceiverDeathReplaysExactGeneration(t *testing.T) {
	for _, stage := range []string{"intent", "directory", "candidate", "copied", "published"} {
		t.Run(stage, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			f.retireKernel(t)
			f.retireCache(t)
			old := protectedCacheTestTree(t, f.anchors[0])
			f.crash(t, stage)
			logical := f.logical(t)
			require.NotNil(t, logical.Kernel.Pending)
			assert.Equal(t, uint64(2), logical.Kernel.Pending.Generation)
			assert.Equal(t, uint64(1), logical.Kernel.Pending.Predecessor)
			assertKernelLineageDiff(t, protectedCacheIdentity(f.caches[1].identity), logical.Kernel.Pending.Cache)
			before := protectedCacheTestTree(t, f.logicalPath())
			// Ordinary launch/restore admission must not complete or expose this explicit transaction.
			lease, err := f.importKernel(t, t.Context(), 1, false)
			require.Nil(t, lease)
			require.Error(t, err)
			assertKernelLineageTree(t, before, f.logicalPath())
			lease, err = f.importKernel(t, t.Context(), 1, true)
			require.NoError(t, err)
			require.NoError(t, lease.Release(t.Context()))
			f.assertCurrent(t, 1, 2)
			assertKernelLineageTree(t, old, f.anchors[0])
		})
	}
}

func TestProtectedKernelSuccessorMissingOrReplacedCandidateCannotAuthorizeAbsence(t *testing.T) {
	for _, failure := range []string{"missing", "replaced"} {
		t.Run(failure, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			f.retireKernel(t)
			f.retireCache(t)
			f.crash(t, "candidate")
			pending := f.disk(1, "pending")
			retained := filepath.Join(f.root, "original-candidate")
			require.NoError(t, os.Rename(pending, retained))
			if failure == "replaced" {
				require.NoError(t, os.WriteFile(pending, []byte("unrelated inode must survive"), 0600))
			}
			caller := protectedCacheTestTree(t, filepath.Join(f.paths[1], "kernels"))
			old := protectedCacheTestTree(t, f.anchors[0])
			data := protectedCacheTestTree(t, filepath.Dir(pending))
			lease, err := f.importKernel(t, t.Context(), 1, true)
			require.Nil(t, lease)
			require.Error(t, err)
			logical := f.logical(t)
			require.NotNil(t, logical.Kernel.Pending)
			assert.Equal(t, uint64(2), logical.Kernel.Pending.Generation)
			assert.Equal(t, uint64(1), logical.Kernel.Current.Generation)
			assert.Equal(t, "removed", logical.Kernel.Current.Phase)
			assertKernelLineageTree(t, data, filepath.Dir(pending))
			assertKernelLineageTree(t, caller, filepath.Join(f.paths[1], "kernels"))
			assertKernelLineageTree(t, old, f.anchors[0])
			_, err = os.Stat(filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000002.json"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestProtectedKernelSuccessorHeldLeaseExcludesCompetingImportAndCacheRetirement(t *testing.T) {
	f := newKernelLineageFixture(t)
	f.retireKernel(t)
	f.retireCache(t)
	lease, err := f.importKernel(t, t.Context(), 1, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	before := protectedCacheTestTree(t, f.logicalPath())
	competing, err := f.importKernel(t, t.Context(), 1, true)
	require.Nil(t, competing)
	require.Error(t, err)
	owner, anchor := openProtectedBranchTestFD(t, f.owner), openProtectedBranchTestFD(t, f.anchors[1])
	assert.Error(t, retireProtectedCache(t.Context(), f.caches[1], anchor, owner))
	require.NoError(t, closeProtectedFDs([]int{owner, anchor}))
	assertKernelLineageTree(t, before, f.logicalPath())
	f.assertCurrent(t, 1, 2)
}

type kernelLineageCancelContext struct {
	context.Context
	cancel context.CancelFunc
	path   string
}

func (ctx kernelLineageCancelContext) Err() error {
	// Cancel only after actual copy effects, independent of reflink versus buffered copying.
	if stat, err := os.Stat(ctx.path); err == nil && stat.Size() > 0 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestProtectedKernelSuccessorCancellationRetainsReceiptAndCallerBytes(t *testing.T) {
	f := newKernelLineageFixture(t)
	f.retireKernel(t)
	f.retireCache(t)
	old := protectedCacheTestTree(t, f.anchors[0])
	caller := protectedCacheTestTree(t, filepath.Join(f.paths[1], "kernels"))
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := kernelLineageCancelContext{Context: base, cancel: cancel, path: f.disk(1, "pending")}
	lease, err := f.importKernel(t, ctx, 1, true)
	require.Nil(t, lease)
	require.ErrorIs(t, err, context.Canceled)
	logical := f.logical(t)
	assert.Nil(t, logical.Kernel.Pending)
	assert.Equal(t, uint64(2), logical.Kernel.LastGeneration)
	assert.Equal(t, uint64(1), logical.Kernel.Current.Generation)
	assertKernelLineageTree(t, caller, filepath.Join(f.paths[1], "kernels"))
	assertKernelLineageTree(t, old, f.anchors[0])
	for _, leaf := range []string{"disk", "pending"} {
		_, err = os.Stat(f.disk(1, leaf))
		assert.ErrorIs(t, err, os.ErrNotExist)
	}
	var aborted protectedKernelReceipt
	data, err := os.ReadFile(filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000002.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &aborted))
	assert.Equal(t, "aborted", aborted.Binding.Phase)
	assert.Equal(t, uint64(2), aborted.Binding.Generation)
	lease, err = f.importKernel(t, t.Context(), 1, true)
	require.NoError(t, err)
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 1, 3)
}

func TestProtectedKernelSuccessorSameCacheRejectsStaleBinding(t *testing.T) {
	f := newKernelLineageFixture(t)
	first, err := f.importKernel(t, t.Context(), 0, true)
	require.NoError(t, err)
	binding, err := first.Binding(t.Context())
	require.NoError(t, err)
	require.NoError(t, first.Remove(t.Context()))
	require.NoError(t, first.Release(t.Context()))
	lease, err := f.importKernel(t, t.Context(), 0, true)
	require.NoError(t, err)
	assert.Error(t, lease.requireBinding(binding))
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 0, 2)
}

func (f *kernelLineageFixture) publicationLease(t *testing.T) *protectedBackingLease {
	t.Helper()
	logical := openProtectedBranchTestFD(t, f.logicalPath())
	metadata := openProtectedBranchTestFD(t, filepath.Join(f.anchors[1], protectedBackingKernel, lineageTestKernelID))
	directory := openProtectedBranchTestFD(t, filepath.Dir(f.disk(1, "disk")))
	anchor := openProtectedBranchTestFD(t, f.anchors[1])
	use, err := acquireProtectedStoreUse(t.Context(), anchor, false)
	require.NoError(t, err)
	logicalLock, err := acquireProtectedLock(t.Context(), logical)
	require.NoError(t, err)
	objectLock, err := acquireProtectedLock(t.Context(), metadata)
	require.NoError(t, err)
	fd, err := openProtectedFile(t.Context(), directory, "disk", unix.O_RDWR)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(f.anchors[1], protectedBackingKernel, lineageTestKernelID, "record.json"))
	require.NoError(t, err)
	var record protectedBackingRecord
	require.NoError(t, json.Unmarshal(data, &record))
	lease := &protectedBackingLease{fd: fd, logicalFD: logical, metadataFD: metadata, directoryFD: directory,
		lockFD: objectLock, ownerUID: 1000, kernelGeneration: 2, record: record,
		retained: []int{logical, metadata, directory, anchor, use, logicalLock, objectLock, fd}}
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	return lease
}

func TestProtectedKernelSuccessorVisiblePublicationRequiresDirectorySync(t *testing.T) {
	// Only the external fsync effect fails. All ready/pending/current records and disk bytes
	// are produced by real import. Unsynced rename models the visible result of parent-sync failure.
	for _, target := range []string{"backing-metadata", "logical-current"} {
		t.Run(target, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			f.retireKernel(t)
			f.retireCache(t)
			f.crash(t, "published")
			lease := f.publicationLease(t)
			if target == "logical-current" {
				require.NoError(t, lease.commitKernelGeneration(t.Context()))
				current := f.logical(t)
				require.NoError(t, writeProtectedRecord(t.Context(), lease.logicalFD, "visible-current.json", current))
				require.NoError(
					t,
					unix.Renameat(lease.logicalFD, "visible-current.json", lease.logicalFD, "logical.json"),
				)
			} else {
				require.NoError(
					t,
					writeProtectedRecord(t.Context(), lease.metadataFD, "visible-ready.json", lease.record),
				)
				require.NoError(
					t,
					unix.Renameat(lease.metadataFD, "visible-ready.json", lease.metadataFD, "record.json"),
				)
			}
			before := protectedCacheTestTree(t, f.logicalPath())
			metadata := protectedCacheTestTree(
				t,
				filepath.Join(f.anchors[1], protectedBackingKernel, lineageTestKernelID),
			)
			fault := errors.New("publication directory fsync failed")
			deps := realProtectedKernelPublicationDeps()
			if target == "backing-metadata" {
				deps.syncBackingMetadata = func(context.Context, int) error { return fault }
			} else {
				deps.syncLogicalAuthority = func(context.Context, int) error { return fault }
			}
			assert.ErrorIs(t, lease.commitKernelGenerationWithPublication(t.Context(), deps), fault)
			assertKernelLineageTree(t, before, f.logicalPath())
			assertKernelLineageTree(
				t,
				metadata,
				filepath.Join(f.anchors[1], protectedBackingKernel, lineageTestKernelID),
			)
			require.NoError(t, lease.commitKernelGeneration(t.Context()))
			f.assertCurrent(t, 1, 2)
		})
	}
}

func (f *kernelLineageFixture) abortSameCache(t *testing.T, cause string) {
	t.Helper()
	ctx := t.Context()
	if cause == "changed-bytes" {
		f.source(t, 0, []byte("changed same-cache bytes"))
	} else {
		base, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctx = kernelLineageCancelContext{Context: base, cancel: cancel, path: f.disk(0, "pending")}
	}
	lease, err := f.importKernel(t, ctx, 0, true)
	require.Nil(t, lease)
	require.Error(t, err)
	if cause == "cancellation" {
		require.ErrorIs(t, err, context.Canceled)
	}
	logical := f.logical(t)
	assert.Nil(t, logical.Kernel.Pending)
	assert.Equal(t, uint64(2), logical.Kernel.LastGeneration)
	assert.Equal(t, uint64(1), logical.Kernel.Current.Generation)
	assert.Equal(t, "removed", logical.Kernel.Current.Phase)
}

func TestProtectedKernelSuccessorSameCacheAbortPermitsVMRetirementAndLaterSuccessor(t *testing.T) {
	for _, cause := range []string{"changed-bytes", "cancellation"} {
		t.Run(cause, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			policy := instanceAuthorityPolicy{rootPath: f.root}
			require.NoError(t, prepareProtectedVMGrantInventory(t.Context(), policy, 1000))
			f.retireKernel(t)
			removedPath := filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000001.json")
			removed, err := os.ReadFile(removedPath)
			require.NoError(t, err)
			f.abortSameCache(t, cause)
			caller := protectedCacheTestTree(t, filepath.Join(f.paths[0], "kernels"))
			// Inventory scans every kernel before filtering this unrelated, grant-free VM.
			vmID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			require.NoError(t, requireProtectedVMGrantsAbsent(t.Context(), policy, 1000, vmID, 65536))
			require.NoError(t, revokeProtectedVMGrants(t.Context(), policy, 1000, vmID, 65536))
			assertKernelLineageTree(t, caller, filepath.Join(f.paths[0], "kernels"))
			abortedPath := filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000002.json")
			aborted, err := os.ReadFile(abortedPath)
			require.NoError(t, err)
			f.retireCache(t)
			old := protectedCacheTestTree(t, f.anchors[0])
			lease, err := f.importKernel(t, t.Context(), 1, true)
			require.NoError(t, err)
			require.NoError(t, lease.Release(t.Context()))
			f.assertCurrent(t, 1, 3)
			assertKernelLineageTree(t, old, f.anchors[0])
			data, err := os.ReadFile(removedPath)
			require.NoError(t, err)
			assertKernelLineageDiff(t, removed, data)
			data, err = os.ReadFile(abortedPath)
			require.NoError(t, err)
			assertKernelLineageDiff(t, aborted, data)
		})
	}
}

func TestProtectedKernelSuccessorSameCacheAbortedProjectionRequiresBothExactReceipts(t *testing.T) {
	failures := []string{"missing-removed", "missing-aborted", "aborted-store", "aborted-backing", "aborted-digest"}
	for _, failure := range failures {
		t.Run(failure, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			policy := instanceAuthorityPolicy{rootPath: f.root}
			require.NoError(t, prepareProtectedVMGrantInventory(t.Context(), policy, 1000))
			f.retireKernel(t)
			f.abortSameCache(t, "changed-bytes")
			if failure == "missing-removed" || failure == "missing-aborted" {
				generation := uint64(1)
				if failure == "missing-aborted" {
					generation = 2
				}
				require.NoError(t, os.Rename(filepath.Join(f.logicalPath(), protectedKernelReceiptLeaf(generation)),
					filepath.Join(f.root, "retained-original-receipt")))
			} else {
				path := filepath.Join(f.logicalPath(), protectedKernelReceiptLeaf(2))
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				var receipt protectedKernelReceipt
				require.NoError(t, json.Unmarshal(data, &receipt))
				switch failure {
				case "aborted-store":
					receipt.Binding.Store.Inode++
				case "aborted-backing":
					receipt.Binding.Backing.Inode++
				case "aborted-digest":
					receipt.Binding.Digest = "unexpected digest authority"
				}
				fd := openProtectedBranchTestFD(t, f.logicalPath())
				require.NoError(t, writeProtectedRecord(t.Context(), fd, protectedKernelReceiptLeaf(2), receipt))
				require.NoError(t, unix.Close(fd))
			}
			before := protectedCacheTestTree(t, f.root)
			assert.Error(t, requireProtectedVMGrantsAbsent(t.Context(), policy, 1000,
				"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 65536))
			owner, anchor := openProtectedBranchTestFD(t, f.owner), openProtectedBranchTestFD(t, f.anchors[0])
			assert.Error(t, retireProtectedCache(t.Context(), f.caches[0], anchor, owner))
			require.NoError(t, closeProtectedFDs([]int{owner, anchor}))
			assertKernelLineageTree(t, before, f.root)
		})
	}
}

func TestProtectedKernelGenerationDoesNotWrap(t *testing.T) {
	for _, last := range []uint64{0, 1, math.MaxUint64 - 1, math.MaxUint64} {
		t.Run(strconv.FormatUint(last, 10), func(t *testing.T) {
			generation, err := nextProtectedKernelGeneration(protectedKernelLineage{LastGeneration: last})
			if last == math.MaxUint64 {
				require.Error(t, err)
				assert.Zero(t, generation)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, last+1, generation)
		})
	}
}
