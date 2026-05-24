package inputs

import (
	"context"
	"fmt"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/version"
)

// BinaryInput matches Python's BinaryInput dataclass.
//
//	@dataclass
//	class BinaryInput:
//	    identifiers: list[str] = field(default_factory=list)
//	    version: str | None = None
type BinaryInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
	Version     *string  `json:"version,omitempty"`
}

// ResolvedBinaryInput matches Python's ResolvedBinaryInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedBinaryInput:
//	    binaries: list[BinaryItem]
type ResolvedBinaryInput struct {
	Binaries []*model.BinaryItem
}

// BinaryRequest matches Python's BinaryRequest.
//
// Resolve binary identifiers to DB records.
type BinaryRequest struct {
	_input   BinaryInput
	_result  *ResolvedBinaryInput
	resolver *binary.Resolver
}

// NewBinaryRequest creates a new BinaryRequest.
func NewBinaryRequest(inputs BinaryInput, binaryRepo binary.Repository) *BinaryRequest {
	return &BinaryRequest{
		_input:   inputs,
		resolver: binary.NewResolver(binaryRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *BinaryRequest) Result() *ResolvedBinaryInput {
	return r._result
}

// Resolve resolves identifiers to BinaryItem list.
// Matches Python's BinaryRequest.resolve().
func (r *BinaryRequest) Resolve(ctx context.Context) (*ResolvedBinaryInput, error) {
	// Build candidates list matching Python logic
	type nameVersion struct {
		name    string
		version string
	}
	var nameVersionPairs []nameVersion
	var bareIdentifiers []string

	for _, ident := range r._input.Identifiers {
		// Try to detect "name:version" inline format using VersionResolver (matching Python):
		//   prefix, value = VersionResolver.parse_selector(ident)
		//   if prefix is not None:
		//       candidates.append([prefix, value])
		name, ver := version.ParseSelector(ident)
		if name != "" && ver != "" {
			nameVersionPairs = append(nameVersionPairs, nameVersion{name: name, version: ver})
		} else if r._input.Version != nil && *r._input.Version != "" {
			// Pair bare name with the shared version
			nameVersionPairs = append(nameVersionPairs, nameVersion{name: ident, version: *r._input.Version})
		} else {
			bareIdentifiers = append(bareIdentifiers, ident)
		}
	}

	var allBinaries []*model.BinaryItem

	// Resolve name:version pairs
	for _, nv := range nameVersionPairs {
		bin, err := r.resolver.ByNameVersion(ctx, nv.name, nv.version)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeBinaryNotFound,
				Op:      "binary",
				Message: fmt.Sprintf("Binary '%s' version '%s' not found", nv.name, nv.version),
				Class:   errs.ClassValidation,
			}
		}
		allBinaries = append(allBinaries, bin)
	}

	// Resolve bare identifiers
	if len(bareIdentifiers) > 0 {
		// Convert []string to []interface{} for the binary resolver
		idAny := make([]interface{}, len(bareIdentifiers))
		for i, id := range bareIdentifiers {
			idAny[i] = id
		}
		result := r.resolver.ResolveMany(ctx, idAny)
		if result != nil {
			allBinaries = append(allBinaries, result.Items...)
		}
	}

	if len(allBinaries) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeBinaryNotFound,
			Op:      "binary",
			Message: "No binary identifiers provided or could be resolved",
			Class:   errs.ClassValidation,
		}
	}

	r._result = &ResolvedBinaryInput{
		Binaries: allBinaries,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r._result, nil
}

func (r *BinaryRequest) ensureValidate() error {
	if r._result == nil {
		return &errs.DomainError{
			Code:    errs.CodeBinaryNotFound,
			Op:      "binary",
			Message: "No resolved binaries to validate",
			Class:   errs.ClassValidation,
		}
	}

	for _, bin := range r._result.Binaries {
		if bin.ID == "" {
			return &errs.DomainError{
				Code:    errs.CodeBinaryNotFound,
				Op:      "binary",
				Message: fmt.Sprintf("Binary '%s' has no ID", bin.Name),
				Class:   errs.ClassValidation,
			}
		}
		if bin.Path == "" {
			return &errs.DomainError{
				Code:    errs.CodeBinaryNotFound,
				Op:      "binary",
				Message: fmt.Sprintf("Binary '%s' has no path", bin.Name),
				Class:   errs.ClassValidation,
			}
		}
	}

	return nil
}
