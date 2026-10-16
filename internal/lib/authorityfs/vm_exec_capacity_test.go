package authorityfs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMExecCapacityCreatesAndSerializesOnFixedGlobalLock(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	firstAuthority, err := openVMExecCapacityWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	secondAuthority, err := openVMExecCapacityWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, secondAuthority.Release(context.Background()))
		require.NoError(t, firstAuthority.Release(context.Background()))
	})

	first, err := firstAuthority.Acquire(t.Context())
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelWait()
	blocked, err := secondAuthority.Acquire(waitCtx)
	assert.Nil(t, blocked)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, first.Release(t.Context()))

	reacquired, err := secondAuthority.Acquire(t.Context())
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(t.Context()))
	lockPath := filepath.Join(root, "run/mvmctl/authority/capacity.lock")
	assertExactMode(t, lockPath, 0600)
	assert.FileExists(t, lockPath)
}
