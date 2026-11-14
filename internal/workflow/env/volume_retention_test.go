package env

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
)

func TestRetainedVolumeRemainsInventoriedUntilExactResourceIsGone(t *testing.T) {
	// Rationale: Retention protects live data, but an explicit external removal must permit cleanup retry.
	ctx := t.Context()
	repo := volume.NewRepository(testutil.NewInMemoryDB(t))
	item := &model.VolumeItem{
		ID:        "c741bfe2a10b4ae9bb6520eec5d3554d",
		Name:      "retained-data",
		CreatedAt: "2026-10-16T12:00:00Z",
	}
	require.NoError(t, repo.Upsert(ctx, item))
	saved := model.ResourceState{Spec: model.ResourceMap{"size": "1M"},
		Output: model.ResourceMap{"volume_id": item.ID, "created_at": item.CreatedAt},
		Meta:   model.ResourceMeta{WasCreated: true}}
	op := &api.Operation{Repos: api.Repos{Volume: repo}}
	step, err := newVolumeStepFromState("volume", "data", saved, nil, op)
	require.NoError(t, err)
	writes := 0
	write := func(context.Context, model.ResourceState) error { writes++; return nil }
	require.NoError(t, step.Destroy(ctx, saved, write, nil))
	assert.Zero(t, writes, "retained resource must remain inventoried")
	got, err := repo.Get(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "retained data must not be deleted")
	require.NoError(t, repo.Delete(ctx, item.ID))
	require.NoError(t, step.Destroy(ctx, saved, write, nil))
	assert.Equal(t, 1, writes, "proven external removal must release stale inventory")
}
