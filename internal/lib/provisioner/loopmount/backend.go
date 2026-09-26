package loopmount

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"

	loopmountsvc "mvmctl/internal/service/loopmount"
)

// LoopMountBackend implements the Backend interface using loop-mount
// authority for managed VMs. Image effects use the image domain's canonical staging producer;
// partition extraction stays in the normal process and never creates a privileged raw-path request.
type LoopMountBackend struct {
	rootfsPath    string
	fsType        string
	cacheDir      string
	managedVM     bool
	vmID          string
	plan          loopmountsvc.VMProvisionPlan
	pending       bool
	expectedBytes uint64
}

// NewLoopMountBackend creates a new LoopMountBackend.
func NewLoopMountBackend(ctx context.Context, rootfsPath string, fsType string, cacheDir string) *LoopMountBackend {
	backend := &LoopMountBackend{rootfsPath: rootfsPath, fsType: fsType, cacheDir: cacheDir}
	relative, err := filepath.Rel(cacheDir, rootfsPath)
	if err == nil && strings.HasPrefix(relative, "vms"+string(filepath.Separator)) {
		backend.managedVM = true
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) == 3 && parts[0] == "vms" && parts[2] == "rootfs.img" {
			backend.vmID = parts[1]
		}
	}
	return backend
}

// --- Builder methods -- queue provisioning operations ---

func (b *LoopMountBackend) Resize(ctx context.Context, targetSizeBytes int64) error {
	if targetSizeBytes <= 0 {
		return errs.New(
			errs.CodeValidationFailed,
			"managed VM resize requires a positive grow target; image shrink requires managed image staging",
		)
	}
	b.plan.ResizeBytes, b.pending = uint64(targetSizeBytes), true
	return nil
}

func (b *LoopMountBackend) SetHostname(ctx context.Context, hostname string) error {
	b.plan.Hostname, b.pending = &hostname, true
	return nil
}

func (b *LoopMountBackend) InjectDNS(ctx context.Context, dnsServer string) error {
	b.plan.DNS, b.pending = &dnsServer, true
	return nil
}

func (b *LoopMountBackend) SetupSSH(ctx context.Context, user string, sshPubkeys []string) error {
	b.plan.SSH = &loopmountsvc.SSHSetup{User: user, PublicKeys: append([]string(nil), sshPubkeys...)}
	b.pending = true
	return nil
}

func (b *LoopMountBackend) SetupSudo(ctx context.Context, user string) error {
	b.plan.SudoUser, b.pending = &user, true
	return nil
}

func (b *LoopMountBackend) DisableCloudInit(ctx context.Context) error {
	return nil // no-op — cloud-init is disabled during image import, not VM creation
}

func (b *LoopMountBackend) InjectCloudInit(ctx context.Context, cloudInitDir string) error {
	if !b.managedVM || cloudInitDir != filepath.Join(b.cacheDir, "vms", b.vmID, "cloud-init") {
		return errs.New(errs.CodeValidationFailed, "managed VM cloud-init must use its fixed seed directory")
	}
	b.plan.InjectCloudInit, b.pending = true, true
	return nil
}

// DetectOS requires the image domain's canonical staging producer for LoopMount images.
func (b *LoopMountBackend) DetectOS(ctx context.Context) (string, error) {
	return "", errs.New(
		errs.CodeValidationFailed,
		"LoopMount OS inspection requires managed image staging; VM provisioning uses its image OS metadata",
	)
}

// Deblob queues deblob (OS cache cleanup) operations.
func (b *LoopMountBackend) Deblob(ctx context.Context, osType *string) error {
	if osType == nil || *osType == "" {
		return errs.New(errs.CodeValidationFailed, "managed VM deblob requires its image OS metadata")
	}
	value := *osType
	b.plan.DeblobOS, b.pending = &value, true
	return nil
}

func (b *LoopMountBackend) FixFstab(ctx context.Context) error {
	b.plan.FixFstab, b.pending = true, true
	return nil
}

func (b *LoopMountBackend) Shrink(ctx context.Context) error {
	return b.Resize(ctx, 0)
}

// InjectAgent queues the vsock guest agent files into the rootfs.
func (b *LoopMountBackend) InjectAgent(_ context.Context, agentBinary []byte, port int, token string) error {
	b.plan.Agent = &loopmountsvc.AgentSetup{Port: port, Token: token}
	b.pending = true
	return nil
}

// --- Execution ---

// Run executes the exact queued managed-VM plan. An error retains the plan for replay.
func (b *LoopMountBackend) Run(ctx context.Context) error {
	if !b.managedVM {
		return errs.New(
			errs.CodeValidationFailed,
			"LoopMount effects require a canonical managed VM or image staging resource",
		)
	}
	return b.runManagedVM(ctx)
}

func (b *LoopMountBackend) runManagedVM(ctx context.Context) error {
	if !b.pending {
		return nil
	}
	if b.expectedBytes == 0 {
		info, err := os.Stat(b.rootfsPath)
		if err != nil {
			return errs.WrapMsg(errs.CodeLoopMountError, "inspect managed VM rootfs before provisioning", err)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return errs.New(errs.CodeValidationFailed, "managed VM rootfs must be a nonempty regular file")
		}
		b.expectedBytes = uint64(info.Size())
	}
	request := loopmountsvc.VMProvisionRequest{
		CacheLocator: b.cacheDir, VMID: b.vmID, ExpectedRootfsBytes: b.expectedBytes,
		Filesystem: loopmountsvc.Filesystem(b.fsType), Plan: b.plan,
	}
	result, err := loopmountsvc.NewClient().ProvisionVM(ctx, request)
	if err != nil {
		return err // Keep the exact old-size intent for a convergent retry; never downgrade to raw sudo.
	}
	b.expectedBytes = result.RootfsBytes
	b.plan, b.pending = loopmountsvc.VMProvisionPlan{}, false
	return nil
}

// --- Other Backend interface methods ---

// ConvertTo requires the image domain's durable staged replacement transaction.
func (b *LoopMountBackend) ConvertTo(ctx context.Context, targetFS string) error {
	return errs.New(
		errs.CodeValidationFailed,
		"LoopMount filesystem conversion requires managed image staging before VM cloning",
	)
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
