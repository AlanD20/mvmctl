package testutil

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
)

// BinaryRepo is an in-memory binary repository for testing.
// Matches Python's mvmctl.core.binary._repository.Repository exactly,
// including soft-delete filtering (deleted_at IS NULL AND is_present = 1).
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

// Get returns a binary by ID. Returns nil if soft-deleted (Python: WHERE deleted_at IS NULL AND is_present = 1).
func (r *BinaryRepo) Get(_ context.Context, id string) (*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.binaries[id]
	if !ok || !r.isNotDeleted(b) {
		return nil, nil
	}
	return b, nil
}

// FindByPrefix returns all non-deleted binaries whose ID starts with prefix.
func (r *BinaryRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.BinaryItem
	for _, b := range r.binaries {
		if r.isNotDeleted(b) && len(b.ID) >= len(prefix) && b.ID[:len(prefix)] == prefix {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// ListAll returns all non-deleted binaries (Python: WHERE deleted_at IS NULL ORDER BY created_at).
func (r *BinaryRepo) ListAll(_ context.Context) ([]*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.BinaryItem
	for _, b := range r.binaries {
		if b.DeletedAt == nil {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// ListByType returns all non-deleted binaries with a given type (Python: WHERE deleted_at IS NULL AND is_present = 1 ORDER BY created_at).
func (r *BinaryRepo) ListByType(_ context.Context, typ string) ([]*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.BinaryItem
	for _, b := range r.binaries {
		if b.Type == typ && r.isNotDeleted(b) {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// GetByTypeAndVersion returns a binary by type and version (Python: WHERE deleted_at IS NULL AND is_present = 1 LIMIT 1).
func (r *BinaryRepo) GetByTypeAndVersion(_ context.Context, typ, version string) (*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.binaries {
		if b.Type == typ && b.Version == version && r.isNotDeleted(b) {
			return b, nil
		}
	}
	return nil, nil
}

func (r *BinaryRepo) Upsert(_ context.Context, b *model.BinaryItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	// Match Python's version normalization: removeprefix("v") + f"v{normalized}"
	normalized := strings.TrimPrefix(version, "v")
	prefixed := "v" + normalized
	for id, b := range r.binaries {
		if b.Type == typ && (b.Version == version || b.Version == normalized || b.Version == prefixed) {
			delete(r.binaries, id)
		}
	}
	return nil
}

// SetDefault sets a binary as default, clearing all others with the same type atomically (Python: BEGIN/COMMIT).
func (r *BinaryRepo) SetDefault(_ context.Context, typ, version, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Match Python: UPDATE binaries SET is_default = 0 WHERE type = ? AND deleted_at IS NULL
	for _, b := range r.binaries {
		if b.Type == typ && b.DeletedAt == nil {
			b.IsDefault = false
		}
	}
	// Match Python: UPDATE binaries SET is_default = 1 WHERE type = ? AND version = ? AND deleted_at IS NULL
	for _, b := range r.binaries {
		if b.Type == typ && b.Version == version && b.DeletedAt == nil {
			b.IsDefault = true
		}
	}
	return nil
}

// Count returns total count of all non-deleted binaries (Python: WHERE deleted_at IS NULL).
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

// GetDefault returns the default binary for a given type (Python: WHERE type = ? AND is_default = 1 AND deleted_at IS NULL AND is_present = 1).
func (r *BinaryRepo) GetDefault(_ context.Context, typ string) (*model.BinaryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.binaries {
		if b.Type == typ && b.IsDefault && r.isNotDeleted(b) {
			return b, nil
		}
	}
	return nil, nil
}

// SoftDelete marks a binary as deleted (Python: datetime.now(tz=UTC).isoformat()).
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

// Ensure BinaryRepo implements binary.Repository.
var _ binary.Repository = (*BinaryRepo)(nil)
