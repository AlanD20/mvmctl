package key

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// ResolveResult holds the result of resolving multiple key identifiers.
// Matches Python's KeyResolveResult.
type ResolveResult struct {
	Items    []*model.SSHKeyItem
	Errors   []string
	ExitCode int
}

// EnrichFunc is a function that enriches keys in-place with relations.
// Set by the API layer during wiring to avoid circular imports.
type EnrichFunc func(ctx context.Context, keys []*model.SSHKeyItem, include []string, relations map[string]any)

// Resolver resolves key identifiers (name, ID prefix, or .pub file path)
// to SSHKeyItem instances using database storage.
// Matches Python's KeyResolver exactly — no keysDir field (Python resolver
// only takes repo and include).
type Resolver struct {
	repo       Repository
	include    []string
	enrichFunc EnrichFunc
}

// NewResolver creates a new KeyResolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{
		repo: repo,
	}
}

// SetEnrichFunc sets the enrichment function called after each resolution.
// Must be set for cross-domain relation enrichment.
func (r *Resolver) SetEnrichFunc(fn EnrichFunc) {
	r.enrichFunc = fn
}

// SetInclude sets the relation names to include during resolution.
func (r *Resolver) SetInclude(include []string) {
	r.include = include
}

// enrich enriches keys with relations if include is set.
func (r *Resolver) enrich(ctx context.Context, keys []*model.SSHKeyItem) []*model.SSHKeyItem {
	if r.include != nil && len(keys) > 0 && r.enrichFunc != nil {
		r.enrichFunc(ctx, keys, r.include, nil)
	}
	return keys
}

// ByID resolves a key by ID (fingerprint) prefix.
// Accepts both "SHA256:abc..." and bare "abc..." by auto-prepending "SHA256:".
// Matches Python's KeyResolver.by_id().
func (r *Resolver) ByID(ctx context.Context, keyID string) (*model.SSHKeyItem, error) {
	candidates := []string{keyID}
	if !strings.HasPrefix(keyID, "SHA256:") {
		candidates = append(candidates, "SHA256:"+keyID)
	}

	for _, candidate := range candidates {
		matches, err := r.repo.FindByPrefix(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if len(matches) == 1 {
			return r.enrich(ctx, matches)[0], nil
		}
		if len(matches) > 1 {
			return nil, &errs.DomainError{
				Code:    errs.CodeKeyNotFound,
				Op:      "key",
				Entity:  keyID,
				Message: fmt.Sprintf("Key ID is ambiguous: '%s'", keyID),
				Class:   errs.ClassValidation,
			}
		}
	}

	return nil, &errs.DomainError{
		Code:    errs.CodeKeyNotFound,
		Op:      "key",
		Entity:  keyID,
		Message: fmt.Sprintf("Key not found: '%s'", keyID),
		Class:   errs.ClassValidation,
	}
}

// ByName resolves a key by name.
// Matches Python's KeyResolver.by_name().
func (r *Resolver) ByName(ctx context.Context, name string) (*model.SSHKeyItem, error) {
	key, err := r.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeKeyNotFound,
			Op:      "key",
			Entity:  name,
			Message: fmt.Sprintf("Key not found: '%s'", name),
			Class:   errs.ClassValidation,
		}
	}
	return r.enrich(ctx, []*model.SSHKeyItem{key})[0], nil
}

// Resolve resolves a key by name, ID prefix, or .pub file path (in that order).
// Matches Python's KeyResolver.resolve() exactly.
// Only catches KeyNotFoundError (matching Python's except KeyNotFoundError);
// other errors (e.g. database errors) propagate immediately.
func (r *Resolver) Resolve(ctx context.Context, value string) (*model.SSHKeyItem, error) {
	// Try by name first
	key, err := r.ByName(ctx, value)
	if err == nil {
		return key, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err
	}

	// Try by ID prefix
	key, err = r.ByID(ctx, value)
	if err == nil {
		return key, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err
	}

	// Try as .pub file path
	if _, statErr := os.Stat(value); statErr == nil && strings.HasSuffix(value, ".pub") {
		stem := strings.TrimSuffix(filepath.Base(value), ".pub")
		key, err = r.ByName(ctx, stem)
		if err == nil {
			return key, nil
		}
		// Python catches ONLY KeyNotFoundError; other errors propagate as-is
		if !errs.IsNotFound(err) {
			return nil, err
		}
		// Python raises MVMKeyError here (not KeyNotFoundError), which propagates
		// through resolve_many as an uncaught exception.
		return nil, errs.MVMKeyError(
			"Public key file '" + value + "' found on disk but key '" + stem + "' is not in the cache. " +
				"Import it first with: mvm key import " + stem + " " + value,
		)
	}

	return nil, &errs.DomainError{
		Code:    errs.CodeKeyNotFound,
		Op:      "key",
		Entity:  value,
		Message: fmt.Sprintf("Key not found: '%s' is not a cached key name, a readable .pub file path, or a resolvable ID.", value),
		Class:   errs.ClassValidation,
	}
}

// ResolveMany resolves multiple key identifiers, deduplicating by input.
// Matches Python's KeyResolver.resolve_many().
// Only catches KeyNotFoundError (not-found errors); all other errors
// (e.g. MVMKeyError from a .pub path that exists but key is not cached)
// propagate immediately, matching Python's except KeyNotFoundError clause.
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) (*ResolveResult, error) {
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.SSHKeyItem
	var errsList []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		key, err := r.Resolve(ctx, identifier)
		if err != nil {
			// Only catch not-found errors (matching Python's except KeyNotFoundError).
			// Other error types (MVMKeyError, database errors) propagate.
			if !errs.IsNotFound(err) {
				return nil, err
			}
			// Match Python's str(e) which returns just the message for MVMError subclasses.
			errsList = append(errsList, identifier+": "+err.Error())
		} else if !resolvedIDs[key.ID] {
			resolvedIDs[key.ID] = true
			items = append(items, key)
		}
	}

	items = r.enrich(ctx, items)

	exitCode := 0
	if len(errsList) > 0 && len(items) == 0 {
		exitCode = 1
	}

	return &ResolveResult{
		Items:    items,
		Errors:   errsList,
		ExitCode: exitCode,
	}, nil
}

// GetDefaults resolves all SSH keys marked as default.
// Matches Python's KeyResolver.get_defaults().
func (r *Resolver) GetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	keys, err := r.repo.GetDefaults(ctx)
	if err != nil {
		return nil, err
	}
	return r.enrich(ctx, keys), nil
}
