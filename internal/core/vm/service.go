package vm

import (
	"context"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
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

// newController creates a VM controller from a resolved VM.
// This is a convenience wrapper since NewController with a *model.VM never errors.
func (s *Service) newController(ctx context.Context, vm *model.VM) *Controller {
	c, err := NewController(ctx, vm, s.repo)
	if err != nil {
		panic("newController: unexpected error with pre-resolved VM: " + err.Error())
	}
	return c
}

// Stop stops a single VM. Matches Python's stop().
func (s *Service) Stop(ctx context.Context, vm *model.VM, force bool) error {
	return s.newController(ctx, vm).Stop(ctx, force)
}

// Start starts a single VM. Matches Python's start().
func (s *Service) Start(ctx context.Context, vm *model.VM) error {
	return s.newController(ctx, vm).Start(ctx)
}

// Pause pauses a single VM. Matches Python's pause().
func (s *Service) Pause(ctx context.Context, vm *model.VM) error {
	return s.newController(ctx, vm).Pause(ctx)
}

// Resume resumes a single VM. Matches Python's resume().
func (s *Service) Resume(ctx context.Context, vm *model.VM) error {
	return s.newController(ctx, vm).Resume(ctx)
}

// Reboot reboots a single VM. Matches Python's reboot().
func (s *Service) Reboot(ctx context.Context, vm *model.VM, force bool) error {
	return s.newController(ctx, vm).Reboot(ctx, force)
}

// ── Bulk operations ──
// These match Python's VMService stop_many, start_many, etc. exactly.
// Uses the shared BulkResult from infra/errs (not a local generic type),
// matching how other domains (image, kernel, etc.) use it.

// StopMany stops multiple VMs. Matches Python's stop_many().
func (s *Service) StopMany(
	ctx context.Context,
	vms []*model.VM,
	force bool,
	parallel bool,
	maxWorkers int,
	batchSize int,
) *errs.BulkResult {
	raw := execute(
		ctx,
		vms,
		func(vm *model.VM) error {
			return s.Stop(ctx, vm, force)
		},
		parallel,
		maxWorkers,
		batchSize,
	)
	return buildResult(raw)
}

// StartMany starts multiple VMs. Matches Python's start_many().
func (s *Service) StartMany(
	ctx context.Context,
	vms []*model.VM,
	parallel bool,
	maxWorkers int,
	batchSize int,
) *errs.BulkResult {
	raw := execute(
		ctx,
		vms,
		func(vm *model.VM) error {
			return s.Start(ctx, vm)
		},
		parallel,
		maxWorkers,
		batchSize,
	)
	return buildResult(raw)
}

// PauseMany pauses multiple VMs. Matches Python's pause_many().
func (s *Service) PauseMany(
	ctx context.Context,
	vms []*model.VM,
	parallel bool,
	maxWorkers int,
	batchSize int,
) *errs.BulkResult {
	raw := execute(
		ctx,
		vms,
		func(vm *model.VM) error {
			return s.Pause(ctx, vm)
		},
		parallel,
		maxWorkers,
		batchSize,
	)
	return buildResult(raw)
}

// ResumeMany resumes multiple VMs. Matches Python's resume_many().
func (s *Service) ResumeMany(
	ctx context.Context,
	vms []*model.VM,
	parallel bool,
	maxWorkers int,
	batchSize int,
) *errs.BulkResult {
	raw := execute(
		ctx,
		vms,
		func(vm *model.VM) error {
			return s.Resume(ctx, vm)
		},
		parallel,
		maxWorkers,
		batchSize,
	)
	return buildResult(raw)
}

// RebootMany reboots multiple VMs. Matches Python's reboot_many().
func (s *Service) RebootMany(
	ctx context.Context,
	vms []*model.VM,
	force bool,
	parallel bool,
	maxWorkers int,
	batchSize int,
) *errs.BulkResult {
	raw := execute(
		ctx,
		vms,
		func(vm *model.VM) error {
			return s.Reboot(ctx, vm, force)
		},
		parallel,
		maxWorkers,
		batchSize,
	)
	return buildResult(raw)
}
