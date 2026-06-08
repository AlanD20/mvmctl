package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
)

func TestLeaseRepo(t *testing.T) {
	repo := testutil.NewLeaseRepo()
	repo.SetNetwork(10, true)

	lease, err := repo.Acquire(ctx, "net-1", "vm-1", nil)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.NotEmpty(t, lease.IPv4)

	leases, err := repo.ListAll(ctx, "net-1")
	require.NoError(t, err)
	assert.Len(t, leases, 1)

	err = repo.Release(ctx, "net-1", "vm-1")
	require.NoError(t, err)

	after, _ := repo.ListAll(ctx, "net-1")
	assert.Empty(t, after)
}
