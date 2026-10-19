package env

import (
	"context"
	"errors"
	"fmt"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// assetRemovalStep implements explicit removes for assets normally retained by Destroy.
type assetRemovalStep struct {
	workflow.Step
	op api.API
}

func (s *assetRemovalStep) Destroy(
	ctx context.Context,
	saved model.ResourceState,
	write workflow.StateWriter,
	progress event.OnProgressCallback,
) error {
	if !saved.Meta.WasCreated {
		return write(ctx, saved)
	}
	var failures []error
	switch s.Type() {
	case "image", "image_import":
		id := saved.Output.GetString("image_id")
		if id == "" {
			return fmt.Errorf("%s: saved image identity is missing", s.Name())
		}
		result := s.op.ImageRemove(ctx, inputs.ImageInput{Identifiers: []string{id}, IncludeDeleted: true}, true)
		for _, item := range result.Errors() {
			if err := item.ToError(); err != nil && !errs.IsNotFound(err) {
				failures = append(failures, err)
			}
		}
	default:
		return fmt.Errorf("%s does not support explicit removal", s.Name())
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	return write(ctx, saved)
}
