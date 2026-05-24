package loopmount

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/provisionercontent"
	"mvmctl/internal/infra/system"

	loopmountsvc "mvmctl/internal/service/loopmount"
)

// LoopMountBackend delegates all operations to LoopMountProvisioner.
// Matches Python's _LoopMountBackend in _backend.py.
//
// NOTE: LoopMountManager (infra/loopmount/manager.go) has been abolished.
// All provisioning logic now lives in internal/service/loopmount/provisioner.go.
// This backend continues to use LoopMountProvisioner which delegates to the
// service layer.
type LoopMountBackend struct {
	lp        *LoopMountProvisioner
	cacheDir  string
	rootfsPath string
	fsType    string
	ctx       context.Context
}

// NewLoopMountBackend creates a new LoopMountBackend.
// Matches Python's __init__().
func NewLoopMountBackend(rootfsPath string, fsType string, cacheDir string) *LoopMountBackend {
	return &LoopMountBackend{
		lp:        NewLoopMountProvisioner(rootfsPath, fsType, cacheDir),
		cacheDir:  cacheDir,
		rootfsPath: rootfsPath,
		fsType:    fsType,
		ctx:       context.Background(),
	}
}

// SetContext sets the context for this backend.
func (b *LoopMountBackend) SetContext(ctx context.Context) {
	b.ctx = ctx
}

// ---------------------------------------------------------------------------
// Backend interface methods
// ---------------------------------------------------------------------------

// Resize queues a rootfs resize operation (0 = shrink to minimum).
func (b *LoopMountBackend) Resize(targetSizeBytes int64) error {
	pc := provisionercontent.ProvisionerContent{}
	if targetSizeBytes == 0 {
		b.lp.QueueOps(pc.BuildShrinkOps(0))
	} else {
		b.lp.Resize(targetSizeBytes)
	}
	return nil
}

// SetHostname queues hostname + /etc/hosts setup.
func (b *LoopMountBackend) SetHostname(hostname string) error {
	b.lp.SetHostname(hostname)
	return nil
}

// InjectDNS queues DNS resolver injection.
func (b *LoopMountBackend) InjectDNS(dnsServer string) error {
	b.lp.InjectDNS(dnsServer)
	return nil
}

// SetupSSH queues SSH key, config, and host-key generation.
func (b *LoopMountBackend) SetupSSH(user string, sshPubkeys []string) error {
	b.lp.SetupSSH(user, sshPubkeys)
	return nil
}

// DisableCloudInit queues cloud-init datasource blocking + service masking.
func (b *LoopMountBackend) DisableCloudInit() error {
	b.lp.DisableCloudInit()
	return nil
}

// InjectCloudInit queues cloud-init seed directory injection.
func (b *LoopMountBackend) InjectCloudInit(cloudInitDir string) error {
	b.lp.InjectCloudInit(cloudInitDir)
	return nil
}

// DetectOS detects OS type from the rootfs via the loopmount service.
// Falls back to "linux" if detection fails or returns an error.
func (b *LoopMountBackend) DetectOS() (string, error) {
	svc := loopmountsvc.NewProvisioner(b.cacheDir)
	results, err := svc.Execute(b.ctx, []loopmountsvc.Op{
		{
			Image:  b.rootfsPath,
			Action: "detect_os",
			FsType: b.fsType,
		},
	})
	if err != nil {
		slog.Warn("OS detection failed, falling back to 'linux'", "error", err)
		return "linux", nil
	}
	if len(results) == 0 || results[0].OSType == "" {
		return "linux", nil
	}
	return results[0].OSType, nil
}

// Deblob queues deblob (OS cache cleanup) operations.
// If osType is empty, auto-detects the OS first (matching Python's
// backend.deblob(self, os_type=None) which calls self.detect_os() when os_type is None).
func (b *LoopMountBackend) Deblob(osType string) error {
	if osType == "" {
		var err error
		osType, err = b.DetectOS()
		if err != nil {
			return fmt.Errorf("OS detection for deblob failed: %w", err)
		}
	}
	pc := provisionercontent.ProvisionerContent{}
	ops := pc.BuildDeblobOps(osType)
	b.lp.QueueOps(ops)
	return nil
}

// FixFstab queues fstab fix for Firecracker (PARTUUID -> /dev/vda).
func (b *LoopMountBackend) FixFstab() error {
	pc := provisionercontent.ProvisionerContent{}
	b.lp.QueueOps(pc.BuildFixFstabOps())
	return nil
}

// Shrink queues filesystem shrink to minimum size.
func (b *LoopMountBackend) Shrink() error {
	return b.Resize(0)
}

// ExtractPartition extracts root partition from a raw disk image.
// Uses sfdisk/parted for partition table parsing and dd for extraction.
// This is the LOOP_MOUNT backend's partition extraction path.
// Matches Python's _LoopMountBackend.extract_partition() (~160 lines).
func (b *LoopMountBackend) ExtractPartition(
	rawPath string,
	outputPath string,
	partition int,
	disabledDetectors []string,
) (string, error) {
	sectorSize := int64(512)

	// Check if the image is a direct filesystem (superfloppy) using blkid
	fsType := detectFilesystemType(rawPath)
	if fsType == "ext4" || fsType == "ext3" || fsType == "ext2" ||
		fsType == "btrfs" || fsType == "xfs" {
		slog.Info("Image is filesystem, using as-is", "type", fsType)
		// Try cp --sparse=always first
		opts := system.DefaultRunCmdOpts()
		opts.Capture = true
		opts.Check = false
		result := system.RunCmdCompat(b.ctx, []string{"cp", "--sparse=always", rawPath, outputPath}, opts)
		if result.ExitCode != 0 {
			// Fall back to dd copy
			if err := copyBytesDD(b.ctx, rawPath, outputPath, 0, 0); err != nil {
				return "", err
			}
		}
		extMap := map[string]string{
			"ext4": ".ext4",
			"ext3": ".ext4",
			"ext2": ".ext4",
			"btrfs": ".btrfs",
			"xfs":   ".xfs",
		}
		ext, ok := extMap[fsType]
		if !ok {
			ext = ".img"
		}
		finalPath := outputPath[:len(outputPath)-len(filepath.Ext(outputPath))] + ext
		if err := safeMove(outputPath, finalPath); err != nil {
			return "", fmt.Errorf("rename output to %s: %w", finalPath, err)
		}
		return finalPath, nil
	}

	// Parse partition table
	parsed := parsePartitionsSfdisk(b.ctx, rawPath, partition)
	if parsed == nil {
		parsed = parsePartitionsParted(b.ctx, rawPath, partition)
	}

	if parsed == nil {
		return "", fmt.Errorf("Failed to parse partition table: neither sfdisk nor parted is available or succeeded")
	}

	if parsed.NoPartitionTable {
		slog.Info("No partition table found, using image as-is")
		if err := safeMove(rawPath, outputPath); err != nil {
			return "", fmt.Errorf("rename raw to output: %w", err)
		}
		return outputPath, nil
	}

	partitions := parsed.Partitions
	requestedPartition := parsed.RequestedPartition

	if len(partitions) == 0 {
		slog.Info("No partitions found, using image as-is")
		if err := safeMove(rawPath, outputPath); err != nil {
			return "", fmt.Errorf("rename raw to output: %w", err)
		}
		return outputPath, nil
	}

	var chosen provisioner.PartitionEntry
	partitionNum := 1

	if len(partitions) > 1 && requestedPartition == 0 {
		// Multiple partitions — use RootPartitionDetector
		slog.Info("Found partitions", "count", len(partitions))
		for i, p := range partitions {
			slog.Debug("Partition",
				"index", i+1,
				"start", p.Start,
				"size", p.Size,
				"type", p.Type,
			)
		}
		chosenIdx := provisioner.RootPartitionDetect(partitions, disabledDetectors)
		if chosenIdx < 1 || chosenIdx > len(partitions) {
			return "", fmt.Errorf("RootPartitionDetector returned invalid index %d", chosenIdx)
		}
		slog.Info("Detector selected partition as root", "partition", chosenIdx)
		chosen = partitions[chosenIdx-1]
		partitionNum = chosenIdx
	} else if requestedPartition > 0 {
		if requestedPartition > len(partitions) {
			return "", fmt.Errorf("Partition %d out of range (1-%d)", requestedPartition, len(partitions))
		}
		slog.Info("Found partitions, using requested as root",
			"count", len(partitions),
			"root", requestedPartition,
		)
		chosen = partitions[requestedPartition-1]
		partitionNum = requestedPartition
	} else {
		chosen = partitions[0]
		partitionNum = 1
	}

	startSector := safeInt(chosen.Start, 0)
	sizeVal := chosen.Size
	var sectorCount int64
	if sizeVal > 0 {
		sectorCount = sizeVal
	}

	skipBytes := startSector * sectorSize
	var countBytes int64
	if sectorCount > 0 {
		countBytes = sectorCount * sectorSize
	}

	// Validate extraction is within file bounds
	rawFileInfo, err := os.Stat(rawPath)
	if err != nil {
		return "", fmt.Errorf("stat raw image: %w", err)
	}
	rawFileSize := rawFileInfo.Size()
	if skipBytes >= rawFileSize {
		return "", fmt.Errorf(
			"Partition %d start sector (%d) offset (%d bytes) exceeds file size (%d bytes). "+
				"Partition table may be corrupted or in unsupported format.",
			partitionNum, startSector, skipBytes, rawFileSize,
		)
	}

	slog.Info("Extracting partition",
		"partition", partitionNum,
		"start_sector", startSector,
		"offset", skipBytes,
	)

	if err := copyBytesDD(b.ctx, rawPath, outputPath, skipBytes, countBytes); err != nil {
		return "", err
	}

	outputPath = detectAndRenameFS(b.ctx, outputPath)

	slog.Info("Extracted partition", "path", filepath.Base(outputPath))
	return outputPath, nil
}

// ConvertTo converts the image filesystem to targetFS via loop-mount.
func (b *LoopMountBackend) ConvertTo(targetFS string) error {
	_, err := b.lp.ConvertTo(b.ctx, targetFS)
	if err != nil {
		slog.Warn("Filesystem conversion failed", "error", err)
		return fmt.Errorf("Filesystem conversion failed: %w", err)
	}
	return nil
}

// Run executes all queued operations.
func (b *LoopMountBackend) Run() error {
	return b.lp.Run(b.ctx)
}

// =========================================================================
// Partition extraction helpers
// =========================================================================

// copyBytesDD copies bytes from src starting at skipBytes into dst using dd.
// Matches Python's _copy_bytes_dd() — raises an error on dd failure.
func copyBytesDD(ctx context.Context, src, dst string, skipBytes, countBytes int64) error {
	ddArgs := []string{
		fmt.Sprintf("if=%s", src),
		fmt.Sprintf("of=%s", dst),
		"bs=1M",
		fmt.Sprintf("skip=%d", skipBytes),
		"iflag=skip_bytes,count_bytes",
		"conv=sparse,fsync",
		"status=none",
	}
	if countBytes > 0 {
		ddArgs = append(ddArgs, fmt.Sprintf("count=%d", countBytes))
	}

	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	result := system.RunCmdCompat(ctx, ddArgs, opts)
	if result.ExitCode != 0 {
		errMsg := strings.TrimSpace(result.Stderr)
		if errMsg == "" {
			errMsg = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("dd failed: %s", errMsg)
	}
	return nil
}

// detectFilesystemType detects filesystem type using blkid.
// Matches Python's _detect_filesystem_type().
func detectFilesystemType(imagePath string) string {
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	result := system.RunCmdCompat(
		context.Background(),
		[]string{"blkid", "-o", "value", "-s", "TYPE", imagePath},
		opts,
	)
	if result.ExitCode == 0 {
		fsType := strings.TrimSpace(result.Stdout)
		if fsType != "" {
			return fsType
		}
	}
	return ""
}

// parsePartitionsSfdisk parses partition table using sfdisk --json.
// Matches Python's _parse_partitions_sfdisk().
func parsePartitionsSfdisk(ctx context.Context, rawPath string, partition int) *provisioner.ParseResult {
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	opts.Timeout = 15 * time.Second

	result := system.RunCmdCompat(ctx, []string{"sfdisk", "--json", rawPath}, opts)
	if result.ExitCode != 0 {
		return &provisioner.ParseResult{NoPartitionTable: true}
	}

	var table struct {
		PartitionTable *struct {
			Partitions []struct {
				Start float64 `json:"start"`
				Size  float64 `json:"size"`
				Type  string  `json:"type"`
				Node  string  `json:"node"`
			} `json:"partitions"`
		} `json:"partitiontable"`
	}

	if err := json.Unmarshal([]byte(result.Stdout), &table); err != nil {
		return nil
	}

	if table.PartitionTable == nil || len(table.PartitionTable.Partitions) == 0 {
		return &provisioner.ParseResult{NoPartitionTable: true}
	}

	partitions := make([]provisioner.PartitionEntry, 0, len(table.PartitionTable.Partitions))
	for _, p := range table.PartitionTable.Partitions {
		partitions = append(partitions, provisioner.PartitionEntry{
			Start: int64(p.Start),
			Size:  int64(p.Size),
			Type:  p.Type,
			Node:  p.Node,
		})
	}

	return &provisioner.ParseResult{
		Partitions:         partitions,
		RequestedPartition: partition,
		NoPartitionTable:   false,
	}
}

// parsePartitionsParted parses partition table using parted as fallback.
// Matches Python's _parse_partitions_parted().
func parsePartitionsParted(ctx context.Context, rawPath string, partition int) *provisioner.ParseResult {
	sectorSize := int64(512)

	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true
	opts.Timeout = 15 * time.Second

	result := system.RunCmdCompat(ctx, []string{"parted", "-sm", rawPath, "unit", "B", "print"}, opts)
	if result.ExitCode != 0 {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) == 0 || lines[0] != "BYT;" {
		return nil
	}

	var partitions []provisioner.PartitionEntry
	for _, line := range lines[2:] {
		line = strings.TrimSuffix(line, ";")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 6 {
			continue
		}
		number := strings.TrimSpace(parts[0])
		startStr := strings.TrimSuffix(strings.TrimSpace(parts[1]), "B")
		sizeStr := strings.TrimSuffix(strings.TrimSpace(parts[3]), "B")
		filesystem := strings.TrimSpace(parts[4])
		partType := strings.TrimSpace(parts[5])

		startBytes, err1 := strconv.ParseInt(startStr, 10, 64)
		sizeBytes, err2 := strconv.ParseInt(sizeStr, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}

		startSector := startBytes / sectorSize
		sizeSector := sizeBytes / sectorSize

		partitions = append(partitions, provisioner.PartitionEntry{
			Start:  startSector,
			Size:   sizeSector,
			Type:   partType,
			Node:   number,
			Fstype: filesystem,
		})
	}

	if len(partitions) == 0 {
		return &provisioner.ParseResult{NoPartitionTable: true}
	}

	return &provisioner.ParseResult{
		Partitions:         partitions,
		RequestedPartition: partition,
		NoPartitionTable:   false,
	}
}

// detectAndRenameFS detects filesystem type via blkid and renames output file.
// Matches Python's _detect_and_rename_fs().
func detectAndRenameFS(ctx context.Context, outputPath string) string {
	opts := system.DefaultRunCmdOpts()
	opts.Check = false
	opts.Capture = true

	result := system.RunCmdCompat(ctx, []string{"blkid", "-o", "value", "-s", "TYPE", outputPath}, opts)
	if result.ExitCode == 0 {
		fsType := strings.TrimSpace(result.Stdout)
		if fsType != "" {
			extMap := map[string]string{
				"ext4":  ".ext4",
				"ext3":  ".ext4",
				"ext2":  ".ext4",
				"btrfs": ".btrfs",
				"xfs":   ".xfs",
			}
			if ext, ok := extMap[fsType]; ok {
				finalPath := outputPath[:len(outputPath)-len(filepath.Ext(outputPath))] + ext
				if err := os.Rename(outputPath, finalPath); err == nil {
					outputPath = finalPath
				}
				slog.Info("Detected filesystem", "type", fsType)
			}
		}
	}
	return outputPath
}

// safeInt safely extracts an int64 from a value.
// Matches Python's CommonUtils.safe_int().
func safeInt(value int64, defaultVal int64) int64 {
	if value > 0 {
		return value
	}
	return defaultVal
}

// safeMove moves a file from src to dst, handling cross-filesystem moves.
// Matches Python's shutil.move() behavior: tries os.Rename first, falls back
// to copy+delete on EXDEV or other rename failures.
func safeMove(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-filesystem or other rename failure: copy + delete
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", dst, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return dstFile.Sync()
}
