package config_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/config"
	"mvmctl/internal/testutil"
)

func TestConfigL1_SetGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := config.NewRepository(db)

	// Set a string value and get it back.
	err := repo.Set(ctx, "general", "name", "test-vm")
	require.NoError(t, err)

	val, err := repo.Get(ctx, "general", "name")
	require.NoError(t, err)
	assert.Equal(t, "test-vm", val)

	// Set an int value and get it back.
	err = repo.Set(ctx, "general", "count", 42)
	require.NoError(t, err)

	val, err = repo.Get(ctx, "general", "count")
	require.NoError(t, err)
	// json.Unmarshal converts JSON numbers to float64 when unmarshalling into any.
	actual, ok := val.(float64)
	require.True(t, ok, "expected float64 from JSON unmarshal")
	assert.InDelta(t, 42.0, actual, 0.01)
}

func TestConfigL1_ListByCategory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := config.NewRepository(db)

	// Set values in two categories.
	require.NoError(t, repo.Set(ctx, "general", "name", "test-vm"))
	require.NoError(t, repo.Set(ctx, "general", "count", 42))
	require.NoError(t, repo.Set(ctx, "network", "bridge", "mvm-br0"))

	// ListByCategory with nil (all categories).
	result, err := repo.ListByCategory(ctx, nil)
	require.NoError(t, err)
	require.Len(t, result, 2)

	// Verify general category.
	general, ok := result["general"]
	require.True(t, ok)
	assert.Equal(t, "test-vm", general["name"])

	countVal, ok := general["count"]
	require.True(t, ok)
	countFloat, ok := countVal.(float64)
	require.True(t, ok, "expected float64 from JSON unmarshal")
	assert.InDelta(t, 42.0, countFloat, 0.01)

	// Verify network category.
	network, ok := result["network"]
	require.True(t, ok)
	assert.Equal(t, "mvm-br0", network["bridge"])
}
