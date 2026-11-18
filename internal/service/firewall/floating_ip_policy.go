package firewall

import (
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

func (intent Intent) validateDurableFloatingIPGrants(
	networks map[string]NetworkIntent,
	reservations map[string]EndpointReservation,
	ids map[string]struct{},
) error {
	resources := make(map[string]FloatingIPBinding, len(intent.FloatingIPs))
	for _, resource := range intent.FloatingIPs {
		resources[resource.ID] = resource
	}
	for _, grant := range intent.VMFloatingIPGrants {
		if grant.Incarnation == "" || !validPolicyOwnership(grant.ClaimID, grant.Incarnation) ||
			grant.ID != vmFloatingIPGrantDigest(grant) {
			return invalidGeneration("invalid durable VM-to-floating-IP policy identity or ownership")
		}
		if err := validateGrantBase(grant.ID, protocol(grant.Protocol),
			portInterval{first: grant.Ports.First, last: grant.Ports.Last}, ids); err != nil {
			return err
		}
		source, sourceFound := reservations[grant.SourceVMID]
		resource, resourceFound := resources[grant.DestinationFloatingIPID]
		if !sourceFound || !resourceFound || source.OwnerUID != resource.OwnerUID {
			return invalidGeneration("durable VM-to-floating-IP grant references unowned authority")
		}
	}
	for _, grant := range intent.NetworkFloatingIPGrants {
		if grant.Incarnation == "" || !validPolicyOwnership(grant.ClaimID, grant.Incarnation) ||
			grant.ID != networkFloatingIPGrantDigest(grant) {
			return invalidGeneration("invalid durable network-to-floating-IP policy identity or ownership")
		}
		if err := validateGrantBase(grant.ID, protocol(grant.Protocol),
			portInterval{first: grant.Ports.First, last: grant.Ports.Last}, ids); err != nil {
			return err
		}
		source, sourceFound := networks[grant.SourceNetworkID]
		resource, resourceFound := resources[grant.DestinationFloatingIPID]
		if !sourceFound || !resourceFound || source.OwnerUID != resource.OwnerUID {
			return invalidGeneration("durable network-to-floating-IP grant references unowned authority")
		}
	}
	return nil
}

func vmFloatingIPGrantDigest(grant VMFloatingIPTrafficGrant) string {
	return crypto.VMToFloatingIPTrafficPolicyDigest(model.VMToFloatingIPTrafficAllowIntent{
		SourceVMID: grant.SourceVMID, DestinationFloatingIPID: grant.DestinationFloatingIPID,
		Protocol:             floatingIPPolicyProtocol(grant.Protocol),
		DestinationPortStart: int(grant.Ports.First), DestinationPortEnd: int(grant.Ports.Last),
	})
}

func networkFloatingIPGrantDigest(grant NetworkFloatingIPTrafficGrant) string {
	return crypto.NetworkToFloatingIPTrafficPolicyDigest(model.NetworkToFloatingIPTrafficAllowIntent{
		SourceNetworkID: grant.SourceNetworkID, DestinationFloatingIPID: grant.DestinationFloatingIPID,
		Protocol:             floatingIPPolicyProtocol(grant.Protocol),
		DestinationPortStart: int(grant.Ports.First), DestinationPortEnd: int(grant.Ports.Last),
	})
}

func floatingIPPolicyProtocol(value Protocol) model.TrafficProtocol {
	switch value {
	case TCP:
		return model.TrafficProtocolTCP
	case UDP:
		return model.TrafficProtocolUDP
	default:
		return ""
	}
}

func (intent Intent) resolveFloatingIPGrants(
	resolved *generationIntent, active map[string]struct{}, activeNetworks map[string]struct{},
) {
	destinations := make(map[string]struct{}, len(resolved.floatingIPs))
	for _, resource := range resolved.floatingIPs {
		if resource.ownerVMID != "" {
			destinations[resource.id] = struct{}{}
		}
	}
	for _, grant := range intent.VMFloatingIPGrants {
		_, sourceActive := active[grant.SourceVMID]
		_, destinationActive := destinations[grant.DestinationFloatingIPID]
		if !sourceActive || !destinationActive {
			continue
		}
		resolved.vmFloatingIPGrants = append(resolved.vmFloatingIPGrants, vmFloatingIPTrafficGrant{
			id: grant.ID, sourceVMID: grant.SourceVMID, destinationFloatingIPID: grant.DestinationFloatingIPID,
			protocol: protocol(grant.Protocol), ports: portInterval{first: grant.Ports.First, last: grant.Ports.Last},
		})
	}
	for _, grant := range intent.NetworkFloatingIPGrants {
		_, sourceActive := activeNetworks[grant.SourceNetworkID]
		_, destinationActive := destinations[grant.DestinationFloatingIPID]
		if !sourceActive || !destinationActive {
			continue
		}
		resolved.networkFloatingIPGrants = append(resolved.networkFloatingIPGrants, networkFloatingIPTrafficGrant{
			id: grant.ID, sourceNetworkID: grant.SourceNetworkID, destinationFloatingIPID: grant.DestinationFloatingIPID,
			protocol: protocol(grant.Protocol), ports: portInterval{first: grant.Ports.First, last: grant.Ports.Last},
		})
	}
}

func validateFloatingIPGrants(
	intent generationIntent, endpoints map[string]endpointIntent, networks map[string]networkIntent,
	ids map[string]struct{},
) error {
	resources := make(map[string]struct{}, len(intent.floatingIPs))
	for _, resource := range intent.floatingIPs {
		resources[resource.id] = struct{}{}
	}
	for _, grant := range intent.vmFloatingIPGrants {
		if err := validateGrantBase(grant.id, grant.protocol, grant.ports, ids); err != nil {
			return err
		}
		if _, found := endpoints[grant.sourceVMID]; !found {
			return invalidGeneration("VM-to-floating-IP grant references an unknown source")
		}
		if _, found := resources[grant.destinationFloatingIPID]; !found {
			return invalidGeneration("VM-to-floating-IP grant references an unknown resource")
		}
	}
	for _, grant := range intent.networkFloatingIPGrants {
		if err := validateGrantBase(grant.id, grant.protocol, grant.ports, ids); err != nil {
			return err
		}
		if _, found := networks[grant.sourceNetworkID]; !found {
			return invalidGeneration("network-to-floating-IP grant references an unknown source")
		}
		if _, found := resources[grant.destinationFloatingIPID]; !found {
			return invalidGeneration("network-to-floating-IP grant references an unknown resource")
		}
	}
	return nil
}

// floatingIPDestination uses the active owner's topology solely for delivery. It never expands
// destination addresses: a resource grant must not authorize the owner's primary or other aliases.
func (compiler *generationCompiler) floatingIPDestination(id string) (endpointIntent, bool) {
	resource := compiler.floatingIPs[id]
	if resource.ownerVMID == "" {
		return endpointIntent{}, false
	}
	destination := compiler.endpoints[resource.ownerVMID]
	destination.ipv4 = resource.ipv4
	return destination, true
}

func (compiler *generationCompiler) compileBridgeFloatingIPGrants(intent generationIntent) {
	forward := compiler.generation.bridge.chains[1]
	for _, grant := range sortedCopy(intent.vmFloatingIPGrants,
		func(grant vmFloatingIPTrafficGrant) string { return grant.id }) {
		destination, active := compiler.floatingIPDestination(grant.destinationFloatingIPID)
		source := compiler.endpoints[grant.sourceVMID]
		if !active || source.networkID != destination.networkID {
			continue
		}
		for _, variant := range compiler.endpointAddressVariants(source) {
			compiler.addRule(&compiler.generation.bridge, forward,
				"bridge.allow.vm-floating-ip."+grant.id+variant.suffix,
				bridgeVMGrantExpressions(variant.endpoint, destination, grant.protocol, grant.ports))
		}
	}
	for _, grant := range sortedCopy(intent.networkFloatingIPGrants,
		func(grant networkFloatingIPTrafficGrant) string { return grant.id }) {
		destination, active := compiler.floatingIPDestination(grant.destinationFloatingIPID)
		source := compiler.networks[grant.sourceNetworkID]
		if !active || source.id != destination.networkID {
			continue
		}
		compiler.addRule(&compiler.generation.bridge, forward, "bridge.allow.network-floating-ip."+grant.id,
			bridgeNetworkGrantExpressions(source, destination, grant.protocol, grant.ports))
	}
}

func (compiler *generationCompiler) compileINetFloatingIPGrants(intent generationIntent) {
	forward := compiler.generation.inet.chains[0]
	for _, grant := range sortedCopy(intent.vmFloatingIPGrants,
		func(grant vmFloatingIPTrafficGrant) string { return grant.id }) {
		destination, active := compiler.floatingIPDestination(grant.destinationFloatingIPID)
		if !active {
			continue
		}
		source := compiler.endpoints[grant.sourceVMID]
		for _, variant := range compiler.endpointAddressVariants(source) {
			compiler.addRule(&compiler.generation.inet, forward,
				"inet.forward.allow.vm-floating-ip."+grant.id+variant.suffix,
				inetVMGrantExpressions(compiler.networks[source.networkID], compiler.networks[destination.networkID],
					variant.endpoint, destination, grant.protocol, grant.ports))
		}
	}
	for _, grant := range sortedCopy(intent.networkFloatingIPGrants,
		func(grant networkFloatingIPTrafficGrant) string { return grant.id }) {
		destination, active := compiler.floatingIPDestination(grant.destinationFloatingIPID)
		if !active {
			continue
		}
		source := compiler.networks[grant.sourceNetworkID]
		compiler.addRule(&compiler.generation.inet, forward, "inet.forward.allow.network-floating-ip."+grant.id,
			inetNetworkGrantExpressions(source, compiler.networks[destination.networkID], destination,
				grant.protocol, grant.ports))
	}
}

func encodeCanonicalFloatingIPGrants(encoder *generationDigestEncoder, intent generationIntent) {
	if len(intent.vmFloatingIPGrants) == 0 && len(intent.networkFloatingIPGrants) == 0 {
		return
	}
	vmGrants := sortedCopy(intent.vmFloatingIPGrants, func(grant vmFloatingIPTrafficGrant) string { return grant.id })
	encoder.count("intent.vm-floating-ip-grants", len(vmGrants))
	for _, grant := range vmGrants {
		encoder.beginRecord("intent.vm-floating-ip-grant")
		encoder.text("id", grant.id)
		encoder.text("source-vm-id", grant.sourceVMID)
		encoder.text("destination-floating-ip-id", grant.destinationFloatingIPID)
		encodeCanonicalProtocolAndPorts(encoder, grant.protocol, grant.ports)
		encoder.endRecord("intent.vm-floating-ip-grant")
	}
	networkGrants := sortedCopy(intent.networkFloatingIPGrants,
		func(grant networkFloatingIPTrafficGrant) string { return grant.id })
	encoder.count("intent.network-floating-ip-grants", len(networkGrants))
	for _, grant := range networkGrants {
		encoder.beginRecord("intent.network-floating-ip-grant")
		encoder.text("id", grant.id)
		encoder.text("source-network-id", grant.sourceNetworkID)
		encoder.text("destination-floating-ip-id", grant.destinationFloatingIPID)
		encodeCanonicalProtocolAndPorts(encoder, grant.protocol, grant.ports)
		encoder.endRecord("intent.network-floating-ip-grant")
	}
}
