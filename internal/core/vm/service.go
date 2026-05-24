package vm

import (
	"context"
	"runtime"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/parallel"
)

// ── Parallel execution helpers matching Python's ParallelExecutor ──
// These are package-level functions (not methods) because Go doesn't allow
// generic methods on structs. The behavior matches Python exactly.

// executorItem is a generic pair of item and result/error.
type executorItem[T, R any] struct {
	Item   T
	Result R
}

// executeSeq runs functions sequentially (fail-fast on first error).
// Matches Python's ParallelExecutor._sequential().
func executeSeq[T, R any](items []T, fn func(T) R) []executorItem[T, R] {
	var results []executorItem[T, R]
	for _, item := range items {
		res := fn(item)
		results = append(results, executorItem[T, R]{item, res})
		// Check if result is an error — fail-fast (matches Python's "break")
		if err, ok := any(res).(error); ok && err != nil {
			break
		}
	}
	return results
}

// executePar runs functions in parallel using a worker pool.
// Matches Python's ParallelExecutor._execute_batch() + _parallel().
func executePar[T, R any](ctx context.Context, items []T, fn func(T) R, maxWorkers int, batchSize int) []executorItem[T, R] {
	var allResults []executorItem[T, R]

	for i := 0; i < len(items); i += batchSize {
		end := i + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		batchResults, _ := parallel.Map(ctx, maxWorkers, batch, func(_ context.Context, it T) (executorItem[T, R], error) {
			res := fn(it)
			return executorItem[T, R]{Item: it, Result: res}, nil
		})

		allResults = append(allResults, batchResults...)
	}

	return allResults
}

// execute runs func on each item, matching Python's ParallelExecutor.execute().
// Sequential mode: fail-fast on first error.
// Parallel mode: process all items, collect errors, continue on failure.
func execute[T, R any](
	ctx context.Context,
	items []T,
	fn func(T) R,
	parallel bool,
	maxWorkers int,
	batchSize int,
) []executorItem[T, R] {
	if parallel {
		n := len(items)
		if maxWorkers <= 0 {
			maxWorkers = min(n, runtime.NumCPU()*2)
		}
		if batchSize <= 0 {
			batchSize = n
		}
		if maxWorkers < 1 {
			maxWorkers = 1
		}
		if batchSize < 1 {
			batchSize = 1
		}
		return executePar(ctx, items, fn, maxWorkers, batchSize)
	}
	return executeSeq(items, fn)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

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
func (s *Service) newController(vm *model.VM) *Controller {
	c, err := NewController(vm, s.repo)
	if err != nil {
		panic("newController: unexpected error with pre-resolved VM: " + err.Error())
	}
	return c
}

// Stop stops a single VM. Matches Python's stop().
func (s *Service) Stop(ctx context.Context, vm *model.VM, force bool) error {
	return s.newController(vm).Stop(ctx, force)
}

// Start starts a single VM. Matches Python's start().
func (s *Service) Start(ctx context.Context, vm *model.VM) error {
	return s.newController(vm).Start(ctx)
}

// Pause pauses a single VM. Matches Python's pause().
func (s *Service) Pause(ctx context.Context, vm *model.VM) error {
	return s.newController(vm).Pause(ctx)
}

// Resume resumes a single VM. Matches Python's resume().
func (s *Service) Resume(ctx context.Context, vm *model.VM) error {
	return s.newController(vm).Resume(ctx)
}

// Reboot reboots a single VM. Matches Python's reboot().
func (s *Service) Reboot(ctx context.Context, vm *model.VM, force bool) error {
	return s.newController(vm).Reboot(ctx, force)
}

// ── Bulk operations ──
// These match Python's VMService stop_many, start_many, etc. exactly.
// Uses the shared BulkResult from infra/errs (not a local generic type),
// matching how other domains (image, kernel, etc.) use it.

// buildResult converts executorItem results to the shared errs.BulkResult.
func buildResult(items []executorItem[*model.VM, error]) *errs.BulkResult {
	result := &errs.BulkResult{
		Items: make([]errs.BulkResultItem, len(items)),
	}
	for i, r := range items {
		result.Items[i] = errs.BulkResultItem{Item: r.Item, Error: r.Result}
	}
	return result
}

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
