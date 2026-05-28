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
	"mvmctl/internal/infra/system"
)

// POSIX access mode constants matching Python's os.R_OK, os.W_OK.
// Go's syscall package may not define these on all platforms.
const (
	rOk = 4
	wOk = 2
)

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
