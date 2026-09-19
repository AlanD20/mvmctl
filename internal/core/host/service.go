package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

const sysctlKey = "net.ipv4.ip_forward"
const sysctlConfPath = infra.DefaultSysctlConfPath

// --- Service ---
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// --- EnableIPForward ---
func EnableIPForward(ctx context.Context) (*model.HostStateChangeItem, error) {
	current, err := GetIPForwardStatus(ctx)
	if err != nil {
		return nil, err
	}
	if current == "1" {
		slog.Debug("IP forwarding already enabled")
		return nil, nil
	}
	err = runAdministratorCommand(ctx, []string{"/usr/sbin/sysctl", "-w", fmt.Sprintf("%s=1", sysctlKey)})
	if err != nil {
		return nil, fmt.Errorf("failed to enable IP forwarding: %w", err)
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

// --- PersistSysctl ---
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

	if err := os.MkdirAll(filepath.Dir(sysctlConfPath), infra.DirPerm); err != nil {
		return nil, errs.New(errs.CodeHostInitFailed,
			fmt.Sprintf("Failed to write %s: %v", sysctlConfPath, err), errs.WithClass(errs.ClassInternal))
	}
	if err := os.WriteFile(sysctlConfPath, []byte(content), 0644); err != nil {
		return nil, errs.New(errs.CodeHostInitFailed,
			fmt.Sprintf("Failed to write %s: %v", sysctlConfPath, err), errs.WithClass(errs.ClassInternal))
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

// --- loadModule ---
func (s *Service) loadModule(
	ctx context.Context,
	module string,
	sessionID string,
	changeOrder int,
	initTimestamp, createdAt string,
) (*model.HostStateChangeItem, error) {
	if module != "kvm" && module != "kvm_intel" && module != "kvm_amd" {
		return nil, errs.New(errs.CodeValidationFailed, "unsupported KVM module")
	}
	err := runAdministratorCommand(ctx, []string{"/usr/sbin/modprobe", module})
	if err != nil {
		return nil, fmt.Errorf("failed to load kernel module %s: %w", module, err)
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
	if s.repo != nil {
		if err := s.repo.AddChange(ctx, change); err != nil {
			return nil, err
		}
	}
	return change, nil
}

// --- EnsureKVMModules ---
func (s *Service) EnsureKVMModules(
	ctx context.Context,
	sessionID string,
	changeOrderStart int,
) ([]*model.HostStateChangeItem, int, error) {
	var changes []*model.HostStateChangeItem
	now := ""
	if sessionID != "" {
		now = time.Now().Format(time.RFC3339)
	}
	nextOrder := changeOrderStart

	// Detect available vendor modules: check lsmod first, then modprobe --dry-run
	var vendorModules []string
	for _, mod := range []string{"kvm_intel", "kvm_amd"} {
		loaded, err := administratorModuleLoaded(ctx, mod)
		if err != nil {
			return nil, nextOrder, err
		}
		dryRunResult, err := system.DefaultRunner.Run(ctx, []string{"/usr/sbin/modprobe", "--dry-run", mod},
			system.RunCmdOpts{Check: false, Capture: true, Timeout: 30 * time.Second,
				MaxStdoutBytes: 4096, MaxStderrBytes: 4096})
		if err != nil || dryRunResult == nil {
			return nil, nextOrder, errs.WrapMsg(errs.CodeHostInitFailed, "inspect KVM module availability", err)
		}
		if loaded || dryRunResult.Success() {
			vendorModules = append(vendorModules, mod)
		}
	}

	if len(vendorModules) == 0 {
		// When CONFIG_MODULES=n, KVM may be built directly into the kernel.
		if CheckKVMAccess() {
			slog.Warn("KVM appears to be built into the kernel (no modules to manage) — /dev/kvm is accessible")
			return changes, nextOrder, nil
		}
		return nil, 0, errs.New(
			errs.CodeHostInitFailed,
			"No KVM vendor modules available. Ensure virtualization is enabled in BIOS and KVM kernel modules are installed.",
			errs.WithClass(errs.ClassInternal),
		)
	}

	kvmModules := []string{"kvm"}
	for _, module := range kvmModules {
		loaded, err := administratorModuleLoaded(ctx, module)
		if err != nil {
			return changes, nextOrder, err
		}
		if loaded {
			slog.Debug("Module already loaded", "module", module)
			continue
		}
		change, err := s.loadModule(ctx, module, sessionID, nextOrder, now, now)
		if err != nil {
			return nil, 0, err
		}
		changes = append(changes, change)
		nextOrder++
	}

	vendorLoaded := false
	for _, m := range vendorModules {
		loaded, err := administratorModuleLoaded(ctx, m)
		if err != nil {
			return changes, nextOrder, err
		}
		if loaded {
			vendorLoaded = true
			break
		}
	}
	if !vendorLoaded {
		var loadErr error
		for _, module := range vendorModules {
			change, err := s.loadModule(ctx, module, sessionID, nextOrder, now, now)
			if err != nil {
				loadErr = errors.Join(loadErr, err)
				continue
			}
			changes = append(changes, change)
			nextOrder++
			vendorLoaded = true
			break
		}
		if !vendorLoaded {
			return changes, nextOrder, errs.WrapMsg(errs.CodeHostInitFailed, "load KVM vendor module", loadErr)
		}
	}

	return changes, nextOrder, nil
}

func administratorModuleLoaded(ctx context.Context, module string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == module {
			return true, nil
		}
	}
	return false, nil
}

// --- DetectAndSaveCapacity ---
func (s *Service) DetectAndSaveCapacity(ctx context.Context) (*model.HostHardware, *model.HostLimits, error) {
	hardware, err := DetectHardware()
	if err != nil {
		return nil, nil, fmt.Errorf("detect hardware: %w", err)
	}
	limits := DetectLimits()
	detectedAt := time.Now().Format(time.RFC3339)

	err = s.repo.SaveCapacity(ctx,
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

// --- RestoreState ---
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
		return nil, errs.New(
			errs.CodeHostResetFailed,
			"No saved host state to restore",
			errs.WithClass(errs.ClassInternal),
		)
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
				slog.Debug("Skipping disallowed sysctl key from state", "key", change.Setting)
				continue
			}
			_, err := system.DefaultRunner.Run(
				ctx,
				[]string{"sysctl", "-w", fmt.Sprintf("%s=%s", change.Setting, *change.OriginalValue)},
				system.RunCmdOpts{Check: true, Capture: true},
			)
			if err != nil {
				return nil, fmt.Errorf("failed to revert %s: %w", change.Setting, err)
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
			allowed := slices.Contains(resolvedAllowedFiles, target)
			if !allowed {
				slog.Debug("Skipping disallowed file path from state", "path", target)
				continue
			}
			if _, err := os.Stat(target); err == nil {
				if change.OriginalValue != nil {
					// Validate sudoers content if the target is under sudoers dir
					if strings.HasPrefix(target, infra.DefaultSudoersDir) {
						result, _ := system.DefaultRunner.Run(ctx, []string{"visudo", "-c", "-f", "-"},
							system.RunCmdOpts{Check: false, Capture: true, Input: *change.OriginalValue})
						if !result.Success() {
							return nil, errs.New(
								errs.CodeHostResetFailed,
								fmt.Sprintf(
									"Sudoers content from state failed visudo validation: %s",
									result.Stderr,
								),
								errs.WithClass(errs.ClassInternal),
							)
						}
					}
					if err := os.WriteFile(target, []byte(*change.OriginalValue), 0644); err != nil {
						return nil, errs.New(
							errs.CodeHostResetFailed,
							fmt.Sprintf(
								"Failed to revert file %s: %v",
								target,
								err,
							),
							errs.WithClass(errs.ClassInternal),
						)
					}
				} else {
					if err := os.Remove(target); err != nil {
						return nil, errs.New(
							errs.CodeHostResetFailed,
							fmt.Sprintf(
								"Failed to revert file %s: %v",
								target,
								err,
							),
							errs.WithClass(errs.ClassInternal),
						)
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
