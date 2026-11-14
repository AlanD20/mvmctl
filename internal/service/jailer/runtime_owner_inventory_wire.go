package jailer

// RuntimeOwnerInventoryAction discovers only the authenticated caller's registered VM authority.
const RuntimeOwnerInventoryAction = "jailer-runtime-owner-inventory"

// RuntimeOwnerInventoryRequest accepts no selection or caller-supplied authority.
type RuntimeOwnerInventoryRequest struct{}

// RuntimeOwnerInventoryResult is the complete caller-owned inventory at observation time.
// It is not a host-wide quiescence guarantee or authorization to remove a network.
type RuntimeOwnerInventoryResult struct {
	VMs []RuntimeStatusObservation `json:"vms"`
}

// Validate requires a complete, ordered set of canonical IDs with available observations.
// Transport framing owns the response-size limit; this protocol imposes no VM admission limit.
func (result RuntimeOwnerInventoryResult) Validate() error {
	if result.VMs == nil {
		return runtimeStatusUnavailableError("", "runtime owner inventory is missing", nil)
	}
	for index, item := range result.VMs {
		if !vmIDPattern.MatchString(item.VMID) || index > 0 && result.VMs[index-1].VMID >= item.VMID {
			return runtimeStatusUnavailableError("", "runtime owner inventory IDs are not canonical and ordered", nil)
		}
		switch item.Status {
		case RuntimeStatusRunning, RuntimeStatusStopped, RuntimeStatusCrashed:
		default:
			return runtimeStatusUnavailableError(item.VMID, "runtime owner inventory observation is unavailable", nil)
		}
	}
	return nil
}
