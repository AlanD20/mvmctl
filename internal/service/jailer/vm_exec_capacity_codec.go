package jailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type vmExecCapacityRecordWire struct {
	SchemaVersion               uint32 `json:"schema_version"`
	Kind                        string `json:"kind"`
	ReservationID               string `json:"reservation_id"`
	OwnerUID                    uint32 `json:"owner_uid"`
	HostBootID                  string `json:"host_boot_id"`
	SourceVMID                  string `json:"source_vm_id"`
	SourceLaunchGeneration      uint64 `json:"source_launch_generation"`
	DestinationVMID             string `json:"destination_vm_id"`
	DestinationLaunchGeneration uint64 `json:"destination_launch_generation"`
	CreatedBoottimeNS           uint64 `json:"created_boottime_ns"`
	ExpiresBoottimeNS           uint64 `json:"expires_boottime_ns"`
}

var vmExecCapacityRecordFields = map[string]struct{}{
	"schema_version": {}, "kind": {}, "reservation_id": {}, "owner_uid": {}, "host_boot_id": {},
	"source_vm_id": {}, "source_launch_generation": {}, "destination_vm_id": {},
	"destination_launch_generation": {}, "created_boottime_ns": {}, "expires_boottime_ns": {},
}

func encodeVMExecCapacityRecord(record vmExecCapacityRecord) ([]byte, error) {
	if err := validateVMExecCapacityRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(vmExecCapacityRecordWire{
		SchemaVersion:               record.schemaVersion,
		Kind:                        record.kind,
		ReservationID:               record.reservationID.value,
		OwnerUID:                    record.ownerUID,
		HostBootID:                  record.hostBootID,
		SourceVMID:                  record.sourceVMID,
		SourceLaunchGeneration:      record.sourceLaunchGeneration,
		DestinationVMID:             record.destinationVMID,
		DestinationLaunchGeneration: record.destinationLaunchGeneration,
		CreatedBoottimeNS:           record.createdBoottimeNS,
		ExpiresBoottimeNS:           record.expiresBoottimeNS,
	})
	if err != nil {
		return nil, vmExecCapacityEffectError("encode VM-exec capacity record", err)
	}
	if len(raw) > maxVMExecCapacityRecordBytes {
		return nil, vmExecCapacityCorruptError("encoded VM-exec capacity record exceeds size limit", nil)
	}
	return raw, nil
}

func decodeVMExecCapacityRecord(raw []byte) (vmExecCapacityRecord, error) {
	if len(raw) == 0 || len(raw) > maxVMExecCapacityRecordBytes {
		return vmExecCapacityRecord{}, vmExecCapacityCorruptError("vm-exec capacity record size is invalid", nil)
	}
	if err := validateVMExecCapacityJSON(raw); err != nil {
		return vmExecCapacityRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire vmExecCapacityRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return vmExecCapacityRecord{}, vmExecCapacityCorruptError("decode VM-exec capacity record", err)
	}
	if err := requireVMExecCapacityJSONEOF(decoder); err != nil {
		return vmExecCapacityRecord{}, err
	}
	reservationID, err := newVMExecReservationID(wire.ReservationID)
	if err != nil {
		return vmExecCapacityRecord{}, vmExecCapacityCorruptError(
			"vm-exec capacity reservation identity is invalid",
			err,
		)
	}
	record := vmExecCapacityRecord{
		schemaVersion:               wire.SchemaVersion,
		kind:                        wire.Kind,
		reservationID:               reservationID,
		ownerUID:                    wire.OwnerUID,
		hostBootID:                  wire.HostBootID,
		sourceVMID:                  wire.SourceVMID,
		sourceLaunchGeneration:      wire.SourceLaunchGeneration,
		destinationVMID:             wire.DestinationVMID,
		destinationLaunchGeneration: wire.DestinationLaunchGeneration,
		createdBoottimeNS:           wire.CreatedBoottimeNS,
		expiresBoottimeNS:           wire.ExpiresBoottimeNS,
	}
	if err := validateVMExecCapacityRecord(record); err != nil {
		return vmExecCapacityRecord{}, err
	}
	return record, nil
}

// CRITICAL: Root-owned records still require a closed decoder because corruption or an equally privileged actor can
// inject duplicate, case-colliding, nested, or omitted values that encoding/json alone does not reject.
func validateVMExecCapacityJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return vmExecCapacityCorruptError("decode strict VM-exec capacity record", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return vmExecCapacityCorruptError("vm-exec capacity record must be an object", nil)
	}
	seen := make(map[string]struct{}, len(vmExecCapacityRecordFields))
	for decoder.More() {
		fieldToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return vmExecCapacityCorruptError("decode strict VM-exec capacity field", tokenErr)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return vmExecCapacityCorruptError("vm-exec capacity field name is invalid", nil)
		}
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return vmExecCapacityCorruptError("duplicate VM-exec capacity field", nil)
			}
		}
		if _, allowed := vmExecCapacityRecordFields[field]; !allowed {
			return vmExecCapacityCorruptError("unknown VM-exec capacity field", nil)
		}
		seen[field] = struct{}{}
		value, valueErr := decoder.Token()
		if valueErr != nil {
			return vmExecCapacityCorruptError("decode strict VM-exec capacity value", valueErr)
		}
		if _, nested := value.(json.Delim); nested {
			return vmExecCapacityCorruptError("nested VM-exec capacity value is invalid", nil)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return vmExecCapacityCorruptError("close strict VM-exec capacity object", err)
	}
	if len(seen) != len(vmExecCapacityRecordFields) {
		return vmExecCapacityCorruptError("vm-exec capacity record is missing required fields", nil)
	}
	return requireVMExecCapacityJSONEOF(decoder)
}

func requireVMExecCapacityJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return vmExecCapacityCorruptError("vm-exec capacity record contains trailing input", nil)
	}
	return vmExecCapacityCorruptError(fmt.Sprintf("decode trailing VM-exec capacity input: %v", err), err)
}
