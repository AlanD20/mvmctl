package network

import (
	"context"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/firewall"
	"mvmctl/pkg/errs"
)

const (
	VMFloatingIPTrafficPolicyApplyAction       = "traffic-policy-vm-floating-ip-apply"
	VMFloatingIPTrafficPolicyRemoveAction      = "traffic-policy-vm-floating-ip-remove"
	NetworkFloatingIPTrafficPolicyApplyAction  = "traffic-policy-network-floating-ip-apply"
	NetworkFloatingIPTrafficPolicyRemoveAction = "traffic-policy-network-floating-ip-remove"
)

// VMFloatingIPTrafficPolicyRequest accepts immutable selectors, never an address or current owner.
type VMFloatingIPTrafficPolicyRequest struct {
	ClaimID                 string                `json:"claim_id,omitempty"`
	Incarnation             string                `json:"incarnation,omitempty"`
	SourceVMID              string                `json:"source_vm_id"`
	DestinationFloatingIPID string                `json:"destination_floating_ip_id"`
	Protocol                model.TrafficProtocol `json:"protocol"`
	PortStart               uint16                `json:"port_start"`
	PortEnd                 uint16                `json:"port_end"`
}

func (request VMFloatingIPTrafficPolicyRequest) Validate() error {
	if err := validateFloatingIPPolicyOwnership(request.ClaimID, request.Incarnation); err != nil {
		return err
	}
	if !vmIDPattern.MatchString(request.SourceVMID) || !vmIDPattern.MatchString(request.DestinationFloatingIPID) {
		return networkAuthorityValidationError("floating-IP policy requires canonical source and resource IDs")
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

func (request VMFloatingIPTrafficPolicyRequest) PolicyID() string {
	return crypto.VMToFloatingIPTrafficPolicyDigest(model.VMToFloatingIPTrafficAllowIntent{
		SourceVMID: request.SourceVMID, DestinationFloatingIPID: request.DestinationFloatingIPID,
		Protocol: request.Protocol, DestinationPortStart: int(request.PortStart), DestinationPortEnd: int(request.PortEnd),
	})
}

func (server *TrafficPolicyServer) ApplyVMFloatingIPTrafficPolicy(
	ctx context.Context, ownerUID uint32, request VMFloatingIPTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.SourceVMID})
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	sourceNetworkID := authority.topologies[request.SourceVMID].NetworkID
	if err := authority.beginFloatingIPPolicy(ctx, sourceNetworkID, request.DestinationFloatingIPID, false); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	receipt, err := authority.firewall.EnsureVMFloatingIPTrafficGrant(ctx, firewall.VMFloatingIPTrafficGrant{
		ID: request.PolicyID(), SourceVMID: request.SourceVMID, DestinationFloatingIPID: request.DestinationFloatingIPID,
		Protocol: trafficFirewallProtocol(request.Protocol), Ports: firewall.PortInterval{
			First: request.PortStart,
			Last:  request.PortEnd,
		},
	}, request.ClaimID, request.Incarnation)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID(), ClaimID: receipt.ClaimID,
		Incarnation: receipt.Incarnation, WasCreated: receipt.WasCreated}, nil
}

func (server *TrafficPolicyServer) RemoveVMFloatingIPTrafficPolicy(
	ctx context.Context, ownerUID uint32, request VMFloatingIPTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.SourceVMID})
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	sourceNetworkID := authority.topologies[request.SourceVMID].NetworkID
	if err := authority.beginFloatingIPPolicy(ctx, sourceNetworkID, request.DestinationFloatingIPID, true); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.RemoveOwnedVMFloatingIPTrafficGrant(
		ctx,
		request.PolicyID(),
		request.ClaimID,
		request.Incarnation,
	); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// NetworkFloatingIPTrafficPolicyRequest accepts immutable selectors, never an address or current owner.
type NetworkFloatingIPTrafficPolicyRequest struct {
	ClaimID                 string                `json:"claim_id,omitempty"`
	Incarnation             string                `json:"incarnation,omitempty"`
	SourceNetworkID         string                `json:"source_network_id"`
	DestinationFloatingIPID string                `json:"destination_floating_ip_id"`
	Protocol                model.TrafficProtocol `json:"protocol"`
	PortStart               uint16                `json:"port_start"`
	PortEnd                 uint16                `json:"port_end"`
}

func (request NetworkFloatingIPTrafficPolicyRequest) Validate() error {
	if err := validateFloatingIPPolicyOwnership(request.ClaimID, request.Incarnation); err != nil {
		return err
	}
	if !networkIDPattern.MatchString(
		request.SourceNetworkID,
	) || !vmIDPattern.MatchString(request.DestinationFloatingIPID) {
		return networkAuthorityValidationError("floating-IP policy requires canonical source and resource IDs")
	}
	return validateTrafficPolicyPorts(request.Protocol, request.PortStart, request.PortEnd)
}

func (request NetworkFloatingIPTrafficPolicyRequest) PolicyID() string {
	return crypto.NetworkToFloatingIPTrafficPolicyDigest(model.NetworkToFloatingIPTrafficAllowIntent{
		SourceNetworkID: request.SourceNetworkID, DestinationFloatingIPID: request.DestinationFloatingIPID,
		Protocol: request.Protocol, DestinationPortStart: int(request.PortStart), DestinationPortEnd: int(request.PortEnd),
	})
}

func (server *TrafficPolicyServer) ApplyNetworkFloatingIPTrafficPolicy(
	ctx context.Context, ownerUID uint32, request NetworkFloatingIPTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, nil)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	sourceNetworkID := request.SourceNetworkID
	if err := authority.beginFloatingIPPolicy(ctx, sourceNetworkID, request.DestinationFloatingIPID, false); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	receipt, err := authority.firewall.EnsureNetworkFloatingIPTrafficGrant(ctx, firewall.NetworkFloatingIPTrafficGrant{
		ID: request.PolicyID(), SourceNetworkID: request.SourceNetworkID,
		DestinationFloatingIPID: request.DestinationFloatingIPID,
		Protocol:                trafficFirewallProtocol(request.Protocol), Ports: firewall.PortInterval{
			First: request.PortStart,
			Last:  request.PortEnd,
		},
	}, request.ClaimID, request.Incarnation)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID(), ClaimID: receipt.ClaimID,
		Incarnation: receipt.Incarnation, WasCreated: receipt.WasCreated}, nil
}

func (server *TrafficPolicyServer) RemoveNetworkFloatingIPTrafficPolicy(
	ctx context.Context, ownerUID uint32, request NetworkFloatingIPTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, nil)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	sourceNetworkID := request.SourceNetworkID
	if err := authority.beginFloatingIPPolicy(ctx, sourceNetworkID, request.DestinationFloatingIPID, true); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.RemoveOwnedNetworkFloatingIPTrafficGrant(
		ctx,
		request.PolicyID(),
		request.ClaimID,
		request.Incarnation,
	); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// beginFloatingIPPolicy admits destination authority independently of whichever VM currently owns it.
// The source VM (if any) is already locked; both networks are acquired in canonical order before firewall.
func (authority *trafficPolicyAuthority) beginFloatingIPPolicy(
	ctx context.Context, sourceNetworkID, resourceID string, allowRetired bool,
) (returnErr error) {
	store, err := openFloatingIPStore(ctx, authority.store, false)
	if err != nil {
		return err
	}
	if store == nil {
		return errs.NotFound(errs.CodeNetworkNotFound, resourceID)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"release floating-IP policy store",
			store.release(context.WithoutCancel(ctx)),
		)
	}()
	before, _, _, found, err := store.read(ctx, resourceID)
	if err != nil {
		return err
	}
	if err := admitFloatingIPPolicyRecord(before, found, authority.ownerUID, resourceID, allowRetired); err != nil {
		return err
	}
	if err := authority.beginPolicyNetworks(ctx, sourceNetworkID, before.NetworkID); err != nil {
		return err
	}
	current, _, _, found, err := store.read(ctx, resourceID)
	if err != nil {
		return err
	}
	if err := admitFloatingIPPolicyRecord(current, found, authority.ownerUID, resourceID, allowRetired); err != nil {
		return err
	}
	if current.NetworkID != before.NetworkID || current.IPv4 != before.IPv4 {
		return networkAuthorityCorruptError("floating-IP policy resource scope changed while acquiring locks", nil)
	}
	if current.Retired {
		return authority.firewall.VerifyRetiredFloatingIPPolicyResource(ctx, current.ID)
	}
	return authority.firewall.VerifyFloatingIPPolicyResource(ctx, floatingIPFirewallBinding(current, current.Owner))
}

func admitFloatingIPPolicyRecord(
	record floatingIPRecord,
	found bool,
	ownerUID uint32,
	resourceID string,
	allowRetired bool,
) error {
	if !found || record.OwnerUID != ownerUID || record.ID != resourceID || (record.Retired && !allowRetired) {
		return networkAuthorityValidationError("floating-IP policy destination is unavailable or foreign")
	}
	if err := validateFloatingIPRecord(record); err != nil {
		return err
	}
	if record.Phase != floatingIPUnassociated && record.Phase != floatingIPOwned {
		return networkAuthorityValidationError("floating-IP policy destination requires completed ownership recovery")
	}
	return nil
}

// Operator-created grants have an empty claim but still carry a root-minted incarnation for exact revocation.
func validateFloatingIPPolicyOwnership(claim, incarnation string) error {
	if (claim != "" && !policyOwnershipToken.MatchString(claim)) ||
		(incarnation != "" && !policyOwnershipToken.MatchString(incarnation)) {
		return networkAuthorityValidationError("invalid floating-IP policy ownership receipt")
	}
	return nil
}
