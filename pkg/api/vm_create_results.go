package api

import (
	"context"
	"errors"
	"fmt"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

type vmCreateResult struct {
	idx  int
	vmID string
	vm   *model.VMItem
	err  error
}

// These selectors describe attempted effects, not authority to remove resources.
type vmCreateFailure struct {
	VMID   string          `json:"vm_id"`
	Name   string          `json:"name"`
	Errors []vmCreateCause `json:"errors"`
}

// Match the structured error envelope without exposing DomainError.Err to JSON.
type vmCreateCause struct {
	Code    errs.Code      `json:"code"`
	Class   errs.Class     `json:"class"`
	Message string         `json:"message"`
	Op      string         `json:"op,omitempty"`
	Entity  string         `json:"entity,omitempty"`
	Details map[string]any `json:"details"`
}

type vmCreateOutcome struct {
	createdVMs []*model.VMItem
	failures   []vmCreateFailure
	causes     []error
	messages   []string
}

func collectVMCreateResults(
	names []string, results <-chan vmCreateResult, atomic bool, cancel context.CancelFunc,
) vmCreateOutcome {
	ordered := make([]vmCreateResult, len(names))
	for range names {
		result := <-results
		ordered[result.idx] = result
		if atomic && result.err != nil {
			cancel()
		}
	}
	outcome := vmCreateOutcome{}
	for idx, result := range ordered {
		if result.err == nil {
			outcome.createdVMs = append(outcome.createdVMs, result.vm)
			continue
		}
		outcome.failures = append(outcome.failures, vmCreateFailure{
			VMID: result.vmID, Name: names[idx], Errors: vmCreateCauses(result.err),
		})
		outcome.causes = append(outcome.causes, result.err)
		outcome.messages = append(outcome.messages, fmt.Sprintf("%s: %v", names[idx], result.err))
	}
	return outcome
}

func (outcome vmCreateOutcome) failure(code errs.Code, message string) *errs.DomainError {
	// A completed creation may subsequently receive an atomic rollback request.
	// These IDs assert neither current existence nor successful cleanup.
	createdIDs := make([]string, 0, len(outcome.createdVMs))
	for _, vm := range outcome.createdVMs {
		createdIDs = append(createdIDs, vm.ID)
	}
	entity := ""
	if len(outcome.failures) == 1 {
		entity = outcome.failures[0].VMID
	}
	return errs.WrapMsg(code, message, errors.Join(outcome.causes...),
		errs.WithClass(errs.ClassInternal), errs.WithEntity(entity), errs.WithDetails(map[string]any{
			"failed_vms": outcome.failures, "created_vm_ids": createdIDs,
		}),
	)
}

// Walk both wrapper and joined branches: errors.As alone loses sibling recovery
// failures. Keep the actual chain separately in DomainError.Err for errors.Is/As.
func vmCreateCauses(err error) []vmCreateCause {
	var causes []vmCreateCause
	var visit func(error, bool)
	visit = func(current error, covered bool) {
		if current == nil {
			return
		}
		if domain, ok := current.(*errs.DomainError); ok {
			causes = append(causes, vmCreateCause{
				Code: domain.Code, Class: domain.Class, Message: domain.Message,
				Op: domain.Op, Entity: domain.Entity, Details: domain.Details,
			})
			covered = true
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				visit(child, covered)
			}
		case interface{ Unwrap() error }:
			visit(wrapped.Unwrap(), covered)
		default:
			if !covered {
				causes = append(causes, vmCreateCause{
					Code: errs.CodeInternal, Class: errs.ClassInternal, Message: current.Error(),
				})
			}
		}
	}
	visit(err, false)
	return causes
}
