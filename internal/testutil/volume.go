package testutil

import (
	"context"
	"sort"
	"sync"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/model"
)

// VolumeRepo is an in-memory volume repository for testing.
// Matches Python's mvmctl.core.volume._repository.Repository.
type VolumeRepo struct {
	mu      sync.RWMutex
	volumes map[string]*model.VolumeItem
}

func NewVolumeRepo() *VolumeRepo {
	return &VolumeRepo{volumes: make(map[string]*model.VolumeItem)}
}

func (r *VolumeRepo) Get(_ context.Context, id string) (*model.VolumeItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.volumes[id]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (r *VolumeRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.VolumeItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VolumeItem
	for _, v := range r.volumes {
		if len(v.ID) >= len(prefix) && v.ID[:len(prefix)] == prefix {
			result = append(result, v)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (r *VolumeRepo) GetByName(_ context.Context, name string) (*model.VolumeItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, nil
}

// ListAll returns all volumes ordered by created_at.
// Matches Python's Repository.list_all().
func (r *VolumeRepo) ListAll(_ context.Context) ([]*model.VolumeItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*model.VolumeItem, 0, len(r.volumes))
	for _, v := range r.volumes {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (r *VolumeRepo) Upsert(_ context.Context, v *model.VolumeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.volumes[v.ID] = v
	return nil
}

func (r *VolumeRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.volumes, id)
	return nil
}

func (r *VolumeRepo) FindByIDs(_ context.Context, ids []string) ([]*model.VolumeItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, id := range ids {
		set[id] = true
	}
	var result []*model.VolumeItem
	for _, v := range r.volumes {
		if set[v.ID] {
			result = append(result, v)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (r *VolumeRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.volumes), nil
}

// Ensure VolumeRepo implements volume.Repository.
var _ volume.Repository = (*VolumeRepo)(nil)
