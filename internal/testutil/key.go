package testutil

import (
	"context"
	"sync"

	"mvmctl/internal/core/key"
	"mvmctl/internal/infra/model"
)

type KeyRepo struct {
	mu   sync.RWMutex
	keys map[string]*model.SSHKeyItem
}

func NewKeyRepo() *KeyRepo {
	return &KeyRepo{keys: make(map[string]*model.SSHKeyItem)}
}

func (r *KeyRepo) GetByName(_ context.Context, name string) (*model.SSHKeyItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, k := range r.keys {
		if k.Name == name {
			return k, nil
		}
	}
	return nil, nil
}

func (r *KeyRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.SSHKeyItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.SSHKeyItem
	for _, k := range r.keys {
		if len(k.ID) >= len(prefix) && k.ID[:len(prefix)] == prefix {
			result = append(result, k)
		}
	}
	return result, nil
}

func (r *KeyRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.keys), nil
}

func (r *KeyRepo) List(_ context.Context) ([]*model.SSHKeyItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*model.SSHKeyItem, 0, len(r.keys))
	for _, k := range r.keys {
		result = append(result, k)
	}
	return result, nil
}

func (r *KeyRepo) Upsert(_ context.Context, k *model.SSHKeyItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[k.ID] = k
	return nil
}

func (r *KeyRepo) UpdateManyIsPresent(_ context.Context, ids []string, present bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if k, ok := r.keys[id]; ok {
			k.IsPresent = present
		}
	}
	return nil
}

func (r *KeyRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, id)
	return nil
}

func (r *KeyRepo) SetDefault(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.keys[id]; ok {
		k.IsDefault = true
	}
	return nil
}

func (r *KeyRepo) GetDefaults(_ context.Context) ([]*model.SSHKeyItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.SSHKeyItem
	for _, k := range r.keys {
		if k.IsDefault {
			result = append(result, k)
		}
	}
	return result, nil
}

// Ensure KeyRepo implements key.Repository.
var _ key.Repository = (*KeyRepo)(nil)

func (r *KeyRepo) ClearDefaults(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		k.IsDefault = false
	}
	return nil
}
