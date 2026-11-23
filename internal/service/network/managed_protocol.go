package network

import (
	"net/netip"

	netlib "mvmctl/internal/lib/network"
	"mvmctl/pkg/errs"
)

const (
	// ManagedNetworkCreateAction creates or readmits one exact root-owned network.
	ManagedNetworkCreateAction = "managed-network-create"
	// ManagedNetworkRemoveAction removes one exact root-owned network.
	ManagedNetworkRemoveAction = "managed-network-remove"
)

// ManagedNetworkCreateRequest carries logical intent. Root resolves any uplink name.
type ManagedNetworkCreateRequest struct {
	NetworkID     string `json:"network_id"`
	Prefix        string `json:"prefix"`
	Gateway       string `json:"gateway"`
	NAT           bool   `json:"nat"`
	Uplink        string `json:"uplink"`
	UplinkGateway string `json:"uplink_gateway"`
}

// Validate admits only canonical immutable identifiers and resolved private IPv4 intent.
func (request ManagedNetworkCreateRequest) Validate() error {
	if !networkIDPattern.MatchString(request.NetworkID) {
		return networkAuthorityValidationError("managed network requires a canonical network identifier")
	}
	if request.Uplink != "" && (!request.NAT || !netlib.ValidUplinkName(request.Uplink)) {
		return networkAuthorityValidationError("managed network uplink requires NAT and a valid host interface name")
	}
	if request.UplinkGateway != "" {
		if _, ok := netlib.ParseUplinkGateway(request.UplinkGateway); !ok || request.Uplink == "" {
			return networkAuthorityValidationError(
				"uplink gateway requires a named interface and a valid IPv4 next hop",
			)
		}
	}
	_, err := request.configuration()
	return err
}

func (request ManagedNetworkCreateRequest) configuration() (managedNetworkL3, error) {
	prefix, prefixErr := netip.ParsePrefix(request.Prefix)
	gateway, gatewayErr := netip.ParseAddr(request.Gateway)
	if prefixErr != nil || gatewayErr != nil || prefix.String() != request.Prefix ||
		gateway.String() != request.Gateway {
		return managedNetworkL3{}, networkAuthorityValidationError(
			"managed network prefix and gateway must be canonical IPv4 values",
		)
	}
	return admitManagedNetworkL3(managedNetworkL3Input{prefix: prefix, gateway: &gateway})
}

// ManagedNetworkRemoveRequest identifies one network without accepting cleanup paths or selectors.
type ManagedNetworkRemoveRequest struct {
	NetworkID string `json:"network_id"`
}

// Validate requires the complete immutable network identity.
func (request ManagedNetworkRemoveRequest) Validate() error {
	if !networkIDPattern.MatchString(request.NetworkID) {
		return networkAuthorityValidationError("managed network removal requires a canonical network identifier")
	}
	return nil
}

// ManagedNetworkResult is the root-observed display projection of a successfully configured network.
type ManagedNetworkResult struct {
	NetworkID     string `json:"network_id"`
	Bridge        string `json:"bridge"`
	Prefix        string `json:"prefix"`
	Gateway       string `json:"gateway"`
	NAT           bool   `json:"nat"`
	Uplink        string `json:"uplink"`
	UplinkGateway string `json:"uplink_gateway"`
}

// Validate binds the acknowledgement to the exact requested logical intent.
func (result ManagedNetworkResult) Validate(request ManagedNetworkCreateRequest) error {
	if result.NetworkID != request.NetworkID || result.Prefix != request.Prefix || result.Gateway != request.Gateway ||
		result.NAT != request.NAT || result.Uplink != request.Uplink ||
		result.UplinkGateway != request.UplinkGateway || validateDerivedEffectLinkName(linkName(result.Bridge), "mvb") != nil {
		return errs.New(errs.CodeNetworkCreateFailed, "root network result differs from requested intent")
	}
	return request.Validate()
}

// ManagedNetworkRemovalResult acknowledges verified kernel and root-state removal.
type ManagedNetworkRemovalResult struct {
	NetworkID string `json:"network_id"`
}

// Validate binds removal acknowledgement to the exact requested network.
func (result ManagedNetworkRemovalResult) Validate(request ManagedNetworkRemoveRequest) error {
	if result.NetworkID != request.NetworkID {
		return errs.New(errs.CodeNetworkRemoveFailed, "root network removal result differs from requested identity")
	}
	return request.Validate()
}
