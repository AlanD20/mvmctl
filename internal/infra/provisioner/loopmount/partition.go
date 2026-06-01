package loopmount

import "time"

const (
	sectorSize    int64 = 512
	partedTimeout       = 15 * time.Second
)

// ── Partition parsing types (used by sfdisk/parted parsers) ──

// partitionEntry represents a parsed partition entry from sfdisk or parted.
type partitionEntry struct {
	Start  int64
	Size   int64
	Type   string
	Node   string
	Fstype string
}

// parseResult is the result of partition table parsing.
// Can be a list of partitions + requested partition number, or the
// "no partition table" sentinel.
type parseResult struct {
	partitions         []partitionEntry
	requestedPartition int
	noPartitionTable   bool
}

// noPartitionTableSentinel is the package-private "no partition table" marker.
// Matches Python's _NO_PARTITION_TABLE singleton.
var noPartitionTableSentinel = &parseResult{noPartitionTable: true}
