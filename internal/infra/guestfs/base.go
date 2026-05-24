package guestfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// ── Errors ──────────────────────────────────────────────────────────────────

// GuestfsNotAvailableError indicates libguestfs/guestfish is not installed.
type GuestfsNotAvailableError struct {
	msg string
}

func (e *GuestfsNotAvailableError) Error() string { return e.msg }

// GuestfsError indicates a guestfs operation failure.
type GuestfsError struct {
	msg string
}

func (e *GuestfsError) Error() string { return e.msg }

// ── Constants ───────────────────────────────────────────────────────────────

const (
	guestfishBin  = "guestfish"
	guestmountBin = "guestmount"
	defaultMemsize = 256
)

// ── GuestfsHandle ───────────────────────────────────────────────────────────
//
// Mirrors src/mvmctl/core/_shared/_guestfs/_base.py OptimizedGuestfs.
// Uses guestfish CLI tool as a subprocess (Go has no native guestfs bindings).

// GuestfsHandle wraps guestfish CLI for disk image operations.
// Each method launches a separate guestfish subprocess, which is
// functionally equivalent but slower than the Python C-bindings approach.
type GuestfsHandle struct {
	diskPath string
	readonly bool
	origEnv  map[string]*string // nil means "was not set"
}

// NewHandle creates a new GuestfsHandle. Returns error if guestfish is not
// available in PATH.
func NewHandle(diskPath string, readonly bool) (*GuestfsHandle, error) {
	if _, err := exec.LookPath(guestfishBin); err != nil {
		return nil, &GuestfsNotAvailableError{
			msg: "libguestfs is not available",
		}
	}
	return &GuestfsHandle{
		diskPath: diskPath,
		readonly: readonly,
		origEnv:  make(map[string]*string),
	}, nil
}

// ── Environment helpers ─────────────────────────────────────────────────────

// SetupEnvironment sets LIBGUESTFS_BACKEND, CACHEDIR, QEMU_LOCKING,
// SUPERMIN_KERNEL, SUPERMIN_MODULES env vars. Saves originals for Restore.
func (h *GuestfsHandle) SetupEnvironment(ctx context.Context) {
	keys := []string{
		"LIBGUESTFS_BACKEND",
		"LIBGUESTFS_CACHEDIR",
		"QEMU_LOCKING",
		"SUPERMIN_KERNEL",
		"SUPERMIN_MODULES",
	}
	for _, key := range keys {
		val := os.Getenv(key)
		if val != "" {
			h.origEnv[key] = &val
		} else {
			h.origEnv[key] = nil
		}
	}

	os.Setenv("LIBGUESTFS_BACKEND", "direct")

	if _, err := os.Stat("/dev/shm"); err == nil {
		os.Setenv("LIBGUESTFS_CACHEDIR", "/dev/shm")
	}

	os.Setenv("QEMU_LOCKING", "off")

	// Force a known-good kernel with virtio drivers
	kd := &KernelDetector{}
	kernelPath, modulesDir, err := kd.FindBestKernel(ctx)
	if err == nil && kernelPath != "" {
		os.Setenv("SUPERMIN_KERNEL", kernelPath)
		os.Setenv("SUPERMIN_MODULES", modulesDir)
	}
}

// RestoreEnvironment restores env vars to their original values.
func (h *GuestfsHandle) RestoreEnvironment() {
	for key, val := range h.origEnv {
		if val != nil {
			os.Setenv(key, *val)
		} else {
			os.Unsetenv(key)
		}
	}
}

// ── Low-level guestfish invocation ──────────────────────────────────────────

// RunGuestfishCommand runs guestfish with the given raw arguments (no -i).
// Returns stdout on success. Exported for use by other packages.
func RunGuestfishCommand(ctx context.Context, diskPath string, readonly bool, args ...string) (string, error) {
	return guestfishRaw(ctx, diskPath, readonly, args...)
}

// guestfishCommonFlags returns the shared guestfish flags matching Python's
// OptimizedGuestfs._create_handle() handle configuration:
//   set_recovery_proc(False)  → --no-recovery-proc
//   set_autosync(False)       → --no-autosync
//   set_network(False)        → --no-network
//   set_smp(1)                → --smp 1
//   set_memsize(256)          → --memsize 256
//   set_backend("direct")     → --backend direct
func guestfishCommonFlags(diskPath string, readonly bool) []string {
	flags := []string{"-a", diskPath}
	if readonly {
		flags = append(flags, "--ro")
	}
	flags = append(flags,
		"--cachemode", "writeback",
		"--no-recovery-proc",
		"--no-autosync",
		"--no-network",
		"--smp", "1",
		"--memsize", "256",
		"--backend", "direct",
	)
	return flags
}

// guestfishRaw runs guestfish with the given raw arguments (no -i).
// Retries up to 3 times with backoff (0.5*(attempt+1)s) matching Python's
// OptimizedGuestfs.__enter__ retry loop.
// Returns stdout on success.
func guestfishRaw(ctx context.Context, diskPath string, readonly bool, args ...string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(500*(attempt+1)) * time.Millisecond):
			}
		}

		result, err := guestfishRawOnce(ctx, diskPath, readonly, args...)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// guestfishRawOnce runs a single guestfish invocation with no retry.
func guestfishRawOnce(ctx context.Context, diskPath string, readonly bool, args ...string) (string, error) {
	allArgs := guestfishCommonFlags(diskPath, readonly)
	allArgs = append(allArgs, args...)

	result := system.RunCmdCompat(ctx, append([]string{guestfishBin}, allArgs...), system.RunCmdOptions{Capture: true, Check: true})
	if result.Err != nil {
		if result.Stderr != "" {
			return "", fmt.Errorf("guestfish[%s]: %s: %w", strings.Join(allArgs, " "), result.Stderr, result.Err)
		}
		return "", fmt.Errorf("guestfish[%s]: %w", strings.Join(allArgs, " "), result.Err)
	}
	return result.Stdout, nil
}

// RunGuestfishInspect runs guestfish with -i (inspect-and-mount) flag.
// Exported for use by other packages.
func RunGuestfishInspect(ctx context.Context, diskPath string, readonly bool, args ...string) (string, error) {
	return guestfishInspect(ctx, diskPath, readonly, args...)
}

// guestfishInspect runs guestfish with -i (inspect-and-mount) flag.
func guestfishInspect(ctx context.Context, diskPath string, readonly bool, args ...string) (string, error) {
	allArgs := append([]string{"-i"}, args...)
	return guestfishRaw(ctx, diskPath, readonly, allArgs...)
}

// guestfishWithInput runs guestfish with stdin input (for batched commands).
func guestfishWithInput(ctx context.Context, diskPath string, readonly bool, input string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(500*(attempt+1)) * time.Millisecond):
			}
		}

		result, err := guestfishWithInputOnce(ctx, diskPath, readonly, input)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// guestfishWithInputOnce runs a single guestfish invocation with stdin input.
func guestfishWithInputOnce(ctx context.Context, diskPath string, readonly bool, input string) (string, error) {
	allArgs := guestfishCommonFlags(diskPath, readonly)
	allArgs = append(allArgs, "-i")

	result := system.RunCmdCompat(ctx, append([]string{guestfishBin}, allArgs...), system.RunCmdOptions{Input: input, Capture: true, Check: true})
	if result.Err != nil {
		if result.Stderr != "" {
			return result.Stdout, fmt.Errorf("guestfish[stdin]: %s: %w", result.Stderr, result.Err)
		}
		return result.Stdout, fmt.Errorf("guestfish[stdin]: %w", result.Err)
	}
	return result.Stdout, nil
}

// ── High-level operations ───────────────────────────────────────────────────

// MountRootfs lists filesystems, identifies root device, returns it.
// No persistent mount in subprocess mode — use RunBatch() or provisioner's Run().
func (h *GuestfsHandle) MountRootfs(ctx context.Context) (string, error) {
	out, err := guestfishRaw(ctx, h.diskPath, h.readonly, "list-filesystems")
	if err != nil {
		return "", &GuestfsError{msg: fmt.Sprintf("Failed to list filesystems for %s: %v", h.diskPath, err)}
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	type fsEntry struct {
		device string
		fstype string
	}
	var entries []fsEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) == 2 {
			entries = append(entries, fsEntry{device: parts[0], fstype: parts[1]})
		}
	}

	var rootDevice string
	candidates := []string{"/dev/sda", "/dev/vda", "/dev/sda1", "/dev/vda1"}
	for _, cand := range candidates {
		for _, e := range entries {
			if e.device == cand {
				rootDevice = cand
				break
			}
		}
		if rootDevice != "" {
			break
		}
	}

	if rootDevice == "" && len(entries) > 0 {
		rootDevice = entries[0].device
	}

	if rootDevice == "" {
		return "", &GuestfsError{msg: fmt.Sprintf("No filesystem found in %s", h.diskPath)}
	}

	return rootDevice, nil
}

// ListPartitions lists partitions in the disk image.
func (h *GuestfsHandle) ListPartitions(ctx context.Context) ([]string, error) {
	out, err := guestfishRaw(ctx, h.diskPath, h.readonly, "list-partitions")
	if err != nil {
		return nil, &GuestfsError{msg: fmt.Sprintf("Failed to list partitions: %v", err)}
	}
	return parseLines(out), nil
}

// VfsType returns the filesystem type of a device.
func (h *GuestfsHandle) VfsType(ctx context.Context, device string) (string, error) {
	out, err := guestfishInspect(ctx, h.diskPath, true, "vfs-type", device)
	if err != nil {
		return "", &GuestfsError{msg: fmt.Sprintf("Failed to get vfs-type for %s: %v", device, err)}
	}
	return strings.TrimSpace(out), nil
}

// BlockdevGetSize64 returns the size of a block device in bytes.
func (h *GuestfsHandle) BlockdevGetSize64(ctx context.Context, device string) (int64, error) {
	out, err := guestfishRaw(ctx, h.diskPath, true, "blockdev-getsize64", device)
	if err != nil {
		return 0, &GuestfsError{msg: fmt.Sprintf("Failed to get blockdev size for %s: %v", device, err)}
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// CopyDeviceToFile copies a device to a file.
func (h *GuestfsHandle) CopyDeviceToFile(ctx context.Context, device string, outputPath string) error {
	_, err := guestfishRaw(ctx, h.diskPath, true, "copy-device-to-file", device, outputPath)
	if err != nil {
		return &GuestfsError{msg: fmt.Sprintf("Failed to copy device %s to %s: %v", device, outputPath, err)}
	}
	return nil
}

// FindLargestLinuxFS finds the largest Linux filesystem among partitions.
// Matches Python's find_largest_linux_fs: checks vfs_type, mounts Linux
// filesystems, and selects the largest by statvfs.
// Returns the device path, or empty string if none found.
func (h *GuestfsHandle) FindLargestLinuxFS(ctx context.Context, partitions []string) (string, error) {
	var maxSize int64
	var rootDevice string

	for _, dev := range partitions {
		// Python: check vfs_type first, only mount Linux filesystems
		fsType, err := h.VfsType(ctx, dev)
		if err != nil {
			continue
		}
		if fsType != "ext2" && fsType != "ext3" && fsType != "ext4" && fsType != "btrfs" && fsType != "xfs" {
			continue
		}

		// Use guestfishRaw (NOT guestfishInspect with -i) to avoid auto-mount
		// interference with explicit mount/umount operations.
		out, err := guestfishRaw(ctx, h.diskPath, true,
			"mount", dev, "/",
			":", "statvfs", "/",
			":", "umount", "/",
		)
		if err != nil {
			// Python: except Exception: continue
			continue
		}
		blocks := parseStatvfsField(out, "blocks")
		bsize := parseStatvfsField(out, "bsize")
		if blocks > 0 && bsize > 0 {
			size := blocks * bsize
			if size > maxSize {
				maxSize = size
				rootDevice = dev
			}
		}
	}

	// Python: if root_device is None and filesystems, use partitions[0] fallback
	// happens at caller (ExtractPartition).
	return rootDevice, nil
}

// GetFSSize returns the size of a filesystem device in bytes.
// Matches Python's get_fs_size: mounts device, calls statvfs, unmounts.
func (h *GuestfsHandle) GetFSSize(ctx context.Context, device string) (int64, error) {
	// Use guestfishRaw (NOT guestfishInspect with -i) to avoid auto-mount
	// interference with explicit mount/umount operations.
	out, err := guestfishRaw(ctx, h.diskPath, true,
		"mount", device, "/",
		":", "statvfs", "/",
		":", "umount", "/",
	)
	if err != nil {
		return 0, &GuestfsError{msg: fmt.Sprintf("Failed to get fs size for %s: %v", device, err)}
	}
	blocks := parseStatvfsField(out, "blocks")
	bsize := parseStatvfsField(out, "bsize")
	if blocks == 0 || bsize == 0 {
		return 0, &GuestfsError{msg: fmt.Sprintf("Failed to parse statvfs for %s", device)}
	}
	return blocks * bsize, nil
}

// ShrinkExt4 shrinks an ext4 filesystem to minimum size.
// Matches Python's OptimizedGuestfs.shrink_ext4() exactly.
func (h *GuestfsHandle) ShrinkExt4(ctx context.Context, device string) error {
	// Python: mount(device, "/"), zero_free_space(device), umount("/"),
	// e2fsck(device, correct=True), umount("/"), resize2fs_size(device, 0)
	_, err := guestfishInspect(ctx, h.diskPath, false,
		"mount", device, "/",
		":", "zero-free-space", device,
		":", "umount", "/",
		":", "e2fsck", device, "correct:true",
		":", "umount", "/",
		":", "resize2fs-size", device, "0",
	)
	if err != nil {
		return &GuestfsError{msg: fmt.Sprintf("Failed to shrink ext4 %s: %v", device, err)}
	}
	return nil
}

// ShrinkBtrfs shrinks a btrfs filesystem to minimum size.
func (h *GuestfsHandle) ShrinkBtrfs(ctx context.Context, device string) error {
	_, err := guestfishInspect(ctx, h.diskPath, false,
		"mount", device, "/",
		":", "sh", `fstrim -av / 2>/dev/null || true`,
		":", "btrfs-filesystem-sync", "/",
		":", "btrfs-filesystem-resize", "/", "0",
		":", "umount", "/",
	)
	if err != nil {
		return &GuestfsError{msg: fmt.Sprintf("Failed to shrink btrfs %s: %v", device, err)}
	}
	return nil
}

// GrowFS grows a filesystem to fill the allocated space.
func (h *GuestfsHandle) GrowFS(ctx context.Context, device string, targetSizeBytes int64) error {
	// First get the filesystem type
	fsType, err := h.VfsType(ctx, device)
	if err != nil {
		return &GuestfsError{msg: fmt.Sprintf("Failed to get fs type for grow: %v", err)}
	}

	switch fsType {
	case "ext2", "ext3", "ext4":
		// Python calls resize2fs(device) directly — no mount/umount needed
		// because the device is already handled by guestfs.
		_, err := guestfishInspect(ctx, h.diskPath, false,
			"resize2fs", device,
		)
		if err != nil {
			return &GuestfsError{msg: fmt.Sprintf("Failed to grow ext fs %s: %v", device, err)}
		}
	case "btrfs":
		targetStr := strconv.FormatInt(targetSizeBytes, 10)
		_, err := guestfishInspect(ctx, h.diskPath, false,
			"mount", device, "/",
			":", "btrfs-filesystem-resize", "/", targetStr,
			":", "umount", "/",
		)
		if err != nil {
			return &GuestfsError{msg: fmt.Sprintf("Failed to grow btrfs %s: %v", device, err)}
		}
	default:
		return &GuestfsError{msg: fmt.Sprintf("Cannot grow %s filesystem: not supported", fsType)}
	}
	return nil
}

// ── extract_partition (classmethod equivalent) ──────────────────────────────

// ExtractPartition extracts root partition using guestfish for reliable VHD handling.
// Returns the output path, or empty string if extraction fails/guestfs unavailable.
// Matches Python's OptimizedGuestfs.extract_partition() — silently returns ("", nil)
// on extraction errors, matching Python's try/except Exception: return None.
func ExtractPartition(ctx context.Context, rawPath, outputPath string, partition *int) (string, error) {
	logger := slog.Default()

	handle, err := NewHandle(rawPath, true)
	if err != nil {
		if _, ok := err.(*GuestfsNotAvailableError); ok {
			return "", nil
		}
		// Non-availability errors propagate upward (matching Python)
		return "", err
	}

	// Wrap extraction body catching errors — matching Python's try/except Exception: return None
	var retErr error
	defer func() {
		if retErr != nil {
			logger.Debug("Guestfs extraction failed", "error", retErr)
		}
	}()

	outputResult, extractErr := extractPartitionInner(handle, ctx, logger, rawPath, outputPath, partition)
	if extractErr != nil {
		retErr = extractErr
		return "", nil
	}
	return outputResult, nil
}

// extractPartitionInner contains the actual partition extraction logic.
func extractPartitionInner(handle *GuestfsHandle, ctx context.Context, logger *slog.Logger, rawPath, outputPath string, partition *int) (string, error) {

	partitions, err := handle.ListPartitions(ctx)
	if err != nil {
		return "", err
	}

	if len(partitions) == 0 {
		// No partition table — check if image is a direct filesystem (superfloppy)
		fsType, vfsErr := handle.VfsType(ctx, "/dev/sda")
		if vfsErr == nil && fsType != "" {
			logger.Debug("Superfloppy image detected", "fs_type", fsType)
			// Copy the whole file as-is
			if cpErr := infra.CopyFile(rawPath, outputPath); cpErr != nil {
				return "", fmt.Errorf("copy superfloppy image: %w", cpErr)
			}
			logger.Info("Copied superfloppy image", "path", filepath.Base(outputPath))
			return outputPath, nil
		}
		logger.Debug("No partitions and not a superfloppy filesystem")
		return "", nil
	}

	var rootDevice string
	if partition != nil {
		if *partition < 1 || *partition > len(partitions) {
			logger.Debug("Partition out of range",
				"partition", *partition,
				"max", len(partitions),
			)
			return "", nil
		}
		rootDevice = partitions[*partition-1]
	} else {
		rootDev, findErr := handle.FindLargestLinuxFS(ctx, partitions)
		if findErr == nil && rootDev != "" {
			rootDevice = rootDev
		}
	}

	if rootDevice == "" {
		rootDevice = partitions[0]
	}

	fsSize, getFSErr := handle.GetFSSize(ctx, rootDevice)
	if getFSErr != nil {
		fsSize = 0
	}

	if copyErr := handle.CopyDeviceToFile(ctx, rootDevice, outputPath); copyErr != nil {
		return "", fmt.Errorf("copy device to file: %w", copyErr)
	}

	if fsSize > 0 {
		finalSize := int64(float64(fsSize) * infra.ShrinkSafetyMargin)
		if truncErr := os.Truncate(outputPath, finalSize); truncErr != nil {
			return "", fmt.Errorf("truncate output: %w", truncErr)
		}
	}

	logger.Info("Extracted root partition via guestfish",
		"path", filepath.Base(outputPath),
	)
	return outputPath, nil
}

// ── Utility / Session lifecycle ─────────────────────────────────────────────

// OpenSession creates a guestfish session with stdin piping.
// Returns input writer and a function to close the session.
// This is used for batched operations in the provisioner.
func (h *GuestfsHandle) OpenSession(ctx context.Context) (func(string), func() error, error) {
	// For the subprocess approach, "session" means running guestfish with -i
	// We don't maintain persistent sessions in the subprocess model.
	// Instead, we provide a batch execution method.
	return nil, nil, fmt.Errorf("persistent sessions not supported in subprocess mode; use RunBatch instead")
}

// RunBatch runs a batch of guestfish commands in a single invocation.
// Commands should be newline-separated guestfish commands.
func (h *GuestfsHandle) RunBatch(ctx context.Context, commands string) (string, error) {
	return guestfishWithInput(ctx, h.diskPath, h.readonly, commands)
}

// ── Parsing helpers ─────────────────────────────────────────────────────────

func parseLines(out string) []string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

// parseStatvfsField parses a field from guestfish statvfs output.
// Output format: "fieldname: value\nfieldname: value\n..."
func parseStatvfsField(out, field string) int64 {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, field+":") {
			valStr := strings.TrimSpace(strings.TrimPrefix(line, field+":"))
			val, err := strconv.ParseInt(valStr, 10, 64)
			if err == nil {
				return val
			}
		}
	}
	return 0
}

// StatVFS parses statvfs output into a map for programmatic access.
func (h *GuestfsHandle) StatVFS(ctx context.Context, path string) (map[string]int64, error) {
	out, err := guestfishInspect(ctx, h.diskPath, true, "statvfs", path)
	if err != nil {
		return nil, &GuestfsError{msg: fmt.Sprintf("statvfs %s failed: %v", path, err)}
	}

	result := make(map[string]int64)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) == 2 {
			val, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			if err == nil {
				result[parts[0]] = val
			}
		}
	}
	return result, nil
}

// ── Retry helpers ───────────────────────────────────────────────────────────

// WithRetry retries an operation up to maxAttempts with backoff (like Python's
// OptimizedGuestfs.__enter__ retry loop).
func WithRetry(ctx context.Context, maxAttempts int, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// DiskPath returns the disk path.
func (h *GuestfsHandle) DiskPath() string { return h.diskPath }

// IsReadonly returns whether the handle is in read-only mode.
func (h *GuestfsHandle) IsReadonly() bool { return h.readonly }
