package jailer

import (
	"mvmctl/internal/infra"
)

// The complete serialized file includes Firecracker's trailing CRC; no extra allowance applies.
const managedSnapshotVMStateMaxBytes uint64 = 10_000_000

// managedSnapshotMemoryBytes accepts captured root authority only; caller SQLite RAM is not evidence.
func managedSnapshotMemoryBytes(capturedRAMMiB int) (uint64, error) {
	if capturedRAMMiB < infra.MemMinMB || capturedRAMMiB > infra.MemMaxMB {
		return 0, managedVMResourceValidationError("managed snapshot captured RAM is outside the supported range")
	}
	const bytesPerMiB uint64 = 1 << 20
	ram := uint64(capturedRAMMiB)
	if ram > ^uint64(0)/bytesPerMiB {
		return 0, managedVMResourceValidationError("managed snapshot captured RAM extent overflows")
	}
	return ram * bytesPerMiB, nil
}
