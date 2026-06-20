package volume_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/testutil"
)

func TestVolumeL1_ListAll_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := volume.NewRepository(db)

	// List all from an empty table — should return empty slice, not nil.
	items, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, items)
}
