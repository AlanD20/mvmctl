package host

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// POSIX access mode constants matching Python's os.R_OK, os.W_OK.
// Go's syscall package may not define these on all platforms.
const (
	rOk = 4
	wOk = 2
)

const sysctlKey = "net.ipv4.ip_forward"
const sysctlConfPath = infra.DefaultSysctlConfPath

// ── Service ──
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// ── _run (matches Python's HostService._run()) ──
func hostRunCmd(ctx context.Context, args []string, failureMsg, missingMsg string, capture, check bool) (*system.RunCmdResult, error) {
	opts := system.DefaultRunCmdOpts()
	opts.Check = check
	opts.Capture = capture
	result := system.RunCmdCompat(ctx, args, opts)

	if result.Err != nil {
		errStr := result.Err.Error()
		if strings.Contains(errStr, "Command not found") {
			return nil, hostError(errs.CodeHostInitFailed, missingMsg)
		}
		return nil, hostError(errs.CodeHostInitFailed, fmt.Sprintf("%s: %s", failureMsg, errStr))
	}
	// If check=false and exit code != 0, result.Err may be nil —
	// caller inspects result directly.
	return result, nil
}

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
	_, err := hostRunCmd(ctx,
		[]string{"groupadd", "--system", groupName},
		fmt.Sprintf("Failed to create group %s", groupName),
		"groupadd command not found",
		true, true,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ── AddUserToGroup ──
func AddUserToGroup(ctx context.Context, username, groupName string) (bool, error) {
	if system.UserInGroup(username, groupName) {
		return false, nil
	}
	_, err := hostRunCmd(ctx,
		[]string{"usermod", "-aG", groupName, username},
		fmt.Sprintf("Failed to add %s to group %s", username, groupName),
		"usermod command not found",
		true, true,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ── RemoveUserFromGroup ──
func RemoveUserFromGroup(ctx context.Context, username, groupName string) (bool, error) {
	if !system.GroupExists(groupName) {
		return false, nil
	}
	if !system.UserInGroup(username, groupName) {
		return false, nil
	}
	_, err := hostRunCmd(ctx,
		[]string{"gpasswd", "-d", username, groupName},
		fmt.Sprintf("Failed to remove user %s from group %s", username, groupName),
		"gpasswd command not found",
		true, true,
	)
	if err != nil {
		return false, err
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
	// Iterate in Python dict literal insertion order.
	binaries := privilegedBinariesOrdered()
	// Service binaries via "mvm run <service>" pattern (sudoers wildcard)
	runCmd := filepath.Join(infra.GetBinDir(), infra.CLIName, "run", "*")
	binaries = append(binaries, runCmd)
	binariesStr := strings.Join(binaries, ", ")
	return fmt.Sprintf(
		"# Managed by %s — do not edit manually.\n"+
			"# To remove: %s host reset\n"+
			"%%%s ALL=(root) NOPASSWD: %s\n",
		infra.ProjectName, infra.ProjectName, groupName, binariesStr,
	)
}

// privilegedBinariesOrdered returns the keys of infra.PrivilegedBinaries in the
// order matching Python's PRIVILEGED_BINARIES dict literal insertion order.
// TODO: Move to infra/ (verdict #33).
func privilegedBinariesOrdered() []string {
	// Python dict literal order from constants.py:
	// /usr/sbin/ip, /usr/sbin/iptables, /usr/sbin/iptables-restore,
	// /usr/sbin/iptables-save, /usr/sbin/nft, /usr/sbin/sysctl, /usr/sbin/modprobe
	return []string{
		"/usr/sbin/ip",
		"/usr/sbin/iptables",
		"/usr/sbin/iptables-restore",
		"/usr/sbin/iptables-save",
		"/usr/sbin/nft",
		"/usr/sbin/sysctl",
		"/usr/sbin/modprobe",
	}
}

// ── WriteSudoers ──
// Matches Python's HostService.write_sudoers() exactly.
func WriteSudoers(ctx context.Context, path string, groupName string) error {
	if err := ValidateSudoersBinaries(); err != nil {
		return err
	}
	content := GenerateSudoersContent(groupName)

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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
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
	_, err := hostRunCmd(ctx,
		[]string{"groupdel", groupName},
		fmt.Sprintf("Failed to remove group %s", groupName),
		"groupdel command not found",
		true, true,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ── getIPForwardStatus ──
// TODO: Move to infra/ (verdict #33).
func getIPForwardStatus(ctx context.Context) (string, error) {
	result, err := hostRunCmd(ctx,
		[]string{"sysctl", "-n", sysctlKey},
		fmt.Sprintf("Failed to read %s", sysctlKey),
		"sysctl command not found",
		true, true,
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

// ── EnableIPForward ──
func EnableIPForward(ctx context.Context) (*model.HostStateChangeItem, error) {
	current, err := getIPForwardStatus(ctx)
	if err != nil {
		return nil, err
	}
	if current == "1" {
		slog.Debug("IP forwarding already enabled")
		return nil, nil
	}
	_, err = hostRunCmd(ctx,
		[]string{"sysctl", "-w", fmt.Sprintf("%s=1", sysctlKey)},
		"Failed to enable IP forwarding",
		"sysctl command not found",
		true, true,
	)
	if err != nil {
		return nil, err
	}
	return &model.HostStateChangeItem{
		SessionID:     "",
		InitTimestamp: "",
		Setting:       sysctlKey,
		Mechanism:     "sysctl",
		AppliedValue:  "1",
		Reverted:      false,
		ChangeOrder:   0,
		CreatedAt:     "",
		OriginalValue: &current,
	}, nil
}

// ── PersistSysctl ──
func PersistSysctl(ctx context.Context) (*model.HostStateChangeItem, error) {
	content := fmt.Sprintf("%s = 1\n", sysctlKey)

	// Check if file already exists with correct content
	if _, err := os.Stat(sysctlConfPath); err == nil {
		data, err := os.ReadFile(sysctlConfPath)
		if err == nil && string(data) == content {
			slog.Debug("sysctl persist file already exists with correct content")
			return nil, nil
		}
	}

	var original *string
	if _, err := os.Stat(sysctlConfPath); err == nil {
		data, err := os.ReadFile(sysctlConfPath)
		if err == nil {
			s := string(data)
			original = &s
		}
	}

	if err := os.MkdirAll(filepath.Dir(sysctlConfPath), 0755); err != nil {
		return nil, hostError(errs.CodeHostInitFailed,
			fmt.Sprintf("Failed to write %s: %v", sysctlConfPath, err))
	}
	if err := os.WriteFile(sysctlConfPath, []byte(content), 0644); err != nil {
		return nil, hostError(errs.CodeHostInitFailed,
			fmt.Sprintf("Failed to write %s: %v", sysctlConfPath, err))
	}

	return &model.HostStateChangeItem{
		SessionID:     "",
		InitTimestamp: "",
		Setting:       "sysctl_persist_file",
		Mechanism:     "file_create",
		AppliedValue:  sysctlConfPath,
		Reverted:      false,
		ChangeOrder:   0,
		CreatedAt:     "",
		OriginalValue: original,
	}, nil
}

// ── isModuleLoaded ──
// TODO: Move to infra/ (verdict #33).
func isModuleLoaded(ctx context.Context, module string) bool {
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	result := system.RunCmdCompat(ctx, []string{"lsmod"}, opts)
	if result.ExitCode != 0 {
		return false
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
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

// ── loadModule ──
// TODO: Move to infra/ (verdict #33).
func loadModule(ctx context.Context, module string, repo Repository, sessionID string, changeOrder int, initTimestamp, createdAt string) (*model.HostStateChangeItem, error) {
	_, err := hostRunCmd(ctx,
		[]string{"modprobe", module},
		fmt.Sprintf("Failed to load kernel module %s", module),
		"modprobe command not found",
		true, true,
	)
	if err != nil {
		return nil, err
	}
	change := &model.HostStateChangeItem{
		SessionID:     sessionID,
		InitTimestamp: initTimestamp,
		Setting:       "kernel_module_load",
		Mechanism:     "modprobe",
		AppliedValue:  module,
		Reverted:      false,
		ChangeOrder:   changeOrder,
		CreatedAt:     createdAt,
		OriginalValue: nil,
	}
	if repo != nil {
		if err := repo.AddChange(ctx, change); err != nil {
			return nil, err
		}
	}
	return change, nil
}

// ── EnsureKVMModules ──
// Matches Python's HostService.ensure_kvm_modules() exactly.
func EnsureKVMModules(ctx context.Context, repo Repository, sessionID string, changeOrderStart int) ([]*model.HostStateChangeItem, int, error) {
	var changes []*model.HostStateChangeItem
	now := ""
	if sessionID != "" {
		now = time.Now().Format(time.RFC3339)
	}
	nextOrder := changeOrderStart

	// Detect available vendor modules: check lsmod first, then modprobe --dry-run
	var vendorModules []string
	for _, mod := range []string{"kvm_intel", "kvm_amd"} {
		loaded := isModuleLoaded(ctx, mod)
		opts := system.DefaultRunCmdOpts()
		opts.Check = false
		dryRunResult := system.RunCmdCompat(ctx, []string{"modprobe", "--dry-run", mod}, opts)
		if loaded || dryRunResult.ExitCode == 0 {
			vendorModules = append(vendorModules, mod)
		}
	}

	if len(vendorModules) == 0 {
		// When CONFIG_MODULES=n, KVM may be built directly into the kernel.
		if CheckKVMAccess() {
			slog.Warn("KVM appears to be built into the kernel (no modules to manage) — /dev/kvm is accessible")
			return changes, nextOrder, nil
		}
		return nil, 0, hostError(errs.CodeHostInitFailed,
			"No KVM vendor modules available. Ensure virtualization is enabled in BIOS and KVM kernel modules are installed.")
	}

	kvmModules := []string{"kvm"}
	for _, module := range kvmModules {
		if isModuleLoaded(ctx, module) {
			slog.Debug("Module already loaded", "module", module)
			continue
		}
		change, err := loadModule(ctx, module, repo, sessionID, nextOrder, now, now)
		if err != nil {
			return nil, 0, err
		}
		changes = append(changes, change)
		nextOrder++
	}

	vendorLoaded := false
	for _, m := range vendorModules {
		if isModuleLoaded(ctx, m) {
			vendorLoaded = true
			break
		}
	}
	if !vendorLoaded {
		for _, module := range vendorModules {
			change, err := loadModule(ctx, module, repo, sessionID, nextOrder, now, now)
			if err != nil {
				continue
			}
			changes = append(changes, change)
			nextOrder++
			break
		}
	}

	return changes, nextOrder, nil
}

// ── DetectAndSaveCapacity ──
// Matches Python's HostService.detect_and_save_capacity().
func (s *Service) DetectAndSaveCapacity(ctx context.Context) (*model.HostHardware, *model.HostLimits, error) {
	hardware := DetectHardware()
	limits := DetectLimits()
	detectedAt := time.Now().Format(time.RFC3339)

	err := s.repo.SaveCapacity(ctx,
		hardware.Hostname,
		hardware.CPUModel,
		hardware.CPUVendor,
		hardware.CPUCores,
		hardware.CPUArchitecture,
		hardware.NumaNodes,
		hardware.MemoryTotalMiB,
		hardware.StorageTotalBytes,
		hardware.KernelVersion,
		hardware.OSRelease,
		limits.PIDMax,
		limits.FDMax,
		limits.ConntrackMax,
		limits.TAPDevicesMax,
		limits.IPLocalPortRange,
		detectedAt,
		hardware.CPUHasVMX,
		hardware.CPUHypervisor,
		limits.NestedVirtAvailable,
		limits.EPTAvailable,
		limits.HugepageCount2MB,
		limits.KSMDisabled,
		limits.CgroupVersion,
		limits.SwapTotalMiB,
		limits.KernelMinimumMet,
	)
	if err != nil {
		return nil, nil, err
	}

	slog.Info("Host capacity detected",
		"hostname", hardware.Hostname,
		"cpu_model", hardware.CPUModel,
		"cpu_cores", hardware.CPUCores,
		"memory_mib", hardware.MemoryTotalMiB,
	)
	return hardware, limits, nil
}

// ── RestoreState ──
// Matches Python's HostService.restore_state() exactly.
func (s *Service) RestoreState(ctx context.Context) ([]*model.HostStateChangeItem, error) {
	_, err := s.repo.InitializeState(ctx)
	if err != nil {
		return nil, err
	}
	changes, err := s.repo.ListChanges(ctx, nil, false)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, hostError(errs.CodeHostResetFailed, "No saved host state to restore")
	}

	var reverted []*model.HostStateChangeItem
	revertedAt := time.Now().Format(time.RFC3339)

	restorableSysctl := map[string]bool{sysctlKey: true}
	restorableFiles := []string{
		infra.SudoersDropInPath(),
		fmt.Sprintf("%s/%s.conf", infra.DefaultSysctlConfDir, infra.ProjectName),
	}
	var resolvedAllowedFiles []string
	for _, p := range restorableFiles {
		resolvedAllowedFiles = append(resolvedAllowedFiles, system.ResolvePath(p))
	}

	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		wasReverted := false

		if change.Mechanism == "sysctl" && change.OriginalValue != nil {
			if !restorableSysctl[change.Setting] {
				slog.Warn("Skipping disallowed sysctl key from state", "key", change.Setting)
				continue
			}
			_, err := hostRunCmd(ctx,
				[]string{"sysctl", "-w", fmt.Sprintf("%s=%s", change.Setting, *change.OriginalValue)},
				fmt.Sprintf("Failed to revert %s", change.Setting),
				"sysctl command not found",
				true, true,
			)
			if err != nil {
				return nil, err
			}
			reverted = append(reverted, &model.HostStateChangeItem{
				SessionID:     change.SessionID,
				InitTimestamp: change.InitTimestamp,
				Setting:       change.Setting,
				Mechanism:     "sysctl",
				AppliedValue:  *change.OriginalValue,
				Reverted:      false,
				ChangeOrder:   change.ChangeOrder,
				CreatedAt:     change.CreatedAt,
				OriginalValue: &change.AppliedValue,
			})
			wasReverted = true

		} else if change.Mechanism == "file_create" {
			target := system.ResolvePath(change.AppliedValue)
			allowed := false
			for _, allowedPath := range resolvedAllowedFiles {
				if target == allowedPath {
					allowed = true
					break
				}
			}
			if !allowed {
				slog.Warn("Skipping disallowed file path from state", "path", target)
				continue
			}
			if _, err := os.Stat(target); err == nil {
				if change.OriginalValue != nil {
					// Validate sudoers content if the target is under sudoers dir
					if strings.HasPrefix(target, infra.DefaultSudoersDir) {
						opts := system.DefaultRunCmdOpts()
						opts.Check = false
						opts.Input = *change.OriginalValue
						result := system.RunCmdCompat(ctx, []string{"visudo", "-c", "-f", "-"}, opts)
						if result.ExitCode != 0 {
							return nil, hostError(errs.CodeHostResetFailed,
								fmt.Sprintf("Sudoers content from state failed visudo validation: %s", result.Stderr))
						}
					}
					if err := os.WriteFile(target, []byte(*change.OriginalValue), 0644); err != nil {
						return nil, hostError(errs.CodeHostResetFailed,
							fmt.Sprintf("Failed to revert file %s: %v", target, err))
					}
				} else {
					if err := os.Remove(target); err != nil {
						return nil, hostError(errs.CodeHostResetFailed,
							fmt.Sprintf("Failed to revert file %s: %v", target, err))
					}
				}
				appliedVal := "(removed)"
				if change.OriginalValue != nil {
					appliedVal = *change.OriginalValue
				}
				reverted = append(reverted, &model.HostStateChangeItem{
					SessionID:     change.SessionID,
					InitTimestamp: change.InitTimestamp,
					Setting:       change.Setting,
					Mechanism:     "file_remove",
					AppliedValue:  appliedVal,
					Reverted:      false,
					ChangeOrder:   change.ChangeOrder,
					CreatedAt:     change.CreatedAt,
					OriginalValue: &change.AppliedValue,
				})
				wasReverted = true
			}
		}

		if wasReverted && change.ID != nil {
			if err := s.repo.MarkChangeReverted(ctx, *change.ID, revertedAt, nil); err != nil {
				return nil, err
			}
		}
	}

	return reverted, nil
}
