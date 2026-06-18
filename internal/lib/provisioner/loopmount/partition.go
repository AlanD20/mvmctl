package loopmount

import "time"

const (
	sectorSize    int64 = 512
	partedTimeout       = 15 * time.Second
)

// --- Partition parsing types (sfdisk/parted) ---

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
var noPartitionTableSentinel = &parseResult{noPartitionTable: true}
