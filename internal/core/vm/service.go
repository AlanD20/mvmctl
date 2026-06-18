// Package vm provides VM lifecycle management (start/stop/pause/resume).
// Layer: Core domain — never imports other core/* packages.
package vm

import (
	"context"

	"mvmctl/internal/infra/pool"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// --- Service ---

// Service is a stateless VM operations coordinator.
// Handles bulk operations and delegates single-VM operations to Controller.
type Service struct {
	repo Repository
}

// NewService creates a new VM service.
func NewService(repo Repository) *Service {
	return &Service{
		repo: repo,
	}
}

// --- Single-VM operations ---

// Stop stops a single VM and cleans up its Firecracker process.
func (s *Service) Stop(ctx context.Context, vm *model.VM, force bool) error {
	c := NewController(vm, s.repo)
	return c.Stop(ctx, force)
}

// Start boots a VM from its current state.
func (s *Service) Start(ctx context.Context, vm *model.VM) error {
	c := NewController(vm, s.repo)
	return c.Start(ctx)
}

// Pause suspends VM execution via the Firecracker API.
func (s *Service) Pause(ctx context.Context, vm *model.VM) error {
	c := NewController(vm, s.repo)
	return c.Pause(ctx)
}

// Resume continues execution of a paused VM.
func (s *Service) Resume(ctx context.Context, vm *model.VM) error {
	c := NewController(vm, s.repo)
	return c.Resume(ctx)
}

// Reboot restarts the VM by stopping and starting it.
func (s *Service) Reboot(ctx context.Context, vm *model.VM, force bool) error {
	c := NewController(vm, s.repo)
	return c.Reboot(ctx, force)
}

// --- Bulk operations ---

// StopMany stops multiple VMs.
func (s *Service) StopMany(
	ctx context.Context,
	vms []*model.VM,
	force bool,
	parallelism bool,
	maxWorkers int,
) *errs.BulkResult {
	fn := func(_ context.Context, vm *model.VM) (*model.VM, error) {
		return vm, s.Stop(ctx, vm, force)
	}
	var results []pool.Result[*model.VM]
	if parallelism {
		results = pool.Gather(ctx, maxWorkers, vms, fn)
	} else {
		results = pool.Seq(ctx, vms, fn)
	}
	return toBulkResult(results)
}

// StartMany starts multiple VMs.
func (s *Service) StartMany(
	ctx context.Context,
	vms []*model.VM,
	parallelism bool,
	maxWorkers int,
) *errs.BulkResult {
	fn := func(_ context.Context, vm *model.VM) (*model.VM, error) {
		return vm, s.Start(ctx, vm)
	}
	var results []pool.Result[*model.VM]
	if parallelism {
		results = pool.Gather(ctx, maxWorkers, vms, fn)
	} else {
		results = pool.Seq(ctx, vms, fn)
	}
	return toBulkResult(results)
}

// PauseMany pauses multiple VMs.
func (s *Service) PauseMany(
	ctx context.Context,
	vms []*model.VM,
	parallelism bool,
	maxWorkers int,
) *errs.BulkResult {
	fn := func(_ context.Context, vm *model.VM) (*model.VM, error) {
		return vm, s.Pause(ctx, vm)
	}
	var results []pool.Result[*model.VM]
	if parallelism {
		results = pool.Gather(ctx, maxWorkers, vms, fn)
	} else {
		results = pool.Seq(ctx, vms, fn)
	}
	return toBulkResult(results)
}

// ResumeMany resumes multiple VMs.
func (s *Service) ResumeMany(
	ctx context.Context,
	vms []*model.VM,
	parallelism bool,
	maxWorkers int,
) *errs.BulkResult {
	fn := func(_ context.Context, vm *model.VM) (*model.VM, error) {
		return vm, s.Resume(ctx, vm)
	}
	var results []pool.Result[*model.VM]
	if parallelism {
		results = pool.Gather(ctx, maxWorkers, vms, fn)
	} else {
		results = pool.Seq(ctx, vms, fn)
	}
	return toBulkResult(results)
}

// RebootMany reboots multiple VMs.
func (s *Service) RebootMany(
	ctx context.Context,
	vms []*model.VM,
	force bool,
	parallelism bool,
	maxWorkers int,
) *errs.BulkResult {
	fn := func(_ context.Context, vm *model.VM) (*model.VM, error) {
		return vm, s.Reboot(ctx, vm, force)
	}
	var results []pool.Result[*model.VM]
	if parallelism {
		results = pool.Gather(ctx, maxWorkers, vms, fn)
	} else {
		results = pool.Seq(ctx, vms, fn)
	}
	return toBulkResult(results)
}

// toBulkResult converts a slice of pool.Result into an errs.BulkResult.
func toBulkResult[T any](results []pool.Result[T]) *errs.BulkResult {
	r := &errs.BulkResult{Items: make([]errs.BulkResultItem, len(results))}
	for i, res := range results {
		r.Items[i] = errs.BulkResultItem{Item: res.Value, Error: res.Err}
	}
	return r
}
