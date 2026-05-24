package testutil

import (
	"context"
	"sort"
	"sync"
	"time"

	"mvmctl/internal/core/kernel"
	"mvmctl/internal/infra/model"
)

// KernelRepo is an in-memory kernel repository for testing.
// Matches Python's mvmctl.core.kernel._repository.Repository exactly,
// including soft-delete filtering (deleted_at IS NULL AND is_present = 1).
type KernelRepo struct {
	mu      sync.RWMutex
	kernels map[string]*model.KernelItem
}

func NewKernelRepo() *KernelRepo {
	return &KernelRepo{kernels: make(map[string]*model.KernelItem)}
}

// isNotDeleted returns true if the kernel is NOT soft-deleted.
func (r *KernelRepo) isNotDeleted(k *model.KernelItem) bool {
	return k.DeletedAt == nil && k.IsPresent
}

// Get returns a kernel by ID. Returns nil if soft-deleted (Python: WHERE deleted_at IS NULL AND is_present = 1).
func (r *KernelRepo) Get(_ context.Context, id string) (*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kernels[id]
	if !ok || !r.isNotDeleted(k) {
		return nil, nil
	}
	return k, nil
}

// FindByPrefix returns all non-deleted kernels whose ID starts with prefix.
func (r *KernelRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.KernelItem
	for _, k := range r.kernels {
		if r.isNotDeleted(k) && len(k.ID) >= len(prefix) && k.ID[:len(prefix)] == prefix {
			result = append(result, k)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// Count returns total count of all non-deleted kernels (Python: WHERE deleted_at IS NULL).
func (r *KernelRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, k := range r.kernels {
		if k.DeletedAt == nil {
			count++
		}
	}
	return count, nil
}

// ListAll returns all non-deleted kernels ordered by created_at (Python: WHERE deleted_at IS NULL ORDER BY created_at).
func (r *KernelRepo) ListAll(_ context.Context) ([]*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.KernelItem
	for _, k := range r.kernels {
		if k.DeletedAt == nil {
			result = append(result, k)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (r *KernelRepo) Upsert(_ context.Context, k *model.KernelItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kernels[k.ID] = k
	return nil
}

// SoftDelete marks a kernel as deleted (Python: datetime.now(tz=UTC).isoformat()).
func (r *KernelRepo) SoftDelete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.kernels[id]; ok {
		now := time.Now().UTC().Format(time.RFC3339)
		k.IsPresent = false
		k.DeletedAt = &now
	}
	return nil
}

func (r *KernelRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.kernels, id)
	return nil
}

// SetDefault sets one kernel as default, clearing all others atomically (Python: BEGIN/COMMIT transaction).
func (r *KernelRepo) SetDefault(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Match Python: UPDATE kernels SET is_default = 0 WHERE deleted_at IS NULL
	for _, k := range r.kernels {
		if k.DeletedAt == nil {
			k.IsDefault = false
		}
	}
	// Match Python: UPDATE kernels SET is_default = 1 WHERE id = ? AND deleted_at IS NULL
	if k, ok := r.kernels[id]; ok && k.DeletedAt == nil {
		k.IsDefault = true
	}
	return nil
}

// GetDefault returns the default kernel (Python: WHERE is_default = 1 AND deleted_at IS NULL AND is_present = 1 LIMIT 1).
func (r *KernelRepo) GetDefault(_ context.Context) (*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, k := range r.kernels {
		if k.IsDefault && r.isNotDeleted(k) {
			return k, nil
		}
	}
	return nil, nil
}

// GetByName returns a kernel by name (Python: WHERE deleted_at IS NULL AND is_present = 1 LIMIT 1).
func (r *KernelRepo) GetByName(_ context.Context, name string) (*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, k := range r.kernels {
		if k.Name == name && r.isNotDeleted(k) {
			return k, nil
		}
	}
	return nil, nil
}

// GetByType returns a kernel by type (Python: WHERE deleted_at IS NULL AND is_present = 1 LIMIT 1).
func (r *KernelRepo) GetByType(_ context.Context, kernelType string) (*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, k := range r.kernels {
		if k.Type == kernelType && r.isNotDeleted(k) {
			return k, nil
		}
	}
	return nil, nil
}

// GetByVersionAndType returns a kernel by version and type (Python: WHERE deleted_at IS NULL AND is_present = 1 LIMIT 1).
func (r *KernelRepo) GetByVersionAndType(_ context.Context, version, kernelType string) (*model.KernelItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, k := range r.kernels {
		if k.Type == kernelType && k.Version == version && r.isNotDeleted(k) {
			return k, nil
		}
	}
	return nil, nil
}

func (r *KernelRepo) UpdateManyIsPresent(_ context.Context, ids []string, isPresent bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if k, ok := r.kernels[id]; ok {
			k.IsPresent = isPresent
		}
	}
	return nil
}

// Ensure KernelRepo implements kernel.Repository.
var _ kernel.Repository = (*KernelRepo)(nil)
