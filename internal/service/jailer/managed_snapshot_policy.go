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

type managedSnapshotCaptureMode uint8

const (
	managedSnapshotRootfsOnly managedSnapshotCaptureMode = iota + 1
	managedSnapshotFull
)

// managedSnapshotCaptureMetadata records the resource policy inputs for a capture.
// CRITICAL: This value is not restore authority. A root-owned capture record must authenticate
// it and bind immutable artifact identities before any restore consumer may use these bounds.
type managedSnapshotCaptureMetadata struct {
	snapshotID     string
	sourceVMID     vmResourceID
	release        releaseIdentity
	mode           managedSnapshotCaptureMode
	rootfsBytes    uint64
	capturedRAMMiB int
}

func newManagedSnapshotCaptureMetadata(
	snapshotID string,
	source vmLaunchResourceSpec,
	release releaseIdentity,
	mode managedSnapshotCaptureMode,
) (managedSnapshotCaptureMetadata, error) {
	if err := validateVMLaunchResourceSpec(source); err != nil {
		return managedSnapshotCaptureMetadata{}, err
	}
	if len(source.firecracker.volumes) != 0 {
		return managedSnapshotCaptureMetadata{}, managedVMResourceValidationError(
			"managed snapshot data-volume capture semantics are not approved",
		)
	}
	metadata := managedSnapshotCaptureMetadata{
		snapshotID: snapshotID, sourceVMID: source.vmID, release: release, mode: mode,
		rootfsBytes: source.expectedRootfsBytes, capturedRAMMiB: source.firecracker.memSizeMiB,
	}
	if err := validateManagedSnapshotCaptureMetadata(metadata); err != nil {
		return managedSnapshotCaptureMetadata{}, err
	}
	return metadata, nil
}

func validateManagedSnapshotCaptureMetadata(metadata managedSnapshotCaptureMetadata) error {
	if !isCanonicalManagedResourceID(metadata.snapshotID, 64) ||
		!isCanonicalManagedResourceID(string(metadata.sourceVMID), 32) {
		return managedVMResourceValidationError("managed snapshot capture identities are invalid")
	}
	if err := validateManagedDeviceRelease(metadata.release); err != nil {
		return err
	}
	if metadata.rootfsBytes < managedVMRootfsMinBytes || metadata.rootfsBytes > managedVMRootfsMaxBytes {
		return managedVMResourceValidationError("managed snapshot captured rootfs size is outside the supported range")
	}
	if _, err := managedSnapshotMemoryBytes(metadata.capturedRAMMiB); err != nil {
		return err
	}
	switch metadata.mode {
	case managedSnapshotRootfsOnly, managedSnapshotFull:
		return nil
	default:
		return managedVMResourceValidationError("managed snapshot capture mode is unsupported")
	}
}

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

func validateManagedSnapshotArtifactSizes(
	metadata managedSnapshotCaptureMetadata,
	rootfsBytes uint64,
	memoryBytes uint64,
	stateBytes uint64,
) error {
	if err := validateManagedSnapshotCaptureMetadata(metadata); err != nil {
		return err
	}
	if rootfsBytes != metadata.rootfsBytes {
		return managedVMResourceValidationError("managed snapshot rootfs size does not match captured metadata")
	}
	if metadata.mode == managedSnapshotRootfsOnly {
		if memoryBytes != 0 || stateBytes != 0 {
			return managedVMResourceValidationError("managed rootfs-only snapshot has unexpected state artifacts")
		}
		return nil
	}
	expectedMemoryBytes, err := managedSnapshotMemoryBytes(metadata.capturedRAMMiB)
	if err != nil {
		return err
	}
	if memoryBytes != expectedMemoryBytes {
		return managedVMResourceValidationError("managed snapshot memory length does not match trusted captured RAM")
	}
	if stateBytes == 0 || stateBytes > managedSnapshotVMStateMaxBytes {
		return managedVMResourceValidationError("managed snapshot complete vmstate exceeds loader size policy")
	}
	return nil
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
