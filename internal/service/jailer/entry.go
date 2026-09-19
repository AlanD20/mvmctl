package jailer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

var (
	versionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[-+][A-Za-z0-9.-]+)?$`)
	vmIDPattern       = regexp.MustCompile(`^[a-f0-9]{32}$`)
	driveIDPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	snapshotIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type managedPaths struct {
	cacheRoot     string
	vmsRoot       string
	volumesRoot   string
	snapshotsRoot string
	vmDir         string
	uid           uint32
}

// Config selects one legacy resource-exposure operation still required by snapshot and volume consumers.
type Config struct {
	Action      string
	VMID        string
	VMDir       string
	SnapshotDir string
	DriveID     string
	HostPath    string
	ReadOnly    bool
}

// Run dispatches the remaining legacy resource-exposure operations.
// Launch and cleanup exclusively use the typed private lifecycle receiver.
func Run(ctx context.Context, cfg Config) error {
	if !system.IsRoot() {
		return fmt.Errorf("jailer service requires root")
	}
	switch cfg.Action {
	case "expose-snapshot":
		return exposeSnapshot(cfg.VMID, cfg.VMDir, cfg.SnapshotDir)
	case "expose-volume":
		return exposeVolume(cfg.VMID, cfg.VMDir, cfg.DriveID, cfg.HostPath, cfg.ReadOnly)
	case "remove-volume":
		return removeVolume(cfg.VMID, cfg.DriveID)
	default:
		return fmt.Errorf("unsupported jailer service action %q", cfg.Action)
	}
}

func resolveManagedPaths(vmID, vmDir string) (*managedPaths, error) {
	if !vmIDPattern.MatchString(vmID) {
		return nil, fmt.Errorf("invalid VM identity for jailed launch")
	}
	uid, _, err := invokingIdentity()
	if err != nil {
		return nil, err
	}
	cleanVMDir, err := canonicalExistingPath(vmDir)
	if err != nil {
		return nil, fmt.Errorf("invalid managed VM directory: %w", err)
	}
	if filepath.Base(cleanVMDir) != vmID || filepath.Base(filepath.Dir(cleanVMDir)) != "vms" {
		return nil, fmt.Errorf("VM directory is outside the managed vms root")
	}
	cacheRoot := filepath.Dir(filepath.Dir(cleanVMDir))
	paths := &managedPaths{
		cacheRoot:     cacheRoot,
		vmsRoot:       filepath.Join(cacheRoot, "vms"),
		volumesRoot:   filepath.Join(cacheRoot, "volumes"),
		snapshotsRoot: filepath.Join(cacheRoot, "snapshots"),
		vmDir:         cleanVMDir,
		uid:           uint32(uid),
	}
	for _, root := range []string{paths.cacheRoot, paths.vmsRoot, paths.vmDir} {
		if err := validateManagedDirectory(root, paths.uid); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func validateManagedSnapshot(path string, paths *managedPaths) error {
	clean, err := canonicalExistingPath(path)
	if err != nil {
		return fmt.Errorf("invalid managed snapshot path: %w", err)
	}
	if filepath.Dir(clean) != paths.snapshotsRoot || !snapshotIDPattern.MatchString(filepath.Base(clean)) {
		return fmt.Errorf("snapshot path is outside the managed snapshots root")
	}
	if err := validateManagedDirectory(paths.snapshotsRoot, paths.uid); err != nil {
		return err
	}
	if err := validateManagedDirectory(clean, paths.uid); err != nil {
		return err
	}
	return nil
}

func validateManagedFile(path, root string, uid uint32, mustExist bool) error {
	clean, err := filepath.Abs(path)
	if err != nil || clean != filepath.Clean(path) || filepath.Dir(clean) != root {
		return fmt.Errorf("path is outside managed root %s: %s", root, path)
	}
	if err := validateManagedDirectory(root, uid); err != nil {
		return err
	}
	info, err := os.Lstat(clean)
	if os.IsNotExist(err) && !mustExist {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed file %s: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("managed path is not a regular non-symlink file: %s", clean)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return fmt.Errorf("managed file is not owned by the invoking user: %s", clean)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return fmt.Errorf("managed file path is not canonical: %s", clean)
	}
	return nil
}

func validateManagedDirectory(path string, uid uint32) error {
	clean, err := canonicalExistingPath(path)
	if err != nil {
		return fmt.Errorf("invalid managed directory: %w", err)
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("managed path is not a real directory: %s", path)
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("managed directory is writable by other users: %s", clean)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return fmt.Errorf("managed directory is not owned by the invoking user: %s", clean)
	}
	return nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil || abs != filepath.Clean(path) {
		return "", fmt.Errorf("path is not absolute and canonical: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if resolved != abs {
		return "", fmt.Errorf("path contains a symlink: %s", path)
	}
	return abs, nil
}

func mountResource(source, target string, readOnly, directory bool) error {
	sourceFile, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open jail resource %s: %w", source, err)
	}
	defer sourceFile.Close()
	info, err := sourceFile.Stat()
	if err != nil {
		return fmt.Errorf("stat jail resource %s: %w", source, err)
	}
	if directory != info.IsDir() {
		return fmt.Errorf("jail resource has unexpected type: %s", source)
	}
	if directory {
		if err := os.MkdirAll(target, 0755); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		file.Close()
	}
	pinnedSource := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(sourceFile.Fd()), 10))
	if err := syscall.Mount(pinnedSource, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind jail resource %s: %w", source, err)
	}
	if readOnly {
		if err := syscall.Mount("", target, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("remount jail resource read-only %s: %w", target, err)
		}
	}
	return nil
}

func exposeSnapshot(vmID, vmDir, snapshotDir string) error {
	paths, err := resolveManagedPaths(vmID, vmDir)
	if err != nil {
		return err
	}
	if err := validateManagedSnapshot(snapshotDir, paths); err != nil {
		return err
	}
	target := filepath.Join(jailRoot(vmID), "snapshot")
	if err := unmountIfMounted(target); err != nil {
		return err
	}
	return mountResource(snapshotDir, target, false, true)
}

func exposeVolume(vmID, vmDir, driveID, hostPath string, readOnly bool) error {
	paths, err := resolveManagedPaths(vmID, vmDir)
	if err != nil {
		return err
	}
	if !driveIDPattern.MatchString(driveID) {
		return fmt.Errorf("invalid volume exposure request")
	}
	if err := validateManagedFile(hostPath, paths.volumesRoot, paths.uid, true); err != nil {
		return fmt.Errorf("invalid managed volume exposure path: %w", err)
	}
	target := filepath.Join(jailRoot(vmID), "volumes", driveID)
	if err := unmountIfMounted(target); err != nil {
		return err
	}
	return mountResource(hostPath, target, readOnly, false)
}

func removeVolume(vmID, driveID string) error {
	if !vmIDPattern.MatchString(vmID) || !driveIDPattern.MatchString(driveID) {
		return fmt.Errorf("invalid volume removal request")
	}
	err := syscall.Unmount(filepath.Join(jailRoot(vmID), "volumes", driveID), syscall.MNT_DETACH)
	if err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
		return err
	}
	return nil
}

func unmountIfMounted(target string) error {
	err := syscall.Unmount(target, syscall.MNT_DETACH)
	if err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
		return fmt.Errorf("unmount existing jail resource %s: %w", target, err)
	}
	return nil
}

func jailRoot(vmID string) string {
	return filepath.Join(infra.JailerChrootBase, "firecracker", vmID, "root")
}

func invokingIdentity() (int, int, error) {
	uid, uidErr := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, gidErr := strconv.Atoi(os.Getenv("SUDO_GID"))
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
		return 0, 0, fmt.Errorf("cannot determine non-root invoking identity")
	}
	return uid, gid, nil
}
