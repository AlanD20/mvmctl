package network

import (
	"net/netip"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/pkg/errs"
)

const (
	// HostServiceInspectAction resolves the fixed NoCloud gateway endpoint.
	HostServiceInspectAction = "network-host-service-inspect"
	// HostServiceApplyAction grants only the fixed NoCloud gateway endpoint.
	HostServiceApplyAction = "network-host-service-apply"
	// HostServiceRemoveAction revokes only the fixed NoCloud gateway endpoint.
	HostServiceRemoveAction = "network-host-service-remove"
)

// HostServiceRequest identifies an owned network; root chooses the service and all network facts.
type HostServiceRequest struct {
	NetworkID string `json:"network_id"`
}

// Validate admits only a canonical immutable network identity.
func (request HostServiceRequest) Validate() error {
	if !networkIDPattern.MatchString(request.NetworkID) {
		return errs.New(errs.CodeValidationFailed, "NoCloud service requires a canonical network identifier")
	}
	return nil
}

func (request HostServiceRequest) serviceID() string {
	return crypto.ContentHash("mvmctl:nocloud:v1:", request.NetworkID)
}

// HostServiceResult projects the root-owned gateway and fixed service port.
type HostServiceResult struct {
	NetworkID string `json:"network_id"`
	Gateway   string `json:"gateway"`
	Port      uint16 `json:"port"`
}

// Validate binds the result to the exact requested network and closed service endpoint.
func (result HostServiceResult) Validate(networkID string) error {
	gateway, err := netip.ParseAddr(result.Gateway)
	if result.NetworkID != networkID || !networkIDPattern.MatchString(networkID) || err != nil ||
		!gateway.Is4() || !gateway.IsPrivate() || gateway.String() != result.Gateway || result.Port != infra.NoCloudNetPort {
		return errs.New(errs.CodeCloudInitNetModeFailed, "root NoCloud endpoint does not match the requested network")
	}
	return nil
}
