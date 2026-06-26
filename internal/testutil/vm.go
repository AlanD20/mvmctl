package testutil

import (
	"context"
	"sync"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
)

// In-memory VM repository for testing.
type VMRepo struct {
	mu  sync.RWMutex
	vms map[string]*model.VMItem
}

func NewVMRepo() *VMRepo {
	return &VMRepo{vms: make(map[string]*model.VMItem)}
}

func (r *VMRepo) Get(_ context.Context, id string) (*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.vms[id]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (r *VMRepo) GetByName(_ context.Context, name string) (*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vms {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, nil
}

func (r *VMRepo) NamesExist(_ context.Context, names []string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []string
	for _, name := range names {
		for _, v := range r.vms {
			if v.Name == name {
				result = append(result, name)
				break
			}
		}
	}
	return result, nil
}

func (r *VMRepo) FindByIP(_ context.Context, ipv4 string) (*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vms {
		if v.IPv4 == ipv4 {
			return v, nil
		}
	}
	return nil, nil
}

func (r *VMRepo) FindByMAC(_ context.Context, mac string) (*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vms {
		if v.MAC == mac {
			return v, nil
		}
	}
	return nil, nil
}

func (r *VMRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		if len(v.ID) >= len(prefix) && v.ID[:len(prefix)] == prefix {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.vms), nil
}

func (r *VMRepo) CountByStatus(_ context.Context, statuses ...string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(statuses) == 0 {
		return len(r.vms), nil
	}
	set := make(map[string]bool)
	for _, s := range statuses {
		set[s] = true
	}
	count := 0
	for _, v := range r.vms {
		if set[string(v.Status)] {
			count++
		}
	}
	return count, nil
}

func (r *VMRepo) FindByNetworkID(_ context.Context, networkID string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		if v.NetworkID == networkID {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) GetByNetworkIDs(_ context.Context, networkIDs []string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, id := range networkIDs {
		set[id] = true
	}
	var result []*model.VMItem
	for _, v := range r.vms {
		if set[v.NetworkID] {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) FindByKernelID(_ context.Context, kernelID string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		if v.KernelID == kernelID {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) GetByKernelIDs(_ context.Context, kernelIDs []string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, id := range kernelIDs {
		set[id] = true
	}
	var result []*model.VMItem
	for _, v := range r.vms {
		if set[v.KernelID] {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) FindByBinaryID(_ context.Context, binaryID string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		if v.BinaryID == binaryID {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) GetByBinaryIDs(_ context.Context, binaryIDs []string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, id := range binaryIDs {
		set[id] = true
	}
	var result []*model.VMItem
	for _, v := range r.vms {
		if set[v.BinaryID] {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) FindByImageID(_ context.Context, imageID string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		if v.ImageID == imageID {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) GetByImageIDs(_ context.Context, imageIDs []string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, id := range imageIDs {
		set[id] = true
	}
	var result []*model.VMItem
	for _, v := range r.vms {
		if set[v.ImageID] {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) FindByVolumeID(_ context.Context, volumeID string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		for _, vid := range v.VolumeIDs {
			if vid == volumeID {
				result = append(result, v)
				break
			}
		}
	}
	return result, nil
}

func (r *VMRepo) FindByVolumeIDsBatch(_ context.Context, volumeIDs []string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, vid := range volumeIDs {
		set[vid] = true
	}
	seen := make(map[string]bool)
	var result []*model.VMItem
	for _, v := range r.vms {
		for _, vid := range v.VolumeIDs {
			if set[vid] {
				if !seen[v.ID] {
					seen[v.ID] = true
					result = append(result, v)
				}
				break
			}
		}
	}
	return result, nil
}

func (r *VMRepo) FindBySSHKeyID(_ context.Context, keyID string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.VMItem
	for _, v := range r.vms {
		for _, k := range v.SSHKeys {
			if k == keyID {
				result = append(result, v)
				break
			}
		}
	}
	return result, nil
}

func (r *VMRepo) ListAll(_ context.Context) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*model.VMItem, 0, len(r.vms))
	for _, v := range r.vms {
		result = append(result, v)
	}
	return result, nil
}

func (r *VMRepo) ListByStatus(ctx context.Context, statuses ...string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(statuses) == 0 {
		return r.ListAll(ctx)
	}
	set := make(map[string]bool)
	for _, s := range statuses {
		set[s] = true
	}
	var result []*model.VMItem
	for _, v := range r.vms {
		if set[string(v.Status)] {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) ListExcludingStatuses(ctx context.Context, excluded ...string) ([]*model.VMItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(excluded) == 0 {
		return r.ListAll(ctx)
	}
	set := make(map[string]bool)
	for _, s := range excluded {
		set[s] = true
	}
	var result []*model.VMItem
	for _, v := range r.vms {
		if !set[string(v.Status)] {
			result = append(result, v)
		}
	}
	return result, nil
}

func (r *VMRepo) Upsert(_ context.Context, v *model.VMItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vms[v.ID] = v
	return nil
}

func (r *VMRepo) UpdateStatus(_ context.Context, id string, status model.VMStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.vms[id]; ok {
		v.Status = status
	}
	return nil
}

func (r *VMRepo) UpdatePID(_ context.Context, id string, pid *int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.vms[id]; ok {
		if pid != nil {
			v.PID = *pid
		}
	}
	return nil
}

func (r *VMRepo) UpdateProcessInfo(_ context.Context, id string, pid *int, processStartTime *int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.vms[id]; ok {
		if pid != nil {
			v.PID = *pid
		}
		v.ProcessStartTime = processStartTime
	}
	return nil
}

func (r *VMRepo) UpdateExitCode(_ context.Context, id string, exitCode int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.vms[id]; ok {
		v.ExitCode = &exitCode
	}
	return nil
}

func (r *VMRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.vms, id)
	return nil
}

// Ensure VMRepo implements vm.Repository.
var _ vm.Repository = (*VMRepo)(nil)

func (r *VMRepo) DeleteMany(_ context.Context, ids []string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, id := range ids {
		if _, ok := r.vms[id]; ok {
			delete(r.vms, id)
			count++
		}
	}
	return count, nil
}
