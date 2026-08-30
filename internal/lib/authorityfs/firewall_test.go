package authorityfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// Rationale: Firewall values are capabilities. Copies must share one terminal decision so a failed close is cached
// and never retried against a descriptor that the kernel may already have reused.
func TestFirewallCapabilityCopiesReleaseReverseOnceCachedWithoutCancellation(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	closeFault := errors.New("injected firewall authority close failure")
	closed := make([]int, 0, 2)
	var unsafeContexts atomic.Int32
	authority := &Firewall{state: &firewallState{
		deps: dependencies{close: func(ctx context.Context, fd int) error {
			closed = append(closed, fd)
			if ctx.Err() != nil || ctx.Value(contextKey{}) != "retained" {
				unsafeContexts.Add(1)
			}
			if fd == 12 {
				return closeFault
			}
			return nil
		}},
		authority: 12,
		retained:  []int{11, 12},
		active:    true,
	}}
	copyOfAuthority := *authority
	cancelled, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "retained"))
	cancel()

	const callers = 24
	results := make(chan error, callers)
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func(useCopy bool) {
			defer group.Done()
			if useCopy {
				results <- copyOfAuthority.Release(cancelled)
				return
			}
			results <- authority.Release(cancelled)
		}(index%2 == 0)
	}
	group.Wait()
	close(results)
	var first error
	for releaseErr := range results {
		require.ErrorIs(t, releaseErr, closeFault)
		if first == nil {
			first = releaseErr
		} else {
			assert.Same(t, first, releaseErr)
		}
	}
	assert.Equal(t, []int{12, 11}, closed)
	assert.Zero(t, unsafeContexts.Load())
}
