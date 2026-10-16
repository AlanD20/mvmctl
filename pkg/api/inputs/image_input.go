package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/image"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ImageInput is the raw input for identifying existing images.
type ImageInput struct {
	Identifiers    []string `json:"identifiers"`
	IncludeDeleted bool     `json:"include_deleted"`
}

// Validate checks that the image input has valid identifiers.
func (i *ImageInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one image identifier is required")
	}
	return nil
}

// Resolve resolves all identifiers in the input to ImageItem objects.
// Delegates to image.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *ImageInput) Resolve(ctx context.Context, repo image.Repository) ([]*model.ImageItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := image.NewResolver(repo)
	result := resolver.ResolveMany(ctx, i.Identifiers, i.IncludeDeleted)
	if result == nil || len(result.Items) == 0 {
		if len(result.Errors) > 0 {
			return nil, errs.NotFound(errs.CodeImageNotFound, result.Errors[0])
		}
		return nil, errs.NotFound(errs.CodeImageNotFound, "No images found matching identifiers")
	}
	return result.Items, nil
}
