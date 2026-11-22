package firewall

import (
	"maps"
	"math"
	"slices"

	"mvmctl/internal/lib/model"
)

type vmLabelRecord struct {
	OwnerUID uint32         `json:"owner_uid"`
	Revision uint64         `json:"revision"`
	Labels   model.VMLabels `json:"labels"`
	Retired  bool           `json:"retired"`
}

// vmLabelRecords tracks root-owned identity independently of a running network endpoint.
// Mutations receive the owner and VM ID from the retained admission, not the label request.
type vmLabelRecords map[string]vmLabelRecord

func (records vmLabelRecords) clone() vmLabelRecords {
	if records == nil {
		return nil
	}
	copy := make(vmLabelRecords, len(records))
	for vmID, record := range records {
		copy[vmID] = cloneVMLabelRecord(record)
	}
	return copy
}

func (records vmLabelRecords) validate() error {
	for vmID, record := range records {
		if err := validateVMLabelIdentity(record.OwnerUID, vmID); err != nil {
			return err
		}
		if record.Revision == 0 || record.Labels == nil || (record.Retired && len(record.Labels) != 0) {
			return invalidGeneration("invalid root VM label record")
		}
		if err := record.Labels.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateVMLabelIdentity(ownerUID uint32, vmID string) error {
	if ownerUID == 0 || len(vmID) != 32 || !allCharacters(vmID, func(value byte) bool {
		return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
	}) {
		return invalidGeneration("invalid root VM label identity")
	}
	return nil
}

func (records vmLabelRecords) enroll(ownerUID uint32, vmID string, labels model.VMLabels) (vmLabelRecord, error) {
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return vmLabelRecord{}, err
	}
	if err := labels.Validate(); err != nil {
		return vmLabelRecord{}, err
	}
	if existing, found := records[vmID]; found {
		if existing.OwnerUID == ownerUID && existing.Revision == 1 && !existing.Retired &&
			maps.Equal(existing.Labels, labels) {
			return cloneVMLabelRecord(existing), nil
		}
		return vmLabelRecord{}, invalidGeneration("VM label identity is already enrolled or retired")
	}
	if records == nil {
		return vmLabelRecord{}, invalidGeneration("root VM label inventory is unavailable")
	}
	record := vmLabelRecord{OwnerUID: ownerUID, Revision: 1, Labels: cloneVMLabels(labels)}
	records[vmID] = record
	return cloneVMLabelRecord(record), nil
}

func (records vmLabelRecords) replace(
	ownerUID uint32, vmID string, expected uint64, labels model.VMLabels,
) (vmLabelRecord, error) {
	if labels == nil {
		return vmLabelRecord{}, invalidGeneration("VM label replacement requires an explicit map")
	}
	if err := labels.Validate(); err != nil {
		return vmLabelRecord{}, err
	}
	record, err := records.ownedAtRevision(ownerUID, vmID, expected)
	if err != nil {
		return vmLabelRecord{}, err
	}
	if !maps.Equal(record.Labels, labels) {
		if record.Revision == math.MaxUint64 {
			return vmLabelRecord{}, invalidGeneration("VM label revision exhausted")
		}
		record.Revision++
		record.Labels = cloneVMLabels(labels)
		records[vmID] = record
	}
	return cloneVMLabelRecord(record), nil
}

func (records vmLabelRecords) retire(ownerUID uint32, vmID string, expected uint64) error {
	record, err := records.ownedAtRevision(ownerUID, vmID, expected)
	if err != nil {
		return err
	}
	if record.Revision == math.MaxUint64 {
		return invalidGeneration("VM label revision exhausted")
	}
	record.Revision++
	record.Labels = make(model.VMLabels)
	record.Retired = true
	records[vmID] = record
	return nil
}

func (records vmLabelRecords) ownedAtRevision(ownerUID uint32, vmID string, expected uint64) (vmLabelRecord, error) {
	record, found := records[vmID]
	if ownerUID == 0 || expected == 0 || !found || record.OwnerUID != ownerUID ||
		record.Retired || record.Revision != expected {
		return vmLabelRecord{}, invalidGeneration("VM label owner or revision differs")
	}
	return record, nil
}

func (records vmLabelRecords) members(ownerUID uint32, selector model.VMLabelSelector) []string {
	var members []string
	for vmID, record := range records {
		if record.OwnerUID == ownerUID && !record.Retired && selector.Matches(record.Labels) {
			members = append(members, vmID)
		}
	}
	slices.Sort(members)
	return members
}

func cloneVMLabelRecord(record vmLabelRecord) vmLabelRecord {
	record.Labels = cloneVMLabels(record.Labels)
	return record
}

func cloneVMLabels(labels model.VMLabels) model.VMLabels {
	if labels == nil {
		return make(model.VMLabels)
	}
	return maps.Clone(labels)
}
