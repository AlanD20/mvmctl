package network

import (
	"bytes"
	"encoding/json"
	"net/netip"

	"mvmctl/internal/lib/validators"
)

const floatingIPRecordSchema = uint32(3)

// floatingIPRecord is root authority, not a projection of the operator database.
// Retirement retains the resource identity so an old create cannot resurrect it.
type floatingIPRecord struct {
	Schema      uint32              `json:"schema"`
	OwnerUID    uint32              `json:"owner_uid"`
	ID          string              `json:"id"`
	NetworkID   string              `json:"network_id"`
	Name        string              `json:"name"`
	IPv4        string              `json:"ipv4"`
	Generation  uint64              `json:"generation"`
	Revision    uint64              `json:"revision"`
	Phase       floatingIPPhase     `json:"phase"`
	Operation   floatingIPOperation `json:"operation"`
	Owner       floatingIPOwner     `json:"owner"`
	BeforeOwner floatingIPOwner     `json:"before_owner"`
	TargetOwner floatingIPOwner     `json:"target_owner"`
	Retired     bool                `json:"retired"`
}

func validateFloatingIPRecord(record floatingIPRecord) error {
	if record.Schema != floatingIPRecordSchema || record.OwnerUID == 0 || record.Generation == 0 ||
		record.Revision < record.Generation ||
		!vmIDPattern.MatchString(record.ID) || !networkIDPattern.MatchString(record.NetworkID) {
		return networkAuthorityCorruptError("floating-IP record identity or generation is invalid", nil)
	}
	if err := validators.EntityName(record.Name, "floating IP", 63); err != nil {
		return networkAuthorityCorruptError("floating-IP record name is invalid", err)
	}
	address, err := netip.ParseAddr(record.IPv4)
	if err != nil || !address.Is4() || address.String() != record.IPv4 {
		return networkAuthorityCorruptError("floating-IP record address is invalid", err)
	}
	return validateFloatingIPOwnership(record)
}

func encodeFloatingIPRecord(record floatingIPRecord) ([]byte, error) {
	if err := validateFloatingIPRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxNetworkAuthorityRecordBytes {
		return nil, networkAuthorityCorruptError("encode bounded floating-IP record", err)
	}
	return raw, nil
}

func decodeFloatingIPRecord(raw []byte) (floatingIPRecord, error) {
	if len(raw) == 0 || len(raw) > maxNetworkAuthorityRecordBytes {
		return floatingIPRecord{}, networkAuthorityCorruptError("floating-IP record size is invalid", nil)
	}
	var record floatingIPRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return floatingIPRecord{}, networkAuthorityCorruptError("decode floating-IP record", err)
	}
	canonical, err := encodeFloatingIPRecord(record)
	// Root writers use one canonical representation, as with topology journals. This rejects duplicate,
	// unknown, missing, case-aliased and null fields rather than accepting JSON's last-value semantics.
	if err != nil || !bytes.Equal(canonical, raw) {
		return floatingIPRecord{}, networkAuthorityCorruptError("floating-IP record is not canonical", err)
	}
	return record, nil
}
