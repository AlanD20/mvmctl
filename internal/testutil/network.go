package testutil

import (
	"context"
	"sort"
	"sync"
	"time"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
)

// NetworkRepo is an in-memory network repository for testing.
// Includes soft-delete filtering (deleted_at IS NULL, is_present = 1).
type NetworkRepo struct {
	mu       sync.RWMutex
	networks map[string]*model.NetworkItem
}

func NewNetworkRepo() *NetworkRepo {
	return &NetworkRepo{
		networks: make(map[string]*model.NetworkItem),
	}
}

// isNotDeleted returns true if the network is NOT soft-deleted.
func (r *NetworkRepo) isNotDeleted(n *model.NetworkItem) bool {
	return n.DeletedAt == nil && n.IsPresent
}

// Get returns a network by ID. Returns nil if soft-deleted.
func (r *NetworkRepo) Get(_ context.Context, id string) (*model.NetworkItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.networks[id]
	if !ok || !r.isNotDeleted(n) {
		return nil, nil
	}
	return n, nil
}

// GetByName returns a network by name. Returns nil if soft-deleted.
func (r *NetworkRepo) GetByName(_ context.Context, name string) (*model.NetworkItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range r.networks {
		if n.Name == name && r.isNotDeleted(n) {
			return n, nil
		}
	}
	return nil, nil
}

// FindByPrefix returns all non-deleted networks whose ID starts with prefix.
func (r *NetworkRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.NetworkItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.NetworkItem
	for _, n := range r.networks {
		if r.isNotDeleted(n) && len(n.ID) >= len(prefix) && n.ID[:len(prefix)] == prefix {
			result = append(result, n)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// Count returns total count of all non-deleted networks.
func (r *NetworkRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, n := range r.networks {
		if n.DeletedAt == nil {
			count++
		}
	}
	return count, nil
}

// ListAll returns all non-deleted networks ordered by created_at.
func (r *NetworkRepo) ListAll(_ context.Context) ([]*model.NetworkItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.NetworkItem
	for _, n := range r.networks {
		if r.isNotDeleted(n) {
			result = append(result, n)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (r *NetworkRepo) Upsert(_ context.Context, n *model.NetworkItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.networks[n.ID] = n
	return nil
}

func (r *NetworkRepo) UpdateBridgeActive(_ context.Context, networkID string, active bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.networks[networkID]; ok {
		n.BridgeActive = active
	}
	return nil
}

// SetDefault sets one network as default, clearing all others atomically.
func (r *NetworkRepo) SetDefault(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Clear existing defaults
	for _, n := range r.networks {
		if n.DeletedAt == nil {
			n.IsDefault = false
		}
	}
	// Set new default
	if n, ok := r.networks[id]; ok && n.DeletedAt == nil {
		n.IsDefault = true
	}
	return nil
}

// GetDefault returns the default network entry, or nil if not set.
func (r *NetworkRepo) GetDefault(_ context.Context) (*model.NetworkItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range r.networks {
		if n.IsDefault && r.isNotDeleted(n) {
			return n, nil
		}
	}
	return nil, nil
}

func (r *NetworkRepo) UpdateManyIsPresent(_ context.Context, networkIDs []string, isPresent bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range networkIDs {
		if n, ok := r.networks[id]; ok {
			n.IsPresent = isPresent
		}
	}
	return nil
}

// SoftDelete marks a network as deleted with timestamp.
func (r *NetworkRepo) SoftDelete(_ context.Context, networkID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.networks[networkID]; ok {
		// Use RFC3339 timestamp
		now := time.Now().UTC().Format(time.RFC3339)
		n.IsPresent = false
		n.DeletedAt = &now
	}
	return nil
}

// Delete hard-deletes a network by ID.
func (r *NetworkRepo) Delete(_ context.Context, networkID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.networks, networkID)
	return nil
}

// Ensure NetworkRepo implements network.Repository.
var _ network.Repository = (*NetworkRepo)(nil)
