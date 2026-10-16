package jailer

import (
	"path/filepath"
	"strconv"

	"mvmctl/internal/lib/system"
)

// VMRootfsExportAction copies one root-authoritative VM disk for image import.
const VMRootfsExportAction = system.PrivilegedVMRootfsExportAction

// VMRootfsExportReleaseAction releases only an owned independent export copy.
const VMRootfsExportReleaseAction = "jailer-vm-rootfs-export-release"

// VMRootfsExportDataRoot is separate from all operator-forbidden backing stores.
const VMRootfsExportDataRoot = "/var/lib/mvmctl/vm-rootfs-export-data"

// VMRootfsExportRequest grants only a VM selection and a derived image-copy identity.
type VMRootfsExportRequest struct {
	VMID      string `json:"vm_id"`
	CacheRoot string `json:"cache_root"`
	ImageID   string `json:"image_id"`
}

// Validate rejects caller-selected effect paths and noncanonical identities.
func (request VMRootfsExportRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) || !isCanonicalManagedResourceID(request.ImageID, 64) {
		return instanceValidationError("VM rootfs export identity is not canonical")
	}
	_, err := parseManagedCacheLocator(request.CacheRoot)
	return err
}

// VMRootfsExportResult describes an independent copy after source state restoration.
type VMRootfsExportResult struct {
	OwnerUID      uint32 `json:"owner_uid"`
	VMID          string `json:"vm_id"`
	ImageID       string `json:"image_id"`
	Bytes         uint64 `json:"bytes"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	OriginalState string `json:"original_state"`
	FinalState    string `json:"final_state"`
}

// Validate binds the completed copy and restored state to the exact request.
func (result VMRootfsExportResult) Validate(request VMRootfsExportRequest) error {
	if result.OwnerUID == 0 || result.VMID != request.VMID || result.ImageID != request.ImageID || result.Bytes == 0 ||
		result.Bytes > managedVMRootfsMaxBytes || result.Inode == 0 ||
		!validVMRootfsExportState(result.OriginalState) || result.FinalState != result.OriginalState {
		return instanceValidationError("VM rootfs export result is incomplete or source state was not restored")
	}
	return nil
}

// CopyPath derives the sole image-import leaf; it never selects protected backing.
func (result VMRootfsExportResult) CopyPath() string {
	return filepath.Join(
		VMRootfsExportDataRoot,
		strconv.FormatUint(uint64(result.OwnerUID), 10),
		result.ImageID,
		"disk.raw",
	)
}

// VMRootfsExportReleaseRequest selects no source VM or filesystem path.
type VMRootfsExportReleaseRequest struct {
	ImageID string `json:"image_id"`
}

func (request VMRootfsExportReleaseRequest) Validate() error {
	if !isCanonicalManagedResourceID(request.ImageID, 64) {
		return instanceValidationError("VM export release requires an exact image ID")
	}
	return nil
}

// VMRootfsExportReleaseResult never projects historical intent as current VM state.
type VMRootfsExportReleaseResult struct {
	ImageID               string `json:"image_id"`
	Found                 bool   `json:"found"`
	Released              bool   `json:"released"`
	SourceVMID            string `json:"source_vm_id,omitempty"`
	SourceRecoveryPending bool   `json:"source_recovery_pending"`
}

func (result VMRootfsExportReleaseResult) Validate(request VMRootfsExportReleaseRequest) error {
	if result.ImageID != request.ImageID || !result.Released ||
		(result.Found && !vmIDPattern.MatchString(result.SourceVMID)) ||
		(!result.Found && (result.SourceVMID != "" || result.SourceRecoveryPending)) {
		return instanceValidationError("VM export release result differs from request")
	}
	return nil
}

func validVMRootfsExportState(state string) bool {
	return state == "Running" || state == "Paused" || state == "Stopped"
}
