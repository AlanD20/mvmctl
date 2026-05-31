package inputs

import (
	"context"
	"fmt"

	"mvmctl/internal/core/image"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"

	"github.com/jmoiron/sqlx"
)

// ImageInput matches Python's ImageInput dataclass.
//
//	@dataclass
//	class ImageInput:
//	    id: list[str] = field(default_factory=list)
//	    type: list[str] = field(default_factory=list)
type ImageInput struct {
	ID   []string `json:"id,omitempty"`
	Type []string `json:"type,omitempty"`
}

// ResolvedImageInput matches Python's ResolvedImageInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedImageInput:
//	    images: list[ImageItem]
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
	identifiers := make([]string, 0, len(r.input.ID)+len(r.input.Type))
	identifiers = append(identifiers, r.input.ID...)
	identifiers = append(identifiers, r.input.Type...)

	if len(identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeImageNotFound,
			Op:      "image",
			Message: "No image identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	// Validate identifier length — max 64 chars.
	for _, ident := range identifiers {
		if len(ident) > 64 {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "image",
				Message: fmt.Sprintf("Image identifier too long: '%s' exceeds maximum length of 64 characters", ident),
				Class:   errs.ClassValidation,
			}
		}
	}

	result := r.resolver.ResolveMany(ctx, identifiers)
	if result == nil || len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeImageNotFound,
			Op:      "image",
			Message: "No images found matching identifiers",
			Class:   errs.ClassValidation,
		}
	}

	r.result = &ResolvedImageInput{
		Images: result.Items,
	}
	return r.result, nil
}
