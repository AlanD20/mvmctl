package network

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxNetworkAuthorityJSONDepth = 3

type networkAuthorityRecordWire struct {
	SchemaVersion uint32                   `json:"schema_version"`
	OwnerUID      uint32                   `json:"owner_uid"`
	NetworkID     string                   `json:"network_id"`
	BootID        string                   `json:"boot_id"`
	ManagedBridge networkAuthorityLinkWire `json:"managed_bridge"`
}

type networkAuthorityLinkWire struct {
	Name      string                        `json:"name"`
	IfIndex   int                           `json:"ifindex"`
	Namespace networkAuthorityNamespaceWire `json:"namespace"`
}

type networkAuthorityNamespaceWire struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

var networkAuthorityObjectFields = map[string]map[string]struct{}{
	"":               networkAuthorityFields("schema_version", "owner_uid", "network_id", "boot_id", "managed_bridge"),
	"managed_bridge": networkAuthorityFields("name", "ifindex", "namespace"),
	"namespace":      networkAuthorityFields("device", "inode"),
}

func encodeNetworkAuthorityRecord(record networkAuthorityRecord) ([]byte, error) {
	if err := validateNetworkAuthorityRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(networkAuthorityRecordWire{
		SchemaVersion: record.schemaVersion,
		OwnerUID:      record.ownerUID,
		NetworkID:     string(record.networkID),
		BootID:        record.bootID,
		ManagedBridge: networkAuthorityLinkWire{
			Name:    record.managedBridge.name.String(),
			IfIndex: record.managedBridge.ifIndex,
			Namespace: networkAuthorityNamespaceWire{
				Device: record.managedBridge.namespace.device,
				Inode:  record.managedBridge.namespace.inode,
			},
		},
	})
	if err != nil {
		return nil, networkAuthorityEffectError("encode network authority record", err)
	}
	if len(raw) > maxNetworkAuthorityRecordBytes {
		return nil, networkAuthorityCorruptError("encoded network authority record exceeds size limit", nil)
	}
	return raw, nil
}

func decodeNetworkAuthorityRecord(raw []byte) (networkAuthorityRecord, error) {
	if len(raw) == 0 {
		return networkAuthorityRecord{}, networkAuthorityCorruptError("network authority record is empty", nil)
	}
	if len(raw) > maxNetworkAuthorityRecordBytes {
		return networkAuthorityRecord{}, networkAuthorityCorruptError(
			"network authority record exceeds size limit",
			nil,
		)
	}
	if err := validateNetworkAuthorityJSON(raw); err != nil {
		return networkAuthorityRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire networkAuthorityRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return networkAuthorityRecord{}, networkAuthorityCorruptError("decode network authority record", err)
	}
	if err := requireNetworkAuthorityJSONEOF(decoder); err != nil {
		return networkAuthorityRecord{}, err
	}
	record := networkAuthorityRecord{
		schemaVersion: wire.SchemaVersion,
		ownerUID:      wire.OwnerUID,
		networkID:     NetworkID(wire.NetworkID),
		bootID:        wire.BootID,
		managedBridge: linkIdentity{
			name:    linkName(wire.ManagedBridge.Name),
			ifIndex: wire.ManagedBridge.IfIndex,
			namespace: namespaceIdentity{
				device: wire.ManagedBridge.Namespace.Device,
				inode:  wire.ManagedBridge.Namespace.Inode,
			},
		},
	}
	if err := validateNetworkAuthorityRecord(record); err != nil {
		return networkAuthorityRecord{}, err
	}
	return record, nil
}

func validateNetworkAuthorityJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanNetworkAuthorityJSONValue(decoder, "", 0); err != nil {
		return networkAuthorityCorruptError("decode strict network authority record", err)
	}
	return requireNetworkAuthorityJSONEOF(decoder)
}

func scanNetworkAuthorityJSONValue(decoder *json.Decoder, objectName string, depth int) error {
	if depth > maxNetworkAuthorityJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxNetworkAuthorityJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("expected JSON object")
	}
	allowed, ok := networkAuthorityObjectFields[objectName]
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
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return fmt.Errorf("duplicate JSON field %q conflicts with %q", field, existing)
			}
		}
		if _, exists := allowed[field]; !exists {
			return fmt.Errorf("unknown JSON field %q", field)
		}
		seen[field] = struct{}{}
		switch field {
		case "managed_bridge", "namespace":
			if err := scanNetworkAuthorityJSONValue(decoder, field, depth+1); err != nil {
				return err
			}
		default:
			if err := scanNetworkAuthorityJSONScalar(decoder); err != nil {
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

func scanNetworkAuthorityJSONScalar(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if _, ok := token.(json.Delim); ok {
		return fmt.Errorf("unexpected nested JSON value")
	}
	return nil
}

func requireNetworkAuthorityJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return networkAuthorityCorruptError("network authority record contains trailing input", nil)
	}
	return networkAuthorityCorruptError("decode trailing network authority input", err)
}

func networkAuthorityFields(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}
