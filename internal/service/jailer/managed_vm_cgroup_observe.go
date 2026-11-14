package jailer

import (
	"context"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

// observeManagedVMCgroup reads only the fixed root-owned VM leaf after its root record has been authorized.
// CRITICAL: The leaf and every control are pinned beneath the admitted cgroup-v2 mount; this function creates nothing.
func observeManagedVMCgroup(
	ctx context.Context,
	deps managedVMCgroupDeps,
	vmID string,
	pid int,
	requested model.VMCgroupLimits,
) (observed model.VMCgroupState, returnErr error) {
	if ctx == nil {
		return model.VMCgroupState{}, instanceAtomicError("observe managed VM cgroup: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return model.VMCgroupState{}, instanceAtomicError("observe managed VM cgroup", err)
	}
	if err := validateManagedVMCgroupDeps(deps); err != nil {
		return model.VMCgroupState{}, err
	}
	if !vmIDPattern.MatchString(vmID) || pid <= 0 {
		return model.VMCgroupState{}, instanceValidationError("managed VM cgroup observation identity is invalid")
	}
	if err := validateManagedVMCgroupLimits(requested); err != nil {
		return model.VMCgroupState{}, err
	}

	retained := make([]int, 0, 3)
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr, "release observed managed VM cgroup",
			closeManagedVMCgroupFDs(context.WithoutCancel(ctx), deps, retained),
		)
		if returnErr != nil {
			observed = model.VMCgroupState{}
		}
	}()

	rootFD, err := deps.openRoot(ctx)
	if err != nil {
		return model.VMCgroupState{}, instanceAtomicError("open fixed cgroup-v2 root for observation", err)
	}
	retained = append(retained, rootFD)
	root, err := inspectManagedVMCgroupDirectory(ctx, deps, rootFD, "cgroup-v2 root", managedVMCgroupRootMode)
	if err != nil {
		return model.VMCgroupState{}, err
	}
	var filesystem unix.Statfs_t
	if err := deps.statfs(ctx, rootFD, &filesystem); err != nil {
		return model.VMCgroupState{}, instanceAtomicError("inspect observed cgroup-v2 filesystem", err)
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return model.VMCgroupState{}, instanceAtomicError("observed cgroup root is not cgroup-v2", nil)
	}

	how := unix.OpenHow{
		Flags: uint64(managedVMCgroupDirectoryFlags), Resolve: uint64(managedVMCgroupResolveFlags),
	}
	parentFD, err := deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	if err != nil {
		return model.VMCgroupState{}, instanceAtomicError("open existing managed cgroup parent", err)
	}
	retained = append(retained, parentFD)
	parent, err := inspectManagedVMCgroupDirectory(
		ctx, deps, parentFD, "observed managed cgroup parent", managedVMCgroupParentMode,
	)
	if err != nil {
		return model.VMCgroupState{}, err
	}
	if parent.mount != root.mount || parent.deviceMajor != root.deviceMajor || parent.deviceMinor != root.deviceMinor {
		return model.VMCgroupState{}, instanceAtomicError("observed cgroup parent left the pinned mount", nil)
	}

	leafFD, err := deps.openAt2(ctx, parentFD, vmID, &how)
	if err != nil {
		return model.VMCgroupState{}, instanceAtomicError("open existing managed VM cgroup", err)
	}
	retained = append(retained, leafFD)
	leaf, err := inspectManagedVMCgroupDirectory(
		ctx, deps, leafFD, "observed managed VM cgroup", managedVMCgroupLeafMode,
	)
	if err != nil {
		return model.VMCgroupState{}, err
	}
	if leaf.mount != parent.mount || leaf.deviceMajor != parent.deviceMajor || leaf.deviceMinor != parent.deviceMinor {
		return model.VMCgroupState{}, instanceAtomicError("observed VM cgroup left the pinned mount", nil)
	}

	actual, err := readObservedManagedVMCgroupLimits(ctx, deps, leafFD, leaf, requested.PolicyVersion)
	if err != nil {
		return model.VMCgroupState{}, err
	}
	usage, err := readObservedManagedVMCgroupUsage(ctx, deps, leafFD, leaf)
	if err != nil {
		return model.VMCgroupState{}, err
	}
	members, err := readManagedVMCgroupMembership(ctx, &managedVMCgroupLeaseState{
		deps: deps, vmID: vmID, leafFD: leafFD, leaf: leaf,
	})
	if err != nil {
		return model.VMCgroupState{}, err
	}
	if len(members) != 1 || members[0] != pid {
		return model.VMCgroupState{}, processVerificationError(
			vmID, "observed cgroup has inexact process membership", nil,
		)
	}
	if err := requireObservedManagedVMCgroupCurrent(ctx, deps, rootFD, parentFD, parent, leaf, vmID); err != nil {
		return model.VMCgroupState{}, err
	}

	observed = model.VMCgroupState{
		Path:      filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent, vmID),
		Requested: requested, Actual: &actual, Usage: &usage,
		Status:     model.VMCgroupEnforcementEnforced,
		Mismatches: observedManagedVMCgroupLimitMismatches(requested, actual),
	}
	if len(observed.Mismatches) > 0 {
		observed.Status = model.VMCgroupEnforcementMismatch
	}
	return observed, nil
}

func requireObservedManagedVMCgroupCurrent(
	ctx context.Context,
	deps managedVMCgroupDeps,
	rootFD, parentFD int,
	parent, leaf managedVMCgroupIdentity,
	vmID string,
) (returnErr error) {
	how := unix.OpenHow{
		Flags: uint64(managedVMCgroupDirectoryFlags), Resolve: uint64(managedVMCgroupResolveFlags),
	}
	currentParentFD, err := deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	if err != nil {
		return instanceAtomicError("reopen observed managed cgroup parent", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close observed cgroup parent recheck",
			deps.close(context.WithoutCancel(ctx), currentParentFD))
	}()
	currentParent, err := inspectManagedVMCgroupDirectory(
		ctx, deps, currentParentFD, "rechecked managed cgroup parent", managedVMCgroupParentMode,
	)
	if err != nil {
		return err
	}
	if currentParent != parent {
		return instanceAtomicError("observed managed cgroup parent was replaced", nil)
	}
	currentLeafFD, err := deps.openAt2(ctx, parentFD, vmID, &how)
	if err != nil {
		return instanceAtomicError("reopen observed VM cgroup", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close observed VM cgroup recheck",
			deps.close(context.WithoutCancel(ctx), currentLeafFD))
	}()
	currentLeaf, err := inspectManagedVMCgroupDirectory(
		ctx, deps, currentLeafFD, "rechecked managed VM cgroup", managedVMCgroupLeafMode,
	)
	if err != nil {
		return err
	}
	if currentLeaf != leaf {
		return instanceAtomicError("observed VM cgroup was replaced", nil)
	}
	return nil
}

func readObservedManagedVMCgroupLimits(
	ctx context.Context,
	deps managedVMCgroupDeps,
	leafFD int,
	leaf managedVMCgroupIdentity,
	policyVersion int,
) (model.VMCgroupLimits, error) {
	cpu, err := readManagedVMCgroupCPUMax(ctx, deps, leafFD, leaf)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	weight, err := readManagedVMCgroupCPUWeight(ctx, deps, leafFD, leaf)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	high, err := readManagedVMCgroupMemoryHigh(ctx, deps, leafFD, leaf)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	memoryMaxRaw, err := readManagedVMCgroupControlBytes(ctx, deps, leafFD, leaf, managedVMCgroupMemoryMaxControl)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	memoryMax := uint64(0)
	memoryMaxUnlimited := string(memoryMaxRaw) == "max\n"
	if !memoryMaxUnlimited {
		memoryMax, err = parseManagedVMCgroupFiniteUint(memoryMaxRaw)
		if err != nil {
			return model.VMCgroupLimits{}, instanceAtomicError("parse observed memory.max", err)
		}
	}
	swap, err := readManagedVMCgroupMemorySwapMax(ctx, deps, leafFD, leaf)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	pids, err := readManagedVMCgroupPIDsMax(ctx, deps, leafFD, leaf)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	for _, value := range []uint64{cpu.quotaMicros, cpu.periodMicros, weight, high, memoryMax, swap, pids} {
		if value > math.MaxInt64 {
			return model.VMCgroupLimits{}, instanceAtomicError("observed VM cgroup control exceeds signed range", nil)
		}
	}
	return model.VMCgroupLimits{
		PolicyVersion: policyVersion, CPUQuotaMicros: int64(cpu.quotaMicros),
		CPUPeriodMicros: int64(cpu.periodMicros), CPUWeight: int64(weight),
		MemoryHighBytes: int64(high), MemoryMaxBytes: int64(memoryMax), MemoryMaxUnlimited: memoryMaxUnlimited,
		SwapMaxBytes: int64(swap), PIDsMax: int64(pids),
	}, nil
}

func readObservedManagedVMCgroupUsage(
	ctx context.Context,
	deps managedVMCgroupDeps,
	leafFD int,
	leaf managedVMCgroupIdentity,
) (model.VMCgroupUsage, error) {
	cpuStat, err := readManagedVMCgroupControlBytes(ctx, deps, leafFD, leaf, managedVMCgroupCPUStatControl)
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	cpuUsage, err := parseObservedManagedVMCgroupCPUUsage(cpuStat)
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	memory, err := readManagedVMCgroupFiniteControl(
		ctx, deps, leafFD, leaf, managedVMCgroupMemoryCurrentControl, "memory.current",
	)
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	swap, err := readManagedVMCgroupFiniteControl(
		ctx, deps, leafFD, leaf, managedVMCgroupMemorySwapCurrentControl, "memory.swap.current",
	)
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	pids, err := readManagedVMCgroupFiniteControl(
		ctx, deps, leafFD, leaf, managedVMCgroupPIDsCurrentControl, "pids.current",
	)
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	for _, value := range []uint64{cpuUsage, memory, swap, pids} {
		if value > math.MaxInt64 {
			return model.VMCgroupUsage{}, instanceAtomicError("observed VM cgroup usage exceeds signed range", nil)
		}
	}
	return model.VMCgroupUsage{
		CPUUsageMicros: int64(cpuUsage), MemoryBytes: int64(memory),
		SwapBytes: int64(swap), PIDsCurrent: int64(pids),
	}, nil
}

func parseObservedManagedVMCgroupCPUUsage(raw []byte) (uint64, error) {
	found := false
	var usage uint64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "usage_usec" {
			continue
		}
		if found || len(fields) != 2 {
			return 0, instanceAtomicError("observed cpu.stat usage field is invalid", nil)
		}
		parsed, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, instanceAtomicError("parse observed cpu.stat usage", err)
		}
		usage = parsed
		found = true
	}
	if !found {
		return 0, instanceAtomicError("observed cpu.stat has no usage field", nil)
	}
	return usage, nil
}

func observedManagedVMCgroupLimitMismatches(requested, actual model.VMCgroupLimits) []string {
	var mismatches []string
	if requested.CPUQuotaMicros != actual.CPUQuotaMicros || requested.CPUPeriodMicros != actual.CPUPeriodMicros {
		mismatches = append(mismatches, "CPU quota")
	}
	if requested.CPUWeight != actual.CPUWeight {
		mismatches = append(mismatches, "CPU weight")
	}
	if requested.MemoryHighBytes != actual.MemoryHighBytes {
		mismatches = append(mismatches, "memory high")
	}
	if requested.MemoryMaxUnlimited != actual.MemoryMaxUnlimited || requested.MemoryMaxBytes != actual.MemoryMaxBytes {
		mismatches = append(mismatches, "memory maximum")
	}
	if requested.SwapMaxBytes != actual.SwapMaxBytes {
		mismatches = append(mismatches, "swap maximum")
	}
	if requested.PIDsMax != actual.PIDsMax {
		mismatches = append(mismatches, "PID maximum")
	}
	return mismatches
}
