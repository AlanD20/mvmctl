package testutil

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
)

// BinaryRepo is an in-memory binary repository for testing.
// Includes soft-delete filtering (deleted_at IS NULL AND is_present = 1).
type BinaryRepo struct {
	mu       sync.RWMutex
	binaries map[string]*model.BinaryItem
}

func NewBinaryRepo() *BinaryRepo {
	return &BinaryRepo{binaries: make(map[string]*model.BinaryItem)}
}

// isNotDeleted returns true if the binary is NOT soft-deleted.
func (r *BinaryRepo) isNotDeleted(b *model.BinaryItem) bool {
	return b.DeletedAt == nil && b.IsPresent
}

// Get returns a binary by ID. Returns nil if soft-deleted.
func (r *BinaryRepo) Get(_ context.Context, id string) (*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.binaries[id]
	if !ok || !r.isNotDeleted(b) {
		return nil, nil
	}
	return b, nil
}

// FindByPrefix returns binaries whose ID starts with prefix.
// When includeDeleted is true, soft-deleted binaries are also returned.
func (r *BinaryRepo) FindByPrefix(
	_ context.Context,
	prefix string,
	architecture string,
	includeDeleted ...bool,
) ([]*model.BinaryItem, error) {
	checkDeleted := len(includeDeleted) == 0 || !includeDeleted[0]
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.BinaryItem
	for _, b := range r.binaries {
		if b.Architecture == architecture && (!checkDeleted || r.isNotDeleted(b)) &&
			len(b.ID) >= len(prefix) && b.ID[:len(prefix)] == prefix {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// ListAll returns all binaries ordered by created_at.
func (r *BinaryRepo) ListAll(_ context.Context, architecture string) ([]*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.BinaryItem
	for _, b := range r.binaries {
		if b.Architecture == architecture && r.isNotDeleted(b) {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// ListByType returns all non-deleted binaries with a given type.
func (r *BinaryRepo) ListByType(_ context.Context, typ, architecture string) ([]*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.BinaryItem
	for _, b := range r.binaries {
		if b.Type == typ && b.Architecture == architecture && r.isNotDeleted(b) {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// GetByTypeAndVersion returns a binary by type, architecture, and version.
func (r *BinaryRepo) GetByTypeAndVersion(
	_ context.Context,
	typ string,
	architecture string,
	version string,
) (*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.binaries {
		if b.Type == typ && b.Architecture == architecture && b.Version == version && r.isNotDeleted(b) {
			return b, nil
		}
	}
	return nil, nil
}

func (r *BinaryRepo) Upsert(_ context.Context, b *model.BinaryItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existing := range r.binaries {
		if id == b.ID || !r.isNotDeleted(existing) {
			continue
		}
		if existing.Type == b.Type && existing.Architecture == b.Architecture && existing.Version == b.Version &&
			b.DeletedAt == nil {
			return fmt.Errorf("active binary release member already exists")
		}
		if existing.Type == b.Type && existing.Architecture == b.Architecture && existing.IsDefault && b.IsDefault &&
			b.DeletedAt == nil {
			return fmt.Errorf("active binary default already exists")
		}
	}
	r.binaries[b.ID] = b
	return nil
}

func (r *BinaryRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.binaries, id)
	return nil
}

func (r *BinaryRepo) DeleteByType(_ context.Context, typ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, b := range r.binaries {
		if b.Type == typ {
			delete(r.binaries, id)
		}
	}
	return nil
}

func (r *BinaryRepo) DeleteByTypeAndVersion(_ context.Context, typ, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Normalize version: strip "v" prefix, then re-add it
	normalized := strings.TrimPrefix(version, "v")
	prefixed := "v" + normalized
	for id, b := range r.binaries {
		if b.Type == typ && (b.Version == version || b.Version == normalized || b.Version == prefixed) {
			delete(r.binaries, id)
		}
	}
	return nil
}

// SetDefault sets a binary as default, clearing all others with the same type atomically.
func (r *BinaryRepo) SetDefault(_ context.Context, typ, architecture, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Clear existing defaults for this type
	for _, b := range r.binaries {
		if b.Type == typ && b.Architecture == architecture && b.DeletedAt == nil {
			b.IsDefault = false
		}
	}
	// Set new default by ID
	if b, ok := r.binaries[id]; ok && b.Type == typ && b.Architecture == architecture && b.DeletedAt == nil {
		b.IsDefault = true
	}
	return nil
}

// Count returns total count of all non-deleted binaries.
func (r *BinaryRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, b := range r.binaries {
		if b.DeletedAt == nil {
			count++
		}
	}
	return count, nil
}

// GetDefault returns the default binary for a given type.
func (r *BinaryRepo) GetDefault(_ context.Context, typ, architecture string) (*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.binaries {
		if b.Type == typ && b.Architecture == architecture && b.IsDefault && r.isNotDeleted(b) {
			return b, nil
		}
	}
	return nil, nil
}

// SoftDelete marks a binary as deleted.
func (r *BinaryRepo) SoftDelete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.binaries[id]; ok {
		now := time.Now().UTC().Format(time.RFC3339)
		b.IsPresent = false
		b.DeletedAt = &now
	}
	return nil
}

func (r *BinaryRepo) UpdateManyIsPresent(_ context.Context, ids []string, present bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if b, ok := r.binaries[id]; ok {
			b.IsPresent = present
		}
	}
	return nil
}

func (r *BinaryRepo) ReplaceReleaseProjection(
	_ context.Context,
	architecture string,
	version string,
	pair *model.BinaryPair,
	setDefault bool,
) error {
	if pair == nil || pair.Firecracker == nil || pair.Jailer == nil ||
		pair.Firecracker.ID == pair.Jailer.ID ||
		pair.Firecracker.Type != "firecracker" || pair.Jailer.Type != "jailer" {
		return fmt.Errorf("complete binary release projection is required")
	}
	for _, member := range []*model.BinaryItem{pair.Firecracker, pair.Jailer} {
		if member.ID == "" || member.Architecture != architecture || member.Version != version ||
			!member.IsPresent || member.DeletedAt != nil {
			return fmt.Errorf("binary release projection member does not match slot")
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, member := range []*model.BinaryItem{pair.Firecracker, pair.Jailer} {
		memberDefault := setDefault
		for _, existing := range r.binaries {
			if existing.Type == member.Type && existing.Architecture == architecture &&
				existing.Version == version && r.isNotDeleted(existing) && existing.IsDefault {
				memberDefault = true
			}
		}
		for _, existing := range r.binaries {
			if existing.Type == member.Type && existing.Architecture == architecture &&
				existing.Version == version && existing.ID != member.ID && r.isNotDeleted(existing) {
				existing.IsDefault = false
				existing.IsPresent = false
				existing.DeletedAt = &now
			}
			if memberDefault && existing.Type == member.Type && existing.Architecture == architecture &&
				r.isNotDeleted(existing) {
				existing.IsDefault = false
			}
		}
		member.IsDefault = memberDefault
		member.IsPresent = true
		member.DeletedAt = nil
		r.binaries[member.ID] = member
	}
	return nil
}

func (r *BinaryRepo) RemoveReleaseProjection(_ context.Context, architecture, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, member := range r.binaries {
		if member.Architecture == architecture && member.Version == version && r.isNotDeleted(member) {
			member.IsDefault = false
			member.IsPresent = false
			member.DeletedAt = &now
		}
	}
	return nil
}

// Ensure BinaryRepo implements binary.Repository.
var _ binary.Repository = (*BinaryRepo)(nil)
