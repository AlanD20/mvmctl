package network

import (
	"time"

	"mvmctl/internal/lib/model"
)

const (
	FloatingIPDelegationRemoveOwnedAction = "floating-ip-delegation-remove-owned"
	FloatingIPDelegationGrantAction       = "floating-ip-delegation-grant"
	FloatingIPDelegationListAction        = "floating-ip-delegation-list"
	FloatingIPDelegationGetAction         = "floating-ip-delegation-get"
	FloatingIPDelegationRevokeAction      = "floating-ip-delegation-revoke"
)

// FloatingIPDelegationGrantRequest selects one permission; root independently derives its incarnation.
type FloatingIPDelegationGrantRequest struct {
	ID            string        `json:"id"`
	NetworkID     string        `json:"network_id"`
	ResourceID    string        `json:"resource_id"`
	VMID          string        `json:"vm_id"`
	Method        string        `json:"method"`
	LeaseDuration time.Duration `json:"lease_duration_ns"`

	// Expected is a comparison with the independently admitted launch when set.
	Expected *model.FloatingIPGrantIncarnation `json:"expected_incarnation,omitempty"`
}

func (request FloatingIPDelegationGrantRequest) Validate() error {
	if !vmIDPattern.MatchString(request.ID) || !networkIDPattern.MatchString(request.NetworkID) ||
		!vmIDPattern.MatchString(request.ResourceID) ||
		!vmIDPattern.MatchString(request.VMID) ||
		!validFloatingIPDelegationMethod(floatingIPDelegationMethod(request.Method)) {
		return networkAuthorityValidationError("delegation requires exact network, resource, VM and method")
	}
	if request.Expected != nil && !request.Expected.WellFormed() {
		return networkAuthorityValidationError("expected delegation launch identity is invalid")
	}
	timed := request.Method == string(floatingIPDelegationAssociate) ||
		request.Method == string(floatingIPDelegationRenew)
	if timed && (request.LeaseDuration <= 0 || request.LeaseDuration%time.Second != 0) ||
		!timed && request.LeaseDuration != 0 {
		return networkAuthorityValidationError(
			"associate and renew require explicit finite whole-second lease; other methods forbid it",
		)
	}
	return nil
}

// FloatingIPDelegationListRequest enumerates stored permissions for one exact owned resource.
type FloatingIPDelegationListRequest struct {
	NetworkID  string `json:"network_id"`
	ResourceID string `json:"resource_id"`
	AfterID    string `json:"after_id"`
}

func (request FloatingIPDelegationListRequest) Validate() error {
	if !networkIDPattern.MatchString(request.NetworkID) || !vmIDPattern.MatchString(request.ResourceID) ||
		request.AfterID != "" && !vmIDPattern.MatchString(request.AfterID) {
		return networkAuthorityValidationError("delegation inventory requires exact network, resource and cursor")
	}
	return nil
}

// FloatingIPDelegationGetRequest selects one immutable grant, including a revoked lifetime.
type FloatingIPDelegationGetRequest struct {
	NetworkID string `json:"network_id"`
	ID        string `json:"id"`
}

func (request FloatingIPDelegationGetRequest) Validate() error {
	if !networkIDPattern.MatchString(request.NetworkID) || !vmIDPattern.MatchString(request.ID) {
		return networkAuthorityValidationError("delegation inspection requires exact network and grant")
	}
	return nil
}

// FloatingIPDelegationRevokeRequest binds revocation to a root-observed grant revision.
type FloatingIPDelegationRevokeRequest struct {
	NetworkID        string `json:"network_id"`
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

func (request FloatingIPDelegationRevokeRequest) Validate() error {
	if err := (FloatingIPDelegationGetRequest{NetworkID: request.NetworkID, ID: request.ID}).Validate(); err != nil {
		return err
	}
	if request.ExpectedRevision == 0 || request.ExpectedRevision > 3 {
		return networkAuthorityValidationError("delegation revocation requires an observed revision")
	}
	return nil
}

// FloatingIPDelegationResult is a display projection, not a capability to invoke guest methods.
type FloatingIPDelegationResult = model.FloatingIPDelegationItem

// FloatingIPDelegationPage fits bounded privileged frames without capping stored grant count.
type FloatingIPDelegationPage struct {
	Items  []FloatingIPDelegationResult `json:"items"`
	NextID string                       `json:"next_id"`
}

func floatingIPDelegationProjection(record floatingIPDelegationRecord) FloatingIPDelegationResult {
	return FloatingIPDelegationResult{ID: record.ID, NetworkID: record.NetworkID, ResourceID: record.ResourceID,
		VMID: record.VMID, BootID: record.BootID, ExecutionUID: record.ExecutionUID,
		LaunchGeneration: record.LaunchGeneration, Method: string(record.Method),
		LeaseDuration: record.LeaseDuration, Revision: record.Revision, State: string(record.State)}
}

func validateFloatingIPDelegationResult(result FloatingIPDelegationResult, networkID string) error {
	incarnation := model.FloatingIPGrantIncarnation{
		BootID: result.BootID, LaunchGeneration: result.LaunchGeneration, ExecutionUID: result.ExecutionUID,
	}
	if result.NetworkID != networkID || !vmIDPattern.MatchString(result.ID) || !incarnation.WellFormed() {
		return networkAuthorityValidationError("delegation result has invalid scope or incarnation")
	}
	request := FloatingIPDelegationGrantRequest{
		ID:            result.ID,
		NetworkID:     result.NetworkID,
		ResourceID:    result.ResourceID,
		VMID:          result.VMID,
		Method:        result.Method,
		LeaseDuration: result.LeaseDuration,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if result.State == "active" && result.Revision == 1 || result.State == "revoking" && result.Revision == 2 ||
		result.State == "revoked" && result.Revision == 3 {
		return nil
	}
	return networkAuthorityValidationError("delegation result has invalid revision or state")
}

// FloatingIPDelegationRemoveOwnedRequest compares an immutable receipt; its identity fields are not caller authority.
type FloatingIPDelegationRemoveOwnedRequest struct {
	Grant FloatingIPDelegationResult `json:"grant"`
}

func (request FloatingIPDelegationRemoveOwnedRequest) Validate() error {
	return validateFloatingIPDelegationResult(request.Grant, request.Grant.NetworkID)
}
