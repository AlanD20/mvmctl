package jailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxInstanceJSONDepth = 4

type instanceRecordWire struct {
	SchemaVersion     uint32              `json:"schema_version"`
	OwnerUID          uint32              `json:"owner_uid"`
	VMID              string              `json:"vm_id"`
	Lifecycle         instanceLifecycle   `json:"lifecycle"`
	Release           releaseIdentityWire `json:"release"`
	Process           processIdentityWire `json:"process"`
	CleanupGeneration uint64              `json:"cleanup_generation"`
}

type releaseIdentityWire struct {
	Version           string `json:"version"`
	Architecture      string `json:"architecture"`
	FirecrackerSHA256 string `json:"firecracker_sha256"`
	JailerSHA256      string `json:"jailer_sha256"`
}

type processIdentityWire struct {
	PID            int    `json:"pid"`
	StartTimeTicks uint64 `json:"start_time_ticks"`
	CgroupPath     string `json:"cgroup_path"`
}

var instanceObjectFields = map[string]map[string]struct{}{
	"": fields(
		"schema_version",
		"owner_uid",
		"vm_id",
		"lifecycle",
		"release",
		"process",
		"cleanup_generation",
	),
	"release": fields("version", "architecture", "firecracker_sha256", "jailer_sha256"),
	"process": fields("pid", "start_time_ticks", "cgroup_path"),
}

func encodeInstanceRecord(record instanceRecord) ([]byte, error) {
	if err := validateInstanceRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(instanceRecordToWire(record))
	if err != nil {
		return nil, instanceAtomicError("encode instance authority record", err)
	}
	if len(raw) > maxInstanceRecordBytes {
		return nil, instanceAtomicError("encoded instance authority record exceeds size limit", nil)
	}
	return raw, nil
}

func decodeInstanceRecord(raw []byte) (instanceRecord, error) {
	if len(raw) == 0 {
		return instanceRecord{}, instanceAtomicError("instance authority record is empty", nil)
	}
	if len(raw) > maxInstanceRecordBytes {
		return instanceRecord{}, instanceAtomicError("instance authority record exceeds size limit", nil)
	}
	if err := validateInstanceJSON(raw); err != nil {
		return instanceRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire instanceRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return instanceRecord{}, instanceAtomicError("decode instance authority record", err)
	}
	if err := requireInstanceJSONEOF(decoder); err != nil {
		return instanceRecord{}, err
	}
	record := instanceRecordFromWire(wire)
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	return record, nil
}

func validateInstanceJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanInstanceJSONValue(decoder, "", 0); err != nil {
		return instanceAtomicError("decode strict instance authority record", err)
	}
	return requireInstanceJSONEOF(decoder)
}

func scanInstanceJSONValue(decoder *json.Decoder, objectName string, depth int) error {
	if depth > maxInstanceJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxInstanceJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' {
		return fmt.Errorf("unexpected JSON container %q", delim)
	}
	allowed, ok := instanceObjectFields[objectName]
	if !ok {
		return fmt.Errorf("unexpected object %q", objectName)
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		fieldToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("object field is not a string")
		}
		if _, exists := allowed[field]; !exists {
			return fmt.Errorf("unknown JSON field %q", field)
		}
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return fmt.Errorf("duplicate JSON field %q conflicts with %q", field, existing)
			}
		}
		seen[field] = struct{}{}
		if field == "release" || field == "process" {
			if err := scanInstanceJSONValue(decoder, field, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := scanInstanceJSONScalar(decoder); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("JSON object %q is missing required fields", objectName)
	}
	return nil
}

func scanInstanceJSONScalar(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if _, ok := token.(json.Delim); ok {
		return fmt.Errorf("unexpected nested JSON value")
	}
	return nil
}

func requireInstanceJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return instanceAtomicError("instance authority record contains trailing input", nil)
	}
	return instanceAtomicError("decode trailing instance authority input", err)
}

func instanceRecordToWire(record instanceRecord) instanceRecordWire {
	return instanceRecordWire{
		SchemaVersion: record.schemaVersion,
		OwnerUID:      record.ownerUID,
		VMID:          record.vmID,
		Lifecycle:     record.lifecycle,
		Release: releaseIdentityWire{
			Version:           record.release.version,
			Architecture:      record.release.architecture,
			FirecrackerSHA256: record.release.firecrackerSHA256,
			JailerSHA256:      record.release.jailerSHA256,
		},
		Process: processIdentityWire{
			PID:            record.process.pid,
			StartTimeTicks: record.process.startTimeTicks,
			CgroupPath:     record.process.cgroupPath,
		},
		CleanupGeneration: record.cleanupGeneration,
	}
}

func instanceRecordFromWire(wire instanceRecordWire) instanceRecord {
	return instanceRecord{
		schemaVersion: wire.SchemaVersion,
		ownerUID:      wire.OwnerUID,
		vmID:          wire.VMID,
		lifecycle:     wire.Lifecycle,
		release: releaseIdentity{
			version:           wire.Release.Version,
			architecture:      wire.Release.Architecture,
			firecrackerSHA256: wire.Release.FirecrackerSHA256,
			jailerSHA256:      wire.Release.JailerSHA256,
		},
		process: processIdentity{
			pid:            wire.Process.PID,
			startTimeTicks: wire.Process.StartTimeTicks,
			cgroupPath:     wire.Process.CgroupPath,
		},
		cleanupGeneration: wire.CleanupGeneration,
	}
}

func fields(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}
