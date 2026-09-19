package jailer

import (
	"mvmctl/internal/lib/model"
)

const (
	// VMSnapshotCaptureAction creates an exclusively captured immutable snapshot.
	VMSnapshotCaptureAction = "jailer-snapshot-capture"
	// VMSnapshotInspectAction returns only the root-authenticated display projection.
	VMSnapshotInspectAction = "jailer-snapshot-inspect"
	// VMSnapshotRemoveAction removes an unreferenced exact immutable capture.
	VMSnapshotRemoveAction = "jailer-snapshot-remove"
	// VMSnapshotRestoreAction launches from one immutable root-authenticated capture.
	VMSnapshotRestoreAction = "jailer-snapshot-restore"
)

// VMSnapshotRequest selects a root-controlled snapshot below one admitted cache.
type VMSnapshotRequest struct {
	CacheRoot  string `json:"cache_root"`
	SnapshotID string `json:"snapshot_id"`
}

// Validate rejects path-shaped snapshot identities; CacheRoot is only a locator.
func (request VMSnapshotRequest) Validate() error {
	if !isCanonicalManagedResourceID(request.SnapshotID, 64) {
		return instanceValidationError("snapshot identity is not canonical")
	}
	_, err := parseManagedCacheLocator(request.CacheRoot)
	return err
}

// VMSnapshotCaptureRequest grants no producer paths, machine state, or backing identity.
type VMSnapshotCaptureRequest struct {
	CacheRoot  string `json:"cache_root"`
	SnapshotID string `json:"snapshot_id"`
	VMID       string `json:"vm_id"`
	PauseOnly  bool   `json:"pause_only"`
}

// Validate checks the closed capture selectors before receiver authority acquisition.
func (request VMSnapshotCaptureRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return instanceValidationError("snapshot source VM identity is not canonical")
	}
	return (VMSnapshotRequest{CacheRoot: request.CacheRoot, SnapshotID: request.SnapshotID}).Validate()
}

// VMSnapshotRemoveResult reports removal only after the protected entries are synced.
type VMSnapshotRemoveResult struct {
	SnapshotID string `json:"snapshot_id"`
	Removed    bool   `json:"removed"`
}

// VMSnapshotRestoreRequest selects a target identity, never captured machine or network state.
type VMSnapshotRestoreRequest struct {
	CacheRoot  string `json:"cache_root"`
	SnapshotID string `json:"snapshot_id"`
	VMID       string `json:"vm_id"`
	Resume     bool   `json:"resume"`
}

// Validate checks canonical identities before immutable capture admission.
func (request VMSnapshotRestoreRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return instanceValidationError("restored VM identity is not canonical")
	}
	return (VMSnapshotRequest{CacheRoot: request.CacheRoot, SnapshotID: request.SnapshotID}).Validate()
}

// Validate binds the successful removal projection to its exact request.
func (result VMSnapshotRemoveResult) Validate(request VMSnapshotRequest) error {
	if result.SnapshotID != request.SnapshotID || !result.Removed {
		return instanceValidationError("snapshot removal result differs from request")
	}
	return nil
}

func validateSnapshotProjection(result model.SnapshotCaptureResult, snapshotID string) error {
	if result.SnapshotID != snapshotID || !vmIDPattern.MatchString(result.SourceVMID) ||
		(result.Mode != "full" && result.Mode != "disks") || result.RootfsBytes == 0 || result.VCPUCount < 1 {
		return instanceValidationError("snapshot result does not match the requested capture")
	}
	if _, err := managedSnapshotMemoryBytes(result.MemSizeMiB); err != nil {
		return err
	}
	return (ReleaseSlot{Version: result.ReleaseVersion, Architecture: result.ReleaseArchitecture}).Validate()
}
