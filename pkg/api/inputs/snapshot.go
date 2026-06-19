package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/core/snapshot"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
)

// SnapshotCreateInput holds input for creating a snapshot from a VM.
type SnapshotCreateInput struct {
	Identifier string  // VM identifier (name, ID, IP, MAC)
	Name       *string // Optional snapshot name
	Pause      bool    // Leave VM paused after snapshot
}

// Validate checks that the snapshot create input is valid.
func (i *SnapshotCreateInput) Validate() error {
	if i.Identifier == "" {
		return fmt.Errorf("VM identifier is required")
	}
	return nil
}

// Resolve resolves the VM identifier to a VMItem for snapshot creation.
func (i *SnapshotCreateInput) Resolve(ctx context.Context, vmRepo vm.Repository) (*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := vm.NewResolver(vmRepo)
	return resolver.Resolve(ctx, i.Identifier)
}

// SnapshotRestoreInput holds input for restoring VMs from a snapshot.
type SnapshotRestoreInput struct {
	SnapshotID string
	Name       string
	Count      int
	Network    *string // Optional network override
	Resume     bool
}

// Validate checks that the snapshot restore input is valid.
func (i *SnapshotRestoreInput) Validate() error {
	if i.SnapshotID == "" {
		return fmt.Errorf("snapshot identifier is required")
	}
	if i.Name == "" {
		return fmt.Errorf("VM name is required")
	}
	if i.Count < 1 {
		return fmt.Errorf("count must be at least 1")
	}
	return nil
}

// ResolveSnapshot resolves the snapshot identifier to a SnapshotItem.
func (i *SnapshotRestoreInput) ResolveSnapshot(ctx context.Context, repo snapshot.Repository) (*model.SnapshotItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := snapshot.NewResolver(repo)
	return resolver.Resolve(ctx, i.SnapshotID)
}

// ResolveNetwork resolves the optional network override. Returns nil if not set.
func (i *SnapshotRestoreInput) ResolveNetwork(ctx context.Context, repo network.Repository) (*model.NetworkItem, error) {
	if i.Network == nil || *i.Network == "" {
		return nil, nil
	}
	resolver := network.NewResolver(repo, nil)
	return resolver.Resolve(ctx, *i.Network)
}

// SnapshotInput holds input for listing, getting, or removing snapshots.
type SnapshotInput struct {
	Identifiers []string
	Force       bool
}

// Validate checks that the snapshot input has valid identifiers.
func (i *SnapshotInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one snapshot identifier is required")
	}
	return nil
}

// Resolve resolves all identifiers in the input to SnapshotItem objects.
// Delegates to snapshot.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *SnapshotInput) Resolve(ctx context.Context, repo snapshot.Repository) ([]*model.SnapshotItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := snapshot.NewResolver(repo)
	result := resolver.ResolveMany(ctx, i.Identifiers)
	if len(result.Errors) > 0 && len(result.Snapshots) == 0 {
		return nil, fmt.Errorf("failed to resolve snapshots: %s", strings.Join(result.Errors, "; "))
	}
	if len(result.Errors) > 0 {
		return result.Snapshots, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.Snapshots, nil
}
