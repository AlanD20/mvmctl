package testutil

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
)

// VsockRepo is an in-memory vsock repository for testing.
type VsockRepo struct {
	mu   sync.RWMutex
	cfgs map[string]*model.VsockConfigItem
}

func NewVsockRepo() *VsockRepo {
	return &VsockRepo{cfgs: make(map[string]*model.VsockConfigItem)}
}

func (r *VsockRepo) GetByVMID(_ context.Context, vmID string) (*model.VsockConfigItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.cfgs {
		if c.VmID == vmID {
			return c, nil
		}
	}
	return nil, nil
}

func (r *VsockRepo) Upsert(_ context.Context, item *model.VsockConfigItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfgs[item.ID] = item
	return nil
}

func (r *VsockRepo) ListByVMIDs(_ context.Context, vmIDs []string) ([]*model.VsockConfigItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make(map[string]bool, len(vmIDs))
	for _, id := range vmIDs {
		ids[id] = true
	}
	var result []*model.VsockConfigItem
	for _, c := range r.cfgs {
		if ids[c.VmID] {
			result = append(result, c)
		}
	}
	return result, nil
}

func (r *VsockRepo) DeleteByVMID(_ context.Context, vmID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.cfgs {
		if c.VmID == vmID {
			delete(r.cfgs, id)
			break
		}
	}
	return nil
}

func (r *VsockRepo) SetUpgradeLock(_ context.Context, vmID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cfgs {
		if c.VmID == vmID {
			if c.Upgrading {
				return fmt.Errorf("upgrade already in progress for VM %s", vmID)
			}
			now := time.Now()
			c.Upgrading = true
			c.UpgradeStartedAt = &now
			return nil
		}
	}
	// CONTRACT: no matching VM means zero rows affected → same error as SQLite.
	return fmt.Errorf("upgrade already in progress for VM %s", vmID)
}

func (r *VsockRepo) ClearUpgradeLock(_ context.Context, vmID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cfgs {
		if c.VmID == vmID {
			c.Upgrading = false
			c.UpgradeStartedAt = nil
			break
		}
	}
	return nil
}

func (r *VsockRepo) UpdateAgentVersion(_ context.Context, vmID, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cfgs {
		if c.VmID == vmID {
			c.AgentVersion = version
			break
		}
	}
	return nil
}

// Compile-time check that VsockRepo implements vsock.Repository.
var _ vsock.Repository = (*VsockRepo)(nil)
