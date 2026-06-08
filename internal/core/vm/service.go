package vm

import (
	"context"

	"mvmctl/internal/infra/pool"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ── Service ──
// Matches Python's VMService class exactly.

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

// ── Single-VM operations ──

// Stop stops a single VM. Matches Python's stop().
func (s *Service) Stop(ctx context.Context, vm *model.VM, force bool) error {
	c := NewController(vm, s.repo)
	return c.Stop(ctx, force)
}

// Start starts a single VM. Matches Python's start().
func (s *Service) Start(ctx context.Context, vm *model.VM) error {
	c := NewController(vm, s.repo)
	return c.Start(ctx)
}

// Pause pauses a single VM. Matches Python's pause().
func (s *Service) Pause(ctx context.Context, vm *model.VM) error {
	c := NewController(vm, s.repo)
	return c.Pause(ctx)
}

// Resume resumes a single VM. Matches Python's resume().
func (s *Service) Resume(ctx context.Context, vm *model.VM) error {
	c := NewController(vm, s.repo)
	return c.Resume(ctx)
}

// Reboot reboots a single VM. Matches Python's reboot().
func (s *Service) Reboot(ctx context.Context, vm *model.VM, force bool) error {
	c := NewController(vm, s.repo)
	return c.Reboot(ctx, force)
}

// ── Bulk operations ──
// These match Python's VMService stop_many, start_many, etc. exactly.

// StopMany stops multiple VMs. Matches Python's stop_many().
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

// StartMany starts multiple VMs. Matches Python's start_many().
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

// PauseMany pauses multiple VMs. Matches Python's pause_many().
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

// ResumeMany resumes multiple VMs. Matches Python's resume_many().
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

// RebootMany reboots multiple VMs. Matches Python's reboot_many().
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
