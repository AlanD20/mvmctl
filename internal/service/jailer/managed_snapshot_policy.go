package jailer

import (
	"context"
	"errors"
	"io"

	"golang.org/x/sys/unix"

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

// readBoundedManagedSnapshotState performs byte admission only. It does not authenticate capture,
// decode VM state, grant restore authority, or establish immutable memory backing.
func readBoundedManagedSnapshotState(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	fd int,
	ownerUID uint32,
) ([]byte, error) {
	if ctx == nil || fd < 0 || deps.statx == nil || deps.pread == nil {
		return nil, managedVMResourceAtomicError("managed snapshot state reader is incomplete", nil)
	}
	before, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return nil, err
	}
	if before.mode&unix.S_IFMT != unix.S_IFREG || before.uid != ownerUID || before.nlink != 1 ||
		before.mode&0400 != 0400 || before.mode&07133 != 0 {
		return nil, managedVMResourceValidationError("managed snapshot vmstate metadata is unsafe")
	}
	if before.size == 0 || before.size > managedSnapshotVMStateMaxBytes {
		return nil, managedVMResourceValidationError("managed snapshot complete vmstate exceeds loader size policy")
	}
	data := make([]byte, int(before.size))
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, managedVMResourceAtomicError("read managed snapshot vmstate", err)
		}
		count, readErr := deps.pread(ctx, fd, data[offset:], int64(offset))
		if count < 0 || count > len(data)-offset {
			return nil, managedVMResourceAtomicError("managed snapshot state reader returned an invalid count", nil)
		}
		offset += count
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, managedVMResourceAtomicError("read managed snapshot vmstate", readErr)
		}
		if count == 0 || (errors.Is(readErr, io.EOF) && offset < len(data)) {
			return nil, managedVMResourceValidationError("managed snapshot vmstate changed while reading")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, managedVMResourceAtomicError("probe managed snapshot vmstate", err)
	}
	var probe [1]byte
	count, probeErr := deps.pread(ctx, fd, probe[:], int64(len(data)))
	if probeErr != nil && !errors.Is(probeErr, io.EOF) {
		return nil, managedVMResourceAtomicError("probe managed snapshot vmstate", probeErr)
	}
	if count != 0 {
		return nil, managedVMResourceValidationError("managed snapshot vmstate changed while reading")
	}
	after, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, managedVMResourceValidationError("managed snapshot vmstate changed while reading")
	}
	return data, nil
}
