package jailer

import (
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// VMCgroupObserveAction selects the closed, read-only managed VM cgroup observation.
const VMCgroupObserveAction = "jailer-vm-cgroup-observe"

// VMCgroupObserveRequest names one VM and its expected process and persisted resource intent.
// The caller's identity comes from the authenticated transport, not this body.
type VMCgroupObserveRequest struct {
	VMID      string               `json:"vm_id"`
	PID       int                  `json:"pid"`
	Requested model.VMCgroupLimits `json:"requested"`
}

func (request VMCgroupObserveRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) || request.PID <= 0 {
		return errs.New(errs.CodeValidationFailed, "VM cgroup observation identity is invalid")
	}
	return validateManagedVMCgroupLimits(request.Requested)
}

// VMCgroupObserveResult carries one complete root-observed cgroup state.
type VMCgroupObserveResult struct {
	State model.VMCgroupState `json:"state"`
}

func (result VMCgroupObserveResult) Validate(request VMCgroupObserveRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	state := result.State
	if state.Path != filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent, request.VMID) ||
		state.Requested != request.Requested ||
		state.Actual == nil || state.Usage == nil ||
		state.Status != model.VMCgroupEnforcementEnforced && state.Status != model.VMCgroupEnforcementMismatch {
		return instanceAtomicError("VM cgroup observation result is invalid", nil)
	}
	return nil
}
