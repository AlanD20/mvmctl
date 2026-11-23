package image_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/image"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestSaveImportedReportsOnlyInsertedIdentity(t *testing.T) {
	// Rationale: Ownership must come from the same transaction as publication, including replacement.
	ctx := t.Context()
	repo := image.NewRepository(testutil.NewInMemoryDB(t))
	item := &model.ImageItem{ID: "imported", Type: "fixture", Name: "first", IsPresent: true, IsImported: true}
	created, err := repo.SaveImported(ctx, item)
	require.NoError(t, err)
	assert.True(t, created)
	item.Name = "replacement"
	created, err = repo.SaveImported(ctx, item)
	require.NoError(t, err)
	assert.False(t, created)
	stored, err := repo.Get(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "replacement", stored.Name)
	assert.True(t, stored.IsImported)
	assert.True(t, stored.IsPresent)
}
