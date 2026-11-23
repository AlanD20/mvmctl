package jailer

import "mvmctl/pkg/errs"

const (
	// RuntimeStatusAction selects the closed, read-only runtime observation protocol.
	RuntimeStatusAction = "jailer-runtime-status"
	// RuntimeStatusMaxVMs bounds one authority observation and one privileged exchange.
	RuntimeStatusMaxVMs = runtimeStatusMaxVMs
)

// RuntimeStatus is a root-observed runtime state, independent of the user database.
type RuntimeStatus string

const (
	RuntimeStatusRunning     RuntimeStatus = "running"
	RuntimeStatusStopped     RuntimeStatus = "stopped"
	RuntimeStatusCrashed     RuntimeStatus = "crashed"
	RuntimeStatusUnavailable RuntimeStatus = "unavailable"
)

// RuntimeStatusRequest carries only canonical VM identifiers. Caller identity comes from authenticated transport.
type RuntimeStatusRequest struct {
	VMIDs []string `json:"vm_ids"`
}

// RuntimeStatusObservation binds one observation to its exact requested VM.
type RuntimeStatusObservation struct {
	VMID   string        `json:"vm_id"`
	Status RuntimeStatus `json:"status"`
}

// RuntimeStatusResult is complete only when every requested VM has exactly one available observation.
type RuntimeStatusResult struct {
	VMs []RuntimeStatusObservation `json:"vms"`
}

// Validate rejects malformed, duplicate, missing, and oversized requests before authority is opened.
func (request RuntimeStatusRequest) Validate() error {
	if request.VMIDs == nil || len(request.VMIDs) > RuntimeStatusMaxVMs {
		return errs.New(errs.CodeValidationFailed, "runtime status requires an array of at most 1000 VM IDs")
	}
	seen := make(map[string]struct{}, len(request.VMIDs))
	for _, id := range request.VMIDs {
		if !vmIDPattern.MatchString(id) {
			return errs.New(errs.CodeValidationFailed, "runtime status VM ID is not canonical")
		}
		if _, duplicate := seen[id]; duplicate {
			return errs.New(errs.CodeValidationFailed, "runtime status VM IDs must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Validate requires exact ordered coverage. An unavailable observation fails the entire read.
func (result RuntimeStatusResult) Validate(request RuntimeStatusRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMs == nil || len(result.VMs) != len(request.VMIDs) {
		return runtimeStatusUnavailableError("", "runtime status result does not cover the complete request", nil)
	}
	for index, observation := range result.VMs {
		if observation.VMID != request.VMIDs[index] {
			return runtimeStatusUnavailableError("", "runtime status result VM does not match request", nil)
		}
		switch observation.Status {
		case RuntimeStatusRunning, RuntimeStatusStopped, RuntimeStatusCrashed:
		case RuntimeStatusUnavailable:
			return runtimeStatusUnavailableError(observation.VMID, "runtime status observation is unavailable", nil)
		default:
			return runtimeStatusUnavailableError(observation.VMID, "runtime status observation is invalid", nil)
		}
	}
	return nil
}
