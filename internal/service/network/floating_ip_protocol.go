package network

import (
	"net/netip"

	"mvmctl/internal/lib/validators"
)

// FloatingIPCreateRequest describes a network reservation, never a host interface or filesystem path.
type FloatingIPCreateRequest struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	Name      string `json:"name"`
	IPv4      string `json:"ipv4"`
}

// Validate admits complete immutable identities and optional canonical IPv4 intent.
func (request FloatingIPCreateRequest) Validate() error {
	if !vmIDPattern.MatchString(request.ID) || !networkIDPattern.MatchString(request.NetworkID) {
		return networkAuthorityValidationError("floating-IP creation requires complete resource and network identities")
	}
	if err := validators.EntityName(request.Name, "floating IP", 63); err != nil {
		return err
	}
	if request.IPv4 != "" {
		address, err := netip.ParseAddr(request.IPv4)
		if err != nil || !address.Is4() || address.String() != request.IPv4 {
			return networkAuthorityValidationError("floating-IP address must be canonical IPv4")
		}
	}
	return nil
}

// FloatingIPRemoveRequest binds removal to one exact observed resource generation.
type FloatingIPRemoveRequest struct {
	ID                 string `json:"id"`
	NetworkID          string `json:"network_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate refuses incomplete identities or an unobserved generation.
func (request FloatingIPRemoveRequest) Validate() error {
	if !vmIDPattern.MatchString(request.ID) || !networkIDPattern.MatchString(request.NetworkID) ||
		request.ExpectedGeneration == 0 {
		return networkAuthorityValidationError(
			"floating-IP removal requires complete identities and the observed generation",
		)
	}
	return nil
}

// FloatingIPResult is a root-observed projection; it grants no address authority to a guest.
type FloatingIPResult struct {
	ID         string `json:"id"`
	NetworkID  string `json:"network_id"`
	Name       string `json:"name"`
	IPv4       string `json:"ipv4"`
	Generation uint64 `json:"generation"`
	Retired    bool   `json:"retired"`
}

func (record floatingIPRecord) result() FloatingIPResult {
	return FloatingIPResult{
		ID: record.ID, NetworkID: record.NetworkID, Name: record.Name, IPv4: record.IPv4,
		Generation: record.Generation, Retired: record.Retired,
	}
}
