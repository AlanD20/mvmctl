package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func (s *FirecrackerSpawner) writeJailerManifest() error {
	volumes := make([]jailersvc.VolumeMount, 0, len(s.config.ExtraDrives))
	for _, drive := range s.config.ExtraDrives {
		volumes = append(volumes, jailersvc.VolumeMount{
			DriveID: drive.DriveID, HostPath: drive.PathOnHost, ReadOnly: drive.IsReadOnly,
		})
	}
	isoPath := ""
	if s.config.CloudInitISOPath != nil {
		isoPath = *s.config.CloudInitISOPath
	}
	var cgroupLimits *model.VMCgroupLimits
	if s.config.CgroupLimits.CPUQuotaMicros > 0 {
		cgroupLimits = &s.config.CgroupLimits
	}
	manifest := jailersvc.LaunchManifest{
		VMID: s.vmID(), VMDir: s.config.VMDir, Version: s.config.BinaryVersion,
		ConfigPath: s.config.ConfigPath, PIDPath: s.config.PIDPath,
		KernelPath: s.config.KernelPath, RootfsPath: s.config.RootfsPath,
		ISOPath: isoPath, SnapshotDir: s.config.SnapshotDir,
		Volumes: volumes, APISocket: s.config.APISocketPath,
		PCIEnabled: s.config.PCIEnabled, SnapshotMode: s.config.SnapshotMode,
		CgroupLimits: cgroupLimits,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal jailed launch manifest: %w", err)
	}
	path := filepath.Join(s.config.VMDir, infra.JailerManifestFilename)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write jailed launch manifest: %w", err)
	}
	return nil
}

func (s *FirecrackerSpawner) vmID() string {
	return filepath.Base(filepath.Clean(s.config.VMDir))
}

func jailedRuntimePath(hostPath string) string {
	return filepath.Join("/run/mvm", filepath.Base(hostPath))
}

func jailedVolumePath(driveID string) string {
	return filepath.Join("/volumes", driveID)
}

func translateConfigForJail(config *model.FirecrackerVMConfig) *model.FirecrackerVMConfig {
	translated := *config
	translated.BootSource = config.BootSource
	translated.BootSource.KernelImagePath = "/kernel"
	translated.Drives = make([]model.DriveConfig, len(config.Drives))
	for i, drive := range config.Drives {
		translated.Drives[i] = drive
		switch drive.DriveID {
		case "rootfs":
			translated.Drives[i].PathOnHost = "/rootfs"
		case "cloud-init":
			translated.Drives[i].PathOnHost = "/cloud-init.iso"
		default:
			translated.Drives[i].PathOnHost = jailedVolumePath(drive.DriveID)
		}
	}
	if config.Logger != nil {
		logger := *config.Logger
		logger.LogPath = jailedRuntimePath(logger.LogPath)
		translated.Logger = &logger
	}
	if config.Metrics != nil {
		metrics := *config.Metrics
		metrics.MetricsPath = jailedRuntimePath(metrics.MetricsPath)
		translated.Metrics = &metrics
	}
	if config.Vsock != nil {
		vsock := *config.Vsock
		vsock.UDSPath = jailedRuntimePath(vsock.UDSPath)
		translated.Vsock = &vsock
	}
	return &translated
}

// ExposeSnapshot exposes one managed snapshot directory inside an existing VM jail.
func ExposeSnapshot(ctx context.Context, vmID, snapshotDir string) error {
	return jailersvc.ExposeSnapshot(ctx, vmID, snapshotDir)
}

// CleanupJail removes all mounts and chroot state for one VM.
func CleanupJail(ctx context.Context, vmID string) error {
	return jailersvc.Cleanup(ctx, vmID)
}

func exposeVolume(ctx context.Context, vmID string, drive model.DriveConfig) error {
	return jailersvc.ExposeVolume(ctx, vmID, drive.DriveID, drive.PathOnHost, drive.IsReadOnly)
}

func removeVolume(ctx context.Context, vmID, driveID string) error {
	return jailersvc.RemoveVolume(ctx, vmID, driveID)
}

// CgroupPath returns the fixed cgroup-v2 path for one canonical Jailer launch.
func CgroupPath(vmID string) string {
	return filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent, vmID)
}

// InspectCgroup reads typed enforcement and usage state for one VM.
func InspectCgroup(
	ctx context.Context,
	vmID string,
	pid int,
	requested model.VMCgroupLimits,
) (*model.VMCgroupState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := CgroupPath(vmID)
	state := &model.VMCgroupState{
		Path: path, Requested: requested, Status: model.VMCgroupEnforcementInactive,
	}
	if pid <= 0 {
		return state, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, errs.WrapMsg(errs.CodeVMCgroupVerificationFailed, "VM cgroup is unavailable", err,
			errs.WithEntity(vmID))
	}
	actual, err := readCgroupLimits(path)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVMCgroupVerificationFailed, "failed to read VM cgroup limits", err,
			errs.WithEntity(vmID))
	}
	actual.PolicyVersion = requested.PolicyVersion
	usage, err := readCgroupUsage(path)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVMCgroupVerificationFailed, "failed to read VM cgroup usage", err,
			errs.WithEntity(vmID))
	}
	state.Actual = &actual
	state.Usage = &usage
	state.Mismatches = cgroupLimitMismatches(requested, actual)
	inExpectedCgroup, err := processInCgroup(pid, vmID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVMCgroupVerificationFailed, "failed to verify VM cgroup membership", err,
			errs.WithEntity(vmID))
	}
	if !inExpectedCgroup {
		state.Mismatches = append(state.Mismatches, "process membership")
	}
	inProcessList, err := cgroupContainsProcess(path, pid)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVMCgroupVerificationFailed, "failed to read VM cgroup process list", err,
			errs.WithEntity(vmID))
	}
	if !inProcessList {
		state.Mismatches = append(state.Mismatches, "cgroup process list")
	}
	if len(state.Mismatches) == 0 {
		state.Status = model.VMCgroupEnforcementEnforced
	} else {
		state.Status = model.VMCgroupEnforcementMismatch
	}
	return state, nil
}

// VerifyCgroup requires exact cgroup membership and controller values.
func VerifyCgroup(ctx context.Context, vmID string, pid int, requested model.VMCgroupLimits) error {
	state, err := InspectCgroup(ctx, vmID, pid, requested)
	if err != nil {
		return err
	}
	if state.Status != model.VMCgroupEnforcementEnforced {
		return errs.New(errs.CodeVMCgroupVerificationFailed,
			fmt.Sprintf("VM cgroup enforcement verification failed: %s", strings.Join(state.Mismatches, ", ")),
			errs.WithEntity(vmID))
	}
	return nil
}

func readCgroupLimits(path string) (model.VMCgroupLimits, error) {
	cpuMax, err := os.ReadFile(filepath.Join(path, "cpu.max"))
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	cpuFields := strings.Fields(string(cpuMax))
	if len(cpuFields) != 2 || cpuFields[0] == "max" {
		return model.VMCgroupLimits{}, fmt.Errorf("invalid finite CPU maximum")
	}
	quota, err := strconv.ParseInt(cpuFields[0], 10, 64)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	period, err := strconv.ParseInt(cpuFields[1], 10, 64)
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	weight, err := readCgroupInt(path, "cpu.weight")
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	memoryHigh, err := readCgroupInt(path, "memory.high")
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	memoryMax, err := readCgroupInt(path, "memory.max")
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	swapMax, err := readCgroupInt(path, "memory.swap.max")
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	pidsMax, err := readCgroupInt(path, "pids.max")
	if err != nil {
		return model.VMCgroupLimits{}, err
	}
	return model.VMCgroupLimits{
		CPUQuotaMicros: quota, CPUPeriodMicros: period, CPUWeight: weight,
		MemoryHighBytes: memoryHigh, MemoryMaxBytes: memoryMax,
		SwapMaxBytes: swapMax, PIDsMax: pidsMax,
	}, nil
}

func readCgroupUsage(path string) (model.VMCgroupUsage, error) {
	cpuStat, err := os.ReadFile(filepath.Join(path, "cpu.stat"))
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	var cpuUsage int64
	for line := range strings.SplitSeq(string(cpuStat), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			cpuUsage, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return model.VMCgroupUsage{}, err
			}
			break
		}
	}
	memory, err := readCgroupInt(path, "memory.current")
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	swap, err := readCgroupInt(path, "memory.swap.current")
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	pids, err := readCgroupInt(path, "pids.current")
	if err != nil {
		return model.VMCgroupUsage{}, err
	}
	return model.VMCgroupUsage{CPUUsageMicros: cpuUsage, MemoryBytes: memory, SwapBytes: swap, PIDsCurrent: pids}, nil
}

func readCgroupInt(path, name string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(path, name))
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return 0, fmt.Errorf("%s is unlimited", name)
	}
	return strconv.ParseInt(value, 10, 64)
}

func processInCgroup(pid int, vmID string) (bool, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return false, err
	}
	expected := "/" + filepath.Join(infra.JailerCgroupParent, vmID)
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "0::") && strings.TrimPrefix(line, "0::") == expected {
			return true, nil
		}
	}
	return false, nil
}

func cgroupContainsProcess(path string, pid int) (bool, error) {
	data, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
	if err != nil {
		return false, err
	}
	expected := strconv.Itoa(pid)
	for process := range strings.FieldsSeq(string(data)) {
		if process == expected {
			return true, nil
		}
	}
	return false, nil
}

func cgroupLimitMismatches(requested, actual model.VMCgroupLimits) []string {
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
	if requested.MemoryMaxBytes != actual.MemoryMaxBytes {
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
