package authorityfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestExistingJailerMissingAuthorityNeverCreates(t *testing.T) {
	// Rationale: Reboot and unknown-VM observation must not recreate missing authority or locks.
	for _, missing := range []string{
		"run/mvmctl", "run/mvmctl/authority", "run/mvmctl/authority/releases",
		"run/mvmctl/authority/index.lock", "run/mvmctl/authority/1000",
		"run/mvmctl/authority/1000/" + testVMID + ".lock",
	} {
		t.Run(missing, func(t *testing.T) {
			root := seedObservedJailer(t)
			require.NoError(t, os.RemoveAll(filepath.Join(root, missing)))
			before := observedJailerTree(t, root)
			t.Cleanup(func() { compareObservedJailerTree(t, before, observedJailerTree(t, root)) })
			err := acquireObservedJailerVM(t.Context(), root)
			assert.Error(t, err)
		})
	}
}

func TestExistingJailerPreservesAuthorityAndCoordinatesLocks(t *testing.T) {
	// Rationale: Read-only admission still serializes against ordinary writers, including cancellation.
	root := seedObservedJailer(t)
	before := observedJailerTree(t, root)
	defer func() { compareObservedJailerTree(t, before, observedJailerTree(t, root)) }()
	assert.NoError(t, acquireObservedJailerVM(t.Context(), root))
	writer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	index, err := writer.AcquireIndex(t.Context())
	require.NoError(t, err)
	indexCtx, cancelIndex := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelIndex()
	assert.ErrorIs(t, acquireObservedJailerVM(indexCtx, root), context.DeadlineExceeded)
	require.NoError(t, index.Release(t.Context()))
	uid, err := writer.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uid.AcquireVM(t.Context(), key)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, acquireObservedJailerVM(ctx, root), context.DeadlineExceeded)
	require.NoError(t, vm.Release(t.Context()))
	assert.NoError(t, acquireObservedJailerVM(t.Context(), root))
}

func TestExistingJailerRefusesUnsafeAuthorityWithoutRepair(t *testing.T) {
	// Rationale: Existing-only is not relaxed admission; even failed lock validation preserves evidence.
	root := seedObservedJailer(t)
	require.NoError(t, os.Chmod(filepath.Join(root, "run/mvmctl/authority/index.lock"), 0644))
	before := observedJailerTree(t, root)
	defer func() { compareObservedJailerTree(t, before, observedJailerTree(t, root)) }()
	assert.Error(t, acquireObservedJailerVM(t.Context(), root))
}

func seedObservedJailer(t *testing.T) string {
	t.Helper()
	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	index, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)
	uid, err := jailer.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uid.AcquireVM(t.Context(), key)
	require.NoError(t, err)
	require.NoError(t, vm.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, index.Release(t.Context()))
	require.NoError(t, jailer.Release(t.Context()))
	for _, name := range []string{
		"run/mvmctl/authority/index.lock", "run/mvmctl/authority/1000/" + testVMID + ".lock",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("retained lock evidence"), 0600))
	}
	return root
}

func acquireObservedJailerVM(ctx context.Context, root string) (returnErr error) {
	jailer, err := openJailerMode(ctx, realDependencies(), testPolicy(root), false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, jailer.Release(context.WithoutCancel(ctx))) }()
	index, err := jailer.AcquireIndex(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, index.Release(context.WithoutCancel(ctx))) }()
	uid, err := jailer.OpenUID(ctx, testUID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, uid.Release(context.WithoutCancel(ctx))) }()
	key, err := NewVMKey(testVMID)
	if err != nil {
		return err
	}
	vm, err := uid.AcquireVM(ctx, key)
	if err != nil {
		return err
	}
	return vm.Release(context.WithoutCancel(ctx))
}

type observedJailerNode struct {
	Mode  fs.FileMode
	Data  string
	Inode uint64
}

func observedJailerTree(t *testing.T, root string) map[string]observedJailerNode {
	t.Helper()
	result := make(map[string]observedJailerNode)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		node := observedJailerNode{Mode: info.Mode()}
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil {
			return err
		}
		node.Inode = stat.Ino
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			node.Data = string(data)
		}
		name, err := filepath.Rel(root, path)
		result[name] = node
		return err
	}))
	return result
}

func compareObservedJailerTree(t *testing.T, want, got map[string]observedJailerNode) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("observation mutated authority (-want +got):\n%s", diff)
	}
}
