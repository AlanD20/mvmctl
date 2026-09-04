package firewall

import (
	"context"
	"errors"
	"maps"
	"time"

	"mvmctl/pkg/errs"
)

const (
	generationApplicationRecoveryTimeout      = 5 * time.Second
	generationApplicationKernelStateDetailKey = "kernel_generation_state"

	generationApplicationOutcomeUnknownDetailKey = "outcome_unknown"
)

type generationApplicationKernelState string

const (
	generationApplicationKernelBefore  generationApplicationKernelState = "before"
	generationApplicationKernelTarget  generationApplicationKernelState = "target"
	generationApplicationKernelUnknown generationApplicationKernelState = "unknown"
)

// generationKernelApplication is the closed kernel capability retained by one exact namespace lease. It exposes no
// descriptor, name, expression, transport, or generic operation surface to the durable generation coordinator.
type generationKernelApplication interface {
	observeGeneration(context.Context) (kernelObservation, error)
	applyGenerationDuringApplication(
		context.Context,
		*persistentGenerationLease,
		kernelObservation,
		compiledGeneration,
		compiledGeneration,
	) (kernelApplicationResult, error)
}

func (lease *persistentGenerationLease) applyCompiledGeneration(
	ctx context.Context,
	kernel generationKernelApplication,
	before compiledGeneration,
	target compiledGeneration,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("persistent firewall generation lease is unavailable", nil, nil)
	}
	if ctx == nil {
		return generationAuthorityError("firewall generation application context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return generationAuthorityError("apply compiled firewall generation", err, nil)
	}
	if kernel == nil {
		return generationAuthorityError("firewall generation application authority is unavailable", nil, nil)
	}
	if err := validateKernelApplicationTarget(before); err != nil {
		return err
	}
	if err := validateKernelApplicationTarget(target); err != nil {
		return err
	}
	state := lease.state
	state.applicationMu.Lock()
	defer state.applicationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return generationAuthorityError("apply compiled firewall generation", err, nil)
	}
	return lease.applyCompiledGenerationDuringApplication(ctx, kernel, before, target)
}

// applyCompiledGenerationDuringApplication owns the non-locking stages of one serialized record -> kernel -> record
// transaction. Its caller holds state.applicationMu for the entire sequence, including fresh reconciliation.
func (lease *persistentGenerationLease) applyCompiledGenerationDuringApplication(
	ctx context.Context,
	kernel generationKernelApplication,
	before compiledGeneration,
	target compiledGeneration,
) error {
	beforeIdentity := generationRecordIdentityFromCompiled(before)
	targetIdentity := generationRecordIdentityFromCompiled(target)
	record, err := lease.state.retainedGenerationRecord()
	if err != nil {
		return preserveGenerationAuthorityError("read admitted firewall generation before application", err)
	}
	switch record.lifecycle {
	case generationRecordCommitted:
		if record.committed != beforeIdentity {
			return generationAuthorityError(
				"compiled prior firewall generation does not match committed authority",
				nil,
				nil,
			)
		}
		if beforeIdentity == targetIdentity {
			return verifyExactCommittedGeneration(ctx, kernel, before)
		}
		applying, err := beginGenerationRecordApplication(record, beforeIdentity, targetIdentity)
		if err != nil {
			return err
		}
		if err := lease.replaceAdmittedRecordDuringApplication(ctx, applying); err != nil {
			return preserveGenerationAuthorityError("persist applying firewall generation", err)
		}
		record = applying
	case generationRecordApplying:
		if record.before == nil || record.target == nil || *record.before != beforeIdentity ||
			*record.target != targetIdentity {
			return generationAuthorityError(
				"compiled firewall generations do not match applying authority",
				nil,
				nil,
			)
		}
	default:
		return generationAuthorityError("firewall generation record lifecycle is invalid", nil, nil)
	}

	// CRITICAL: The applying record is durable before this fresh observation. Only exact-before permits Flush; exact
	// target completes metadata, and every other result retains the applying transition for later reconciliation.
	observed, err := kernel.observeGeneration(ctx)
	if err != nil {
		return generationApplicationFailure(
			"inspect applying firewall generation",
			err,
			generationApplicationKernelUnknown,
			true,
		)
	}
	switch classifyGenerationApplicationObservation(observed, before, target) {
	case generationApplicationKernelTarget:
		return completeObservedGenerationApplication(ctx, lease, record, targetIdentity, nil)
	case generationApplicationKernelBefore:
		// The admitted applying record and exact fresh observation authorize one atomic kernel mutation.
	case generationApplicationKernelUnknown:
		return generationApplicationFailure(
			"cannot reconcile applying firewall generation",
			nil,
			generationApplicationKernelUnknown,
			true,
		)
	}

	result, applyErr := kernel.applyGenerationDuringApplication(ctx, lease, observed, before, target)
	if !result.commitAttempted {
		if applyErr == nil {
			applyErr = errors.New("kernel application returned before its commit boundary without an error")
		}
		recoveryCtx, cancel := generationApplicationRecoveryContext(ctx)
		defer cancel()
		committed, recordErr := newCommittedGenerationRecord(record.binding, beforeIdentity)
		if recordErr == nil {
			recordErr = lease.replaceAdmittedRecordDuringApplication(recoveryCtx, committed)
		}
		return generationApplicationFailure(
			"firewall generation application failed before commit",
			errors.Join(applyErr, recordErr),
			generationApplicationKernelBefore,
			false,
		)
	}

	recoveryCtx, cancel := generationApplicationRecoveryContext(ctx)
	defer cancel()
	recovered, recoveryErr := kernel.observeGeneration(recoveryCtx)
	if recoveryErr != nil {
		return generationApplicationFailure(
			"firewall generation outcome could not be inspected",
			errors.Join(applyErr, recoveryErr),
			generationApplicationKernelUnknown,
			true,
		)
	}
	observedState := classifyGenerationApplicationObservation(recovered, before, target)
	switch observedState {
	case generationApplicationKernelBefore:
		if applyErr == nil {
			applyErr = errors.New("atomic kernel application did not install the target firewall generation")
		}
		committed, recordErr := newCommittedGenerationRecord(record.binding, beforeIdentity)
		if recordErr == nil {
			recordErr = lease.replaceAdmittedRecordDuringApplication(recoveryCtx, committed)
		}
		return generationApplicationFailure(
			"kernel retained the prior firewall generation",
			errors.Join(applyErr, recordErr),
			generationApplicationKernelBefore,
			false,
		)
	case generationApplicationKernelTarget:
		return completeObservedGenerationApplication(recoveryCtx, lease, record, targetIdentity, applyErr)
	case generationApplicationKernelUnknown:
		return generationApplicationFailure(
			"kernel firewall generation outcome is unknown",
			applyErr,
			generationApplicationKernelUnknown,
			true,
		)
	default:
		return generationApplicationFailure(
			"kernel firewall generation outcome is invalid",
			applyErr,
			generationApplicationKernelUnknown,
			true,
		)
	}
}

func verifyExactCommittedGeneration(
	ctx context.Context,
	kernel generationKernelApplication,
	expected compiledGeneration,
) error {
	observed, err := kernel.observeGeneration(ctx)
	if err != nil {
		return generationApplicationFailure(
			"inspect committed firewall generation",
			err,
			generationApplicationKernelUnknown,
			true,
		)
	}
	if err := compareKernelObservation(observed, expected); err != nil {
		return generationApplicationFailure(
			"kernel firewall generation does not match committed authority",
			err,
			generationApplicationKernelUnknown,
			true,
		)
	}
	return nil
}

func completeObservedGenerationApplication(
	ctx context.Context,
	lease *persistentGenerationLease,
	record generationRecord,
	target generationRecordIdentity,
	applicationErr error,
) error {
	committed, recordErr := completeGenerationRecordApplication(record, target)
	if recordErr == nil {
		recordErr = lease.replaceAdmittedRecordDuringApplication(ctx, committed)
	}
	if applicationErr == nil && recordErr == nil {
		return nil
	}
	return generationApplicationFailure(
		"kernel installed the target firewall generation but application did not complete cleanly",
		errors.Join(applicationErr, recordErr),
		generationApplicationKernelTarget,
		false,
	)
}

func classifyGenerationApplicationObservation(
	observed kernelObservation,
	before compiledGeneration,
	target compiledGeneration,
) generationApplicationKernelState {
	if compareKernelObservation(observed, before) == nil {
		return generationApplicationKernelBefore
	}
	if compareKernelObservation(observed, target) == nil {
		return generationApplicationKernelTarget
	}
	return generationApplicationKernelUnknown
}

func generationRecordIdentityFromCompiled(generation compiledGeneration) generationRecordIdentity {
	return generationRecordIdentity{empty: generation.empty, id: generation.id}
}

func generationApplicationRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), generationApplicationRecoveryTimeout)
}

func generationApplicationFailure(
	message string,
	cause error,
	kernelState generationApplicationKernelState,
	outcomeUnknown bool,
) error {
	if cause == nil {
		cause = errors.New(message)
	}
	domainErr := errs.AsDomainError(cause)
	if domainErr == nil {
		domainErr = generationAuthorityError(message, cause, nil)
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any, 2)
	}
	result.Details[generationApplicationKernelStateDetailKey] = string(kernelState)
	result.Details[generationApplicationOutcomeUnknownDetailKey] = outcomeUnknown
	result.Err = cause
	return &result
}
