package env

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/image"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

func TestImageImportReuseCannotAuthorizeRemoves(t *testing.T) {
	// Rationale: The actual cached-import API branch must not transfer deletion ownership.
	ctx := t.Context()
	repo := image.NewRepository(testutil.NewInMemoryDB(t))
	source := filepath.Join(t.TempDir(), "cached.raw")
	require.NoError(t, os.WriteFile(source, []byte("preexisting image contents"), 0600))
	require.NoError(
		t,
		repo.Upsert(
			ctx,
			&model.ImageItem{ID: "cached-image", Type: "cached", Version: "1", Path: source, IsPresent: true},
		),
	)
	op := &api.Operation{Repos: api.Repos{Image: repo}}
	step := NewImageImportStep(
		op,
		"export",
		inputs.ImageImportInput{Name: "cached", Source: source, Format: "raw", Version: "1"},
	)
	var saved model.ResourceState
	write := func(_ context.Context, state model.ResourceState) error { saved = state; return nil }
	require.NoError(
		t,
		step.Apply(ctx, workflow.NewSharedState(), model.ResourceState{}, write, func(event.Progress) {}),
	)
	assert.False(t, saved.Meta.WasCreated)
	removal := &assetRemovalStep{Step: step, op: op}
	require.NoError(t, removal.Destroy(ctx, saved, write, nil))
	stored, err := repo.Get(ctx, "cached-image")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.True(t, stored.IsPresent)
	contents, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, "preexisting image contents", string(contents))
}

func TestImagePullReuseReceiptCannotAuthorizeRemoves(t *testing.T) {
	// Rationale: cache resolution after the workflow pre-read can find an existing image.
	// The step must obey the acquisition receipt rather than infer ownership from that pre-read.
	op := &testutil.MockOperation{}
	op.ImageGetFunc = func(context.Context, inputs.ImageInput) (*model.ImageItem, error) { return nil, nil }
	op.ImagePullWithResultFunc = func(context.Context, inputs.ImagePullInput, event.OnProgressCallback) (*results.ImagePullResult, error) {
		return &results.ImagePullResult{Image: &model.ImageItem{ID: "concurrent-image"}, Created: false}, nil
	}
	removed := false
	op.ImageRemoveFunc = func(context.Context, inputs.ImageInput, bool) *errs.BatchResult { removed = true; return nil }
	step := NewImageStep(op, "base", inputs.ImagePullInput{Type: "alpine", Version: "3.23"})
	var saved model.ResourceState
	write := func(_ context.Context, state model.ResourceState) error { saved = state; return nil }
	require.NoError(
		t,
		step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, func(event.Progress) {}),
	)
	assert.False(t, saved.Meta.WasCreated)
	assert.Equal(t, "concurrent-image", saved.Output["image_id"])
	removal := &assetRemovalStep{Step: step, op: op}
	require.NoError(t, removal.Destroy(t.Context(), saved, write, nil))
	assert.False(t, removed, "reused receipt must never authorize deletion")
}
