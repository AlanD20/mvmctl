package testutil

import (
	"context"
	"sync"

	"mvmctl/internal/core/snapshot"
	"mvmctl/internal/lib/model"
)

// SnapshotRepo is an in-memory snapshot repository for testing.
type SnapshotRepo struct {
	mu        sync.RWMutex
	snapshots map[string]*model.SnapshotItem
}

func NewSnapshotRepo() *SnapshotRepo {
	return &SnapshotRepo{snapshots: make(map[string]*model.SnapshotItem)}
}

func (r *SnapshotRepo) Get(_ context.Context, id string) (*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.snapshots[id]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (r *SnapshotRepo) GetByName(_ context.Context, name string) (*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.snapshots {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}

func (r *SnapshotRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if len(s.ID) >= len(prefix) && s.ID[:len(prefix)] == prefix {
			result = append(result, s)
		}
	}
	return result, nil
}

func (r *SnapshotRepo) ListAll(_ context.Context) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*model.SnapshotItem, 0, len(r.snapshots))
	for _, s := range r.snapshots {
		result = append(result, s)
	}
	return result, nil
}

func (r *SnapshotRepo) Upsert(_ context.Context, item *model.SnapshotItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots[item.ID] = item
	return nil
}

func (r *SnapshotRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.snapshots, id)
	return nil
}

func (r *SnapshotRepo) CountByKernelID(_ context.Context, kernelID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, s := range r.snapshots {
		if s.KernelID == kernelID {
			count++
		}
	}
	return count, nil
}

func (r *SnapshotRepo) CountByNetworkID(_ context.Context, networkID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, s := range r.snapshots {
		if s.NetworkID == networkID {
			count++
		}
	}
	return count, nil
}

func (r *SnapshotRepo) CountByBinaryID(_ context.Context, binaryID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, s := range r.snapshots {
		if s.BinaryID == binaryID {
			count++
		}
	}
	return count, nil
}

func (r *SnapshotRepo) FindByKernelID(_ context.Context, kernelID string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if s.KernelID == kernelID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (r *SnapshotRepo) FindByNetworkID(_ context.Context, networkID string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if s.NetworkID == networkID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (r *SnapshotRepo) FindByBinaryID(_ context.Context, binaryID string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if s.BinaryID == binaryID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (r *SnapshotRepo) FindByKernelIDs(_ context.Context, kernelIDs []string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make(map[string]bool, len(kernelIDs))
	for _, id := range kernelIDs {
		ids[id] = true
	}
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if ids[s.KernelID] {
			result = append(result, s)
		}
	}
	return result, nil
}

func (r *SnapshotRepo) FindByNetworkIDs(_ context.Context, networkIDs []string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make(map[string]bool, len(networkIDs))
	for _, id := range networkIDs {
		ids[id] = true
	}
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if ids[s.NetworkID] {
			result = append(result, s)
		}
	}
	return result, nil
}

func (r *SnapshotRepo) FindByBinaryIDs(_ context.Context, binaryIDs []string) ([]*model.SnapshotItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make(map[string]bool, len(binaryIDs))
	for _, id := range binaryIDs {
		ids[id] = true
	}
	var result []*model.SnapshotItem
	for _, s := range r.snapshots {
		if ids[s.BinaryID] {
			result = append(result, s)
		}
	}
	return result, nil
}

// Compile-time check
var _ snapshot.Repository = (*SnapshotRepo)(nil)
