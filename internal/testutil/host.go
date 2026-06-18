package testutil

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mvmctl/internal/core/host"
	"mvmctl/internal/lib/model"
)

// HostRepo is an in-memory host repository for testing.
type HostRepo struct {
	mu      sync.RWMutex
	state   *model.HostStateItem
	changes []*model.HostStateChangeItem
	nextID  int
}

func NewHostRepo() *HostRepo {
	return &HostRepo{
		changes: make([]*model.HostStateChangeItem, 0),
		nextID:  1,
	}
}

func (r *HostRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.changes), nil
}

func (r *HostRepo) GetState(_ context.Context) (*model.HostStateItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.state == nil {
		return nil, nil
	}
	return r.state, nil
}

func (r *HostRepo) InitializeState(_ context.Context) (*model.HostStateItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		now := time.Now().UTC().Format(time.RFC3339)
		r.state = &model.HostStateItem{
			ID:            1,
			Initialized:   false,
			InitializedAt: now,
			UpdatedAt:     now,
		}
	}
	return r.state, nil
}

func (r *HostRepo) SetInitialized(_ context.Context, initializedAt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != nil {
		r.state.Initialized = true
		r.state.InitializedAt = initializedAt
	}
	return nil
}

// UpdateComponent updates a single host initialization component flag.
// Validates against allowed set and returns error for unknown components.
func (r *HostRepo) UpdateComponent(_ context.Context, component string, value bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		return nil
	}
	// Validate against allowed set
	allowed := map[string]bool{
		"mvm_group_created":       true,
		"sudoers_configured":      true,
		"default_network_created": true,
	}
	if !allowed[component] {
		return fmt.Errorf("Unknown host state component: %q", component)
	}
	switch component {
	case "mvm_group_created":
		r.state.MvmGroupCreated = value
	case "sudoers_configured":
		r.state.SudoersConfigured = value
	case "default_network_created":
		r.state.DefaultNetworkCreated = value
	}
	return nil
}

func (r *HostRepo) ResetState(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != nil {
		r.state.Initialized = false
		r.state.MvmGroupCreated = false
		r.state.SudoersConfigured = false
		r.state.DefaultNetworkCreated = false
	}
	return nil
}

// SaveCapacity upserts host capacity detection results atomically.
func (r *HostRepo) SaveCapacity(_ context.Context,
	hostname string,
	cpuModel string,
	cpuVendor string,
	cpuCores int,
	cpuArchitecture string,
	numaNodes int,
	memoryTotalMiB int,
	storageTotalBytes int,
	kernelVersion string,
	osRelease string,
	pidMax int,
	fdMax int,
	conntrackMax int,
	tapDevicesMax int,
	ipLocalPortRange [2]int,
	detectedAt string,
	cpuHasVMX bool,
	cpuHypervisor bool,
	nestedVirtAvailable bool,
	eptAvailable bool,
	hugepageCount2MB int,
	ksmDisabled bool,
	cgroupVersion int,
	swapTotalMiB int,
	kernelMinimumMet bool,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ensure row exists (singleton id=1) — use lock as our transaction
	now := time.Now().UTC().Format(time.RFC3339)
	if r.state == nil {
		r.state = &model.HostStateItem{
			ID:            1,
			Initialized:   false,
			InitializedAt: now,
			UpdatedAt:     now,
		}
	}

	// Update host state fields
	h := hostname
	cm := cpuModel
	cv := cpuVendor
	ca := cpuArchitecture
	kv := kernelVersion
	or := osRelease
	da := detectedAt
	ipRange := fmt.Sprintf("%d,%d", ipLocalPortRange[0], ipLocalPortRange[1])

	r.state.Hostname = &h
	r.state.CPUModel = &cm
	r.state.CPUVendor = &cv
	r.state.CPUCores = &cpuCores
	r.state.CPUArchitecture = &ca
	r.state.NumaNodes = &numaNodes
	r.state.MemoryTotalMiB = &memoryTotalMiB
	r.state.StorageTotalBytes = &storageTotalBytes
	r.state.KernelVersion = &kv
	r.state.OSRelease = &or
	r.state.PIDMax = &pidMax
	r.state.FDMax = &fdMax
	r.state.ConntrackMax = &conntrackMax
	r.state.TAPDevicesMax = &tapDevicesMax
	r.state.IPLocalPortRange = &ipRange
	r.state.DetectedAt = &da

	cvx := 0
	if cpuHasVMX {
		cvx = 1
	}
	r.state.CPUHasVMX = &cvx
	ch := 0
	if cpuHypervisor {
		ch = 1
	}
	r.state.CPUHypervisor = &ch
	nv := 0
	if nestedVirtAvailable {
		nv = 1
	}
	r.state.NestedVirtAvailable = &nv
	ea := 0
	if eptAvailable {
		ea = 1
	}
	r.state.EPTAvailable = &ea
	r.state.HugepageCount2MB = &hugepageCount2MB
	ksm := 0
	if ksmDisabled {
		ksm = 1
	}
	r.state.KSMDisabled = &ksm
	r.state.CgroupVersion = &cgroupVersion
	r.state.SwapTotalMiB = &swapTotalMiB
	km := 0
	if kernelMinimumMet {
		km = 1
	}
	r.state.KernelMinimumMet = &km
	return nil
}

func (r *HostRepo) AddChange(_ context.Context, change *model.HostStateChangeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	change.ID = &id
	r.changes = append(r.changes, change)
	return nil
}

// AddChanges bulk inserts host state changes atomically.
func (r *HostRepo) AddChanges(_ context.Context, changes []*model.HostStateChangeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, change := range changes {
		id := r.nextID
		r.nextID++
		change.ID = &id
		r.changes = append(r.changes, change)
	}
	return nil
}

func (r *HostRepo) DeleteChangesExceptSession(_ context.Context, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var kept []*model.HostStateChangeItem
	for _, c := range r.changes {
		if c.SessionID == sessionID {
			kept = append(kept, c)
		}
	}
	r.changes = kept
	return nil
}

// ListChanges returns host state changes, optionally filtered by session and reverted status.
func (r *HostRepo) ListChanges(
	_ context.Context,
	sessionID *string,
	includeReverted bool,
) ([]*model.HostStateChangeItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.HostStateChangeItem
	for _, c := range r.changes {
		if sessionID != nil && c.SessionID != *sessionID {
			continue
		}
		if !includeReverted && c.Reverted {
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

func (r *HostRepo) MarkChangeReverted(
	_ context.Context,
	changeID int,
	revertedAt string,
	revertMechanism *string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.changes {
		if c.ID != nil && *c.ID == changeID {
			c.Reverted = true
			c.RevertedAt = &revertedAt
			c.RevertMechanism = revertMechanism
			break
		}
	}
	return nil
}

// RevertChanges marks all unreverted changes for a session as reverted (LIFO order).
func (r *HostRepo) RevertChanges(
	_ context.Context,
	sessionID string,
	revertedAt string,
) ([]*model.HostStateChangeItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var reverted []*model.HostStateChangeItem
	for i := len(r.changes) - 1; i >= 0; i-- {
		c := r.changes[i]
		if c.SessionID == sessionID && !c.Reverted {
			c.Reverted = true
			c.RevertedAt = &revertedAt
			reverted = append(reverted, c)
		}
	}
	return reverted, nil
}

// Ensure HostRepo implements host.Repository.
var _ host.Repository = (*HostRepo)(nil)
