package firewall

import (
	"encoding/json"
	"maps"
	"slices"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

// VMPolicyEndpoint selects one exact VM or an owner-scoped label set.
type VMPolicyEndpoint struct {
	VMID   string                `json:"vm_id,omitempty"`
	Labels model.VMLabelSelector `json:"labels,omitempty"`
}

// LabelTrafficPolicy is root intent. OwnerUID comes from privileged authentication.
type LabelTrafficPolicy struct {
	ID          string           `json:"id"`
	OwnerUID    uint32           `json:"owner_uid"`
	Source      VMPolicyEndpoint `json:"source"`
	Destination VMPolicyEndpoint `json:"destination"`
	Protocol    Protocol         `json:"protocol"`
	Ports       PortInterval     `json:"ports"`
	ClaimID     string           `json:"claim_id"`
	Incarnation string           `json:"incarnation"`
}

// LabelTrafficPolicyID is scoped to the authenticated operator and immutable policy tuple.
func LabelTrafficPolicyID(
	ownerUID uint32, source, destination VMPolicyEndpoint, transport Protocol, ports PortInterval,
) (string, error) {
	return (LabelTrafficPolicy{
		OwnerUID: ownerUID, Source: source, Destination: destination, Protocol: transport, Ports: ports,
	}).identity()
}

func (policy LabelTrafficPolicy) identity() (string, error) {
	canonical, err := json.Marshal(struct {
		OwnerUID    uint32
		Source      VMPolicyEndpoint
		Destination VMPolicyEndpoint
		Protocol    Protocol
		Ports       PortInterval
	}{policy.OwnerUID, policy.Source, policy.Destination, policy.Protocol, policy.Ports})
	if err != nil {
		return "", err
	}
	return crypto.SHA256(append([]byte("mvmctl.label-traffic.v1\x00"), canonical...)), nil
}

func validateLabelPolicyEndpoint(endpoint VMPolicyEndpoint, ownerUID uint32) error {
	if endpoint.VMID != "" {
		if endpoint.Labels != nil {
			return invalidGeneration("exact VM and label selector are mutually exclusive")
		}
		return validateVMLabelIdentity(ownerUID, endpoint.VMID)
	}
	return endpoint.Labels.Validate()
}

func (policy LabelTrafficPolicy) validate(
	reservations map[string]EndpointReservation, identities map[string]struct{},
) error {
	if policy.OwnerUID == 0 || policy.Incarnation == "" || !validPolicyOwnership(policy.ClaimID, policy.Incarnation) {
		return invalidGeneration("invalid label traffic policy ownership")
	}
	if policy.Source.VMID != "" && policy.Destination.VMID != "" {
		return invalidGeneration("label traffic policy requires at least one selector")
	}
	for _, endpoint := range []VMPolicyEndpoint{policy.Source, policy.Destination} {
		if err := validateLabelPolicyEndpoint(endpoint, policy.OwnerUID); err != nil {
			return err
		}
		if endpoint.VMID != "" {
			reservation, found := reservations[endpoint.VMID]
			if !found || reservation.OwnerUID != policy.OwnerUID {
				return invalidGeneration("label traffic policy references an unowned exact endpoint")
			}
		}
	}
	canonical, err := policy.identity()
	if err != nil {
		return err
	}
	if policy.ID != canonical {
		return invalidGeneration("label traffic policy identity differs from its intent")
	}
	return validateGrantBase(policy.ID, protocol(policy.Protocol),
		portInterval{first: policy.Ports.First, last: policy.Ports.Last}, identities)
}

type labelTrafficMembership struct {
	policy       LabelTrafficPolicy
	sources      []EndpointBinding
	destinations []EndpointBinding
}

type ownerLabelSelectorKey struct {
	ownerUID uint32
	labels   string
}

// materializeLabelTraffic projects active endpoint membership once per distinct selector.
// It retains each side separately so policies never require a list of every source/destination pair.
func (intent Intent) materializeLabelTraffic() ([]labelTrafficMembership, error) {
	if len(intent.LabelTrafficPolicies) == 0 {
		return nil, nil
	}
	active := make(map[string]EndpointBinding, len(intent.Endpoints))
	byOwner := make(map[uint32][]EndpointBinding)
	for _, endpoint := range intent.Endpoints {
		active[endpoint.VMID] = endpoint
		byOwner[endpoint.OwnerUID] = append(byOwner[endpoint.OwnerUID], endpoint)
	}
	cache := make(map[ownerLabelSelectorKey][]EndpointBinding)
	resolve := func(ownerUID uint32, selector VMPolicyEndpoint) ([]EndpointBinding, error) {
		if selector.VMID != "" {
			if endpoint, found := active[selector.VMID]; found && endpoint.OwnerUID == ownerUID {
				return []EndpointBinding{endpoint}, nil
			}
			return nil, nil
		}
		canonical, err := json.Marshal(selector.Labels)
		if err != nil {
			return nil, err
		}
		key := ownerLabelSelectorKey{ownerUID, string(canonical)}
		if members, found := cache[key]; found {
			return members, nil
		}
		var members []EndpointBinding
		for _, endpoint := range byOwner[ownerUID] {
			record, found := intent.VMLabels[endpoint.VMID]
			if found && record.OwnerUID == ownerUID && !record.Retired && selector.Labels.Matches(record.Labels) {
				members = append(members, endpoint)
			}
		}
		members = sortedCopy(members, func(endpoint EndpointBinding) string { return endpoint.VMID })
		cache[key] = members
		return members, nil
	}

	var memberships []labelTrafficMembership
	for _, policy := range sortedCopy(intent.LabelTrafficPolicies, func(policy LabelTrafficPolicy) string {
		return policy.ID
	}) {
		sources, err := resolve(policy.OwnerUID, policy.Source)
		if err != nil {
			return nil, err
		}
		destinations, err := resolve(policy.OwnerUID, policy.Destination)
		if err != nil {
			return nil, err
		}
		if len(sources) != 0 && len(destinations) != 0 {
			memberships = append(memberships, labelTrafficMembership{policy, sources, destinations})
		}
	}
	return memberships, nil
}

func cloneLabelTrafficPolicies(policies []LabelTrafficPolicy) []LabelTrafficPolicy {
	cloned := slices.Clone(policies)
	for index := range cloned {
		cloned[index].Source.Labels = maps.Clone(cloned[index].Source.Labels)
		cloned[index].Destination.Labels = maps.Clone(cloned[index].Destination.Labels)
	}
	return cloned
}
