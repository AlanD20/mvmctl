package jailer

import (
	"context"
	"fmt"

	"mvmctl/internal/lib/model"
)

func validateManagedVMCgroupLimits(limits model.VMCgroupLimits) error {
	if limits.PolicyVersion != model.VMCgroupPolicyVersion {
		return instanceAtomicError("managed VM cgroup policy requires a clean pressure-only policy-v2 VM", nil)
	}
	if limits.CPUQuotaMicros <= 0 || limits.CPUPeriodMicros <= 0 {
		return instanceAtomicError("managed VM CPU cgroup limit is invalid", nil)
	}
	if limits.CPUWeight < 1 || limits.CPUWeight > 10000 {
		return instanceAtomicError("managed VM CPU cgroup weight is invalid", nil)
	}
	if limits.MemoryHighBytes <= 0 || !limits.MemoryMaxUnlimited || limits.MemoryMaxBytes != 0 {
		return instanceAtomicError("managed VM memory cgroup limit is invalid", nil)
	}
	if limits.SwapMaxBytes < 0 || limits.PIDsMax <= 0 {
		return instanceAtomicError("managed VM swap or PID cgroup limit is invalid", nil)
	}
	return nil
}

func verifyManagedVMCgroupLimits(
	ctx context.Context,
	deps managedVMCgroupDeps,
	leafFD int,
	leaf managedVMCgroupIdentity,
	want model.VMCgroupLimits,
) error {
	cpuMax, err := readManagedVMCgroupCPUMax(ctx, deps, leafFD, leaf)
	if err != nil {
		return err
	}
	wantCPUMax := managedVMCgroupCPUMax{
		quotaMicros:  uint64(want.CPUQuotaMicros),
		periodMicros: uint64(want.CPUPeriodMicros),
	}
	if cpuMax != wantCPUMax {
		return managedVMCgroupLimitMismatch("cpu.max")
	}
	cpuWeight, err := readManagedVMCgroupCPUWeight(ctx, deps, leafFD, leaf)
	if err != nil {
		return err
	}
	if cpuWeight != uint64(want.CPUWeight) {
		return managedVMCgroupLimitMismatch("cpu.weight")
	}
	memoryHigh, err := readManagedVMCgroupMemoryHigh(ctx, deps, leafFD, leaf)
	if err != nil {
		return err
	}
	if memoryHigh != uint64(want.MemoryHighBytes) {
		return managedVMCgroupLimitMismatch("memory.high")
	}
	if err := requireManagedVMCgroupMemoryMaxUnlimited(ctx, deps, leafFD, leaf); err != nil {
		return err
	}
	swapMax, err := readManagedVMCgroupMemorySwapMax(ctx, deps, leafFD, leaf)
	if err != nil {
		return err
	}
	if swapMax != uint64(want.SwapMaxBytes) {
		return managedVMCgroupLimitMismatch("memory.swap.max")
	}
	pidsMax, err := readManagedVMCgroupPIDsMax(ctx, deps, leafFD, leaf)
	if err != nil {
		return err
	}
	if pidsMax != uint64(want.PIDsMax) {
		return managedVMCgroupLimitMismatch("pids.max")
	}
	return nil
}

func managedVMCgroupLimitMismatch(control string) error {
	return instanceAtomicError(fmt.Sprintf("%s control does not match launch intent", control), nil)
}
