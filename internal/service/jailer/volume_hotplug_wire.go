package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
)

const (
	VMVolumeAttachAction  = "jailer-vm-volume-attach"
	VMVolumeDetachAction  = "jailer-vm-volume-detach"
	VMVolumeObserveAction = "jailer-vm-volume-observe"
)

// VMVolumeAttachRequest selects one raw volume. CacheRoot is only the namespace
// locator used to recover the exact cache identity committed at VM launch.
type VMVolumeAttachRequest struct {
	VMID      string         `json:"vm_id"`
	CacheRoot string         `json:"cache_root"`
	Volume    VMLaunchVolume `json:"volume"`
}

func (request VMVolumeAttachRequest) Validate() error {
	if err := (VMCleanupRequest{VMID: request.VMID}).Validate(); err != nil {
		return err
	}
	if _, err := parseManagedCacheLocator(request.CacheRoot); err != nil {
		return err
	}
	return request.Volume.Validate()
}

func (request *VMVolumeAttachRequest) UnmarshalJSON(raw []byte) error {
	type wire VMVolumeAttachRequest
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return instanceValidationError("volume attach contains trailing JSON")
	}
	*request = VMVolumeAttachRequest(value)
	return request.Validate()
}

// VMVolumeDetachRequest never accepts a source path or a drive configuration.
// The receiver recovers that intent from its own committed volume inventory.
type VMVolumeDetachRequest struct {
	VMID     string              `json:"vm_id"`
	VolumeID string              `json:"volume_id"`
	Observed VMVolumeObservation `json:"observed"`
}

func (request VMVolumeDetachRequest) Validate() error {
	if err := (VMCleanupRequest{VMID: request.VMID}).Validate(); err != nil {
		return err
	}
	if !isCanonicalManagedResourceID(request.VolumeID, 64) {
		return instanceValidationError("volume ID must be 64 lowercase hexadecimal characters")
	}
	return request.Observed.Validate(request.VMID, request.VolumeID)
}

// VMVolumeObserveRequest selects root-owned identity without changing it.
type VMVolumeObserveRequest struct {
	VMID     string `json:"vm_id"`
	VolumeID string `json:"volume_id"`
}

// Validate admits the closed VM and logical volume selection.
func (request VMVolumeObserveRequest) Validate() error {
	if err := (VMCleanupRequest{VMID: request.VMID}).Validate(); err != nil {
		return err
	}
	if !isCanonicalManagedResourceID(request.VolumeID, 64) {
		return instanceValidationError("volume ID must be 64 lowercase hexadecimal characters")
	}
	return nil
}

// VMVolumeObservation binds one guest release to the receiver's observed attachment.
// CRITICAL: The detach receiver compares every field freshly under the VM lock.
type VMVolumeObservation struct {
	VMID             string `json:"vm_id"`
	VolumeID         string `json:"volume_id"`
	LaunchGeneration uint64 `json:"launch_generation"`
	Attached         bool   `json:"attached"`
	GuestDriveID     string `json:"guest_drive_id"`
	Serial           string `json:"serial"`
	ExpectedBytes    uint64 `json:"expected_bytes"`
	MountID          uint64 `json:"mount_id"`
	SourceMajor      uint32 `json:"source_major"`
	SourceMinor      uint32 `json:"source_minor"`
	SourceInode      uint64 `json:"source_inode"`
}

// Validate checks response identity and complete attachment evidence.
func (result VMVolumeObservation) Validate(vmID, volumeID string) error {
	if result.VMID != vmID || result.VolumeID != volumeID || result.LaunchGeneration == 0 {
		return instanceValidationError("volume observation differs from requested identity")
	}
	if result.Attached {
		if !isCanonicalManagedResourceID(result.GuestDriveID, 64) || result.Serial == "" || len(result.Serial) > 20 ||
			result.ExpectedBytes == 0 ||
			result.MountID == 0 ||
			result.SourceInode == 0 {
			return instanceValidationError("volume observation has incomplete attachment identity")
		}
	} else if result.GuestDriveID != "" || result.Serial != "" || result.ExpectedBytes != 0 || result.MountID != 0 || result.SourceMajor != 0 || result.SourceMinor != 0 || result.SourceInode != 0 {
		return instanceValidationError("absent volume observation carries attachment identity")
	}
	return nil
}

// ObserveVolume obtains authoritative attachment identity without journal repair.
func (client *VMLifecycleClient) ObserveVolume(
	ctx context.Context,
	request VMVolumeObserveRequest,
) (VMVolumeObservation, error) {
	if err := request.Validate(); err != nil {
		return VMVolumeObservation{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMVolumeObserveAction, request, nil, 0,
		func(result VMVolumeObservation) error { return result.Validate(request.VMID, request.VolumeID) })
}

type VMVolumeResult struct {
	VMID             string `json:"vm_id"`
	VolumeID         string `json:"volume_id"`
	LaunchGeneration uint64 `json:"launch_generation"`
	Attached         bool   `json:"attached"`
}

func (result VMVolumeResult) Validate(vmID, volumeID string, attached bool) error {
	if result.VMID != vmID || result.VolumeID != volumeID || result.Attached != attached ||
		result.LaunchGeneration == 0 {
		return instanceValidationError("volume response does not match its requested VM, volume, and outcome")
	}
	return nil
}

func (client *VMLifecycleClient) AttachVolume(
	ctx context.Context,
	request VMVolumeAttachRequest,
) (VMVolumeResult, error) {
	if err := request.Validate(); err != nil {
		return VMVolumeResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMVolumeAttachAction, request, nil, 0,
		func(result VMVolumeResult) error { return result.Validate(request.VMID, request.Volume.ID, true) })
}

func (client *VMLifecycleClient) DetachVolume(
	ctx context.Context,
	request VMVolumeDetachRequest,
) (VMVolumeResult, error) {
	if err := request.Validate(); err != nil {
		return VMVolumeResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMVolumeDetachAction, request, nil, 0,
		func(result VMVolumeResult) error { return result.Validate(request.VMID, request.VolumeID, false) })
}
