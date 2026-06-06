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
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/infra"
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
	ps := &provisionState{step: "parse"}

	// State variables
	var mountPoint string
	var rootPart string
	var detectedFSType string
	var resizeNewBytes int64
	filesWritten := 0
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

	// Cleanup function — deferred to always run
	// Uses background context with timeouts so cleanup still runs even if ctx is cancelled.
	defer func() {
		ps.debugLog(input.Debug, fmt.Sprintf("cleanup: mount_point=%q loop_dev=%q", mountPoint, ps.loopDev))
		if mountPoint != "" {
			CleanupMount(mountPoint)
			mountPoint = ""
		}
		detachLoopDevice(ctx, ps.loopDev)
		ps.loopDev = ""
	}()

	// ── Pre-loop resize: grow (truncate file before mounting) ──
	if input.Resize != nil && input.Resize.Action == "grow" && input.Resize.Bytes > 0 {
		ps.step = "resize"
		ps.debugLog(input.Debug, fmt.Sprintf("truncating image to %d bytes", input.Resize.Bytes))
		if err := truncateImage(input.Image, input.Resize.Bytes); err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
	}

	// ── Debug: log system info ──
	if input.Debug {
		ps.debugLog(input.Debug, fmt.Sprintf("uid=%d gid=%d euid=%d", os.Getuid(), os.Getgid(), os.Geteuid()))
		ps.debugLog(input.Debug, fmt.Sprintf("image=%s fs_type_hint=%s", input.Image, input.FsType))
		statusData, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", os.Getpid()))
		if err == nil {
			for line := range strings.SplitSeq(string(statusData), "\n") {
				if strings.HasPrefix(line, "Cap") {
					ps.debugLog(input.Debug, fmt.Sprintf("cap: %s", strings.TrimSpace(line)))
				}
			}
		} else {
			ps.debugLog(input.Debug, fmt.Sprintf("cannot read /proc/self/status: %v", err))
		}
	}

	// ── Loop device setup ──
	ps.step = "loop"
	ps.debugLog(input.Debug, "setting up loop device")
	loopDev, err := setupLoopDevice(ctx, input.Image)
	if err != nil {
		return Result{Status: "error", Error: err.Error(), Step: ps.step}
	}
	ps.loopDev = loopDev
	ps.debugLog(input.Debug, fmt.Sprintf("loop device: %s", ps.loopDev))

	// ── Root partition detection ──
	ps.step = "partition"
	ps.debugLog(input.Debug, "finding root partition")
	rootPart = findRootPartition(ctx, ps.loopDev)
	ps.debugLog(input.Debug, fmt.Sprintf("root partition: %s", rootPart))

	// ── Filesystem type detection ──
	ps.step = "detect_fs"
	if input.FsType != "" {
		detectedFSType = input.FsType
	} else {
		detectedFSType = detectFSType(ctx, rootPart)
	}
	ps.debugLog(input.Debug, fmt.Sprintf("fs type: %s", detectedFSType))

	// ── Mount ──
	ps.step = "mount"
	mountPoint, err = mountImage(ctx, rootPart, detectedFSType)
	if err != nil {
		return Result{Status: "error", Error: err.Error(), Step: ps.step}
	}

	// ── Btrfs grow (after truncation, before chroot work — must be done while mounted) ──
	if input.Resize != nil && input.Resize.Action == "grow" && detectedFSType == "btrfs" {
		ps.step = "resize"
		ps.debugLog(input.Debug, "growing btrfs filesystem to fill device")
		if err := growBtrfs(ctx, mountPoint); err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
	}

	// ── Write files ──
	for _, f := range input.Files {
		ps.step = "write"
		if err := writeFile(mountPoint, f, input.Debug, ps); err != nil {
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
		count, err := copyDirectory(mountPoint, c)
		if err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
		filesWritten += count
	}

	// ── Chroot commands (buffered, then flushed in batches) ──
	commandsRun := 0
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
		commandsRun++
	}

	// ── Flush any remaining buffered chroot commands ──
	if err := flushChroot(); err != nil {
		return Result{Status: "error", Error: err.Error(), Step: ps.step}
	}

	// ── Post-mount resize: shrink ──
	if input.Resize != nil && input.Resize.Action == "shrink" {
		ps.step = "resize"
		if detectedFSType == "btrfs" {
			var shrinkErr error
			resizeNewBytes, shrinkErr = shrinkBtrfs(ctx, mountPoint, rootPart, input.Resize.Bytes)
			if shrinkErr != nil {
				return Result{Status: "error", Error: shrinkErr.Error(), Step: ps.step}
			}
		} else {
			var shrinkErr error
			resizeNewBytes, shrinkErr = shrinkExt4(ctx, &mountPoint, rootPart, input.Resize.Headroom)
			if shrinkErr != nil {
				return Result{Status: "error", Error: shrinkErr.Error(), Step: ps.step}
			}
		}
	}

	// ── Post-mount resize: ext4 grow (btrfs already handled above) ──
	if input.Resize != nil && input.Resize.Action == "grow" && detectedFSType != "btrfs" {
		ps.step = "resize"
		if err := growExt4(ctx, &mountPoint, rootPart); err != nil {
			return Result{Status: "error", Error: err.Error(), Step: ps.step}
		}
	}

	// ── Post-detach truncation for shrink ──
	if resizeNewBytes > 0 {
		if err := truncateImage(input.Image, resizeNewBytes); err != nil {
			slog.Warn("failed to truncate image after shrink", "path", input.Image, "error", err)
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
	ps := &provisionState{step: "loop"}

	// Setup loop device
	losetupArgs := []string{"-f", "-P", "--show", "--direct-io=on", input.Image}
	output, err := exec.CommandContext(ctx, "losetup", losetupArgs...).CombinedOutput()
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("losetup: %w", err), Step: ps.step}
	}
	loopDev := strings.TrimSpace(string(output))

	// Cleanup on return
	defer func() {
		if err := exec.CommandContext(context.Background(), "losetup", "-d", loopDev).Run(); err != nil {
			slog.Warn("failed to detach loop device", "device", loopDev, "error", err)
		}
	}()

	// Find root partition
	ps.step = "partition"
	rootPart := findRootPartition(ctx, loopDev)

	// Detect filesystem type
	ps.step = "detect_fs"
	fsType := input.FsType
	if fsType == "" {
		fsType = detectFSType(ctx, rootPart)
	}

	// Mount
	ps.step = "mount"
	mountPoint, err := os.MkdirTemp("", "mvm-detect-os-")
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mkdtemp: %w", err), Step: ps.step}
	}
	defer func() {
		if err := os.RemoveAll(mountPoint); err != nil {
			slog.Warn("failed to remove mount point", "path", mountPoint, "error", err)
		}
	}()

	mountArgs := []string{rootPart, mountPoint}
	if fsType == "btrfs" {
		mountArgs = append([]string{"-t", "btrfs"}, mountArgs...)
	}
	if err := exec.CommandContext(ctx, "mount", mountArgs...).Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mount: %w", err), Step: ps.step}
	}
	defer func() {
		if err := exec.CommandContext(context.Background(), "umount", mountPoint).Run(); err != nil {
			slog.Warn("failed to unmount", "path", mountPoint, "error", err)
		}
	}()

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
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "ID="); ok {
			osType = strings.Trim(after, "\"' \t\r\n")
			break
		}
	}

	return Result{Status: "ok", OSType: osType}
}

// doConvertFS handles the "convert_fs" action — convert filesystem to ext4.
func (p *Provisioner) doConvertFS(ctx context.Context, input Op) Result {
	ps := &provisionState{step: "parse"}

	targetFS := input.TargetFS
	if targetFS == "" {
		targetFS = "ext4"
	}
	if targetFS != "ext4" {
		return Result{
			Status: "error",
			Error:  fmt.Sprintf("unsupported target filesystem: %q; only 'ext4' is supported", targetFS),
			Step:   "parse",
		}
	}

	ps.step = "loop"

	// Setup loop device
	losetupArgs := []string{"-f", "-P", "--show", "--direct-io=on", input.Image}
	output, err := exec.CommandContext(ctx, "losetup", losetupArgs...).CombinedOutput()
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("losetup: %w", err), Step: ps.step}
	}
	loopDev := strings.TrimSpace(string(output))
	defer func() {
		if err := exec.CommandContext(context.Background(), "losetup", "-d", loopDev).Run(); err != nil {
			slog.Warn("failed to detach loop device", "device", loopDev, "error", err)
		}
	}()

	// Find root partition
	ps.step = "partition"
	rootPart := findRootPartition(ctx, loopDev)

	// Detect filesystem type
	ps.step = "detect_fs"
	fsType := input.FsType
	if fsType == "" {
		fsType = detectFSType(ctx, rootPart)
	}

	// Mount
	ps.step = "mount"
	mountPoint, err := os.MkdirTemp("", "mvm-convert-fs-")
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mkdtemp: %w", err), Step: ps.step}
	}

	mountArgs := []string{rootPart, mountPoint}
	if fsType == "btrfs" {
		mountArgs = append([]string{"-t", "btrfs"}, mountArgs...)
	}
	if err := exec.CommandContext(ctx, "mount", mountArgs...).Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mount: %w", err), Step: ps.step}
	}

	// Get actual data size
	ps.step = "du"
	duOutput, err := exec.CommandContext(ctx, "du", "-sb", mountPoint).CombinedOutput()
	if err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("du failed: %w", err), Step: ps.step}
	}
	fields := strings.Fields(string(duOutput))
	var dataBytes int64
	if len(fields) > 0 {
		dataBytes, _ = strconv.ParseInt(fields[0], 10, 64)
	}

	// Calculate size: data + 150 MiB headroom, round up to MiB
	const headroomBytes = 150 * 1024 * 1024
	const mebi = 1024 * 1024
	sizeBytes := dataBytes + headroomBytes
	sizeBytes = ((sizeBytes + mebi - 1) / mebi) * mebi
	sizeMiB := sizeBytes / mebi

	outputPath := input.Image + ".ext4"

	// Create sparse output file
	ps.step = "truncate"
	if err := exec.CommandContext(ctx, "truncate", "-s", fmt.Sprintf("%dM", sizeMiB), outputPath).Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("truncate: %w", err), Step: ps.step}
	}

	// Create ext4 filesystem populated from mount point
	ps.step = "mkfs"
	if err := exec.CommandContext(ctx, "mkfs.ext4", "-d", mountPoint, "-L", "rootfs", "-F", outputPath).Run(); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("mkfs.ext4: %w", err), Step: ps.step}
	}

	// Cleanup (unmount + detach) before replacing file
	if err := exec.CommandContext(context.Background(), "umount", mountPoint).Run(); err != nil {
		slog.Warn("failed to unmount during convert_fs cleanup", "path", mountPoint, "error", err)
	}
	if err := os.RemoveAll(mountPoint); err != nil {
		slog.Warn("failed to remove mount point during convert_fs cleanup", "path", mountPoint, "error", err)
	}
	if err := exec.CommandContext(context.Background(), "losetup", "-d", loopDev).Run(); err != nil {
		slog.Warn("failed to detach loop device during convert_fs cleanup", "device", loopDev, "error", err)
	}

	// Replace original with new ext4 file
	ps.step = "replace"
	if err := os.Remove(input.Image); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("remove original: %w", err), Step: ps.step}
	}
	if err := os.Rename(outputPath, input.Image); err != nil {
		return Result{Status: "error", Error: fmt.Sprintf("rename: %w", err), Step: ps.step}
	}

	return Result{
		Status:       "ok",
		NewFSType:    targetFS,
		NewSizeBytes: sizeBytes,
	}
}

// ── Helper functions ────────────────────────────────────────────────────

// truncateImage truncates (or extends) the image file to the specified size.
func truncateImage(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return fmt.Errorf("truncate: %w", err)
	}
	if err := f.Close(); err != nil {
		slog.Warn("failed to close file after truncation", "path", path, "error", err)
	}
	return nil
}

// setupLoopDevice sets up a loop device with partition scanning and returns the device path.
func setupLoopDevice(ctx context.Context, image string) (string, error) {
	losetupArgs := []string{"-f", "-P", "--show", "--direct-io=on", image}
	output, err := exec.CommandContext(ctx, "losetup", losetupArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// mountImage creates a temp mount point and mounts the root partition.
func mountImage(ctx context.Context, rootPart, fsType string) (string, error) {
	mountPoint, err := os.MkdirTemp("", infra.MVMProvisionPrefix)
	if err != nil {
		return "", fmt.Errorf("mkdtemp: %w", err)
	}

	mountArgs := []string{rootPart, mountPoint}
	if fsType == "btrfs" {
		mountArgs = append([]string{"-t", "btrfs"}, mountArgs...)
	}
	if output, err := exec.CommandContext(ctx, "mount", mountArgs...).CombinedOutput(); err != nil {
		// Clean up the temp dir on mount failure
		os.Remove(mountPoint)
		return "", fmt.Errorf("mount: %w: %s", err, string(output))
	}
	return mountPoint, nil
}

// growBtrfs grows a btrfs filesystem to fill available device space.
// Must be called while the filesystem is mounted.
func growBtrfs(ctx context.Context, mountPoint string) error {
	if err := exec.CommandContext(ctx, "btrfs", "filesystem", "resize", "max", mountPoint).Run(); err != nil {
		return fmt.Errorf("btrfs resize max: %w", err)
	}
	return nil
}

// growExt4 grows an ext4 filesystem.
// Unmounts first (ext4 online grow is supported, but the code historically unmounts,
// so we preserve that behavior).
// Takes *string so it can clear mountPoint after successful umount, preventing
// deferred cleanup from retrying an unmount on intermediate failure.
func growExt4(ctx context.Context, mountPoint *string, rootPart string) error {
	if _, err := exec.CommandContext(ctx, "umount", *mountPoint).CombinedOutput(); err != nil {
		return fmt.Errorf("umount failed: %w", err)
	}
	*mountPoint = "" // prevent deferred cleanup from retrying umount

	if _, err := exec.CommandContext(ctx, "e2fsck", "-f", "-y", rootPart).CombinedOutput(); err != nil {
		return fmt.Errorf("e2fsck: %w", err)
	}

	var resizeOutBuf strings.Builder
	cmd := exec.CommandContext(ctx, "resize2fs", rootPart)
	cmd.Stdout = &resizeOutBuf
	cmd.Stderr = &resizeOutBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("resize2fs: %w: %s", err, strings.TrimSpace(resizeOutBuf.String()))
	}
	return nil
}

// shrinkExt4 shrinks an ext4 filesystem to minimum size, optionally adding headroom.
// Unmounts first (shrink requires offline fs).
// Takes *string so it can clear mountPoint after successful umount, preventing
// deferred cleanup from retrying an unmount on intermediate failure.
// Returns the new filesystem size in bytes.
func shrinkExt4(ctx context.Context, mountPoint *string, rootPart string, headroom int) (int64, error) {
	if _, err := exec.CommandContext(ctx, "umount", *mountPoint).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("umount failed: %w", err)
	}
	*mountPoint = "" // prevent deferred cleanup from retrying umount

	if _, err := exec.CommandContext(ctx, "e2fsck", "-f", "-y", rootPart).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("e2fsck: %w", err)
	}

	if _, err := exec.CommandContext(ctx, "resize2fs", "-M", rootPart).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("resize2fs -M: %w", err)
	}

	if headroom > 0 {
		curSize := getFSByteSize(ctx, rootPart)
		targetSize := curSize + int64(headroom)
		if _, err := exec.CommandContext(ctx, "resize2fs", rootPart, strconv.FormatInt(targetSize, 10)).CombinedOutput(); err != nil {
			return 0, fmt.Errorf("resize2fs headroom: %w", err)
		}
	}

	return getFSByteSize(ctx, rootPart), nil
}

// detachLoopDevice detaches a loop device. Logs on error.
func detachLoopDevice(ctx context.Context, loopDev string) {
	if loopDev == "" {
		return
	}
	detachCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(detachCtx, "losetup", "-d", loopDev).Run(); err != nil {
		slog.Warn("failed to detach loop device during cleanup",
			"device", loopDev, "error", err)
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
	step    string
	loopDev string
}

func (ps *provisionState) debugLog(debug bool, msg string) {
	if !debug {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	pid := os.Getpid()
	line := fmt.Sprintf("[%s] [PID=%d] [step=%s] %s\n", ts, pid, ps.step, msg)
	f, err := os.OpenFile(provisionDebugLogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		slog.Debug("failed to open debug log", "error", err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Debug("failed to close debug log", "error", err)
		}
	}()
	if _, err := f.WriteString(line); err != nil {
		slog.Debug("failed to write debug log", "error", err)
	}
}

// findRootPartition scans partitions of loopDev for a Linux filesystem,
// matching Python's _find_root_partition() logic:
//
//  1. No partitions → raw loop device
//  2. Scan all partitions for Linux filesystems, collect with sizes
//  3. Try p1, then p2 first
//  4. Otherwise pick largest
func findRootPartition(ctx context.Context, loopDev string) string {
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
		fsType := detectFSType(ctx, p)
		if linuxFSTypes[fsType] {
			size := getDeviceSize(ctx, p)
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
func detectFSType(ctx context.Context, dev string) string {
	output, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "TYPE", dev).CombinedOutput()
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
func getDeviceSize(ctx context.Context, dev string) int64 {
	output, err := exec.CommandContext(ctx, "blockdev", "--getsize64", dev).CombinedOutput()
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
func getFSByteSize(ctx context.Context, dev string) int64 {
	output, err := exec.CommandContext(ctx, "tune2fs", "-l", dev).CombinedOutput()
	if err != nil {
		return 0
	}
	var blockCount, blockSize int64
	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "Block count:"); ok {
			blockCount, _ = strconv.ParseInt(strings.TrimSpace(after), 10, 64)
		} else if after, ok := strings.CutPrefix(line, "Block size:"); ok {
			blockSize, _ = strconv.ParseInt(strings.TrimSpace(after), 10, 64)
		}
	}
	return blockCount * blockSize
}

// writeFile writes a file inside the mount point, matching Python's _write_file().
// Default mode is 0644 when not specified (matching Python's file_op.get("mode", 0o644)).
func writeFile(mountPoint string, f FileOp, debug bool, ps *provisionState) error {
	fullPath := filepath.Join(mountPoint, strings.TrimLeft(f.Path, "/"))
	ps.debugLog(debug, fmt.Sprintf("write: path=%s full=%s", f.Path, fullPath))

	// Remove existing path if it exists (handles symlinks, sockets, FIFOs, hardlinks)
	if _, err := os.Lstat(fullPath); err == nil {
		ps.debugLog(debug, fmt.Sprintf("write: removing existing at %s", f.Path))
		if err := os.Remove(fullPath); err != nil {
			ps.debugLog(debug, fmt.Sprintf("write: failed to remove %s: %w", f.Path, err))
			return fmt.Errorf("cannot remove existing path %s: %w", f.Path, err)
		}
	}

	// Create parent directories
	parent := filepath.Dir(fullPath)
	if err := os.MkdirAll(parent, infra.DirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", parent, err)
	}

	// Data is raw bytes (already decoded from base64 in cmd/mvm bridge if coming from JSON)
	// If Data is nil and Path is set, could be a zero-length write; proceed.
	data := f.Data
	ps.debugLog(debug, fmt.Sprintf("write: writing %d bytes to %s", len(data), f.Path))

	// Resolve mode: default 0644 when not specified (matching Python's file_op.get("mode", 0o644))
	mode := f.Mode
	if mode == 0 {
		mode = 0644
	}

	// Write file
	if err := os.WriteFile(fullPath, data, os.FileMode(mode)); err != nil {
		return fmt.Errorf("write %s: %w", f.Path, err)
	}

	// Set permissions (best effort — root in container may lack CAP_CHOWN)
	// Matching Python's try/except OSError: pass with debug logging
	if err := os.Chmod(fullPath, os.FileMode(mode)); err != nil {
		ps.debugLog(debug, fmt.Sprintf("write: chmod failed for %s: %w", f.Path, err))
	}
	if f.UID != 0 || f.GID != 0 {
		if err := os.Chown(fullPath, f.UID, f.GID); err != nil {
			ps.debugLog(debug, fmt.Sprintf("write: chown failed for %s: %w", f.Path, err))
		}
	}

	return nil
}

// copyDirectory copies a directory tree into the mount point, matching Python's _copy_directory().
// Uses 64KB chunks (matching Python's sf.read(65536)) instead of io.Copy.
// Returns the number of files copied (Python returns int, not counting directories).
func copyDirectory(mountPoint string, c CopyDirOp) (int, error) {
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
			if err := os.MkdirAll(parent, infra.DirPerm); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", parent, err)
			}
		}

		// Copy file using 64KB chunks matching Python's sf.read(65536)
		srcFile, err := os.Open(srcPath)
		if err != nil {
			return err
		}

		dstFile, err := os.Create(dstPath)
		if err != nil {
			srcFile.Close()
			return err
		}

		buf := make([]byte, 65536)
		var copyErr error
		for {
			n, readErr := srcFile.Read(buf)
			if n > 0 {
				if _, writeErr := dstFile.Write(buf[:n]); writeErr != nil {
					copyErr = writeErr
					break
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				copyErr = readErr
				break
			}
		}

		srcFile.Close()
		dstFile.Close()
		if copyErr != nil {
			return copyErr
		}

		// Set mode — matching Python's try/except OSError: pass
		if err := os.Chmod(dstPath, os.FileMode(mode)); err != nil {
			slog.Debug("failed to set mode on copied file", "path", dstPath, "error", err)
		}
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
		if err := os.MkdirAll(devDir, infra.DirPerm); err != nil {
			return fmt.Errorf("mkdir /dev in chroot: %w", err)
		}
		// makedev(1, 3) on Linux = (1 << 8) | 3 = 259 for /dev/null (major=1, minor=3)
		if err := syscall.Mknod(nullPath, syscall.S_IFCHR|0666, 259); err != nil {
			return fmt.Errorf("mknod /dev/null in chroot: %w", err)
		}
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
func shrinkBtrfs(ctx context.Context, mountPoint, rootPart string, targetBytes int64) (int64, error) {
	// fstrim before shrink — matching Python (best-effort, log on error)
	if err := exec.CommandContext(ctx, "fstrim", mountPoint).Run(); err != nil {
		slog.Warn("fstrim before btrfs shrink failed", "mount", mountPoint, "error", err)
	}

	if targetBytes == 0 {
		// Calculate minimum size
		targetBytes = calcBtrfsMinSize(ctx, mountPoint, rootPart)
	}
	if targetBytes == 0 {
		return 0, fmt.Errorf("cannot determine btrfs shrink target size")
	}

	output, err := exec.CommandContext(ctx, "btrfs", "filesystem", "resize", strconv.FormatInt(targetBytes, 10), mountPoint).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf(
			"btrfs filesystem resize to %d failed (exit %v): %s",
			targetBytes,
			err,
			strings.TrimSpace(string(output)),
		)
	}

	return getBtrfsDeviceSize(ctx, mountPoint), nil
}

func calcBtrfsMinSize(ctx context.Context, mountPoint, rootPart string) int64 {
	// Get current device size as upper bound
	currentSize := getDeviceSize(ctx, rootPart)
	if currentSize == 0 {
		return 0
	}

	output, err := exec.CommandContext(ctx, "btrfs", "filesystem", "usage", "-b", mountPoint).CombinedOutput()
	if err != nil {
		return currentSize
	}

	// Parse "Used:" line using regex matching Python's r"[\d.]+"
	usedRe := regexp.MustCompile(`[\d.]+`)
	var usedBytes int64
	for line := range strings.SplitSeq(string(output), "\n") {
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
	headroom := min(usedBytes, int64(2*1024*1024*1024)) + int64(1024*1024*1024)

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

func getBtrfsDeviceSize(ctx context.Context, mountPoint string) int64 {
	output, err := exec.CommandContext(ctx, "btrfs", "filesystem", "show", mountPoint).CombinedOutput()
	if err != nil {
		return 0
	}
	// Parse line like:   devid    1 size 1.75GiB used 1.32GiB path /dev/loop0
	// Using regex matching Python's r"size\s+([\d.]+)([kKmMgGtTbB])"
	sizeRe := regexp.MustCompile(`size\s+([\d.]+)\s*([kKmMgGtTbB])`)
	for line := range strings.SplitSeq(string(output), "\n") {
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
	umount := exec.CommandContext(context.Background(), "umount", mountPoint)
	if output, err := umount.CombinedOutput(); err == nil {
		_ = output
		// Success — remove empty directory (matches Python's os.rmdir() with try/except OSError: pass)
		if err := os.Remove(mountPoint); err != nil {
			slog.Debug("failed to remove mount point after umount", "path", mountPoint, "error", err)
		}
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
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					slog.Debug("failed to kill orphan process", "pid", pid, "error", err)
				}
			}
		}
	}

	// Retry umount after killing orphaned processes
	umount = exec.CommandContext(context.Background(), "umount", mountPoint)
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
