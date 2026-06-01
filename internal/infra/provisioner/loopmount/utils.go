package loopmount

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// ── Partition table parsing (sfdisk + parted) ───────────────────────────────

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
func parsePartitionsSfdisk(ctx context.Context, rawPath string, partition int) *parseResult {
	result := system.RunCmdCompat(ctx, []string{"sfdisk", "--json", rawPath}, system.RunCmdOpts{
		Check: false, Capture: true, Timeout: partedTimeout,
	})
	if result.ExitCode != 0 {
		return noPartitionTableSentinel
	}

	var table sfdiskTable
	if err := json.Unmarshal([]byte(result.Stdout), &table); err != nil {
		return nil
	}
	if table.PartitionTable == nil || len(table.PartitionTable.Partitions) == 0 {
		return noPartitionTableSentinel
	}

	partitions := make([]partitionEntry, 0, len(table.PartitionTable.Partitions))
	for _, p := range table.PartitionTable.Partitions {
		partitions = append(partitions, partitionEntry{
			Start: int64(p.Start), Size: int64(p.Size), Type: p.Type, Node: p.Node,
		})
	}
	return &parseResult{partitions: partitions, requestedPartition: partition}
}

// parsePartitionsParted parses partition table using parted as fallback.
func parsePartitionsParted(ctx context.Context, rawPath string, partition int) *parseResult {
	result := system.RunCmdCompat(ctx, []string{"parted", "-sm", rawPath, "unit", "B", "print"}, system.RunCmdOpts{
		Check: false, Capture: true, Timeout: partedTimeout,
	})
	if result.ExitCode != 0 {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) == 0 || lines[0] != "BYT;" {
		return nil
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
		return noPartitionTableSentinel
	}
	return &parseResult{partitions: partitions, requestedPartition: partition}
}

// ── Post-extraction helpers ──────────────────────────────────────────────────

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
