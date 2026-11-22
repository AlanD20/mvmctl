package jailer

import (
	"context"
	"math"

	"mvmctl/internal/lib/model"
)

// VMLabelUpdateAction updates labels for one protected VM.
const VMLabelUpdateAction = "jailer-vm-label-update"

type VMLabelUpdateRequest struct {
	VMID             string         `json:"vm_id"`
	CacheRoot        string         `json:"cache_root"`
	ExpectedRevision uint64         `json:"expected_revision"`
	Labels           model.VMLabels `json:"labels"`
}

func (request VMLabelUpdateRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) || request.Labels == nil {
		return instanceValidationError("VM label update requires an exact VM ID and an explicit map")
	}
	if _, err := parseManagedCacheLocator(request.CacheRoot); err != nil {
		return err
	}
	return request.Labels.Validate()
}

type VMLabelUpdateResult struct {
	VMID     string `json:"vm_id"`
	Revision uint64 `json:"revision"`
}

func (result VMLabelUpdateResult) Validate(request VMLabelUpdateRequest) error {
	if result.VMID != request.VMID || result.Revision == 0 ||
		(result.Revision != request.ExpectedRevision &&
			(request.ExpectedRevision == math.MaxUint64 || result.Revision != request.ExpectedRevision+1)) {
		return instanceValidationError("VM label update returned an invalid receipt")
	}
	return nil
}

func UpdateVMLabels(ctx context.Context, request VMLabelUpdateRequest) (VMLabelUpdateResult, error) {
	if err := request.Validate(); err != nil {
		return VMLabelUpdateResult{}, err
	}
	return exchangeTrustedRelease(ctx, NewTrustedReleaseClient(), VMLabelUpdateAction, request, nil, 0,
		func(result VMLabelUpdateResult) error { return result.Validate(request) })
}
