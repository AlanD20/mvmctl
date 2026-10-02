package api

import (
	"context"
	"fmt"

	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// vmRemoveUnrecorded recovers root authority without guessing ownership of caller artifacts.
// A nil result delegates to ordinary registered VM removal.
func (op *Operation) vmRemoveUnrecorded(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	vmID, err := input.ResolveUnrecordedRemoval(ctx, op.Repos.VM)
	if vmID == "" && err == nil {
		return nil
	}
	code := errs.CodeDatabaseError
	var cleaned jailer.VMCleanupResult
	if err == nil {
		code = errs.CodeVMCgroupCleanupFailed
		cleaned, err = jailer.NewVMLifecycleClient().Remove(ctx, jailer.VMCleanupRequest{VMID: vmID})
	}
	if err != nil {
		if domainErr := errs.AsDomainError(err); domainErr != nil {
			code = domainErr.Code
		} else {
			err = errs.Wrap(code, err, errs.WithEntity(vmID))
		}
		return &errs.BatchResult{Items: []errs.OperationResult{{
			Status: "error", Code: string(code), Item: vmID, Message: err.Error(), Exception: err,
		}}}
	}
	return &errs.BatchResult{Items: []errs.OperationResult{{
		Status: "success", Code: "vm.removed", Item: cleaned,
		Message:  fmt.Sprintf("Unrecorded VM '%s' root authority removed; caller artifacts retained", vmID),
		Metadata: map[string]any{"caller_artifacts_retained": true},
		Warnings: []string{"Caller-owned source files, VM directory, and staging artifacts were not removed."},
	}}}
}
