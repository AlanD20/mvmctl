package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/validators"
)

// POSIX access mode constants matching Python's os.R_OK, os.W_OK.
// Go's syscall package may not define these on all platforms.
const (
	rOk = 4
	wOk = 2
)

// ── _run (matches Python's HostService._run()) ──
// ── CheckKVMAccess ──
// Matches Python's HostService.check_kvm_access() — checks /dev/kvm exists and os.access(path, R_OK|W_OK).
// Python's os.access checks the REAL UID; Go's os.OpenFile checks effective UID.
// syscall.Access matches Python's os.access behavior.
func CheckKVMAccess() bool {
	kvmPath := "/dev/kvm"
	if _, err := os.Stat(kvmPath); err != nil {
		return false
	}
	if err := syscall.Access(kvmPath, rOk|wOk); err != nil {
		return false
	}
	return true
}

// ── CheckRequiredBinaries ──
func CheckRequiredBinaries() []string {
	var missing []string
	for _, name := range infra.RequiredBinaries {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	return missing
}

// ── CheckCloudLocalds ──
func CheckCloudLocalds() bool {
	_, err := exec.LookPath("cloud-localds")
	return err == nil
}

// ── CheckKernelVersion ──
func CheckKernelVersion() bool {
	release := system.KernelRelease()
	re := regexp.MustCompile(`(\d+)\.(\d+)`)
	match := re.FindStringSubmatch(release)
	if match == nil {
		return false
	}
	major := 0
	minor := 0
	fmt.Sscanf(match[1], "%d", &major)
	fmt.Sscanf(match[2], "%d", &minor)
	return (major > infra.MinKernelMajor) || (major == infra.MinKernelMajor && minor >= infra.MinKernelMinor)
}

// ── CreateGroup ──
func CreateGroup(ctx context.Context, groupName string) (bool, error) {
	if system.GroupExists(groupName) {
		return false, nil
	}

	res := system.RunCmdCompat(ctx, []string{"groupadd", "--system", groupName}, system.DefaultRunCmdOpts())
	if res.Err != nil {
		return false, fmt.Errorf("failed to create group %s: %w", groupName, res.Err)
	}
	return true, nil
}

// ── AddUserToGroup ──
func AddUserToGroup(ctx context.Context, username, groupName string) (bool, error) {
	if system.UserInGroup(ctx, username, groupName) {
		return false, nil
	}

	res := system.RunCmdCompat(ctx, []string{"usermod", "-aG", groupName, username}, system.DefaultRunCmdOpts())
	if res.Err != nil {
		return false, fmt.Errorf("failed to add %s to group %s: %w", username, groupName, res.Err)
	}
	return true, nil
}

// ── RemoveUserFromGroup ──
func RemoveUserFromGroup(ctx context.Context, username, groupName string) (bool, error) {
	if !system.GroupExists(groupName) {
		return false, nil
	}
	if !system.UserInGroup(ctx, username, groupName) {
		return false, nil
	}

	res := system.RunCmdCompat(ctx, []string{"gpasswd", "-d", username, groupName}, system.DefaultRunCmdOpts())
	if res.Err != nil {
		return false, fmt.Errorf("failed to remove user %s from group %s: %w", username, groupName, res.Err)
	}
	return true, nil
}

// ── ValidateSudoersBinaries ──
// Matches Python's HostService.validate_sudoers_binaries().
func ValidateSudoersBinaries() error {
	for binary, pkg := range infra.PrivilegedBinaries {
		if _, err := os.Stat(binary); os.IsNotExist(err) {
			return hostError(errs.CodeHostInitFailed,
				fmt.Sprintf("Required binary not found: %s (install %s)", binary, pkg))
		}
	}
	return nil
}

// ── GenerateSudoersContent ──
// Matches Python's HostService._generate_sudoers_content().
// Python's PRIVILEGED_BINARIES dict (Python 3.7+) preserves insertion order.
// Go maps have random iteration, so we iterate keys in an ordered slice
// that matches Python's dict literal order from constants.py.
func GenerateSudoersContent(groupName string) string {
	// Service binaries via "mvm run <service>" pattern (sudoers wildcard)
	// Use the current binary's path so sudoers matches how provisioner invokes it.
	mvmPath, _ := os.Executable()
	runCmd := mvmPath + " run *"
	binaries := append(infra.PrivilegedBinariesOrdered[:], runCmd)
	binariesStr := strings.Join(binaries, ", ")
	return fmt.Sprintf(
		"# Managed by %s — do not edit manually.\n"+
			"# To remove: %s host reset\n"+
			"%%%s ALL=(root) NOPASSWD: %s\n",
		infra.ProjectName, infra.ProjectName, groupName, binariesStr,
	)
}

// ── WriteSudoers ──
// Matches Python's HostService.write_sudoers() exactly.
func WriteSudoers(ctx context.Context, path string, content string) error {
	if err := ValidateSudoersBinaries(); err != nil {
		return err
	}

	// Write to temp file using tempfile.NamedTemporaryFile equivalent:
	// mode="w", suffix=".sudoers", delete=False
	tmpFile, err := os.CreateTemp("", "*.sudoers")
	if err != nil {
		return hostError(errs.CodeHostInitFailed, fmt.Sprintf("Failed to create temp file: %v", err))
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write([]byte(content)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return hostError(errs.CodeHostInitFailed, fmt.Sprintf("Failed to write sudoers file %s: %v", path, err))
	}
	tmpFile.Close()
	// Clean up temp file on function exit (Python uses try/finally/except OSError: pass)
	defer func() {
		os.Remove(tmpPath) // ignore error, matching Python's except OSError: pass
	}()

	// Validate with visudo
	// Python: try: run_cmd([...], check=False)
	//            if result.returncode != 0: raise HostError(...)
	//         except ProcessError: raise HostError("visudo not found ...")
	// ProcessError is raised when the command binary cannot be found.
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	result := system.RunCmdCompat(ctx, []string{"visudo", "-c", "-f", tmpPath}, opts)
	if result.Err != nil {
		// Command not found or other execution failure → "visudo not found"
		return hostError(errs.CodePrivilegeSudoers, "visudo not found — cannot validate sudoers syntax")
	}
	if result.ExitCode != 0 {
		return hostError(errs.CodePrivilegeSudoers,
			fmt.Sprintf("Generated sudoers file failed visudo validation: %s", result.Stderr))
	}

	// Write to final location
	if err := os.MkdirAll(filepath.Dir(path), infra.DirPerm); err != nil {
		return hostError(errs.CodeHostInitFailed,
			fmt.Sprintf("Failed to write sudoers file %s: %v", path, err))
	}
	if err := os.WriteFile(path, []byte(content), infra.SudoersPerm); err != nil {
		return hostError(errs.CodeHostInitFailed,
			fmt.Sprintf("Failed to write sudoers file %s: %v", path, err))
	}

	return nil
}

// ── RemoveSudoers ──
func RemoveSudoers(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, hostError(errs.CodeHostCleanFailed,
			fmt.Sprintf("Failed to remove sudoers file %s: %v", path, err))
	}
	return true, nil
}

// ── RemoveGroup ──
func RemoveGroup(ctx context.Context, groupName string) (bool, error) {
	if !system.GroupExists(groupName) {
		return false, nil
	}

	res := system.RunCmdCompat(ctx, []string{"groupdel", groupName}, system.DefaultRunCmdOpts())
	if res.Err != nil {
		return false, fmt.Errorf("failed to remove group %s: %w", groupName, res.Err)
	}
	return true, nil
}

// ── isModuleLoaded ──
func isModuleLoaded(ctx context.Context, module string) bool {
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	result := system.RunCmdCompat(ctx, []string{"lsmod"}, opts)
	if result.ExitCode != 0 {
		return false
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) > 0 && parts[0] == module {
			return true
		}
	}
	return false
}

// ── GetIPForwardStatus ──
func GetIPForwardStatus(ctx context.Context) (string, error) {

	res := system.RunCmdCompat(ctx, []string{"sysctl", "-n", sysctlKey}, system.DefaultRunCmdOpts())
	if res.Err != nil {
		return "", fmt.Errorf("failed to read %s: %w", sysctlKey, res.Err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// ── HardwareFromState ──
// Reconstructs HostHardware from stored host state (cache layer).
func HardwareFromState(state *model.HostStateItem) *model.HostHardware {
	if state.CPUModel == nil {
		return nil
	}
	h := &model.HostHardware{}
	if state.Hostname != nil {
		h.Hostname = *state.Hostname
	}
	if state.CPUModel != nil {
		h.CPUModel = *state.CPUModel
	}
	if state.CPUVendor != nil {
		h.CPUVendor = *state.CPUVendor
	}
	if state.CPUCores != nil {
		h.CPUCores = *state.CPUCores
	}
	if state.CPUArchitecture != nil {
		h.CPUArchitecture = *state.CPUArchitecture
	}
	if state.NumaNodes != nil && *state.NumaNodes != 0 {
		h.NumaNodes = *state.NumaNodes
	} else {
		h.NumaNodes = 1
	}
	if state.MemoryTotalMiB != nil {
		h.MemoryTotalMiB = *state.MemoryTotalMiB
	}
	if state.StorageTotalBytes != nil {
		h.StorageTotalBytes = *state.StorageTotalBytes
	}
	if state.KernelVersion != nil {
		h.KernelVersion = *state.KernelVersion
	}
	if state.OSRelease != nil {
		h.OSRelease = *state.OSRelease
	}
	if state.CPUHasVMX != nil {
		h.CPUHasVMX = *state.CPUHasVMX != 0
	}
	if state.CPUHypervisor != nil {
		h.CPUHypervisor = *state.CPUHypervisor != 0
	}
	return h
}

// ── LimitsFromState ──
// Reconstructs HostLimits from stored host state (cache layer).
func LimitsFromState(state *model.HostStateItem) *model.HostLimits {
	if state.PIDMax == nil {
		return nil
	}
	var portRange [2]int
	if state.IPLocalPortRange != nil {
		portRange = validators.ParsePortRange(*state.IPLocalPortRange)
	} else {
		portRange = infra.DefaultIPLocalPortRange
	}
	l := &model.HostLimits{}
	if state.PIDMax != nil {
		l.PIDMax = *state.PIDMax
	}
	if state.FDMax != nil {
		l.FDMax = *state.FDMax
	}
	if state.ConntrackMax != nil {
		l.ConntrackMax = *state.ConntrackMax
	}
	if state.TAPDevicesMax != nil {
		l.TAPDevicesMax = *state.TAPDevicesMax
	}
	l.IPLocalPortRange = portRange
	if state.NestedVirtAvailable != nil {
		l.NestedVirtAvailable = *state.NestedVirtAvailable != 0
	}
	if state.EPTAvailable != nil {
		l.EPTAvailable = *state.EPTAvailable != 0
	}
	if state.HugepageCount2MB != nil {
		l.HugepageCount2MB = *state.HugepageCount2MB
	}
	if state.KSMDisabled != nil {
		l.KSMDisabled = *state.KSMDisabled != 0
	} else {
		l.KSMDisabled = true
	}
	if state.CgroupVersion != nil && *state.CgroupVersion != 0 {
		l.CgroupVersion = *state.CgroupVersion
	} else {
		l.CgroupVersion = 1
	}
	if state.SwapTotalMiB != nil {
		l.SwapTotalMiB = *state.SwapTotalMiB
	}
	if state.KernelMinimumMet != nil {
		l.KernelMinimumMet = *state.KernelMinimumMet != 0
	}
	return l
}
