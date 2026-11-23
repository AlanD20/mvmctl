package env

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestImageAcquisitionErrorPreservesReceipt(t *testing.T) {
	// Rationale: a post-publication failure must not erase the identity needed for recovery.
	// This owns workflow persistence; it does not prove the API's publication transaction.
	for _, kind := range []string{"pull", "import"} {
		for _, created := range []bool{false, true} {
			name := kind + "/reused"
			if created {
				name = kind + "/created"
			}
			t.Run(name, func(t *testing.T) {
				acquisitionErr := errors.New("post-publication effect failed")
				persistenceErr := errors.New("state write failed")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				op := &testutil.MockOperation{}
				op.ImageGetFunc = func(context.Context, inputs.ImageInput) (*model.ImageItem, error) { return nil, nil }
				op.ImagePullWithResultFunc = func(context.Context, inputs.ImagePullInput, event.OnProgressCallback) (*results.ImagePullResult, error) {
					cancel()
					return &results.ImagePullResult{
						Image:   &model.ImageItem{ID: "published-image"},
						Created: created,
					}, acquisitionErr
				}
				op.ImageImportWithResultFunc = func(context.Context, inputs.ImageImportInput, event.OnProgressCallback) (*results.ImageImportResult, error) {
					cancel()
					return &results.ImageImportResult{
						Image:   &model.ImageItem{ID: "published-image"},
						Created: created,
					}, acquisitionErr
				}
				var step workflow.Step = NewImageStep(op, "base", inputs.ImagePullInput{Type: "alpine"})
				if kind == "import" {
					step = NewImageImportStep(op, "base", inputs.ImageImportInput{Name: "base", Source: "/image.raw"})
				}
				var persisted model.ResourceState
				write := func(writeCtx context.Context, saved model.ResourceState) error {
					require.NoError(
						t,
						writeCtx.Err(),
						"committed receipt persistence must survive acquisition cancellation",
					)
					persisted = saved
					if created {
						return persistenceErr
					}
					return nil
				}
				err := step.Apply(ctx, workflow.NewSharedState(), model.ResourceState{}, write, func(event.Progress) {})
				assert.ErrorIs(t, err, acquisitionErr)
				if created {
					assert.ErrorIs(t, err, persistenceErr)
				}
				assert.Equal(t, "published-image", persisted.Output.GetString("image_id"))
				assert.Equal(t, created, persisted.Meta.WasCreated)
				assert.Equal(t, "published-image", step.StateData().Output.GetString("image_id"))
			})
		}
	}
}
