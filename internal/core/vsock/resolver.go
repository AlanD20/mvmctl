package vsock

import (
	"context"
	"fmt"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Resolver resolves vsock configuration by VM ID for enrichment.
type Resolver struct {
	repo Repository
}

// NewResolver creates a new vsock resolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{repo: repo}
}

// GetByVMID returns the vsock config for a VM. Returns error if not found.
// This is the resolution hook called by the enricher for "vsock" relations.
func (r *Resolver) GetByVMID(ctx context.Context, vmID string) (*model.VsockConfigItem, error) {
	item, err := r.repo.GetByVMID(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errs.NotFound(errs.CodeVsockNotFound, fmt.Sprintf("vsock config not found for VM: %s", vmID))
	}
	return item, nil
}
