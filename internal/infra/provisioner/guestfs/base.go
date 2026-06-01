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
	"sync"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// ── Constants ───────────────────────────────────────────────────────────────

const (
	guestfishBin   = "guestfish"
	guestmountBin  = "guestmount"
	defaultMemsize = 256
)

// ── GuestfsHandle ───────────────────────────────────────────────────────────
//
// Uses guestfish CLI tool as a subprocess (Go has no native guestfs bindings).
// Each method launches a separate guestfish subprocess.
// The optimized environment (LIBGUESTFS_BACKEND=direct, QEMU_LOCKING=off,
// forced kernel with virtio) is set up automatically on the first invocation
// via guestfishRun — no manual env setup needed.

// GuestfsHandle wraps guestfish CLI for disk image operations.
type GuestfsHandle struct {
	diskPath string
	readonly bool
}

// NewHandle creates a new GuestfsHandle.
func NewHandle(diskPath string, readonly bool) (*GuestfsHandle, error) {
	if _, err := exec.LookPath(guestfishBin); err != nil {
		return nil, &GuestfsNotAvailableError{msg: "libguestfs is not available"}
	}
	return &GuestfsHandle{diskPath: diskPath, readonly: readonly}, nil
}

// ── Lazy environment initialization (once per process) ───────────────────────

var (
	envOnce     sync.Once
	origEnvVars map[string]*string
)

// initEnv sets the optimized guestfs environment variables once per process.
// Called automatically by guestfishRun on the first invocation.
func initEnv(ctx context.Context) {
	envOnce.Do(func() {
		keys := []string{
			"LIBGUESTFS_BACKEND",
			"LIBGUESTFS_CACHEDIR",
			"QEMU_LOCKING",
			"SUPERMIN_KERNEL",
			"SUPERMIN_MODULES",
		}
		origEnvVars = make(map[string]*string, len(keys))
		for _, key := range keys {
			val := os.Getenv(key)
			if val != "" {
				v := val
				origEnvVars[key] = &v
			} else {
				origEnvVars[key] = nil
			}
		}

		os.Setenv("LIBGUESTFS_BACKEND", "direct")
		if _, err := os.Stat("/dev/shm"); err == nil {
			os.Setenv("LIBGUESTFS_CACHEDIR", "/dev/shm")
		}
		os.Setenv("QEMU_LOCKING", "off")

		kd := &KernelDetector{}
		kernelPath, modulesDir, err := kd.FindBestKernel(ctx)
		if err == nil && kernelPath != "" {
			os.Setenv("SUPERMIN_KERNEL", kernelPath)
			os.Setenv("SUPERMIN_MODULES", modulesDir)
		}
	})
}

// ── Low-level guestfish invocation ──────────────────────────────────────────

// guestfishRun executes guestfish with retry (3 attempts, backoff).
// If input is non-empty, it is piped to guestfish's stdin (interactive mode).
func guestfishRun(
	ctx context.Context,
	diskPath string,
	readonly bool,
	input string,
	args ...string,
) (string, error) {
	initEnv(ctx)

	allArgs := []string{"-a", diskPath}
	if readonly {
		allArgs = append(allArgs, "--ro")
	}
	allArgs = append(allArgs,
		"--cachemode", "writeback",
		"--no-recovery-proc",
		"--no-autosync",
		"--no-network",
		"--smp", "1",
		"--memsize", "256",
		"--backend", "direct",
	)
	allArgs = append(allArgs, args...)

	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(500*(attempt+1)) * time.Millisecond):
			}
		}

		runOpts := system.RunCmdOpts{Capture: true, Check: true}
		if input != "" {
			runOpts.Input = input
		}
		result := system.RunCmdCompat(ctx, append([]string{guestfishBin}, allArgs...), runOpts)
		if result.Err != nil {
			label := "guestfish"
			if input != "" {
				label = "guestfish[stdin]"
			}
			if result.Stderr != "" {
				lastErr = fmt.Errorf("%s[%s]: %s: %w", label, strings.Join(allArgs, " "), result.Stderr, result.Err)
			} else {
				lastErr = fmt.Errorf("%s[%s]: %w", label, strings.Join(allArgs, " "), result.Err)
			}
			continue
		}
		return result.Stdout, nil
	}
	return "", lastErr
}

// h.run is a convenience wrapper so handle methods don't repeat diskPath/readonly.
func (h *GuestfsHandle) run(ctx context.Context, args ...string) (string, error) {
	return guestfishRun(ctx, h.diskPath, h.readonly, "", args...)
}

// ── High-level operations ───────────────────────────────────────────────────

// MountRootfs lists filesystems, identifies root device, returns it.
// No persistent mount in subprocess mode — use RunBatch() or provisioner's Run().
func (h *GuestfsHandle) MountRootfs(ctx context.Context) (string, error) {
	out, err := h.run(ctx, "list-filesystems")
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
	out, err := h.run(ctx, "list-partitions")
	if err != nil {
		return nil, &GuestfsError{msg: fmt.Sprintf("Failed to list partitions: %v", err)}
	}
	var parts []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts, nil
}

// VfsType returns the filesystem type of a device.
func (h *GuestfsHandle) VfsType(ctx context.Context, device string) (string, error) {
	out, err := guestfishRun(ctx, h.diskPath, true, "", "-i", "vfs-type", device)
	if err != nil {
		return "", &GuestfsError{msg: fmt.Sprintf("Failed to get vfs-type for %s: %v", device, err)}
	}
	return strings.TrimSpace(out), nil
}

// BlockdevGetSize64 returns the size of a block device in bytes.
func (h *GuestfsHandle) BlockdevGetSize64(ctx context.Context, device string) (int64, error) {
	out, err := guestfishRun(ctx, h.diskPath, true, "", "blockdev-getsize64", device)
	if err != nil {
		return 0, &GuestfsError{msg: fmt.Sprintf("Failed to get blockdev size for %s: %v", device, err)}
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// CopyDeviceToFile copies a device to a file.
func (h *GuestfsHandle) CopyDeviceToFile(ctx context.Context, device string, outputPath string) error {
	_, err := guestfishRun(ctx, h.diskPath, true, "", "copy-device-to-file", device, outputPath)
	if err != nil {
		return &GuestfsError{msg: fmt.Sprintf("Failed to copy device %s to %s: %v", device, outputPath, err)}
	}
	return nil
}

// FindLargestLinuxFS finds the largest Linux filesystem among partitions.
// Matches Python's find_largest_linux_fs: checks vfs_type, mounts Linux
// filesystems, and selects the largest by statvfs.
func (h *GuestfsHandle) FindLargestLinuxFS(ctx context.Context, partitions []string) (string, error) {
	var maxSize int64
	var rootDevice string

	for _, dev := range partitions {
		fsType, err := h.VfsType(ctx, dev)
		if err != nil {
			continue
		}
		if fsType != "ext2" && fsType != "ext3" && fsType != "ext4" && fsType != "btrfs" && fsType != "xfs" {
			continue
		}
		out, err := guestfishRun(ctx, h.diskPath, true, "",
			"mount", dev, "/",
			":", "statvfs", "/",
			":", "umount", "/",
		)
		if err != nil {
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
	return rootDevice, nil
}

// GetFSSize returns the size of a filesystem device in bytes.
func (h *GuestfsHandle) GetFSSize(ctx context.Context, device string) (int64, error) {
	out, err := guestfishRun(ctx, h.diskPath, true, "",
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
func (h *GuestfsHandle) ShrinkExt4(ctx context.Context, device string) error {
	_, err := guestfishRun(ctx, h.diskPath, false, "",
		"-i", "mount", device, "/",
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
	_, err := guestfishRun(ctx, h.diskPath, false, "",
		"-i", "mount", device, "/",
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
	fsType, err := h.VfsType(ctx, device)
	if err != nil {
		return &GuestfsError{msg: fmt.Sprintf("Failed to get fs type for grow: %v", err)}
	}
	switch fsType {
	case "ext2", "ext3", "ext4":
		_, err := guestfishRun(ctx, h.diskPath, false, "", "-i", "resize2fs", device)
		if err != nil {
			return &GuestfsError{msg: fmt.Sprintf("Failed to grow ext fs %s: %v", device, err)}
		}
	case "btrfs":
		targetStr := strconv.FormatInt(targetSizeBytes, 10)
		_, err := guestfishRun(ctx, h.diskPath, false, "",
			"-i", "mount", device, "/",
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

// ── Utility / Session lifecycle ─────────────────────────────────────────────

// RunBatch runs a batch of guestfish commands in a single invocation.
func (h *GuestfsHandle) RunBatch(ctx context.Context, commands string) (string, error) {
	return guestfishRun(ctx, h.diskPath, h.readonly, commands, "-i")
}

// ReadFile reads a file from the guestfs image (uses inspect mode `-i`
// to auto-mount the rootfs). Returns an error if the file does not exist
// or guestfish fails. Callers that want to fall back to alternate paths
// (e.g. /etc/os-release → /usr/lib/os-release) should catch the error
// and try the next path.
func (h *GuestfsHandle) ReadFile(ctx context.Context, path string) (string, error) {
	out, err := guestfishRun(ctx, h.diskPath, true, "", "read-file", path)
	if err != nil {
		return "", &GuestfsError{msg: fmt.Sprintf("read-file %s failed: %v", path, err)}
	}
	return out, nil
}

// ── Parsing helpers ─────────────────────────────────────────────────────────

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
	out, err := guestfishRun(ctx, h.diskPath, true, "", "-i", "statvfs", path)
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

// ── extract_partition (classmethod equivalent) ──────────────────────────────

// ExtractPartition extracts root partition using guestfish for reliable VHD handling.
// Returns the output path, or empty string if extraction fails/guestfs unavailable.
func ExtractPartition(ctx context.Context, rawPath, outputPath string, partition *int) (string, error) {
	handle, err := NewHandle(rawPath, true)
	if err != nil {
		if _, ok := err.(*GuestfsNotAvailableError); ok {
			return "", nil
		}
		return "", err
	}

	var retErr error
	defer func() {
		if retErr != nil {
			slog.Debug("Guestfs extraction failed", "error", retErr)
		}
	}()

	outputResult, extractErr := handle.extractPartitionInner(ctx, rawPath, outputPath, partition)
	if extractErr != nil {
		retErr = extractErr
		return "", nil
	}
	return outputResult, nil
}

// extractPartitionInner contains the actual partition extraction logic.
func (h *GuestfsHandle) extractPartitionInner(
	ctx context.Context,
	rawPath, outputPath string,
	partition *int,
) (string, error) {

	partitions, err := h.ListPartitions(ctx)
	if err != nil {
		return "", err
	}

	if len(partitions) == 0 {
		fsType, vfsErr := h.VfsType(ctx, "/dev/sda")
		if vfsErr == nil && fsType != "" {
			slog.Debug("Superfloppy image detected", "fs_type", fsType)
			if cpErr := infra.CopyPreservingMetadata(rawPath, outputPath); cpErr != nil {
				return "", fmt.Errorf("copy superfloppy image: %w", cpErr)
			}
			slog.Info("Copied superfloppy image", "path", filepath.Base(outputPath))
			return outputPath, nil
		}
		slog.Debug("No partitions and not a superfloppy filesystem")
		return "", nil
	}

	var rootDevice string
	if partition != nil {
		if *partition < 1 || *partition > len(partitions) {
			slog.Debug("Partition out of range", "partition", *partition, "max", len(partitions))
			return "", nil
		}
		rootDevice = partitions[*partition-1]
	} else {
		rootDev, findErr := h.FindLargestLinuxFS(ctx, partitions)
		if findErr == nil && rootDev != "" {
			rootDevice = rootDev
		}
	}

	if rootDevice == "" {
		rootDevice = partitions[0]
	}

	fsSize, getFSErr := h.GetFSSize(ctx, rootDevice)
	if getFSErr != nil {
		fsSize = 0
	}

	if copyErr := h.CopyDeviceToFile(ctx, rootDevice, outputPath); copyErr != nil {
		return "", fmt.Errorf("copy device to file: %w", copyErr)
	}

	if fsSize > 0 {
		finalSize := int64(float64(fsSize) * infra.ShrinkSafetyMargin)
		if truncErr := os.Truncate(outputPath, finalSize); truncErr != nil {
			return "", fmt.Errorf("truncate output: %w", truncErr)
		}
	}

	slog.Info("Extracted root partition via guestfish", "path", filepath.Base(outputPath))
	return outputPath, nil
}
