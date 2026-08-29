package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/archive"
	"mvmctl/internal/lib/model"
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
	kernelsRoot   string
	volumesRoot   string
	snapshotsRoot string
	vmDir         string
	uid           uint32
}

// Config selects one fixed-policy privileged Jailer operation.
type Config struct {
	Action      string
	Version     string
	Arch        string
	SHA256      string
	VMID        string
	VMDir       string
	SnapshotDir string
	DriveID     string
	HostPath    string
	ReadOnly    bool
}

// Run executes one privileged Jailer operation.
// CRITICAL: No raw executable, identity, chroot, mount target, or Jailer argument is accepted.
func Run(ctx context.Context, cfg Config) error {
	if !system.IsRoot() {
		return fmt.Errorf("jailer service requires root")
	}
	switch cfg.Action {
	case "install":
		return installRelease(ctx, cfg)
	case "launch":
		return launch(cfg)
	case "cleanup":
		return cleanup(cfg.VMID)
	case "expose-snapshot":
		return exposeSnapshot(cfg.VMID, cfg.VMDir, cfg.SnapshotDir)
	case "expose-volume":
		return exposeVolume(cfg.VMID, cfg.VMDir, cfg.DriveID, cfg.HostPath, cfg.ReadOnly)
	case "remove-volume":
		return removeVolume(cfg.VMID, cfg.DriveID)
	case "remove-release":
		return removeRelease(cfg.Version)
	default:
		return fmt.Errorf("unsupported jailer service action %q", cfg.Action)
	}
}

func installRelease(ctx context.Context, cfg Config) error {
	if !versionPattern.MatchString(cfg.Version) {
		return fmt.Errorf("invalid Firecracker version")
	}
	if cfg.Arch != "x86_64" && cfg.Arch != "aarch64" {
		return fmt.Errorf("unsupported Firecracker architecture")
	}
	if len(cfg.SHA256) != sha256.Size*2 {
		return fmt.Errorf("invalid release checksum")
	}
	if err := ensureTrustedDirectoryChain(infra.TrustedBinaryRoot); err != nil {
		return fmt.Errorf("prepare trusted binary root: %w", err)
	}
	tmp, err := os.CreateTemp(infra.TrustedBinaryRoot, ".release-*.tgz")
	if err != nil {
		return fmt.Errorf("create trusted archive: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), os.Stdin); err != nil {
		tmp.Close()
		return fmt.Errorf("copy verified release archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close trusted archive: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, cfg.SHA256) {
		return fmt.Errorf("release checksum changed before trusted installation")
	}

	destDir := filepath.Join(infra.TrustedBinaryRoot, cfg.Version)
	staging, err := os.MkdirTemp(infra.TrustedBinaryRoot, ".pair-*")
	if err != nil {
		return fmt.Errorf("create trusted pair staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	fcPath := filepath.Join(staging, "firecracker")
	jlPath := filepath.Join(staging, "jailer")
	if err := archive.ExtractRenamed(ctx, tmpPath, []archive.RenameEntry{
		{ArchiveName: fmt.Sprintf("firecracker-v%s-%s", cfg.Version, cfg.Arch), OutputPath: fcPath, Mode: 0755},
		{ArchiveName: fmt.Sprintf("jailer-v%s-%s", cfg.Version, cfg.Arch), OutputPath: jlPath, Mode: 0755},
	}); err != nil {
		return fmt.Errorf("extract trusted release pair: %w", err)
	}
	if err := os.Chmod(staging, 0755); err != nil {
		return fmt.Errorf("set trusted release directory mode: %w", err)
	}
	if !validTrustedPair(staging) {
		return fmt.Errorf("staged trusted release pair failed ownership or mode validation")
	}
	if err := syncTrustedPair(staging); err != nil {
		return fmt.Errorf("sync trusted release pair: %w", err)
	}

	destInfo, destErr := os.Lstat(destDir)
	switch {
	case destErr == nil:
		if destInfo.IsDir() && validTrustedPair(destDir) {
			same, err := pairsEqual(staging, destDir)
			if err != nil {
				return fmt.Errorf("compare trusted release pairs: %w", err)
			}
			if same {
				return writeInstallResult(destDir)
			}
		}
		if err := unix.Renameat2(
			unix.AT_FDCWD,
			staging,
			unix.AT_FDCWD,
			destDir,
			unix.RENAME_EXCHANGE,
		); err != nil {
			return fmt.Errorf("atomically replace trusted release pair: %w", err)
		}
		if err := syncDirectory(infra.TrustedBinaryRoot); err != nil {
			return fmt.Errorf("sync trusted binary root after replacement: %w", err)
		}
		if err := os.RemoveAll(staging); err != nil {
			return fmt.Errorf("remove replaced trusted release pair: %w", err)
		}
		if err := syncDirectory(infra.TrustedBinaryRoot); err != nil {
			return fmt.Errorf("sync trusted binary root after old-pair removal: %w", err)
		}
	case os.IsNotExist(destErr):
		if err := os.Rename(staging, destDir); err != nil {
			return fmt.Errorf("install trusted release pair: %w", err)
		}
		if err := syncDirectory(infra.TrustedBinaryRoot); err != nil {
			return fmt.Errorf("sync trusted binary root after installation: %w", err)
		}
	default:
		return fmt.Errorf("inspect existing trusted release pair: %w", destErr)
	}
	return writeInstallResult(destDir)
}

func writeInstallResult(destDir string) error {
	result := InstallResult{
		FirecrackerPath: filepath.Join(destDir, "firecracker"),
		JailerPath:      filepath.Join(destDir, "jailer"),
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func launch(cfg Config) error {
	manifest, err := loadManifest(cfg.VMID, cfg.VMDir)
	if err != nil {
		return err
	}
	if manifest.CgroupLimits == nil {
		return fmt.Errorf("cgroup limits are required for jailed launch")
	}
	if err := validateCgroupLimits(*manifest.CgroupLimits); err != nil {
		return err
	}
	if err := ensureTrustedDirectoryChain(infra.JailerChrootBase); err != nil {
		return fmt.Errorf("prepare Jailer chroot base: %w", err)
	}
	if err := cleanup(manifest.VMID); err != nil {
		return err
	}
	if err := prepareCgroupV2(); err != nil {
		return fmt.Errorf("prepare cgroup-v2 resource enforcement: %w", err)
	}
	jailRoot := jailRoot(manifest.VMID)
	if err := ensureTrustedDirectoryChain(jailRoot); err != nil {
		return fmt.Errorf("prepare jail root: %w", err)
	}
	if err := mountResource(manifest.VMDir, filepath.Join(jailRoot, "run", "mvm"), false, true); err != nil {
		return err
	}
	if err := mountResource(manifest.KernelPath, filepath.Join(jailRoot, "kernel"), true, false); err != nil {
		return err
	}
	if err := mountResource(manifest.RootfsPath, filepath.Join(jailRoot, "rootfs"), false, false); err != nil {
		return err
	}
	if manifest.ISOPath != "" {
		if err := mountResource(
			manifest.ISOPath,
			filepath.Join(jailRoot, infra.VMCloudInitISOFilename),
			true,
			false,
		); err != nil {
			return err
		}
	}
	if manifest.SnapshotDir != "" {
		if err := mountResource(manifest.SnapshotDir, filepath.Join(jailRoot, "snapshot"), false, true); err != nil {
			return err
		}
	}
	for _, volume := range manifest.Volumes {
		if !driveIDPattern.MatchString(volume.DriveID) {
			return fmt.Errorf("invalid volume drive ID")
		}
		target := filepath.Join(jailRoot, "volumes", volume.DriveID)
		if err := mountResource(volume.HostPath, target, volume.ReadOnly, false); err != nil {
			return err
		}
	}

	uid, gid, err := invokingIdentity()
	if err != nil {
		return err
	}
	if err := writeProcessPID(manifest.PIDPath, os.Getpid(), uid, gid); err != nil {
		return fmt.Errorf("write jailed process PID: %w", err)
	}
	jailerPath := filepath.Join(infra.TrustedBinaryRoot, manifest.Version, "jailer")
	firecrackerPath := filepath.Join(infra.TrustedBinaryRoot, manifest.Version, "firecracker")
	if err := validateTrustedExecutable(jailerPath); err != nil {
		return err
	}
	if err := validateTrustedExecutable(firecrackerPath); err != nil {
		return err
	}
	args := []string{
		jailerPath,
		"--id", manifest.VMID,
		"--exec-file", firecrackerPath,
		"--uid", strconv.Itoa(uid),
		"--gid", strconv.Itoa(gid),
		"--chroot-base-dir", infra.JailerChrootBase,
		"--cgroup-version", "2",
		"--parent-cgroup", infra.JailerCgroupParent,
		"--cgroup", fmt.Sprintf("cpu.max=%d %d", manifest.CgroupLimits.CPUQuotaMicros,
			manifest.CgroupLimits.CPUPeriodMicros),
		"--cgroup", fmt.Sprintf("cpu.weight=%d", manifest.CgroupLimits.CPUWeight),
		"--cgroup", fmt.Sprintf("memory.high=%d", manifest.CgroupLimits.MemoryHighBytes),
		"--cgroup", fmt.Sprintf("memory.max=%d", manifest.CgroupLimits.MemoryMaxBytes),
		"--cgroup", fmt.Sprintf("memory.swap.max=%d", manifest.CgroupLimits.SwapMaxBytes),
		"--cgroup", fmt.Sprintf("pids.max=%d", manifest.CgroupLimits.PIDsMax),
		"--",
		"--api-sock", "/run/mvm/" + infra.VMFirecrackerAPISocketFilename,
	}
	if manifest.PCIEnabled {
		args = append(args, "--enable-pci")
	}
	if !manifest.SnapshotMode {
		args = append(args, "--config-file", "/run/mvm/"+infra.VMFirecrackerConfigFilename)
	}
	return syscall.Exec(jailerPath, args, os.Environ())
}

func writeProcessPID(path string, pid, uid, gid int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chown(uid, gid); err != nil {
		return err
	}
	if _, err := file.WriteString(strconv.Itoa(pid)); err != nil {
		return err
	}
	return file.Sync()
}

func loadManifest(vmID, vmDir string) (*LaunchManifest, error) {
	paths, err := resolveManagedPaths(vmID, vmDir)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(paths.vmDir, infra.JailerManifestFilename)
	f, err := os.OpenFile(manifestPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open jailed launch manifest: %w", err)
	}
	defer f.Close()
	var manifest LaunchManifest
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode jailed launch manifest: %w", err)
	}
	if manifest.VMID != vmID || filepath.Clean(manifest.VMDir) != paths.vmDir {
		return nil, fmt.Errorf("jailed launch manifest identity mismatch")
	}
	if !versionPattern.MatchString(manifest.Version) {
		return nil, fmt.Errorf("invalid jailed launch version")
	}
	if err := validateCanonicalManifestVMResources(&manifest, paths.vmDir); err != nil {
		return nil, err
	}
	if err := validateManagedFile(manifestPath, paths.vmDir, paths.uid, true); err != nil {
		return nil, fmt.Errorf("invalid managed launch manifest: %w", err)
	}
	for _, runtimePath := range []string{manifest.ConfigPath, manifest.RootfsPath} {
		if err := validateManagedFile(runtimePath, paths.vmDir, paths.uid, true); err != nil {
			return nil, fmt.Errorf("invalid managed VM runtime path: %w", err)
		}
	}
	for _, runtimePath := range []string{manifest.PIDPath, manifest.APISocket} {
		if err := validateManagedFile(runtimePath, paths.vmDir, paths.uid, false); err != nil {
			return nil, fmt.Errorf("invalid managed VM runtime path: %w", err)
		}
	}
	if manifest.ISOPath != "" {
		if err := validateManagedFile(manifest.ISOPath, paths.vmDir, paths.uid, true); err != nil {
			return nil, fmt.Errorf("invalid managed cloud-init ISO path: %w", err)
		}
	}
	if err := validateManagedFile(manifest.KernelPath, paths.kernelsRoot, paths.uid, true); err != nil {
		return nil, fmt.Errorf("invalid managed kernel path: %w", err)
	}
	if manifest.SnapshotDir != "" {
		if err := validateManagedSnapshot(manifest.SnapshotDir, paths); err != nil {
			return nil, err
		}
	}
	for _, volume := range manifest.Volumes {
		if !driveIDPattern.MatchString(volume.DriveID) {
			return nil, fmt.Errorf("invalid managed volume drive ID")
		}
		if err := validateManagedFile(volume.HostPath, paths.volumesRoot, paths.uid, true); err != nil {
			return nil, fmt.Errorf("invalid managed volume path: %w", err)
		}
	}
	return &manifest, nil
}

func validateCanonicalManifestVMResources(manifest *LaunchManifest, vmDir string) error {
	type resourcePath struct {
		name string
		got  string
		want string
	}
	resources := []resourcePath{
		{
			name: "configuration",
			got:  manifest.ConfigPath,
			want: filepath.Join(vmDir, infra.VMFirecrackerConfigFilename),
		},
		{name: "rootfs", got: manifest.RootfsPath, want: filepath.Join(vmDir, infra.VMRootfsFilename)},
		{name: "PID", got: manifest.PIDPath, want: filepath.Join(vmDir, infra.VMFirecrackerPIDFilename)},
		{name: "API socket", got: manifest.APISocket, want: filepath.Join(vmDir, infra.VMFirecrackerAPISocketFilename)},
	}
	if manifest.ISOPath != "" {
		resources = append(resources, resourcePath{
			name: "cloud-init ISO",
			got:  manifest.ISOPath,
			want: filepath.Join(vmDir, infra.VMCloudInitISOFilename),
		})
	}
	for _, resource := range resources {
		if resource.got != resource.want {
			return fmt.Errorf("non-canonical managed VM resource path for %s", resource.name)
		}
	}
	return nil
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
		kernelsRoot:   filepath.Join(cacheRoot, "kernels"),
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

func pathWithin(path, base string) bool {
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, pathAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

func cleanup(vmID string) error {
	if !vmIDPattern.MatchString(vmID) {
		return fmt.Errorf("invalid VM ID for jail cleanup")
	}
	root := jailRoot(vmID)
	if err := ensureCgroupEmpty(vmID); err != nil {
		return err
	}
	if _, err := os.Lstat(infra.JailerChrootBase); err == nil {
		if err := validateTrustedDirectoryChain(infra.JailerChrootBase); err != nil {
			return fmt.Errorf("validate Jailer chroot base before cleanup: %w", err)
		}
		data, readErr := os.ReadFile("/proc/self/mountinfo")
		if readErr == nil {
			var targets []string
			for line := range strings.SplitSeq(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) > 4 && (fields[4] == root || strings.HasPrefix(fields[4], root+"/")) {
					targets = append(targets, fields[4])
				}
			}
			sort.Slice(targets, func(i, j int) bool { return len(targets[i]) > len(targets[j]) })
			for _, target := range targets {
				err := syscall.Unmount(target, syscall.MNT_DETACH)
				if err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
					return fmt.Errorf("unmount jail resource %s: %w", target, err)
				}
			}
		}
		if err := os.RemoveAll(filepath.Dir(root)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove jail: %w", err)
		}
	}
	if err := removeCgroup(vmID); err != nil {
		return err
	}
	return nil
}

func validateCgroupLimits(limits model.VMCgroupLimits) error {
	if limits.CPUQuotaMicros <= 0 || limits.CPUPeriodMicros <= 0 {
		return fmt.Errorf("invalid CPU cgroup limit")
	}
	if limits.CPUWeight < 1 || limits.CPUWeight > 10000 {
		return fmt.Errorf("invalid CPU cgroup weight")
	}
	if limits.MemoryHighBytes <= 0 || limits.MemoryMaxBytes <= 0 ||
		limits.MemoryHighBytes > limits.MemoryMaxBytes {
		return fmt.Errorf("invalid memory cgroup limit")
	}
	if limits.SwapMaxBytes < 0 || limits.PIDsMax <= 0 {
		return fmt.Errorf("invalid swap or PID cgroup limit")
	}
	return nil
}

func prepareCgroupV2() error {
	controllersPath := filepath.Join(infra.CgroupV2Root, "cgroup.controllers")
	controllers, err := os.ReadFile(controllersPath)
	if err != nil {
		return fmt.Errorf("unified cgroup v2 is required: %w", err)
	}
	required := []string{"cpu", "memory", "pids"}
	available := strings.Fields(string(controllers))
	for _, controller := range required {
		if !slices.Contains(available, controller) {
			return fmt.Errorf("required cgroup-v2 controller is unavailable: %s", controller)
		}
	}
	if err := enableCgroupControllers(infra.CgroupV2Root, required); err != nil {
		return err
	}
	parent := filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent)
	if err := os.Mkdir(parent, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create fixed cgroup parent: %w", err)
	}
	if err := enableCgroupControllers(parent, required); err != nil {
		return err
	}
	return nil
}

func enableCgroupControllers(path string, controllers []string) error {
	controlPath := filepath.Join(path, "cgroup.subtree_control")
	for _, controller := range controllers {
		if err := os.WriteFile(controlPath, []byte("+"+controller), 0644); err != nil {
			return fmt.Errorf("enable %s controller in %s: %w", controller, path, err)
		}
	}
	return nil
}

func removeCgroup(vmID string) error {
	path := cgroupPath(vmID)
	if err := ensureCgroupEmpty(vmID); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove empty VM cgroup: %w", err)
	}
	return nil
}

func ensureCgroupEmpty(vmID string) error {
	path := cgroupPath(vmID)
	procs, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read VM cgroup processes before cleanup: %w", err)
	}
	if strings.TrimSpace(string(procs)) != "" {
		return fmt.Errorf("refusing to remove VM cgroup while it contains live processes")
	}
	return nil
}

func cgroupPath(vmID string) string {
	return filepath.Join(
		infra.CgroupV2Root,
		infra.JailerCgroupParent,
		vmID,
	)
}

func removeRelease(version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid release version")
	}
	if _, err := os.Lstat(infra.TrustedBinaryRoot); os.IsNotExist(err) {
		return nil
	}
	if err := validateTrustedDirectoryChain(infra.TrustedBinaryRoot); err != nil {
		return fmt.Errorf("validate trusted binary root before removal: %w", err)
	}
	return os.RemoveAll(filepath.Join(infra.TrustedBinaryRoot, version))
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

func validateTrustedExecutable(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !pathWithin(path, infra.TrustedBinaryRoot) {
		return fmt.Errorf("trusted executable path is invalid: %s", path)
	}
	if err := validateTrustedDirectoryChain(filepath.Dir(path)); err != nil {
		return fmt.Errorf("trusted executable parent validation failed: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("trusted executable is unusable: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("trusted executable is not root-owned: %s", path)
	}
	return nil
}

func ensureTrustedDirectoryChain(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil || abs != filepath.Clean(path) {
		return fmt.Errorf("trusted directory path is not canonical: %s", path)
	}
	current := string(filepath.Separator)
	if err := validateTrustedDirectory(current); err != nil {
		return err
	}
	for _, component := range strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if _, err := os.Lstat(current); os.IsNotExist(err) {
			if err := os.Mkdir(current, 0755); err != nil {
				return fmt.Errorf("create trusted directory %s: %w", current, err)
			}
		} else if err != nil {
			return fmt.Errorf("inspect trusted directory %s: %w", current, err)
		}
		if err := validateTrustedDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func validateTrustedDirectoryChain(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil || abs != filepath.Clean(path) {
		return fmt.Errorf("trusted directory path is not canonical: %s", path)
	}
	current := string(filepath.Separator)
	if err := validateTrustedDirectory(current); err != nil {
		return err
	}
	for _, component := range strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := validateTrustedDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func validateTrustedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect trusted directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("trusted path component is not a real directory: %s", path)
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("trusted path component is writable by non-root users: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("trusted path component is not root-owned: %s", path)
	}
	return nil
}

func validTrustedPair(dir string) bool {
	if err := validateTrustedDirectoryChain(dir); err != nil {
		return false
	}
	for _, name := range []string{"firecracker", "jailer"} {
		if err := validateTrustedExecutable(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func pairsEqual(first, second string) (bool, error) {
	for _, name := range []string{"firecracker", "jailer"} {
		firstHash, err := fileSHA256(filepath.Join(first, name))
		if err != nil {
			return false, err
		}
		secondHash, err := fileSHA256(filepath.Join(second, name))
		if err != nil {
			return false, err
		}
		if firstHash != secondHash {
			return false, nil
		}
	}
	return true, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func syncTrustedPair(dir string) error {
	for _, name := range []string{"firecracker", "jailer"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
