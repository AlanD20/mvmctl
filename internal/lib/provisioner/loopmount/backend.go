package loopmount

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/provcontent"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/system"

	loopmountsvc "mvmctl/internal/service/loopmount"
)

// runWireOp serializes a WireInput to JSON, pipes it to
// "sudo env mvm run provision" via stdin, and returns the parsed WireOutput.
func runWireOp(ctx context.Context, input *loopmountsvc.WireInput) (*loopmountsvc.WireOutput, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal wire input: %w", err)
	}

	mvmPath, _ := os.Executable()
	provisionArgs := []string{mvmPath, "run", "provision"}
	if !system.IsRoot() {
		provisionArgs = append([]string{"sudo"}, provisionArgs...)
	}
	result, err := system.DefaultRunner.Run(ctx, provisionArgs,
		system.RunCmdOpts{Capture: true, Check: true, Input: string(data)})
	if err != nil {
		return nil, fmt.Errorf("provision subprocess failed: %s: %w", result.Stderr, err)
	}

	var output loopmountsvc.WireOutput
	if err := json.Unmarshal([]byte(result.Stdout), &output); err != nil {
		return nil, fmt.Errorf("parse wire output: %s: %w", result.Stdout, err)
	}
	if output.Status == "error" {
		return &output, fmt.Errorf("provision failed: %s (step: %s)", output.Error, output.Step)
	}
	return &output, nil
}

var pc = provcontent.Builder{}

// LoopMountBackend implements the Backend interface using loop-mount
// (mounts the rootfs via loop device, operates directly with host tools).
type LoopMountBackend struct {
	rootfsPath string
	fsType     string
	cacheDir   string
	ops        []provcontent.Operation
}

// NewLoopMountBackend creates a new LoopMountBackend.
func NewLoopMountBackend(ctx context.Context, rootfsPath string, fsType string, cacheDir string) *LoopMountBackend {
	return &LoopMountBackend{rootfsPath: rootfsPath, fsType: fsType, cacheDir: cacheDir}
}

// ═════════════════════════════════════════════════════════════════════════════
// Builder methods — queue provisioning operations
// ═════════════════════════════════════════════════════════════════════════════

func (b *LoopMountBackend) Resize(ctx context.Context, targetSizeBytes int64) error {
	if targetSizeBytes == 0 {
		b.ops = append(b.ops, pc.BuildShrinkOps(0)...)
	} else {
		b.ops = append(b.ops, pc.BuildResizeOps(targetSizeBytes)...)
	}
	return nil
}

func (b *LoopMountBackend) SetHostname(ctx context.Context, hostname string) error {
	b.ops = append(b.ops, pc.BuildHostnameOps(hostname)...)
	return nil
}

func (b *LoopMountBackend) InjectDNS(ctx context.Context, dnsServer string) error {
	b.ops = append(b.ops, pc.BuildDNSOps(dnsServer)...)
	return nil
}

func (b *LoopMountBackend) SetupSSH(ctx context.Context, user string, sshPubkeys []string) error {
	b.ops = append(b.ops, pc.BuildSSHOps(user, sshPubkeys)...)
	return nil
}

func (b *LoopMountBackend) SetupSudo(ctx context.Context, user string) error {
	b.ops = append(b.ops, pc.SetupSudo(user)...)
	return nil
}

func (b *LoopMountBackend) DisableCloudInit(ctx context.Context) error {
	return nil // no-op — cloud-init is disabled during image import, not VM creation
}

func (b *LoopMountBackend) InjectCloudInit(ctx context.Context, cloudInitDir string) error {
	b.ops = append(b.ops, pc.BuildCloudInitInjectOps(cloudInitDir)...)
	return nil
}

// DetectOS detects OS type from the rootfs via the loopmount service subprocess.
func (b *LoopMountBackend) DetectOS(ctx context.Context) (string, error) {
	out, err := runWireOp(ctx, &loopmountsvc.WireInput{
		Image:  b.rootfsPath,
		Action: "detect_os",
		FsType: b.fsType,
	})
	if err != nil {
		slog.Warn("OS detection failed, falling back to 'linux'", "error", err)
		return "linux", nil
	}
	if out.OsType == "" {
		return "linux", nil
	}
	return out.OsType, nil
}

// Deblob queues deblob (OS cache cleanup) operations.
func (b *LoopMountBackend) Deblob(ctx context.Context, osType *string) error {
	if osType == nil || *osType == "" {
		detected, err := b.DetectOS(ctx)
		if err != nil {
			return fmt.Errorf("OS detection for deblob failed: %w", err)
		}
		osType = &detected
	}
	b.ops = append(b.ops, pc.BuildDeblobOps(*osType)...)
	return nil
}

func (b *LoopMountBackend) FixFstab(ctx context.Context) error {
	b.ops = append(b.ops, pc.BuildFixFstabOps()...)
	return nil
}

func (b *LoopMountBackend) Shrink(ctx context.Context) error {
	return b.Resize(ctx, 0)
}

// ═════════════════════════════════════════════════════════════════════════════
// Execution
// ═════════════════════════════════════════════════════════════════════════════

// Run executes all queued operations via the loopmount service subprocess.
func (b *LoopMountBackend) Run(ctx context.Context) error {
	winput := loopmountsvc.WireInput{
		Image:  b.rootfsPath,
		Action: "provision",
		FsType: b.fsType,
	}
	for _, o := range b.ops {
		switch o := o.(type) {
		case provcontent.FileOp:
			mode := o.Mode
			if mode == 0 {
				mode = infra.PublicKeyPerm
			}
			winput.Ops.Files = append(winput.Ops.Files, loopmountsvc.WireFileOp{
				Path: o.Path,
				Data: base64.StdEncoding.EncodeToString(o.Data),
				Mode: mode,
				UID:  o.UID,
				GID:  o.GID,
			})
		case provcontent.ChrootOp:
			winput.Ops.Commands = append(winput.Ops.Commands, o.Command)
		case provcontent.CopyDirOp:
			winput.Ops.CopyDirs = append(winput.Ops.CopyDirs, loopmountsvc.WireCopyDirOp{
				Src: o.Src, Dst: o.Dst, Mode: infra.ExecutablePerm,
			})
		case provcontent.ResizeOp:
			winput.Ops.Resize = &loopmountsvc.WireResizeOp{
				Action: string(o.Action), Bytes: o.Bytes,
			}
		}
	}

	_, err := runWireOp(ctx, &winput)
	return err
}

// ═════════════════════════════════════════════════════════════════════════════
// Other Backend interface methods
// ═════════════════════════════════════════════════════════════════════════════

// ConvertTo converts the image filesystem to targetFS via loop-mount subprocess.
// On success, updates b.fsType so subsequent operations (deblob, shrink) use
// the correct filesystem type for mounting.
func (b *LoopMountBackend) ConvertTo(ctx context.Context, targetFS string) error {
	_, err := runWireOp(ctx, &loopmountsvc.WireInput{
		Image:    b.rootfsPath,
		Action:   "convert_fs",
		FsType:   b.fsType,
		TargetFS: targetFS,
	})
	if err != nil {
		return err
	}
	b.fsType = targetFS
	return nil
}

// ExtractPartition extracts root partition from a raw disk image.
// Uses sfdisk/parted for partition table parsing and dd for extraction.
func (b *LoopMountBackend) ExtractPartition(
	ctx context.Context,
	rawPath string,
	outputPath string,
	partition int,
	disabledDetectors []string,
) (string, error) {
	fsType := system.DetectFilesystemType(ctx, rawPath)
	if fsType == "ext4" || fsType == "ext3" || fsType == "ext2" ||
		fsType == "btrfs" || fsType == "xfs" {
		slog.Info("Image is filesystem, using as-is", "type", fsType)
		result, _ := system.DefaultRunner.Run(
			ctx,
			[]string{"cp", "--sparse=always", rawPath, outputPath},
			system.RunCmdOpts{Capture: true, Check: false},
		)
		if !result.Success() {
			if err := system.CopyBytesDD(ctx, rawPath, outputPath, 0, 0); err != nil {
				return "", err
			}
		}
		ext, ok := infra.FSTypeToExt[fsType]
		if !ok {
			ext = ".img"
		}
		finalPath := outputPath[:len(outputPath)-len(filepath.Ext(outputPath))] + ext
		if err := infra.SafeMove(outputPath, finalPath); err != nil {
			return "", fmt.Errorf("rename output to %s: %w", finalPath, err)
		}
		return finalPath, nil
	}

	parsed := parsePartitionsSfdisk(ctx, rawPath, partition)
	if parsed == nil {
		parsed = parsePartitionsParted(ctx, rawPath, partition)
	}
	if parsed == nil {
		return "", fmt.Errorf("Failed to parse partition table: neither sfdisk nor parted is available or succeeded")
	}

	if parsed.noPartitionTable {
		slog.Info("No partition table found, using image as-is")
		if err := infra.SafeMove(rawPath, outputPath); err != nil {
			return "", fmt.Errorf("rename raw to output: %w", err)
		}
		return outputPath, nil
	}

	partitions := parsed.partitions
	requestedPartition := parsed.requestedPartition
	if len(partitions) == 0 {
		slog.Info("No partitions found, using image as-is")
		if err := infra.SafeMove(rawPath, outputPath); err != nil {
			return "", fmt.Errorf("rename raw to output: %w", err)
		}
		return outputPath, nil
	}

	var chosen partitionEntry
	partitionNum := 1
	if len(partitions) > 1 && requestedPartition == 0 {
		slog.Info("Found partitions", "count", len(partitions))
		for i, p := range partitions {
			slog.Debug("Partition", "index", i+1, "start", p.Start, "size", p.Size, "type", p.Type)
		}
		diskPartitions := make([]disk.Partition, len(partitions))
		for i, p := range partitions {
			diskPartitions[i] = disk.Partition{
				Node:   p.Node,
				Size:   p.Size,
				Type:   p.Type,
				Fstype: p.Fstype,
				Start:  p.Start,
			}
		}
		detector := disk.NewRootPartitionDetector(disabledDetectors)
		chosenIdx, err := detector.Detect(diskPartitions)
		if err != nil {
			return "", fmt.Errorf("root partition detection: %w", err)
		}
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
		chosen = partitions[requestedPartition-1]
		partitionNum = requestedPartition
	} else {
		chosen = partitions[0]
		partitionNum = 1
	}

	startSector := max(chosen.Start, int64(0))
	var sectorCount int64
	if chosen.Size > 0 {
		sectorCount = chosen.Size
	}
	skipBytes := startSector * sectorSize
	var countBytes int64
	if sectorCount > 0 {
		countBytes = sectorCount * sectorSize
	}

	rawFileInfo, err := os.Stat(rawPath)
	if err != nil {
		return "", fmt.Errorf("stat raw image: %w", err)
	}
	if skipBytes >= rawFileInfo.Size() {
		return "", fmt.Errorf("Partition %d start sector (%d) offset (%d bytes) exceeds file size (%d bytes)",
			partitionNum, startSector, skipBytes, rawFileInfo.Size())
	}

	slog.Info("Extracting partition", "partition", partitionNum, "start_sector", startSector, "offset", skipBytes)
	if err := system.CopyBytesDD(ctx, rawPath, outputPath, skipBytes, countBytes); err != nil {
		return "", err
	}
	outputPath = detectAndRenameFS(ctx, outputPath)
	slog.Info("Extracted partition", "path", filepath.Base(outputPath))
	return outputPath, nil
}
