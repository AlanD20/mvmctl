// Package loopmount provides a goroutine-safe provisioner for loop-mount
// operations on VM root filesystem images.
//
// This is the direct provisioning engine — no subprocess, no JSON protocol.
// The original cmd/mvm/provision.go binary logic was moved here as private
// methods on Provisioner (doProvision, doDetectOS, doConvertFS).
package loopmount

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// ── Public types (no interface{} in public API) ─────────────────────────

// Op represents a single provisioning operation.
type Op struct {
	// Image is the path to the root filesystem image.
	Image string

	// Action is the operation type: "provision", "detect_os", or "convert_fs".
	Action string

	// FsType is an optional hint for the filesystem type (e.g. "ext4", "btrfs").
	FsType string

	// TargetFS is the target filesystem type for convert_fs actions.
	TargetFS string

	// Files is a list of files to write into the image.
	Files []FileOp

	// CopyDirs is a list of directories to copy into the image.
	CopyDirs []CopyDirOp

	// Commands is a list of shell commands to run inside a chroot.
	Commands []string

	// Debug enables debug logging to /tmp/mvm-provision-debug.log.
	Debug bool

	// Shell is an optional custom shell path for chroot commands.
	Shell string

	// Resize is an optional resize operation.
	Resize *ResizeOp
}

// FileOp describes a single file to write into the image.
type FileOp struct {
	Path string      // absolute path inside the image
	Data []byte      // raw file content (not base64-encoded)
	Mode os.FileMode // file permissions (default 0644)
	UID  int         // owner UID
	GID  int         // owner GID
}

// CopyDirOp describes a directory to recursively copy into the image.
type CopyDirOp struct {
	Src  string      // source path on the host
	Dst  string      // destination path inside the image
	Mode os.FileMode // directory permissions (default 0755)
}

// ResizeOp describes a filesystem resize operation.
type ResizeOp struct {
	Action   string // "grow" or "shrink"
	Bytes    int64  // target size in bytes (0 = shrink to minimum)
	Headroom int    // extra headroom in bytes after shrink-to-minimum
}

// Result holds the outcome of a provisioning operation.
type Result struct {
	Status       string // "ok" or "error"
	Error        string // error message when Status is "error"
	Step         string // the step that failed (only set on error)
	FilesWritten int    // number of files written
	CommandsRun  int    // number of commands executed
	OSType       string // detected OS type (for detect_os)
	NewFSType    string // new filesystem type (for convert_fs)
	NewSizeBytes int64  // new filesystem size (for convert_fs)
}

// ── Provisioner ─────────────────────────────────────────────────────────

// Provisioner executes loop-mount provisioning operations directly
// (no subprocess, no JSON protocol).
type Provisioner struct {
	cacheDir string
}

// NewProvisioner creates a new Provisioner.
func NewProvisioner(cacheDir string) *Provisioner {
	return &Provisioner{cacheDir: cacheDir}
}

// Execute runs a batch of provisioning operations sequentially.
// Each Op is dispatched to the appropriate handler: doProvision,
// doDetectOS, or doConvertFS.
func (p *Provisioner) Execute(ctx context.Context, ops []Op) ([]Result, error) {
	results := make([]Result, 0, len(ops))

	for _, op := range ops {
		var result Result

		switch op.Action {
		case "detect_os":
			result = p.doDetectOS(ctx, op)
		case "convert_fs":
			result = p.doConvertFS(ctx, op)
		default: // "provision" or any unrecognised action
			result = p.doProvision(ctx, op)
		}

		if result.Status == "error" {
			return results, fmt.Errorf("provision %s: %s", op.Action, result.Error)
		}
		results = append(results, result)
	}

	return results, nil
}

// =========================================================================
// Private action handlers
// =========================================================================

// doProvision handles the "provision" action — loop device setup, mount,
// file writes, chroot commands, and resize.
func (p *Provisioner) doProvision(ctx context.Context, input Op) Result {
	ps := &provisionState{
		input: input,
		step:  "parse",
	}

	// Resolve filesystem type hint
	fsType := input.FsType
	if fsType == "" {
		fsType = "ext4"
	}

	// State variables
	var mountPoint string
	var rootPart string
	var detectedFSType string
	var resizeNewBytes int64

	// Cleanup function — deferred to always run
	defer func() {
		ps.debugLog(fmt.Sprintf("cleanup: mount_point=%q loop_dev=%q", mountPoint, ps.loopDev))
		if mountPoint != "" {
			CleanupMount(mountPoint)
			mountPoint = ""
		}
		if ps.loopDev != "" {
			exec.Command("losetup", "-d", ps.loopDev).Run()
			ps.loopDev = ""
		}
	}()

	// ── Pre-loop resize: grow (truncate file before mounting) ──
	if input.Resize != nil && input.Resize.Action == "grow" && input.Resize.Bytes > 0 {
		ps.step = "resize"
		ps.debugLog(fmt.Sprintf("truncating image to %d bytes", input.Resize.Bytes))
		f, err := os.OpenFile(input.Image, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return Result{Status: "error", Error: fmt.Sprintf("truncate: %v", err), Step: ps.step}
		}
		if err := f.Truncate(input.Resize.Bytes); err != nil {
			f.Close()
			return Result{Status: "error", Error: fmt.Sprintf("truncate: %v", err), Step: ps.step}
		}
		f.Close()
	}

	// ── Debug: log system info ──
	if input.Debug {
		ps.debugLog(fmt.Sprintf("uid=%d gid=%d euid=%d", os.Getuid(), os.Getgid(), os.Geteuid()))
		ps.debugLog(fmt.Sprintf("image=%s fs_type_hint=%s", input.Image, input.FsType))
		statusData, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", os.Getpid()))
		if err == nil {
			for _, line := range strings.Split(string(statusData), "\n") {
				if strings.HasPrefix(line, "Cap") {
					ps.debugLog(fmt.Sprintf("cap: %s", strings.TrimSpace(line)))
				}
			}
		} else {
			ps.debugLog(fmt.Sprintf("cannot read /proc/self/status: %v", err))
		}
	}

	// ── Loop device setup ──
	ps.step = "loop"
	ps.debugLog("setting up loop device")
	{
		losetupArgs := []string{"-f", "-P", "--show", "--direct-io=on", input.Image}
		cmd := exec.Command("losetup", losetupArgs...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return Result{Status: "error", Error: fmt.Sprintf("losetup: %v", err), Step: ps.step}
		}
		ps.loopDev = strings.TrimSpace(string(output))
		ps.debugLog(fmt.Sprintf("loop device: %s", ps.loopDev))
	}

	// ── Root partition detection ──
	ps.step = "partition"
	ps.debugLog("finding root partition")
	rootPart = findRootPartition(ps.loopDev)
	ps.debugLog(fmt.Sprintf("root partition: %s", rootPart))

	// ── Filesystem type detection ──
	ps.step = "detect_fs"
	if input.FsType != "" {
		detectedFSType = input.FsType
	} else {
		detectedFSType = detectFSType(rootPart)
	}
	ps.debugLog(fmt.Sprintf("fs type: %s", detectedFSType))

	// ── Mount ──
	ps.step = "mount"
	{
		var err error
		mountPoint, err = os.MkdirTemp("", infra.MVMProvisionPrefix)
		if err != nil {
			return Result{Status: "error", Error: fmt.Sprintf("mkdtemp: %v", err), Step: ps.step}
		}

		mountArgs := []string{rootPart, mountPoint}
		if detectedFSType == "btrfs" {
			mountArgs = append([]string{"-t", "btrfs"}, mountArgs...)
		}
		cmd := exec.Command("mount", mountArgs...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return Result{Status: "error", Error: fmt.Sprintf("mount: %v: %s", err, string(output)), Step: ps.step}
		}
	}

	// ── Chroot buffer ──
	chrootBuffer := make([]string, 0, chrootBatchSize)

	// Helper: flush chroot buffer
	flushChroot := func() error {
		if len(chrootBuffer) == 0 {
			return nil
		}
		command := strings.Join(chrootBuffer, " && ")
		chrootBuffer = chrootBuffer[:0]
		return runChrootCommands(ctx, mountPoint, command, input.Shell)
	}

	// ── Flush any pending chroot commands before file operations ──
	if err := flushChroot(); err != nil {
		return Result{Status: "error", Error: err.Error(), Step: ps.step}
	}

	// ── Write files ──
	filesWritten := 0
	for _, f := range input.Files {
		ps.step = "write"
		if err := writeFile(mountPoint, f, ps); err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
		filesWritten++
	}

	// ── Flush any pending chroot commands before copy directory operations ──
	if err := flushChroot(); err != nil {
		return Result{Status: "error", Error: err.Error(), Step: ps.step}
	}

	// ── Copy directories ──
	for _, c := range input.CopyDirs {
		ps.step = "copy_dir"
		count, err := copyDirectory(mountPoint, c, ps)
		if err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
		filesWritten += count
	}

	// ── Chroot commands (buffered, then flushed in batches) ──
	for _, cmdStr := range input.Commands {
		ps.step = "chroot"
		chrootBuffer = append(chrootBuffer, cmdStr)
		if len(chrootBuffer) >= chrootBatchSize {
			command := strings.Join(chrootBuffer, " && ")
			chrootBuffer = chrootBuffer[:0]
			if err := runChrootCommands(ctx, mountPoint, command, input.Shell); err != nil {
				return Result{Status: "error", Error: err.Error(), Step: ps.step}
			}
		}
	}
	commandsRun := len(input.Commands)

	// ── Flush any remaining buffered chroot commands ──
	if err := flushChroot(); err != nil {
		return Result{Status: "error", Error: err.Error(), Step: ps.step}
	}

	// ── Post-mount resize: shrink ──
	if input.Resize != nil && input.Resize.Action == "shrink" {
		ps.step = "resize"
		if detectedFSType == "btrfs" {
			// btrfs shrink
			var shrinkErr error
			resizeNewBytes, shrinkErr = shrinkBtrfs(mountPoint, rootPart, input.Resize.Bytes)
			if shrinkErr != nil {
				return Result{Status: "error", Error: shrinkErr.Error(), Step: ps.step}
			}
		} else {
			// ext4 shrink: unmount → e2fsck → resize2fs -M (NO remount — Python leaves it unmounted)
			if _, err := system.DefaultRunner.Run(
				ctx,
				[]string{"umount", mountPoint},
				system.WithCapture(false),
			); err != nil {
				return Result{Status: "error", Error: fmt.Sprintf("umount failed: %v", err), Step: ps.step}
			}
			if _, err := system.DefaultRunner.Run(
				ctx,
				[]string{"e2fsck", "-f", "-y", rootPart},
				system.WithCapture(true),
				system.WithCheck(true),
			); err != nil {
				return Result{Status: "error", Error: err.Error(), Step: ps.step}
			}
			if _, err := system.DefaultRunner.Run(
				ctx,
				[]string{"resize2fs", "-M", rootPart},
				system.WithCapture(true),
				system.WithCheck(true),
			); err != nil {
				return Result{Status: "error", Error: err.Error(), Step: ps.step}
			}
			if input.Resize.Headroom > 0 {
				// Run resize2fs with extra headroom
				curSize := getFSByteSize(rootPart)
				targetSize := curSize + int64(input.Resize.Headroom)
				if _, err := system.DefaultRunner.Run(
					ctx,
					[]string{"resize2fs", rootPart, strconv.FormatInt(targetSize, 10)},
					system.WithCapture(true),
					system.WithCheck(true),
				); err != nil {
					return Result{Status: "error", Error: err.Error(), Step: ps.step}
				}
			}
			resizeNewBytes = getFSByteSize(rootPart)
		}
	}

	// ── Post-mount resize: grow (ext4 only; btrfs grown after truncate) ──
	if input.Resize != nil && input.Resize.Action == "grow" && detectedFSType != "btrfs" {
		ps.step = "resize"
		if _, err := system.DefaultRunner.Run(
			ctx,
			[]string{"umount", mountPoint},
			system.WithCapture(false),
		); err != nil {
			return Result{Status: "error", Error: fmt.Sprintf("umount failed: %v", err), Step: ps.step}
		}
		if _, err := system.DefaultRunner.Run(
			ctx,
			[]string{"e2fsck", "-f", "-y", rootPart},
			system.WithCapture(true),
			system.WithCheck(true),
		); err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
		if _, err := system.DefaultRunner.Run(
			ctx,
			[]string{"resize2fs", rootPart},
			system.WithCapture(true),
			system.WithCheck(true),
		); err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
	}

	// ── Post-detach truncation for shrink ──
	if resizeNewBytes > 0 {
		f, err := os.OpenFile(input.Image, os.O_WRONLY|os.O_CREATE, 0644)
		if err == nil {
			f.Truncate(resizeNewBytes)
			f.Close()
		}
	}

	return Result{
		Status:       "ok",
		FilesWritten: filesWritten,
		CommandsRun:  commandsRun,
	}
}

// doDetectOS handles the "detect_os" action — loop device, mount, read os-release.
func (p *Provisioner) doDetectOS(ctx context.Context, input Op) Result {
	ps := &provisionState{input: input, step: "loop"}

	// Setup loop device
	losetupArgs := []string{"-f", "-P", "--show", "--direct-io=on", input.Image}
	cmd := exec.Command("losetup", losetupArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("losetup: %v", err), Step: ps.step}
	}
	loopDev := strings.TrimSpace(string(output))

	// Cleanup on return
	defer func() {
		exec.Command("losetup", "-d", loopDev).Run()
	}()

	// Find root partition
	ps.step = "partition"
	rootPart := findRootPartition(loopDev)

	// Detect filesystem type
	ps.step = "detect_fs"
	fsType := input.FsType
	if fsType == "" {
		fsType = detectFSType(rootPart)
	}

	// Mount
	ps.step = "mount"
	mountPoint, err := os.MkdirTemp("", "mvm-detect-os-")
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mkdtemp: %v", err), Step: ps.step}
	}
	defer os.RemoveAll(mountPoint)

	mountArgs := []string{rootPart, mountPoint}
	if fsType == "btrfs" {
		mountArgs = append([]string{"-t", "btrfs"}, mountArgs...)
	}
	cmd = exec.Command("mount", mountArgs...)
	if err := cmd.Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mount: %v", err), Step: ps.step}
	}
	defer exec.Command("umount", mountPoint).Run()

	// Read /etc/os-release
	osReleasePath := filepath.Join(mountPoint, "etc", "os-release")
	data, err := os.ReadFile(osReleasePath)
	if err != nil {
		return Result{
			Status:       "ok",
			OSType:       "linux",
			Error:        "",
			Step:         "",
			FilesWritten: 0,
			CommandsRun:  0,
			NewFSType:    "",
			NewSizeBytes: 0,
		}
	}

	osType := "linux"
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			osType = strings.Trim(strings.TrimPrefix(line, "ID="), "\"' \t\r\n")
			break
		}
	}

	return Result{Status: "ok", OSType: osType}
}

// doConvertFS handles the "convert_fs" action — convert filesystem to ext4.
func (p *Provisioner) doConvertFS(ctx context.Context, input Op) Result {
	ps := &provisionState{input: input, step: "parse"}

	targetFS := input.TargetFS
	if targetFS == "" {
		targetFS = "ext4"
	}
	if targetFS != "ext4" {
		return Result{
			Status: "error",
			Error:  fmt.Sprintf("Unsupported target filesystem: %q. Only 'ext4' is supported.", targetFS),
			Step:   "parse",
		}
	}

	ps.step = "loop"

	// Setup loop device
	losetupArgs := []string{"-f", "-P", "--show", "--direct-io=on", input.Image}
	cmd := exec.Command("losetup", losetupArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("losetup: %v", err), Step: ps.step}
	}
	loopDev := strings.TrimSpace(string(output))
	defer exec.Command("losetup", "-d", loopDev).Run()

	// Find root partition
	ps.step = "partition"
	rootPart := findRootPartition(loopDev)

	// Detect filesystem type
	ps.step = "detect_fs"
	fsType := input.FsType
	if fsType == "" {
		fsType = detectFSType(rootPart)
	}

	// Mount
	ps.step = "mount"
	mountPoint, err := os.MkdirTemp("", "mvm-convert-fs-")
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mkdtemp: %v", err), Step: ps.step}
	}

	mountArgs := []string{rootPart, mountPoint}
	if fsType == "btrfs" {
		mountArgs = append([]string{"-t", "btrfs"}, mountArgs...)
	}
	cmd = exec.Command("mount", mountArgs...)
	if err := cmd.Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mount: %v", err), Step: ps.step}
	}

	// Get actual data size
	ps.step = "du"
	duCmd := exec.Command("du", "-sb", mountPoint)
	duOutput, err := duCmd.CombinedOutput()
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("du failed: %v", err), Step: ps.step}
	}
	fields := strings.Fields(string(duOutput))
	var dataBytes int64
	if len(fields) > 0 {
		dataBytes, _ = strconv.ParseInt(fields[0], 10, 64)
	}

	// Calculate size: data + 150 MiB headroom, round up to MiB
	const headroom = 150 * 1024 * 1024
	const mebi = 1024 * 1024
	sizeBytes := dataBytes + headroom
	sizeBytes = ((sizeBytes + mebi - 1) / mebi) * mebi
	sizeMiB := sizeBytes / mebi

	outputPath := input.Image + ".ext4"

	// Create sparse output file
	ps.step = "truncate"
	truncateCmd := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMiB), outputPath)
	if err := truncateCmd.Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("truncate: %v", err), Step: ps.step}
	}

	// Create ext4 filesystem populated from mount point
	ps.step = "mkfs"
	mkfsCmd := exec.Command("mkfs.ext4", "-d", mountPoint, "-L", "rootfs", "-F", outputPath)
	if err := mkfsCmd.Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mkfs.ext4: %v", err), Step: ps.step}
	}

	// Cleanup (unmount + detach) before replacing file
	exec.Command("umount", mountPoint).Run()
	os.RemoveAll(mountPoint)
	exec.Command("losetup", "-d", loopDev).Run()

	// Replace original with new ext4 file
	ps.step = "replace"
	if err := os.Remove(input.Image); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("remove original: %v", err), Step: ps.step}
	}
	if err := os.Rename(outputPath, input.Image); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("rename: %v", err), Step: ps.step}
	}

	return Result{
		Status:       "ok",
		NewFSType:    targetFS,
		NewSizeBytes: sizeBytes,
	}
}

// =========================================================================
// Constants
// =========================================================================

// Default shells for chroot — matches Python's _DEFAULT_SHELLS.
var defaultShells = []string{
	"/bin/sh",
	"/bin/bash",
	"/bin/dash",
	"/bin/ash",
	"/usr/bin/sh",
	"/usr/bin/bash",
	"/bin/busybox",
	"/usr/bin/busybox",
}

// Linux filesystem types — matches Python's _LINUX_FS_TYPES.
var linuxFSTypes = map[string]bool{
	"ext2":  true,
	"ext3":  true,
	"ext4":  true,
	"btrfs": true,
}

// Debug log file path — matches Python's _DEBUG_LOG_PATH.
const provisionDebugLogPath = "/tmp/mvm-provision-debug.log"

// BATCH_SIZE — matches Python's Provisioner.BATCH_SIZE.
const chrootBatchSize = 10

// =========================================================================
// provisionState — debug logging for provisioning operations
// =========================================================================

type provisionState struct {
	input   Op
	step    string
	loopDev string
}

func (ps *provisionState) debugLog(msg string) {
	if !ps.input.Debug {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	pid := os.Getpid()
	line := fmt.Sprintf("[%s] [PID=%d] [step=%s] %s\n", ts, pid, ps.step, msg)
	f, err := os.OpenFile(provisionDebugLogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err == nil {
		f.WriteString(line)
		f.Close()
	}
}

// findRootPartition scans partitions of loopDev for a Linux filesystem,
// matching Python's _find_root_partition() logic:
//
//  1. No partitions → raw loop device
//  2. Scan all partitions for Linux filesystems, collect with sizes
//  3. Try p1, then p2 first
//  4. Otherwise pick largest
func findRootPartition(loopDev string) string {
	// List partitions
	var partitions []string
	for i := 1; i <= 16; i++ {
		partDev := fmt.Sprintf("%sp%d", loopDev, i)
		if _, err := os.Stat(partDev); err == nil {
			partitions = append(partitions, partDev)
		}
	}

	// No partitions — raw filesystem image
	if len(partitions) == 0 {
		return loopDev
	}

	// Collect Linux filesystem partitions with size
	type partSize struct {
		dev  string
		size int64
	}
	var linuxParts []partSize
	for _, p := range partitions {
		fsType := detectFSType(p)
		if linuxFSTypes[fsType] {
			size := getDeviceSize(p)
			linuxParts = append(linuxParts, partSize{p, size})
		}
	}

	// No Linux filesystem — fall back to p1
	if len(linuxParts) == 0 {
		return partitions[0]
	}

	// Try p1, then p2 in order
	linuxDevSet := make(map[string]bool)
	for _, lp := range linuxParts {
		linuxDevSet[lp.dev] = true
	}
	for _, candidate := range partitions {
		if len(partitions) >= 2 && candidate != partitions[0] && candidate != partitions[1] {
			break
		}
		if linuxDevSet[candidate] {
			return candidate
		}
	}

	// Multiple candidates — pick largest
	best := linuxParts[0]
	for _, lp := range linuxParts[1:] {
		if lp.size > best.size {
			best = lp
		}
	}
	return best.dev
}

// detectFSType returns the filesystem type of dev via blkid.
// Falls back to "ext4" on error.
func detectFSType(dev string) string {
	cmd := exec.Command("blkid", "-o", "value", "-s", "TYPE", dev)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "ext4"
	}
	fsType := strings.TrimSpace(string(output))
	if fsType == "" {
		return "ext4"
	}
	return fsType
}

// getDeviceSize returns the size of a block device in bytes via blockdev.
func getDeviceSize(dev string) int64 {
	cmd := exec.Command("blockdev", "--getsize64", dev)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0
	}
	return size
}

// getFSByteSize returns the ext4 filesystem size in bytes via tune2fs.
func getFSByteSize(dev string) int64 {
	cmd := exec.Command("tune2fs", "-l", dev)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}
	var blockCount, blockSize int64
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Block count:") {
			blockCount, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Block count:")), 10, 64)
		} else if strings.HasPrefix(line, "Block size:") {
			blockSize, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Block size:")), 10, 64)
		}
	}
	return blockCount * blockSize
}

// writeFile writes a file inside the mount point, matching Python's _write_file().
// Default mode is 0644 when not specified (matching Python's file_op.get("mode", 0o644)).
func writeFile(mountPoint string, f FileOp, ps *provisionState) error {
	fullPath := filepath.Join(mountPoint, strings.TrimLeft(f.Path, "/"))
	ps.debugLog(fmt.Sprintf("write: path=%s full=%s", f.Path, fullPath))

	// Remove existing path if it exists (handles symlinks, sockets, FIFOs, hardlinks)
	if _, err := os.Lstat(fullPath); err == nil {
		ps.debugLog(fmt.Sprintf("write: removing existing at %s", f.Path))
		if err := os.Remove(fullPath); err != nil {
			ps.debugLog(fmt.Sprintf("write: failed to remove %s: %v", f.Path, err))
			return fmt.Errorf("Cannot remove existing path %s: %v", f.Path, err)
		}
	}

	// Create parent directories
	parent := filepath.Dir(fullPath)
	if err := os.MkdirAll(parent, infra.DirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %v", parent, err)
	}

	// Data is raw bytes (already decoded from base64 in cmd/mvm bridge if coming from JSON)
	// If Data is nil and Path is set, could be a zero-length write; proceed.
	data := f.Data
	ps.debugLog(fmt.Sprintf("write: writing %d bytes to %s", len(data), f.Path))

	// Resolve mode: default 0644 when not specified (matching Python's file_op.get("mode", 0o644))
	mode := f.Mode
	if mode == 0 {
		mode = 0644
	}

	// Write file
	if err := os.WriteFile(fullPath, data, os.FileMode(mode)); err != nil {
		return fmt.Errorf("write %s: %v", f.Path, err)
	}

	// Set permissions (best effort — root in container may lack CAP_CHOWN)
	// Matching Python's try/except OSError: pass with debug logging
	if err := os.Chmod(fullPath, os.FileMode(mode)); err != nil {
		ps.debugLog(fmt.Sprintf("write: chmod failed for %s: %v", f.Path, err))
	}
	if f.UID != 0 || f.GID != 0 {
		if err := os.Chown(fullPath, f.UID, f.GID); err != nil {
			ps.debugLog(fmt.Sprintf("write: chown failed for %s: %v", f.Path, err))
		}
	}

	return nil
}

// copyDirectory copies a directory tree into the mount point, matching Python's _copy_directory().
// Uses 64KB chunks (matching Python's sf.read(65536)) instead of io.Copy.
// Returns the number of files copied (Python returns int, not counting directories).
func copyDirectory(mountPoint string, c CopyDirOp, ps *provisionState) (int, error) {
	mode := c.Mode
	if mode == 0 {
		mode = 0755
	}

	count := 0
	err := filepath.Walk(c.Src, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(c.Src, srcPath)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(mountPoint, strings.TrimLeft(c.Dst, "/"), relPath)

		if info.IsDir() {
			// Python's _copy_directory() only processes files, not directories.
			// Parent directories are created implicitly via os.makedirs before each file.
			// We still create them here to match behavior, but don't count them.
			return os.MkdirAll(dstPath, os.FileMode(mode))
		}

		// Create parent directories for file
		if parent := filepath.Dir(dstPath); parent != "" {
			os.MkdirAll(parent, infra.DirPerm)
		}

		// Copy file using 64KB chunks matching Python's sf.read(65536)
		srcFile, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		buf := make([]byte, 65536)
		for {
			n, readErr := srcFile.Read(buf)
			if n > 0 {
				if _, writeErr := dstFile.Write(buf[:n]); writeErr != nil {
					return writeErr
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}

		// Set mode — matching Python's try/except OSError: pass
		os.Chmod(dstPath, os.FileMode(mode))
		count++
		return nil
	})

	return count, err
}

// runChrootCommands runs a command inside the chroot, trying each available shell.
// Matches Python's _flush_chroot_buffer() logic.
func runChrootCommands(ctx context.Context, mountPoint, command, customShell string) error {
	// Ensure /dev/null exists in the chroot — matches Python's os.mknod(null_path, 0o666, os.makedev(1, 3)).
	nullPath := filepath.Join(mountPoint, "dev", "null")
	if _, err := os.Stat(nullPath); os.IsNotExist(err) {
		devDir := filepath.Dir(nullPath)
		os.MkdirAll(devDir, infra.DirPerm)
		// makedev(1, 3) on Linux = (1 << 8) | 3 = 259 for /dev/null (major=1, minor=3)
		syscall.Mknod(nullPath, syscall.S_IFCHR|0666, 259)
	}

	shells := defaultShells
	if customShell != "" {
		shells = []string{customShell}
	}

	// Copy entire host environment and override PATH — matching Python's os.environ.copy()
	// then env["PATH"] = "...". Go's exec.Cmd.Env is a []string; duplicate keys have
	// system-dependent behavior (last one wins on Linux), so we filter out existing PATH.
	env := os.Environ()
	var cleanedEnv []string
	for _, e := range env {
		if !strings.HasPrefix(e, "PATH=") {
			cleanedEnv = append(cleanedEnv, e)
		}
	}
	cleanedEnv = append(cleanedEnv, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	env = cleanedEnv

	var lastError string
	for _, shell := range shells {
		shellInChroot := filepath.Join(mountPoint, strings.TrimLeft(shell, "/"))
		if _, err := os.Stat(shellInChroot); os.IsNotExist(err) {
			continue
		}

		shellBase := filepath.Base(shell)

		// Matching Python's proc.communicate(timeout=60) — wrap in context with timeout.
		chrootCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		var cmd *exec.Cmd
		if shellBase == "busybox" {
			cmd = exec.CommandContext(chrootCtx, "chroot", mountPoint, shell, "sh", "-c", command)
		} else {
			cmd = exec.CommandContext(chrootCtx, "chroot", mountPoint, shell, "-c", command)
		}
		cmd.Env = env

		// Python captures stdout and stderr separately via subprocess.PIPE — both captured
		var stdoutBuf, stderrBuf strings.Builder
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf

		err := cmd.Run()
		if chrootCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("chroot command timed out: %.100s", command)
		}
		if err == nil {
			return nil
		}
		// Python: last_error = stderr.decode("utf-8", errors="replace")
		stderrText := stderrBuf.String()
		if len(stderrText) > 500 {
			stderrText = stderrText[:500]
		}
		lastError = stderrText
	}

	// Python: raise RuntimeError(f"chroot failed (no working shell found): {last_error[:500]}")
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	return fmt.Errorf("chroot failed (no working shell found): %s", lastError)
}

// shrinkBtrfs shrinks a btrfs filesystem.
// Returns the new device size and any error.
// Matching Python's _shrink_btrfs() — raises error on failure or when targetBytes is unresolvable.
func shrinkBtrfs(mountPoint, rootPart string, targetBytes int64) (int64, error) {
	// fstrim before shrink — matching Python (no error check)
	exec.Command("fstrim", mountPoint).Run()

	if targetBytes == 0 {
		// Calculate minimum size
		targetBytes = calcBtrfsMinSize(mountPoint, rootPart)
	}
	if targetBytes == 0 {
		return 0, fmt.Errorf("cannot determine btrfs shrink target size")
	}

	cmd := exec.Command("btrfs", "filesystem", "resize", strconv.FormatInt(targetBytes, 10), mountPoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf(
			"btrfs filesystem resize to %d failed (exit %v): %s",
			targetBytes,
			err,
			strings.TrimSpace(string(output)),
		)
	}

	return getBtrfsDeviceSize(mountPoint), nil
}

func calcBtrfsMinSize(mountPoint, rootPart string) int64 {
	// Get current device size as upper bound
	currentSize := getDeviceSize(rootPart)
	if currentSize == 0 {
		return 0
	}

	cmd := exec.Command("btrfs", "filesystem", "usage", "-b", mountPoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return currentSize
	}

	// Parse "Used:" line using regex matching Python's r"[\d.]+"
	usedRe := regexp.MustCompile(`[\d.]+`)
	var usedBytes int64
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Used:") {
			match := usedRe.FindString(line)
			if match != "" {
				if f, err := strconv.ParseFloat(match, 64); err == nil {
					usedBytes = int64(f)
				}
			}
			break
		}
	}

	if usedBytes == 0 {
		return currentSize
	}

	// headroom = min(used, 2GiB) + 1GiB
	twoGiB := int64(2 * 1024 * 1024 * 1024)
	oneGiB := int64(1024 * 1024 * 1024)
	headroom := usedBytes
	if headroom > twoGiB {
		headroom = twoGiB
	}
	headroom += oneGiB

	target := usedBytes + headroom
	if target > currentSize {
		// Shrink by at most 256 MiB
		target = currentSize - (256 * 1024 * 1024)
		alternative := usedBytes + (512 * 1024 * 1024)
		if target < alternative {
			target = alternative
		}
	}
	return target
}

func getBtrfsDeviceSize(mountPoint string) int64 {
	cmd := exec.Command("btrfs", "filesystem", "show", mountPoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}
	// Parse line like:   devid    1 size 1.75GiB used 1.32GiB path /dev/loop0
	// Using regex matching Python's r"size\s+([\d.]+)([kKmMgGtTbB])"
	sizeRe := regexp.MustCompile(`size\s+([\d.]+)\s*([kKmMgGtTbB])`)
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "devid") && strings.Contains(line, "size") {
			match := sizeRe.FindStringSubmatch(line)
			if len(match) >= 3 {
				val, err := strconv.ParseFloat(match[1], 64)
				if err != nil {
					continue
				}
				unit := strings.ToLower(match[2])
				switch unit {
				case "k":
					return int64(val * 1024)
				case "m":
					return int64(val * 1024 * 1024)
				case "g":
					return int64(val * 1024 * 1024 * 1024)
				case "t":
					return int64(val * 1024 * 1024 * 1024 * 1024)
				case "b":
					return int64(val)
				}
			}
		}
	}
	return 0
}

// CleanupMount unmounts a mount point, kills orphan processes keeping it busy,
// and removes the directory. Returns true if both umount and rmdir succeeded.
// Matches Python's Provisioner._cleanup_mount() exactly, and the identical
// cleanupMount function that was in cmd/mvm/provision.go.
func CleanupMount(mountPoint string) bool {
	resolvedMount, err := filepath.EvalSymlinks(mountPoint)
	if err != nil {
		resolvedMount = mountPoint
	}

	// Fast path: try umount directly (succeeds ~99% of the time)
	umount := exec.Command("umount", mountPoint)
	if output, err := umount.CombinedOutput(); err == nil {
		_ = output
		// Success — remove empty directory (matches Python's os.rmdir() with try/except OSError: pass)
		os.Remove(mountPoint)
		return true
	}

	// Slow path: umount failed — find and kill orphaned processes
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if _, parseErr := strconv.Atoi(name); parseErr != nil {
				continue
			}
			rootLink := filepath.Join("/proc", name, "root")
			// Check if it's a symlink first — matching Python's os.path.islink()
			if linkInfo, lstatErr := os.Lstat(rootLink); lstatErr != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, linkErr := os.Readlink(rootLink)
			if linkErr != nil {
				continue
			}
			if target == resolvedMount {
				pid, _ := strconv.Atoi(name)
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}

	// Retry umount after killing orphaned processes
	umount = exec.Command("umount", mountPoint)
	out, umountErr := umount.CombinedOutput()
	_ = out

	// Python: unmount_ok = result.returncode == 0
	unmountOk := umountErr == nil
	// Python: try os.rmdir(mount_point); except OSError: rmdir_ok = False
	rmdirErr := os.Remove(mountPoint)
	_ = rmdirErr
	// Python: return unmount_ok and rmdir_ok
	return unmountOk && rmdirErr == nil
}
