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
	_input   KeyInput
	_result  *ResolvedKeyInput
	resolver *key.Resolver
}

// NewKeyRequest creates a new KeyRequest.
func NewKeyRequest(inputs KeyInput, db *sql.DB, keyRepo key.Repository) *KeyRequest {
	return &KeyRequest{
		db:       db,
		_input:   inputs,
		resolver: key.NewResolver(keyRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
func (r *KeyRequest) Result() *ResolvedKeyInput {
	return r._result
}

// Resolve resolves key identifiers to DB records.
// Matches Python's KeyRequest.resolve().
func (r *KeyRequest) Resolve(ctx context.Context) (*ResolvedKeyInput, error) {
	identifiers := append(r._input.Name, r._input.ID...)

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

	r._result = &ResolvedKeyInput{
		Keys: result.Items,
	}

	return r._result, nil
}
