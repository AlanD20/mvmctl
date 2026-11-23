package jailer

import (
	"context"
)

// VMCgroupObserveServer authorizes a VM through root runtime authority before reading its fixed cgroup leaf.
type VMCgroupObserveServer struct {
	status *RuntimeStatusServer
	deps   managedVMCgroupDeps
}

func NewVMCgroupObserveServer() *VMCgroupObserveServer {
	return &VMCgroupObserveServer{status: NewRuntimeStatusServer(), deps: realManagedVMCgroupDeps()}
}

func (server *VMCgroupObserveServer) Observe(
	ctx context.Context,
	caller RuntimeStatusCaller,
	request VMCgroupObserveRequest,
) (VMCgroupObserveResult, error) {
	if err := request.Validate(); err != nil {
		return VMCgroupObserveResult{}, err
	}
	if server == nil || server.status == nil {
		return VMCgroupObserveResult{}, instanceAtomicError("VM cgroup observer is not active", nil)
	}
	status, err := server.status.Observe(ctx, caller, RuntimeStatusRequest{VMIDs: []string{request.VMID}})
	if err != nil {
		return VMCgroupObserveResult{}, err
	}
	if len(status.VMs) != 1 || status.VMs[0].Status != RuntimeStatusRunning {
		return VMCgroupObserveResult{}, runtimeStatusUnavailableError(
			request.VMID, "VM is not running during cgroup observation", nil,
		)
	}
	observed, err := observeManagedVMCgroup(ctx, server.deps, request.VMID, request.PID, request.Requested)
	if err != nil {
		return VMCgroupObserveResult{}, err
	}
	result := VMCgroupObserveResult{State: observed}
	if err := result.Validate(request); err != nil {
		return VMCgroupObserveResult{}, err
	}
	return result, nil
}
