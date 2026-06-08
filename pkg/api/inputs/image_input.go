package inputs

import (
	"context"
	"fmt"

	"mvmctl/internal/core/image"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"

	"github.com/jmoiron/sqlx"
)

// ImageInput is the raw input for identifying existing images.
type ImageInput struct {
	Identifiers []string `json:"identifiers"`
}

// ResolvedImageInput matches Python's ResolvedImageInput (frozen dataclass).
type ResolvedImageInput struct {
	Images []*model.ImageItem
}

// ImageRequest matches Python's ImageRequest.
//
// Request that resolves ImageInput to ImageItem via DB.
type ImageRequest struct {
	db       *sqlx.DB
	input    ImageInput
	result   *ResolvedImageInput
	resolver *image.Resolver
}

// NewImageRequest creates a new ImageRequest.
func NewImageRequest(inputs ImageInput, db *sqlx.DB, imageRepo image.Repository) *ImageRequest {
	return &ImageRequest{
		db:       db,
		input:    inputs,
		resolver: image.NewResolver(imageRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves identifiers to ImageItem records from DB.
// Matches Python's ImageRequest.resolve().
func (r *ImageRequest) Resolve(ctx context.Context) (*ResolvedImageInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, errs.NotFound(errs.CodeImageNotFound, "No image identifiers provided")
	}

	// Validate identifier length — max 64 chars.
	for _, ident := range r.input.Identifiers {
		if len(ident) > 64 {
			return nil, errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("Image identifier too long: '%s' exceeds maximum length of 64 characters", ident),
			)
		}
	}

	result := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if result == nil || len(result.Items) == 0 {
		return nil, errs.NotFound(errs.CodeImageNotFound, "No images found matching identifiers")
	}

	r.result = &ResolvedImageInput{
		Images: result.Items,
	}
	return r.result, nil
}
