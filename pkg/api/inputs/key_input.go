package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// KeyInput is the raw input for identifying existing SSH keys.
// Matches Python's KeyInput dataclass behaviour — identifiers are resolved
// by name or ID in a single pass (lumping both).
type KeyInput struct {
	Identifiers []string `json:"identifiers,omitempty"`
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
	input    KeyInput
	resolver *key.Resolver
}

// NewKeyRequest creates a new KeyRequest.
func NewKeyRequest(inputs KeyInput, keyRepo key.Repository) *KeyRequest {
	return &KeyRequest{
		input:    inputs,
		resolver: key.NewResolver(keyRepo),
	}
}

// Resolve resolves key identifiers to DB records.
// Matches Python's KeyRequest.resolve().
func (r *KeyRequest) Resolve(ctx context.Context) (*ResolvedKeyInput, error) {
	identifiers := r.input.Identifiers

	if len(identifiers) == 0 {
		return nil, errs.NotFound(errs.CodeKeyNotFound, "No key identifiers provided")
	}

	result, err := r.resolver.ResolveMany(ctx, identifiers)
	if err != nil {
		return nil, err
	}

	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, errs.NotFound(
			errs.CodeKeyNotFound,
			fmt.Sprintf("Could not resolve any keys: %s", strings.Join(result.Errors, ", ")),
		)
	}

	return &ResolvedKeyInput{
		Keys: result.Items,
	}, nil
}
