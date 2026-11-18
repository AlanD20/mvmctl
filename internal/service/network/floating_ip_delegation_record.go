package network

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

type floatingIPDelegationMethod string
type floatingIPDelegationState string

const (
	floatingIPDelegationActiveRevision uint64                     = 1
	floatingIPDelegationInspect        floatingIPDelegationMethod = "inspect"
	floatingIPDelegationAssociate      floatingIPDelegationMethod = "associate"
	floatingIPDelegationRenew          floatingIPDelegationMethod = "renew"
	floatingIPDelegationRelease        floatingIPDelegationMethod = "release"
	floatingIPDelegationActive         floatingIPDelegationState  = "active"
	floatingIPDelegationRevoking       floatingIPDelegationState  = "revoking"
	floatingIPDelegationRevoked        floatingIPDelegationState  = "revoked"
)

// floatingIPDelegationRecord binds one root-issued grant lifetime to one exact incarnation and method.
type floatingIPDelegationRecord struct {
	Schema           uint32                     `json:"schema"`
	ID               string                     `json:"id"`
	OwnerUID         uint32                     `json:"owner_uid"`
	NetworkID        string                     `json:"network_id"`
	ResourceID       string                     `json:"resource_id"`
	VMID             string                     `json:"vm_id"`
	BootID           string                     `json:"boot_id"`
	LaunchGeneration uint64                     `json:"launch_generation"`
	ExecutionUID     uint32                     `json:"execution_uid"`
	Method           floatingIPDelegationMethod `json:"method"`
	LeaseDuration    time.Duration              `json:"lease_duration_ns"`
	Revision         uint64                     `json:"revision"`
	State            floatingIPDelegationState  `json:"state"`
}

type floatingIPDelegationTuple struct {
	resourceID, vmID string
	method           floatingIPDelegationMethod
}

func (record floatingIPDelegationRecord) tuple() floatingIPDelegationTuple {
	return floatingIPDelegationTuple{record.ResourceID, record.VMID, record.Method}
}
func validFloatingIPDelegationMethod(method floatingIPDelegationMethod) bool {
	return method == floatingIPDelegationInspect || method == floatingIPDelegationAssociate ||
		method == floatingIPDelegationRenew || method == floatingIPDelegationRelease
}
func validateFloatingIPDelegationRecord(record floatingIPDelegationRecord) error {
	if record.Schema != 1 || !vmIDPattern.MatchString(record.ID) || record.OwnerUID == 0 ||
		record.OwnerUID == ^uint32(0) ||
		!networkIDPattern.MatchString(record.NetworkID) || !vmIDPattern.MatchString(record.ResourceID) ||
		!vmIDPattern.MatchString(
			record.VMID,
		) || !bootIDPattern.MatchString(record.BootID) || record.LaunchGeneration == 0 ||
		record.ExecutionUID == 0 || record.ExecutionUID == record.OwnerUID || record.ExecutionUID == ^uint32(0) ||
		!validFloatingIPDelegationMethod(record.Method) || record.Revision == 0 ||
		(record.State != floatingIPDelegationActive && record.State != floatingIPDelegationRevoking &&
			record.State != floatingIPDelegationRevoked) {
		return networkAuthorityCorruptError("delegation identity, method or state is invalid", nil)
	}
	timed := record.Method == floatingIPDelegationAssociate || record.Method == floatingIPDelegationRenew
	if (timed && (record.LeaseDuration <= 0 || record.LeaseDuration%time.Second != 0)) ||
		(!timed && record.LeaseDuration != 0) {
		return networkAuthorityCorruptError("delegation requires an explicit method-specific lease policy", nil)
	}
	// This immutable grant lifetime has exactly three publications. Revision therefore
	// also proves which active authority revision a revocation retires.
	if (record.State == floatingIPDelegationActive && record.Revision != floatingIPDelegationActiveRevision) ||
		(record.State == floatingIPDelegationRevoking && record.Revision != 2) ||
		(record.State == floatingIPDelegationRevoked && record.Revision != 3) {
		return networkAuthorityCorruptError("delegation state and legal revision chain disagree", nil)
	}
	return nil
}
func encodeFloatingIPDelegationRecord(record floatingIPDelegationRecord) ([]byte, error) {
	if err := validateFloatingIPDelegationRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxNetworkAuthorityRecordBytes {
		return nil, networkAuthorityCorruptError("encode bounded delegation record", err)
	}
	return raw, nil
}
func decodeFloatingIPDelegationRecord(raw []byte) (floatingIPDelegationRecord, error) {
	var record floatingIPDelegationRecord
	if len(raw) == 0 || len(raw) > maxNetworkAuthorityRecordBytes {
		return record, networkAuthorityCorruptError("delegation record size is invalid", nil)
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, networkAuthorityCorruptError("decode delegation record", err)
	}
	canonical, err := encodeFloatingIPDelegationRecord(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return floatingIPDelegationRecord{}, networkAuthorityCorruptError("delegation record is not canonical", err)
	}
	return record, nil
}
func validateFloatingIPDelegationReplacement(
	next floatingIPDelegationRecord, previous *floatingIPDelegationRecord,
) error {
	if err := validateFloatingIPDelegationRecord(next); err != nil {
		return err
	}
	if previous == nil {
		if next.State == floatingIPDelegationActive && next.Revision == floatingIPDelegationActiveRevision {
			return nil
		}
	} else {
		if err := validateFloatingIPDelegationRecord(*previous); err != nil {
			return err
		}
		expected := *previous
		expected.Revision++
		if previous.Revision != ^uint64(0) &&
			((previous.State == floatingIPDelegationActive && next.State == floatingIPDelegationRevoking) ||
				(previous.State == floatingIPDelegationRevoking && next.State == floatingIPDelegationRevoked)) {
			expected.State = next.State
			if expected == next {
				return nil
			}
		}
	}
	return networkAuthorityCorruptError("delegation replacement changes immutable authority or revision", nil)
}

// newFloatingIPDelegation issues a new identity only from already admitted root resource and VM facts.
func newFloatingIPDelegation(
	resource floatingIPRecord, principal floatingIPGuestPrincipal,
	method floatingIPDelegationMethod, duration time.Duration,
) (floatingIPDelegationRecord, error) {
	if err := validateFloatingIPRecord(resource); err != nil {
		return floatingIPDelegationRecord{}, err
	}
	if resource.Retired || !principal.matchesResource(resource) {
		return floatingIPDelegationRecord{}, networkAuthorityCorruptError(
			"delegation resource and admitted VM differ",
			nil,
		)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return floatingIPDelegationRecord{}, networkAuthorityEffectError("generate delegation identity", err)
	}
	record := floatingIPDelegationRecord{
		Schema: 1, ID: strings.ReplaceAll(id.String(), "-", ""), OwnerUID: resource.OwnerUID,
		NetworkID: resource.NetworkID, ResourceID: resource.ID, VMID: principal.vmID, BootID: principal.bootID,
		LaunchGeneration: principal.launchGeneration, ExecutionUID: principal.executionUID,
		Method: method, LeaseDuration: duration, Revision: floatingIPDelegationActiveRevision,
		State: floatingIPDelegationActive,
	}
	return record, validateFloatingIPDelegationRecord(record)
}
