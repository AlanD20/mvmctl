package inputs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"mvmctl/internal/core/key"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// KeyInput matches Python's KeyInput dataclass.
//
//	@dataclass
//	class KeyInput:
//	    name: list[str] = field(default_factory=list)
//	    id: list[str] = field(default_factory=list)
type KeyInput struct {
	Name []string `json:"name,omitempty"`
	ID   []string `json:"id,omitempty"`
}

// ResolvedKeyInput matches Python's ResolvedKeyInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedKeyInput:
//	    keys: list[SSHKeyItem]
type ResolvedKeyInput struct {
	Keys []*model.SSHKeyItem
}

// KeyRequest matches Python's KeyRequest.
//
// Resolve key identifiers to DB records.
type KeyRequest struct {
	db       *sql.DB
	input   KeyInput
	result  *ResolvedKeyInput
	resolver *key.Resolver
}

// NewKeyRequest creates a new KeyRequest.
func NewKeyRequest(inputs KeyInput, db *sql.DB, keyRepo key.Repository) *KeyRequest {
	return &KeyRequest{
		db:       db,
		input:   inputs,
		resolver: key.NewResolver(keyRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves key identifiers to DB records.
// Matches Python's KeyRequest.resolve().
func (r *KeyRequest) Resolve(ctx context.Context) (*ResolvedKeyInput, error) {
	identifiers := append(r.input.Name, r.input.ID...)

	if len(identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeKeyNotFound,
			Op:      "key",
			Message: "No key identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	result, err := r.resolver.ResolveMany(ctx, identifiers)
	if err != nil {
		return nil, err
	}

	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeKeyNotFound,
			Op:      "key",
			Message: fmt.Sprintf("Could not resolve any keys: %s", strings.Join(result.Errors, ", ")),
			Class:   errs.ClassValidation,
		}
	}

	r.result = &ResolvedKeyInput{
		Keys: result.Items,
	}

	return r.result, nil
}
