package inputs

import (
	"context"
	"fmt"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// BinaryInput matches Python's BinaryInput dataclass.
type BinaryInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
	Version     *string  `json:"version,omitempty"`
}

type ResolvedBinaryInput struct {
	Binaries []*model.BinaryItem
}

// BinaryRequest matches Python's BinaryRequest.
//
// Resolve binary identifiers to DB records.
type BinaryRequest struct {
	input    BinaryInput
	result   *ResolvedBinaryInput
	resolver *binary.Resolver
}

// NewBinaryRequest creates a new BinaryRequest.
func NewBinaryRequest(inputs BinaryInput, binaryRepo binary.Repository) *BinaryRequest {
	return &BinaryRequest{
		input:    inputs,
		resolver: binary.NewResolver(binaryRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves identifiers to BinaryItem list.
// Matches Python's BinaryRequest.resolve().
func (r *BinaryRequest) Resolve(ctx context.Context) (*ResolvedBinaryInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeBinaryNotFound,
			Op:      "binary",
			Message: "No binary identifiers provided or could be resolved",
			Class:   errs.ClassValidation,
		}
	}

	// Validate identifier length — max 64 chars matching SHA256 hex ID length.
	for _, ident := range r.input.Identifiers {
		if len(ident) > 64 {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "binary",
				Message: fmt.Sprintf("Binary identifier too long: '%s' exceeds maximum length of 64 characters", ident),
				Class:   errs.ClassValidation,
			}
		}
	}

	result := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if result == nil || len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeBinaryNotFound,
			Op:      "binary",
			Message: "No binary identifiers provided or could be resolved",
			Class:   errs.ClassValidation,
		}
	}

	r.result = &ResolvedBinaryInput{
		Binaries: result.Items,
	}
	return r.result, nil
}
