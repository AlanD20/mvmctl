package network

import "mvmctl/pkg/errs"

const (
	// FloatingIPAssociateAction associates an unowned reservation to an admitted operator VM.
	FloatingIPAssociateAction = "floating-ip-associate"
	// FloatingIPTransferAction explicitly replaces existing ownership with an admitted operator VM.
	FloatingIPTransferAction = "floating-ip-transfer"
	// FloatingIPReleaseAction fences current ownership without requiring the old VM to be live.
	FloatingIPReleaseAction = "floating-ip-release"
)

// FloatingIPAssociateRequest names exact target identities and the observed ownership generation.
type FloatingIPAssociateRequest struct {
	ID                 string `json:"id"`
	NetworkID          string `json:"network_id"`
	VMID               string `json:"vm_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate admits no caller-supplied principal, grant, incarnation or kernel policy.
func (request FloatingIPAssociateRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return networkAuthorityValidationError("floating-IP target requires a complete VM identity")
	}
	return (FloatingIPReleaseRequest{ID: request.ID, NetworkID: request.NetworkID, ExpectedGeneration: request.ExpectedGeneration}).Validate()
}

// FloatingIPTransferRequest is the explicit operator takeover request.
type FloatingIPTransferRequest struct {
	ID                 string `json:"id"`
	NetworkID          string `json:"network_id"`
	VMID               string `json:"vm_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate requires exact identities and an observed generation.
func (request FloatingIPTransferRequest) Validate() error {
	return (FloatingIPAssociateRequest{ID: request.ID, NetworkID: request.NetworkID, VMID: request.VMID, ExpectedGeneration: request.ExpectedGeneration}).Validate()
}

// FloatingIPReleaseRequest binds revocation to one exact resource generation.
type FloatingIPReleaseRequest struct {
	ID                 string `json:"id"`
	NetworkID          string `json:"network_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate refuses wildcard identity and unobserved mutation.
func (request FloatingIPReleaseRequest) Validate() error {
	if !vmIDPattern.MatchString(request.ID) || !networkIDPattern.MatchString(request.NetworkID) ||
		request.ExpectedGeneration == 0 {
		return networkAuthorityValidationError(
			"floating-IP ownership mutation requires exact identities and generation",
		)
	}
	return nil
}

func admitFloatingIPOperatorGeneration(record floatingIPRecord, generation uint64) error {
	if record.Generation != generation || generation == 0 || generation == ^uint64(0) ||
		record.Revision > ^uint64(0)-6 {
		return errs.New(
			errs.CodeValidationFailed,
			"floating-IP generation is stale or exhausted",
			errs.WithClass(errs.ClassConflict),
		)
	}
	if record.Retired {
		return networkAuthorityValidationError("floating-IP reservation is retired")
	}
	if record.Phase != floatingIPOwned && record.Phase != floatingIPUnassociated {
		return networkAuthorityCorruptError("floating-IP transition requires root recovery", nil)
	}
	return nil
}
