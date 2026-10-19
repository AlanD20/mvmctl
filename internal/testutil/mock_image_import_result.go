package testutil

import (
	"context"
	"fmt"

	"mvmctl/internal/infra/event"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func (m *MockImageAPI) ImagePullWithResult(
	ctx context.Context,
	input inputs.ImagePullInput,
	progress event.OnProgressCallback,
) (*results.ImagePullResult, error) {
	if m.ImagePullWithResultFunc == nil {
		return nil, fmt.Errorf("unexpected image pull ownership request")
	}
	return m.ImagePullWithResultFunc(ctx, input, progress)
}

func (m *MockImageAPI) ImageImportWithResult(
	ctx context.Context,
	input inputs.ImageImportInput,
	progress event.OnProgressCallback,
) (*results.ImageImportResult, error) {
	if m.ImageImportWithResultFunc == nil {
		return nil, fmt.Errorf("unexpected image import ownership request")
	}
	return m.ImageImportWithResultFunc(ctx, input, progress)
}
