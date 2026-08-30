package authorityfs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirewallCreatesAndRetainsFixedGlobalLock(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority, err := openFirewallWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)

	lock, err := authority.Acquire(t.Context())
	require.NoError(t, err)
	require.NoError(t, authority.Release(t.Context()))
	require.NoError(t, lock.Release(t.Context()))

	lockPath := filepath.Join(root, "run/mvmctl/authority/firewall.lock")
	assertExactMode(t, lockPath, 0600)
	assert.FileExists(t, lockPath)
}

func TestFirewallRejectsUnsafeExistingLockWithoutRepair(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority, err := openFirewallWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })
	lockPath := filepath.Join(root, "run/mvmctl/authority/firewall.lock")
	require.NoError(t, os.WriteFile(lockPath, nil, 0600))
	require.NoError(t, os.Chmod(lockPath, 0644))

	lock, err := authority.Acquire(t.Context())
	assert.Nil(t, lock)
	require.Error(t, err)
	info, statErr := os.Lstat(lockPath)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
}

func TestFirewallLockWaitIsCancellableAndCopiedReleaseIsExactOnce(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority, err := openFirewallWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })

	first, err := authority.Acquire(t.Context())
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelWait()
	blocked, err := authority.Acquire(waitCtx)
	assert.Nil(t, blocked)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	const callers = 24
	results := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		copyOfFirst := *first
		group.Add(1)
		go func() {
			defer group.Done()
			results <- copyOfFirst.Release(t.Context())
		}()
	}
	group.Wait()
	close(results)
	for releaseErr := range results {
		assert.NoError(t, releaseErr)
	}

	reacquired, err := authority.Acquire(t.Context())
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(t.Context()))
}
