package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
)

const (
	VMVolumeAttachAction = "jailer-vm-volume-attach"
	VMVolumeDetachAction = "jailer-vm-volume-detach"
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
	VMID     string `json:"vm_id"`
	VolumeID string `json:"volume_id"`
}

func (request VMVolumeDetachRequest) Validate() error {
	if err := (VMCleanupRequest{VMID: request.VMID}).Validate(); err != nil {
		return err
	}
	if !isCanonicalManagedResourceID(request.VolumeID, 64) {
		return instanceValidationError("volume ID must be 64 lowercase hexadecimal characters")
	}
	return nil
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
