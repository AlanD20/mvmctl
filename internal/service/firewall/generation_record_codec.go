package firewall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	maxGenerationRecordBytes = 4 * 1024
	maxGenerationJSONDepth   = 3
)

type generationRecordWire struct {
	SchemaVersion uint32                        `json:"schema_version"`
	Lifecycle     generationRecordLifecycle     `json:"lifecycle"`
	BootID        string                        `json:"boot_id"`
	HostNamespace generationNamespaceWire       `json:"host_namespace"`
	Committed     generationRecordIdentityWire  `json:"committed"`
	Before        *generationRecordIdentityWire `json:"before"`
	Target        *generationRecordIdentityWire `json:"target"`
}

type generationNamespaceWire struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type generationRecordIdentityWire struct {
	Empty        bool   `json:"empty"`
	GenerationID string `json:"generation_id"`
}

var generationRecordObjectFields = map[string]map[string]struct{}{
	"": generationRecordFields(
		"schema_version",
		"lifecycle",
		"boot_id",
		"host_namespace",
		"committed",
		"before",
		"target",
	),
	"host_namespace": generationRecordFields("device", "inode"),
	"committed":      generationRecordFields("empty", "generation_id"),
	"before":         generationRecordFields("empty", "generation_id"),
	"target":         generationRecordFields("empty", "generation_id"),
}

func encodeGenerationRecord(record generationRecord) ([]byte, error) {
	if err := validateGenerationRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(generationRecordToWire(record))
	if err != nil {
		return nil, generationAuthorityError("encode firewall generation record", err, nil)
	}
	if len(raw) > maxGenerationRecordBytes {
		return nil, generationAuthorityError("encoded firewall generation record exceeds size limit", nil, nil)
	}
	return raw, nil
}

func decodeGenerationRecord(raw []byte) (generationRecord, error) {
	if len(raw) == 0 {
		return generationRecord{}, generationAuthorityError("firewall generation record is empty", nil, nil)
	}
	if len(raw) > maxGenerationRecordBytes {
		return generationRecord{}, generationAuthorityError("firewall generation record exceeds size limit", nil, nil)
	}
	if err := validateGenerationRecordJSON(raw); err != nil {
		return generationRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire generationRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return generationRecord{}, generationAuthorityError("decode firewall generation record", err, nil)
	}
	if err := requireGenerationRecordJSONEOF(decoder); err != nil {
		return generationRecord{}, err
	}
	record := generationRecordFromWire(wire)
	if err := validateGenerationRecord(record); err != nil {
		return generationRecord{}, err
	}
	return record, nil
}

func validateGenerationRecordJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanGenerationRecordObject(decoder, "", 0, false); err != nil {
		return generationAuthorityError("decode strict firewall generation record", err, nil)
	}
	return requireGenerationRecordJSONEOF(decoder)
}

func scanGenerationRecordObject(
	decoder *json.Decoder,
	objectName string,
	depth int,
	allowNull bool,
) error {
	if depth > maxGenerationJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxGenerationJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil && allowNull {
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("object %q is not a JSON object", objectName)
	}
	allowed, ok := generationRecordObjectFields[objectName]
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
		switch field {
		case "host_namespace", "committed":
			if err := scanGenerationRecordObject(decoder, field, depth+1, false); err != nil {
				return err
			}
		case "before", "target":
			if err := scanGenerationRecordObject(decoder, field, depth+1, true); err != nil {
				return err
			}
		default:
			if err := scanGenerationRecordScalar(decoder); err != nil {
				return err
			}
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

func scanGenerationRecordScalar(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if _, ok := token.(json.Delim); ok || token == nil {
		return fmt.Errorf("unexpected non-scalar JSON value")
	}
	return nil
}

func requireGenerationRecordJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return generationAuthorityError("firewall generation record contains trailing input", nil, nil)
	}
	return generationAuthorityError("decode trailing firewall generation record input", err, nil)
}

func generationRecordToWire(record generationRecord) generationRecordWire {
	result := generationRecordWire{
		SchemaVersion: record.schemaVersion,
		Lifecycle:     record.lifecycle,
		BootID:        record.binding.bootID,
		HostNamespace: generationNamespaceWire{
			Device: record.binding.namespace.device,
			Inode:  record.binding.namespace.inode,
		},
		Committed: generationIdentityToWire(record.committed),
	}
	if record.before != nil {
		before := generationIdentityToWire(*record.before)
		result.Before = &before
	}
	if record.target != nil {
		target := generationIdentityToWire(*record.target)
		result.Target = &target
	}
	return result
}

func generationRecordFromWire(wire generationRecordWire) generationRecord {
	record := generationRecord{
		schemaVersion: wire.SchemaVersion,
		lifecycle:     wire.Lifecycle,
		binding: generationRecordBinding{
			bootID: wire.BootID,
			namespace: generationNamespaceIdentity{
				device: wire.HostNamespace.Device,
				inode:  wire.HostNamespace.Inode,
			},
		},
		committed: generationIdentityFromWire(wire.Committed),
	}
	if wire.Before != nil {
		before := generationIdentityFromWire(*wire.Before)
		record.before = &before
	}
	if wire.Target != nil {
		target := generationIdentityFromWire(*wire.Target)
		record.target = &target
	}
	return record
}

func generationIdentityToWire(identity generationRecordIdentity) generationRecordIdentityWire {
	return generationRecordIdentityWire{Empty: identity.empty, GenerationID: identity.id}
}

func generationIdentityFromWire(wire generationRecordIdentityWire) generationRecordIdentity {
	return generationRecordIdentity{empty: wire.Empty, id: wire.GenerationID}
}

func generationRecordFields(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}
