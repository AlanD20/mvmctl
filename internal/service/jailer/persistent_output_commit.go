package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// commitPersistentOutputs runs once under durable launch authority while both children remain blocked.
// CALLER MUST HOLD: state.mu. Partial destructive attempts are never replayed on the same launch.
func commitPersistentOutputs(ctx context.Context, state *managedVMPersistentOutputState) error {
	if state.attempted {
		return managedVMResourceAtomicError("persistent outputs already have a preparation attempt", nil)
	}
	state.attempted = true
	for _, output := range state.outputs {
		if output.fd >= 0 {
			if _, err := inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1); err != nil {
				return err
			}
		}
	}
	for index := range state.outputs {
		if err := ctx.Err(); err != nil {
			return persistentOutputError("prepare persistent outputs", err)
		}
		output := &state.outputs[index]
		if output.fd < 0 {
			if err := publishManagedPersistentOutput(ctx, state, output); err != nil {
				return err
			}
			continue
		}
		if _, err := inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1); err != nil {
			return err
		}
		state.mutationAttempted = true
		if err := state.deps.ftruncate(ctx, output.fd, 0); err != nil {
			state.outcomeUnknown = true
			return persistentOutputError("truncate persistent output", err)
		}
		state.modified = true
		stat, err := inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1)
		if err != nil {
			return err
		}
		if stat.Size != 0 {
			return managedVMResourceValidationError("persistent output changed after truncation")
		}
		if err := state.deps.fsync(context.WithoutCancel(ctx), output.fd); err != nil {
			state.durabilityUncertain = true
			return persistentOutputError("sync truncated persistent output", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return persistentOutputError("complete persistent output preparation", err)
	}
	state.ready = true
	return nil
}

// CRITICAL: only the final link exposes a new output. Ownership, mode, truncation, and file sync finish anonymously.
// No pathname fallback is permitted if the filesystem does not support anonymous creation/publication.
func publishManagedPersistentOutput(
	ctx context.Context,
	state *managedVMPersistentOutputState,
	output *managedPersistentOutput,
) error {
	how := unix.OpenHow{
		Flags:   uint64(unix.O_RDWR | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_TMPFILE),
		Mode:    0600,
		Resolve: uint64(managedVMResourceResolveFlags),
	}
	fd, err := state.deps.openAt2(ctx, state.vmDirectoryFD, ".", &how)
	if err != nil {
		return persistentOutputError("create anonymous persistent output", err)
	}
	state.retained = append(state.retained, fd)
	output.fd = fd
	if err := state.deps.fchown(ctx, fd, int(state.owner.uid), int(state.owner.gid)); err != nil {
		return persistentOutputError("set anonymous persistent output owner", err)
	}
	if err := ctx.Err(); err != nil {
		return persistentOutputError("prepare anonymous persistent output", err)
	}
	if err := state.deps.fchmod(ctx, fd, 0600); err != nil {
		return persistentOutputError("set anonymous persistent output mode", err)
	}
	if err := ctx.Err(); err != nil {
		return persistentOutputError("prepare anonymous persistent output", err)
	}
	if err := state.deps.ftruncate(ctx, fd, 0); err != nil {
		return persistentOutputError("truncate anonymous persistent output", err)
	}
	stat, err := inspectManagedPersistentOutputObject(ctx, state, fd, false, 0)
	if err != nil {
		return err
	}
	if stat.Size != 0 {
		return managedVMResourceValidationError("anonymous persistent output is not empty")
	}
	if err := state.deps.fsync(ctx, fd); err != nil {
		return persistentOutputError("sync anonymous persistent output", err)
	}
	if err := ctx.Err(); err != nil {
		return persistentOutputError("publish persistent output", err)
	}
	state.mutationAttempted = true
	if err := state.deps.publish(ctx, fd, state.vmDirectoryFD, output.name); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errs.WrapMsg(errs.CodeValidationFailed, "persistent output name already exists", err)
		}
		state.outcomeUnknown = true
		return persistentOutputError("publish persistent output", err)
	}
	state.modified = true
	state.published = true
	if err := state.deps.fsync(context.WithoutCancel(ctx), state.vmDirectoryFD); err != nil {
		state.durabilityUncertain = true
		return persistentOutputError("sync published persistent output directory", err)
	}
	stat, err = inspectManagedPersistentOutputObject(ctx, state, fd, false, 1)
	if err != nil {
		return err
	}
	if stat.Size != 0 {
		return managedVMResourceValidationError("persistent output changed after publication")
	}
	return nil
}

func persistentOutputError(message string, cause error) error {
	if errs.AsDomainError(cause) != nil {
		return cause
	}
	return managedVMResourceAtomicError(message, cause)
}

// annotatePersistentOutputError copies primary metadata so concurrent callers cannot mutate a cached error.
func annotatePersistentOutputError(primary error, state *managedVMPersistentOutputState) error {
	if primary == nil {
		return nil
	}
	domain := errs.AsDomainError(primary)
	if domain == nil {
		domain = managedVMResourceAtomicError("prepare persistent outputs", primary)
	}
	copyOf := *domain
	copyOf.Details = make(map[string]any, len(domain.Details)+7)
	for key, value := range domain.Details {
		copyOf.Details[key] = value
	}
	copyOf.Details["output_mutation_attempted"] = state.mutationAttempted
	copyOf.Details["outputs_modified"] = state.modified
	copyOf.Details["output_published"] = state.published
	priorDurability, _ := domain.Details["durability_uncertain"].(bool)
	priorUnknown, _ := domain.Details["outcome_unknown"].(bool)
	copyOf.Details["durability_uncertain"] = state.durabilityUncertain || priorDurability
	copyOf.Details["outcome_unknown"] = state.outcomeUnknown || priorUnknown
	copyOf.Details["output_cleanup_required"] = !state.released
	return &copyOf
}
