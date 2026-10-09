package loopmount

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// --- Partition table parsing (sfdisk + parted) ---

// sfdiskTable mirrors the JSON output of "sfdisk --json <image>".
type sfdiskTable struct {
	PartitionTable *struct {
		Partitions []struct {
			Start float64 `json:"start"`
			Size  float64 `json:"size"`
			Type  string  `json:"type"`
			Node  string  `json:"node"`
		} `json:"partitions"`
	} `json:"partitiontable"`
}

// parsePartitionsSfdisk parses partition table using sfdisk --json.
func parsePartitionsSfdisk(ctx context.Context, rawPath string, partition int) (*parseResult, error) {
	result, err := system.DefaultRunner.Run(ctx, []string{"sfdisk", "--json", rawPath}, system.RunCmdOpts{
		Check: false, Capture: true, Timeout: partedTimeout,
	})
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "sfdisk partition probe failed", err)
	}
	if !result.Success() {
		return nil, errs.New(errs.CodeProcessError, fmt.Sprintf(
			"sfdisk partition probe failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Stderr),
		))
	}

	var table sfdiskTable
	if err := json.Unmarshal([]byte(result.Stdout), &table); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "invalid sfdisk partition table", err)
	}
	if table.PartitionTable == nil || len(table.PartitionTable.Partitions) == 0 {
		return noPartitionTableSentinel, nil
	}

	partitions := make([]partitionEntry, 0, len(table.PartitionTable.Partitions))
	for _, p := range table.PartitionTable.Partitions {
		partitions = append(partitions, partitionEntry{
			Start: int64(p.Start), Size: int64(p.Size), Type: p.Type, Node: p.Node,
		})
	}
	return &parseResult{partitions: partitions, requestedPartition: partition}, nil
}

// parsePartitionsParted parses partition table using parted as fallback.
func parsePartitionsParted(ctx context.Context, rawPath string, partition int) (*parseResult, error) {
	result, err := system.DefaultRunner.Run(
		ctx,
		[]string{"parted", "-sm", rawPath, "unit", "B", "print"},
		system.RunCmdOpts{
			Check: false, Capture: true, Timeout: partedTimeout,
		},
	)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "parted partition probe failed", err)
	}
	if !result.Success() {
		return nil, errs.New(errs.CodeProcessError, fmt.Sprintf(
			"parted partition probe failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Stderr),
		))
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) < 2 || lines[0] != "BYT;" {
		return nil, errs.New(errs.CodeProcessError, "invalid parted partition table")
	}

	var partitions []partitionEntry
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
		startStr := strings.TrimSuffix(strings.TrimSpace(parts[1]), "B")
		sizeStr := strings.TrimSuffix(strings.TrimSpace(parts[3]), "B")
		startBytes, err1 := strconv.ParseInt(startStr, 10, 64)
		sizeBytes, err2 := strconv.ParseInt(sizeStr, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		partitions = append(partitions, partitionEntry{
			Start: startBytes / sectorSize, Size: sizeBytes / sectorSize,
			Type: strings.TrimSpace(parts[5]), Node: strings.TrimSpace(parts[0]),
			Fstype: strings.TrimSpace(parts[4]),
		})
	}

	if len(partitions) == 0 {
		return noPartitionTableSentinel, nil
	}
	return &parseResult{partitions: partitions, requestedPartition: partition}, nil
}

// --- Post-extraction helpers ---

// detectAndRenameFS detects filesystem type via blkid and renames output file.
func detectAndRenameFS(ctx context.Context, outputPath string) string {
	fsType := system.DetectFilesystemType(ctx, outputPath)
	if fsType == "" {
		return outputPath
	}
	ext, ok := infra.FSTypeToExt[fsType]
	if !ok {
		return outputPath
	}
	finalPath := outputPath[:len(outputPath)-len(filepath.Ext(outputPath))] + ext
	if err := os.Rename(outputPath, finalPath); err == nil {
		return finalPath
	}
	return outputPath
}
