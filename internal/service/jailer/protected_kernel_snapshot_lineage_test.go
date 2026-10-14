package jailer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

func kernelSnapshotTestSource(t *testing.T, path string) int {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(lineageTestBytes), 0400))
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(fd)) })
	return fd
}

func restoreSnapshotLineageKernel(
	t *testing.T, ctx context.Context, f *kernelLineageFixture, i, source int,
) (*protectedBackingLease, error) {
	t.Helper()
	size := uint64(len(lineageTestBytes))
	if err := validateRestoredBackingSource(ctx, source, size); err != nil {
		return nil, err
	}
	owner, anchor := openProtectedBranchTestFD(t, f.owner), openProtectedBranchTestFD(t, f.anchors[i])
	defer func() { require.NoError(t, closeProtectedFDs([]int{owner, anchor})) }()
	items := []restoreBackingSelection{{kind: protectedBackingKernel, id: lineageTestKernelID,
		sourceFD: source, bytes: size}}
	admissions, err := acquireRestoredBackingSetInAnchor(ctx, f.caches[i], items, owner, anchor)
	if err != nil {
		return nil, err
	}
	defer func() { require.NoError(t, releaseRestoredBackingAdmissions(context.WithoutCancel(ctx), admissions)) }()
	return restoreAdmittedBacking(ctx, admissions[0], source, size)
}

func TestProtectedKernelSnapshotProducerPublishesInitialCurrentBeforeConsumer(t *testing.T) {
	// The three full-set contention tests own lock-order/no-copy proof; this is the producer's
	// lineage contract through the same admitted restore path, with independent disk readback.
	f := newKernelLineageFixture(t)
	source := kernelSnapshotTestSource(t, filepath.Join(f.root, "trusted-kernel"))
	caller := protectedCacheTestTree(t, filepath.Join(f.paths[1], "kernels"))
	lease, err := restoreSnapshotLineageKernel(t, t.Context(), f, 1, source)
	require.NoError(t, err)
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 1, 1)
	assertKernelLineageTree(t, caller, filepath.Join(f.paths[1], "kernels"))
	content, err := os.ReadFile(filepath.Join(f.root, "trusted-kernel"))
	require.NoError(t, err)
	assertKernelLineageDiff(t, []byte(lineageTestBytes), content)
	consumer, err := f.importKernel(t, t.Context(), 1, false)
	require.NoError(t, err)
	binding, err := consumer.Binding(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), binding.generation)
	require.NoError(t, consumer.Release(t.Context()))
}

type snapshotKernelCrashContext struct {
	context.Context
	logical, metadata, stage string
}

func (ctx snapshotKernelCrashContext) Err() error {
	data, _ := os.ReadFile(ctx.logical)
	var logical protectedLogicalRecord
	_ = json.Unmarshal(data, &logical)
	if logical.Kernel != nil && logical.Kernel.Pending != nil {
		data, _ = os.ReadFile(ctx.metadata)
		var backing protectedBackingRecord
		_ = json.Unmarshal(data, &backing)
		if ctx.stage == "intent" && logical.Kernel.Pending.Phase == "preparing" ||
			ctx.stage == "candidate" && backing.Phase == "copying" && backing.Backing.Inode != 0 ||
			ctx.stage == "published" && backing.Phase == "ready" {
			os.Exit(73) // Real receiver death; no deferred abort runs.
		}
	}
	return ctx.Context.Err()
}

func TestProtectedKernelSnapshotProducerCrashWorker(t *testing.T) {
	root := os.Getenv("MVM_SNAPSHOT_KERNEL_TEST_ROOT")
	if root == "" {
		t.Skip("private initial-producer receiver-death subprocess only")
	}
	cachePath := filepath.Join(root, "second-cache")
	cache := openKernelLineageCache(t, cachePath)
	ownerPath := filepath.Join(root, "var", "lib", "mvmctl", "protected-backing", "1000")
	anchorPath := filepath.Join(ownerPath, protectedCacheKey(cache.identity))
	ctx := snapshotKernelCrashContext{Context: t.Context(), stage: os.Getenv("MVM_SNAPSHOT_KERNEL_TEST_STAGE"),
		logical:  filepath.Join(ownerPath, "logical", protectedBackingKernel, lineageTestKernelID, "logical.json"),
		metadata: filepath.Join(anchorPath, protectedBackingKernel, lineageTestKernelID, "record.json")}
	f := &kernelLineageFixture{root: root, owner: ownerPath}
	f.paths[1], f.anchors[1], f.caches[1] = cachePath, anchorPath, cache
	var lease *protectedBackingLease
	var err error
	if os.Getenv("MVM_SNAPSHOT_KERNEL_TEST_PRODUCER") == "operator" {
		lease, err = f.importKernel(t, ctx, 1, true)
	} else {
		source, openErr := unix.Open(filepath.Join(root, "trusted-kernel"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		require.NoError(t, openErr)
		defer func() { require.NoError(t, unix.Close(source)) }()
		lease, err = restoreSnapshotLineageKernel(t, ctx, f, 1, source)
	}
	if lease != nil {
		require.NoError(t, lease.Release(t.Context()))
	}
	t.Fatalf("producer never reached requested crash stage: %v", err)
}

func crashKernelSnapshotProducer(t *testing.T, f *kernelLineageFixture, producer, stage string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	result, err := system.DefaultRunner.Run(t.Context(), []string{executable,
		"-test.run=^TestProtectedKernelSnapshotProducerCrashWorker$"}, system.RunCmdOpts{
		Capture: true, Timeout: 20 * time.Second, MaxStdoutBytes: 4096, MaxStderrBytes: 4096,
		Env: map[string]string{"MVM_SNAPSHOT_KERNEL_TEST_ROOT": f.root,
			"MVM_SNAPSHOT_KERNEL_TEST_STAGE": stage, "MVM_SNAPSHOT_KERNEL_TEST_PRODUCER": producer},
	})
	require.NotNil(t, result, "%v", err)
	require.Equal(t, 73, result.ExitCode, "producer stdout=%s stderr=%s error=%v", result.Stdout, result.Stderr, err)
}

func TestProtectedKernelSnapshotProducerReceiverDeathResumesOnlyExactInitialSource(t *testing.T) {
	for _, stage := range []string{"intent", "candidate", "published"} {
		t.Run(stage, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			source := kernelSnapshotTestSource(t, filepath.Join(f.root, "trusted-kernel"))
			crashKernelSnapshotProducer(t, f, "snapshot", stage)
			logical := f.logical(t)
			require.NotNil(t, logical.Kernel.Pending)
			require.NotNil(t, logical.Kernel.Pending.SnapshotSource)
			assert.Nil(t, logical.Kernel.Current)
			assert.Equal(t, uint64(1), logical.Kernel.Pending.Generation)
			var stat unix.Stat_t
			require.NoError(t, unix.Fstat(source, &stat))
			assertKernelLineageDiff(t, protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size},
				*logical.Kernel.Pending.SnapshotSource)
			before := protectedCacheTestTree(t, f.logicalPath())
			consumer, err := f.importKernel(t, t.Context(), 1, false)
			require.Nil(t, consumer)
			require.Error(t, err)
			// Explicit operator import also cannot claim or abort the trusted producer's intent.
			consumer, err = f.importKernel(t, t.Context(), 1, true)
			require.Nil(t, consumer)
			require.Error(t, err)
			assertKernelLineageTree(t, before, f.logicalPath())
			lease, err := restoreSnapshotLineageKernel(t, t.Context(), f, 1, source)
			require.NoError(t, err)
			require.NoError(t, lease.Release(t.Context()))
			f.assertCurrent(t, 1, 1)
		})
	}
}

func TestProtectedKernelSnapshotProducerRefusesUnrelatedPendingAndRetiredAuthority(t *testing.T) {
	for _, failure := range []string{"operator-pending", "changed-source", "cross-cache-pending", "removed-current"} {
		t.Run(failure, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			source := kernelSnapshotTestSource(t, filepath.Join(f.root, "trusted-kernel"))
			selected := 1
			originalIndex := 1
			if failure == "removed-current" {
				f.retireKernel(t)
				f.retireCache(t)
				originalIndex = 0
			} else if failure == "operator-pending" {
				crashKernelSnapshotProducer(t, f, "operator", "candidate")
			} else {
				crashKernelSnapshotProducer(t, f, "snapshot", "candidate")
				if failure == "changed-source" {
					source = kernelSnapshotTestSource(t, filepath.Join(f.root, "different-snapshot-inode"))
				} else {
					selected = 0
				}
			}
			before := protectedCacheTestTree(t, f.logicalPath())
			original := protectedCacheTestTree(t, f.anchors[originalIndex])
			lease, err := restoreSnapshotLineageKernel(t, t.Context(), f, selected, source)
			require.Nil(t, lease)
			require.Error(t, err)
			assertKernelLineageTree(t, before, f.logicalPath())
			assertKernelLineageTree(t, original, f.anchors[originalIndex])
			_, err = os.Stat(f.disk(selected, "disk"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestProtectedKernelSnapshotProducerInitialAbortRequiresExactReceiptAndSource(t *testing.T) {
	for _, retry := range []string{"matching", "changed-source", "missing-receipt", "operator-abort"} {
		t.Run(retry, func(t *testing.T) {
			f := newKernelLineageFixture(t)
			source := kernelSnapshotTestSource(t, filepath.Join(f.root, "trusted-kernel"))
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := kernelLineageCancelContext{Context: base, cancel: cancel, path: f.disk(1, "pending")}
			var lease *protectedBackingLease
			var err error
			if retry == "operator-abort" {
				lease, err = f.importKernel(t, ctx, 1, true)
			} else {
				lease, err = restoreSnapshotLineageKernel(t, ctx, f, 1, source)
			}
			require.Nil(t, lease)
			require.ErrorIs(t, err, context.Canceled)
			logical := f.logical(t)
			assert.Nil(t, logical.Kernel.Current)
			assert.Nil(t, logical.Kernel.Pending)
			assert.Equal(t, uint64(1), logical.Kernel.LastGeneration)
			path := filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000001.json")
			receipt, err := os.ReadFile(path)
			require.NoError(t, err)
			if retry == "changed-source" {
				source = kernelSnapshotTestSource(t, filepath.Join(f.root, "replacement-snapshot-inode"))
			} else if retry == "missing-receipt" {
				require.NoError(t, os.Rename(path, filepath.Join(f.root, "retained-aborted-receipt")))
			}
			before := protectedCacheTestTree(t, f.logicalPath())
			lease, err = restoreSnapshotLineageKernel(t, t.Context(), f, 1, source)
			if retry != "matching" {
				require.Nil(t, lease)
				require.Error(t, err)
				assertKernelLineageTree(t, before, f.logicalPath())
				return
			}
			require.NoError(t, err)
			require.NoError(t, lease.Release(t.Context()))
			f.assertCurrent(t, 1, 2)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assertKernelLineageDiff(t, receipt, data)
		})
	}
}

func TestProtectedKernelSnapshotProducerInitialAbortRefusesOperatorTakeover(t *testing.T) {
	// Reciprocal producer fencing: a terminal initial snapshot attempt is not an operator
	// import entitlement. No counter/record/receipt/caller inode may change on that refusal.
	f := newKernelLineageFixture(t)
	source := kernelSnapshotTestSource(t, filepath.Join(f.root, "trusted-kernel"))
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := kernelLineageCancelContext{Context: base, cancel: cancel, path: f.disk(1, "pending")}
	lease, err := restoreSnapshotLineageKernel(t, ctx, f, 1, source)
	require.Nil(t, lease)
	require.ErrorIs(t, err, context.Canceled)
	logical := f.logical(t)
	assert.Nil(t, logical.Kernel.Current)
	assert.Nil(t, logical.Kernel.Pending)
	assert.Equal(t, uint64(1), logical.Kernel.LastGeneration)
	before := protectedCacheTestTree(t, f.root)
	lease, err = f.importKernel(t, t.Context(), 1, true)
	require.Nil(t, lease)
	require.Error(t, err)
	assertKernelLineageTree(t, before, f.root)
	receiptPath := filepath.Join(f.logicalPath(), "kernel-generation-00000000000000000001.json")
	receipt, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	lease, err = restoreSnapshotLineageKernel(t, t.Context(), f, 1, source)
	require.NoError(t, err)
	require.NoError(t, lease.Release(t.Context()))
	f.assertCurrent(t, 1, 2)
	data, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	assertKernelLineageDiff(t, receipt, data)
}
