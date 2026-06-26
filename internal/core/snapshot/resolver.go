package snapshot

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Resolver resolves snapshot identifiers (name or ID prefix) to SnapshotItem
// objects. This is pure resolution — no enrichment. Enrichment is handled by
// the enricher package.
type Resolver struct {
	repo Repository
}

// NewResolver creates a new snapshot resolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{repo: repo}
}

// ByID resolves a snapshot by ID prefix. Returns error if not found or ambiguous.
func (r *Resolver) ByID(ctx context.Context, id string) (*model.SnapshotItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeSnapshotNotFound, fmt.Sprintf("snapshot not found: %s", id))
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return nil, errs.NotFound(errs.CodeSnapshotNotFound,
			fmt.Sprintf("ID %s matches multiple snapshots: %s", id, strings.Join(names, ", ")))
	}
	return matches[0], nil
}

// ByName resolves a snapshot by exact name. Returns error if not found.
func (r *Resolver) ByName(ctx context.Context, name string) (*model.SnapshotItem, error) {
	snap, err := r.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, errs.NotFound(errs.CodeSnapshotNotFound, fmt.Sprintf("snapshot not found: %s", name))
	}
	return snap, nil
}

// Resolve resolves a snapshot by name first, then falls back to ID prefix.
// Matches the VM resolver pattern of trying the most specific identifier first.
func (r *Resolver) Resolve(ctx context.Context, identifier string) (*model.SnapshotItem, error) {
	snap, err := r.ByName(ctx, identifier)
	if err == nil {
		return snap, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err
	}
	return r.ByID(ctx, identifier)
}

// ResolveResult holds the result of resolving multiple snapshot identifiers.
type ResolveResult struct {
	Snapshots []*model.SnapshotItem
	Errors    []string
	ExitCode  int
}

// ResolveMany resolves multiple snapshot identifiers, deduplicating results.
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) *ResolveResult {
	uniqueIDs := infra.Dedup(identifiers)

	var snapshots []*model.SnapshotItem
	var errsList []string
	resolvedIDs := make(map[string]bool)

	for _, ident := range uniqueIDs {
		snap, err := r.Resolve(ctx, ident)
		if err != nil {
			errsList = append(errsList, err.Error())
			continue
		}
		if !resolvedIDs[snap.ID] {
			resolvedIDs[snap.ID] = true
			snapshots = append(snapshots, snap)
		}
	}

	exitCode := 0
	if len(errsList) > 0 && len(snapshots) == 0 {
		exitCode = 1
	}
	return &ResolveResult{Snapshots: snapshots, Errors: errsList, ExitCode: exitCode}
}
