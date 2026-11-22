package firewall

import (
	"github.com/google/nftables/expr"
)

func (compiler *generationCompiler) compileBridge(intent generationIntent) error {
	prerouting := compiler.generation.bridge.chains[0]
	forward := compiler.generation.bridge.chains[1]
	endpoints := sortedCopy(intent.endpoints, func(endpoint endpointIntent) string { return endpoint.id })

	for _, endpoint := range endpoints {
		guards := ingressGuardSet{}
		ipv4Source := rejectIPv4AddressMismatch(12, endpoint.ipv4)
		arpSource := matchARPAddress(14, endpoint.ipv4, expr.CmpOpNeq)
		if compiler.generation.bridge.secondarySet != nil {
			ipv4Source = rejectSecondaryIPv4Source(endpoint.ipv4)
			arpSource = rejectSecondaryARPSource(endpoint.ipv4)
		}
		guards.mac = compiler.addRule(&compiler.generation.bridge, prerouting, "bridge.spoof.mac."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchMACSource(endpoint.mac, expr.CmpOpNeq),
				dropVerdict(),
			))
		guards.ipv4 = compiler.addRule(&compiler.generation.bridge, prerouting, "bridge.spoof.ipv4."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchEtherType(etherTypeIPv4),
				ipv4Source,
				dropVerdict(),
			))
		// CRITICAL: Fixed sender-address offsets require the Ethernet/IPv4 ARP header shape first.
		// These compiler-owned primitives are immutable during encoding, validation, and nftables marshaling.
		// Each guard gets its own expression slice; only this endpoint's ARP match prefix is shared.
		arpInput := joinExpressions(matchInputInterface(endpoint.hostVeth), matchEtherType(etherTypeARP))
		guards.arpHeader = compiler.addRule(
			&compiler.generation.bridge,
			prerouting,
			"bridge.spoof.arp-header."+endpoint.id,
			joinExpressions(arpInput, matchARPHeader(expr.CmpOpNeq), dropVerdict()),
		)
		guards.arpOperation = compiler.addRule(
			&compiler.generation.bridge, prerouting, "bridge.spoof.arp-operation."+endpoint.id,
			joinExpressions(arpInput, matchARPOperation(expr.CmpOpNeq), dropVerdict()))
		guards.arpMAC = compiler.addRule(&compiler.generation.bridge, prerouting, "bridge.spoof.arp-mac."+endpoint.id,
			joinExpressions(
				arpInput,
				matchARPSourceMAC(endpoint.mac, expr.CmpOpNeq),
				dropVerdict(),
			))
		guards.arpIPv4 = compiler.addRule(&compiler.generation.bridge, prerouting, "bridge.spoof.arp-ipv4."+endpoint.id,
			joinExpressions(
				arpInput,
				arpSource,
				dropVerdict(),
			))
		guards.unsupportedL3 = compiler.addRule(
			&compiler.generation.bridge,
			prerouting,
			"bridge.unsupported-l3."+endpoint.id,
			joinExpressions(matchInputInterface(endpoint.hostVeth), rejectUnsupportedEtherType()))
		compiler.generation.ingressGuards[endpoint.id] = guards
	}

	for _, endpoint := range endpoints {
		compiler.addRule(&compiler.generation.bridge, forward, "bridge.arp."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchEtherType(etherTypeARP),
				matchSupportedARP(),
				acceptVerdict(),
			))
	}

	for _, grant := range sortedCopy(intent.vmGrants, func(grant vmTrafficGrant) string { return grant.id }) {
		source := compiler.endpoints[grant.sourceVMID]
		destination := compiler.endpoints[grant.destinationVMID]
		if source.networkID != destination.networkID {
			continue
		}
		for _, sourceAddress := range compiler.endpointAddressVariants(source) {
			for _, destinationAddress := range compiler.endpointAddressVariants(destination) {
				compiler.addDirectionalTrafficRules(
					&compiler.generation.bridge,
					forward,
					"bridge.allow.vm."+grant.id+addressPairRuleSuffix(sourceAddress, destinationAddress),
					func(direction trafficDirection) []expr.Any {
						return bridgeVMGrantExpressions(sourceAddress.endpoint, destinationAddress.endpoint,
							grant.protocol, grant.ports, direction)
					},
				)
			}
		}
	}

	for _, grant := range sortedCopy(intent.networkGrants, func(grant networkTrafficGrant) string { return grant.id }) {
		source := compiler.networks[grant.sourceNetworkID]
		destination := compiler.endpoints[grant.destinationVMID]
		if source.id != destination.networkID {
			continue
		}
		for _, destinationAddress := range compiler.endpointAddressVariants(destination) {
			compiler.addDirectionalTrafficRules(
				&compiler.generation.bridge,
				forward,
				"bridge.allow.network."+grant.id+destinationAddress.suffix,
				func(direction trafficDirection) []expr.Any {
					return bridgeNetworkGrantExpressions(source, destinationAddress.endpoint,
						grant.protocol, grant.ports, direction)
				},
			)
		}
	}

	compiler.compileBridgeFloatingIPGrants(intent)
	if err := compiler.compileLabelTraffic(intent.labelTraffic, &compiler.generation.bridge,
		forward, "bridge.allow.labels.", matchEtherType(etherTypeIPv4)); err != nil {
		return err
	}

	for _, endpoint := range endpoints {
		compiler.addRule(&compiler.generation.bridge, forward, "bridge.default-deny."+endpoint.id,
			joinExpressions(matchInputInterface(endpoint.hostVeth), dropVerdict()))
	}
	return nil
}

func bridgeVMGrantExpressions(
	source endpointIntent,
	destination endpointIntent,
	protocolValue protocol,
	ports portInterval,
	direction trafficDirection,
) []expr.Any {
	sourceOffset, destinationOffset, sourceKey, destinationKey := direction.endpoints()
	return joinExpressions(
		matchInterface(sourceKey, source.hostVeth),
		matchInterface(destinationKey, destination.hostVeth),
		matchEtherType(etherTypeIPv4),
		matchIPv4Address(sourceOffset, source.ipv4),
		matchIPv4Address(destinationOffset, destination.ipv4),
		matchOriginalTraffic(direction, protocolValue, ports),
		acceptVerdict(),
	)
}

func bridgeNetworkGrantExpressions(
	source networkIntent,
	destination endpointIntent,
	protocolValue protocol,
	ports portInterval,
	direction trafficDirection,
) []expr.Any {
	sourceOffset, destinationOffset, _, destinationKey := direction.endpoints()
	return joinExpressions(
		matchInterface(destinationKey, destination.hostVeth),
		matchEtherType(etherTypeIPv4),
		matchIPv4Prefix(sourceOffset, source.subnet),
		matchIPv4Address(destinationOffset, destination.ipv4),
		matchOriginalTraffic(direction, protocolValue, ports),
		acceptVerdict(),
	)
}
