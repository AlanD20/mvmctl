package jailer

import "math"

const (
	// ProtectedVolumeInspectAction selects authoritative raw-volume accounting.
	ProtectedVolumeInspectAction = "jailer-volume-inspect"
	// ProtectedVolumeGrowAction grows the current authoritative raw-volume backing.
	ProtectedVolumeGrowAction = "jailer-volume-grow"
	// ProtectedVolumeRemoveAction removes an unreferenced volume and retains its tombstone.
	ProtectedVolumeRemoveAction = "jailer-volume-remove"
)

// ProtectedVolumeInspectRequest selects a volume; CacheRoot is only an admission locator.
type ProtectedVolumeInspectRequest struct {
	CacheRoot string `json:"cache_root"`
	VolumeID  string `json:"volume_id"`
}

// ProtectedVolumeGrowRequest selects an absolute logical target, never a path or allocation command.
type ProtectedVolumeGrowRequest struct {
	CacheRoot   string `json:"cache_root"`
	VolumeID    string `json:"volume_id"`
	TargetBytes uint64 `json:"target_bytes"`
}

// ProtectedVolumeRemoveRequest grants no bypass for retained VM references.
type ProtectedVolumeRemoveRequest struct {
	CacheRoot string `json:"cache_root"`
	VolumeID  string `json:"volume_id"`
}

// ProtectedVolumeInspectResult reports the currently authoritative backing, not the import source.
type ProtectedVolumeInspectResult struct {
	VolumeID       string `json:"volume_id"`
	LogicalBytes   uint64 `json:"logical_bytes"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
	Protected      bool   `json:"protected"`
}

// ProtectedVolumeRemoveResult acknowledges durable removal, including an existing tombstone.
type ProtectedVolumeRemoveResult struct {
	VolumeID string `json:"volume_id"`
	Removed  bool   `json:"removed"`
}

func validateProtectedVolumeSelection(cacheRoot, id string) error {
	if !isCanonicalManagedResourceID(id, 64) {
		return managedVMResourceValidationError("volume ID must be 64 lowercase hexadecimal characters")
	}
	_, err := parseManagedCacheLocator(cacheRoot)
	return err
}

// Validate checks the complete closed inspection selection.
func (request ProtectedVolumeInspectRequest) Validate() error {
	return validateProtectedVolumeSelection(request.CacheRoot, request.VolumeID)
}

// Validate rejects nonrepresentable file extents without imposing a product storage quota.
func (request ProtectedVolumeGrowRequest) Validate() error {
	if err := validateProtectedVolumeSelection(request.CacheRoot, request.VolumeID); err != nil {
		return err
	}
	if request.TargetBytes == 0 || request.TargetBytes > math.MaxInt64 {
		return managedVMResourceValidationError("volume growth requires a positive representable logical size")
	}
	return nil
}

// Validate checks the complete closed removal selection.
func (request ProtectedVolumeRemoveRequest) Validate() error {
	return validateProtectedVolumeSelection(request.CacheRoot, request.VolumeID)
}

// Validate binds observed accounting to the selected volume.
func (result ProtectedVolumeInspectResult) Validate(volumeID string) error {
	if result.VolumeID != volumeID || !isCanonicalManagedResourceID(result.VolumeID, 64) ||
		result.LogicalBytes == 0 || result.LogicalBytes > math.MaxInt64 {
		return managedVMResourceValidationError("volume inspection result is invalid or belongs to another volume")
	}
	return nil
}

// Validate requires an authoritative successful removal for the selected volume.
func (result ProtectedVolumeRemoveResult) Validate(volumeID string) error {
	if result.VolumeID != volumeID || !isCanonicalManagedResourceID(result.VolumeID, 64) || !result.Removed {
		return managedVMResourceValidationError("volume removal result is invalid or belongs to another volume")
	}
	return nil
}
