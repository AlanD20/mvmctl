package inputs

import (
	"context"
	"database/sql"
	"strings"

	"mvmctl/internal/core/image"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
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
	db       *sql.DB
	_input   ImageInput
	_result  *ResolvedImageInput
	resolver *image.Resolver
}

// NewImageRequest creates a new ImageRequest.
func NewImageRequest(inputs ImageInput, db *sql.DB, imageRepo image.Repository) *ImageRequest {
	return &ImageRequest{
		db:       db,
		_input:   inputs,
		resolver: image.NewResolver(imageRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *ImageRequest) Result() *ResolvedImageInput {
	return r._result
}

// Resolve resolves identifiers to ImageItem records from DB.
// Matches Python's ImageRequest.resolve().
func (r *ImageRequest) Resolve(ctx context.Context) (*ResolvedImageInput, error) {
	identifiers := append(r._input.ID, r._input.Type...)

	if len(identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeImageNotFound,
			Op:      "image",
			Message: "No image identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	result := r.resolver.ResolveMany(ctx, identifiers)
	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeImageNotFound,
			Op:      "image",
			Message: "Could not resolve any images: " + strings.Join(result.Errors, ", "),
			Class:   errs.ClassValidation,
		}
	}

	r._result = &ResolvedImageInput{
		Images: result.Items,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r._result, nil
}

func (r *ImageRequest) ensureValidate() error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeImageNotFound,
			Op:      "image",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	if len(r._result.Images) == 0 {
		return &errs.DomainError{
			Code:    errs.CodeImageNotFound,
			Op:      "image",
			Message: "No images found matching identifiers",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}
