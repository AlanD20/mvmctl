package network_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestNetworkL1_ListAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := network.NewRepository(db)

	now := time.Now().Format(time.RFC3339)

	// Seed two networks.
	n1 := &model.NetworkItem{
		ID:          "net-1111111111111111111111111111111111111111111111111111111111111111",
		Name:        "default",
		Subnet:      "10.0.0.0/24",
		Bridge:      "mvm-br0",
		IPv4Gateway: "10.0.0.1",
		IsPresent:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	n2 := &model.NetworkItem{
		ID:          "net-2222222222222222222222222222222222222222222222222222222222222222",
		Name:        "isolated",
		Subnet:      "10.0.1.0/24",
		Bridge:      "mvm-br1",
		IPv4Gateway: "10.0.1.1",
		IsPresent:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	require.NoError(t, repo.Upsert(ctx, n1))
	require.NoError(t, repo.Upsert(ctx, n2))

	// List all and verify count and field values.
	items, err := repo.ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)

	assert.Equal(t, "default", items[0].Name)
	assert.Equal(t, "10.0.0.0/24", items[0].Subnet)
	assert.Equal(t, "mvm-br0", items[0].Bridge)
	assert.Equal(t, "10.0.0.1", items[0].IPv4Gateway)

	assert.Equal(t, "isolated", items[1].Name)
	assert.Equal(t, "10.0.1.0/24", items[1].Subnet)
	assert.Equal(t, "mvm-br1", items[1].Bridge)
	assert.Equal(t, "10.0.1.1", items[1].IPv4Gateway)
}
