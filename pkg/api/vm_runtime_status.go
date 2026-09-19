package api

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
)

// reconcileVMRuntimeStatus observes the complete selection before modifying its SQLite projection or result rows.
// The caller must fetch candidates before filtering by status and propagate errors before rendering any rows.
// Activation requires the schema-3 lifecycle writer; absence cannot classify a transitional legacy launch.
func (op *Operation) reconcileVMRuntimeStatus(ctx context.Context, vms []*model.VMItem) error {
	client := jailer.NewRuntimeStatusClient()
	observations := make([]jailer.RuntimeStatusObservation, 0, len(vms))
	for offset := 0; offset < len(vms); offset += jailer.RuntimeStatusMaxVMs {
		end := min(offset+jailer.RuntimeStatusMaxVMs, len(vms))
		ids := make([]string, 0, end-offset)
		for _, item := range vms[offset:end] {
			ids = append(ids, item.ID)
		}
		result, err := client.Observe(ctx, jailer.RuntimeStatusRequest{VMIDs: ids})
		if err != nil {
			return err
		}
		observations = append(observations, result.VMs...)
	}
	updates := make([]model.VMRuntimeStatusUpdate, 0, len(vms))
	statuses := make([]model.VMStatus, len(observations))
	for index, observed := range observations {
		status := model.VMStatus(observed.Status)
		// Process liveness cannot establish whether the guest CPUs are paused.
		// Preserve the recorded pause until a stopped observation or explicit resume.
		if status == model.VMStatusRunning && vms[index].Status == model.VMStatusPaused {
			status = model.VMStatusPaused
		}
		statuses[index] = status
		if vms[index].Status != status {
			updates = append(updates, model.VMRuntimeStatusUpdate{VMID: observed.VMID, Status: status})
		}
	}
	if err := op.Repos.VM.UpdateRuntimeStatuses(ctx, updates); err != nil {
		return err
	}
	for index, status := range statuses {
		vms[index].Status = status
	}
	return nil
}
