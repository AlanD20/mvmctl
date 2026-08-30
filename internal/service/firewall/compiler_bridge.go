package firewall

import (
	"github.com/google/nftables/expr"
)

func (compiler *generationCompiler) compileBridge(intent generationIntent) {
	chain := compiler.generation.bridge.chains[0]
	endpoints := sortedCopy(intent.endpoints, func(endpoint endpointIntent) string { return endpoint.id })

	for _, endpoint := range endpoints {
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.spoof.mac."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchMACSource(endpoint.mac, expr.CmpOpNeq),
				dropVerdict(),
			))
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.spoof.ipv4."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchEtherType(etherTypeIPv4),
				rejectIPv4AddressMismatch(12, endpoint.ipv4),
				dropVerdict(),
			))
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.spoof.arp-mac."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchEtherType(etherTypeARP),
				matchARPSourceMAC(endpoint.mac, expr.CmpOpNeq),
				dropVerdict(),
			))
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.spoof.arp-ipv4."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchEtherType(etherTypeARP),
				matchARPAddress(14, endpoint.ipv4, expr.CmpOpNeq),
				dropVerdict(),
			))
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.unsupported-l3."+endpoint.id,
			joinExpressions(matchInputInterface(endpoint.hostVeth), rejectUnsupportedEtherType()))
	}

	for _, endpoint := range endpoints {
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.arp."+endpoint.id,
			joinExpressions(
				matchInputInterface(endpoint.hostVeth),
				matchEtherType(etherTypeARP),
				acceptVerdict(),
			))
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.established."+endpoint.id,
			joinExpressions(matchInputInterface(endpoint.hostVeth), acceptEstablishedRelated()))
	}

	for _, grant := range sortedCopy(intent.vmGrants, func(grant vmTrafficGrant) string { return grant.id }) {
		source := compiler.endpoints[grant.sourceVMID]
		destination := compiler.endpoints[grant.destinationVMID]
		if source.networkID != destination.networkID {
			continue
		}
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.allow.vm."+grant.id,
			bridgeVMGrantExpressions(source, destination, grant.protocol, grant.ports))
	}

	for _, grant := range sortedCopy(intent.networkGrants, func(grant networkTrafficGrant) string { return grant.id }) {
		source := compiler.networks[grant.sourceNetworkID]
		destination := compiler.endpoints[grant.destinationVMID]
		if source.id != destination.networkID {
			continue
		}
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.allow.network."+grant.id,
			bridgeNetworkGrantExpressions(source, destination, grant.protocol, grant.ports))
	}

	for _, endpoint := range endpoints {
		compiler.addRule(&compiler.generation.bridge, chain, "bridge.default-deny."+endpoint.id,
			joinExpressions(matchInputInterface(endpoint.hostVeth), dropVerdict()))
	}
}

func bridgeVMGrantExpressions(
	source endpointIntent,
	destination endpointIntent,
	protocolValue protocol,
	ports portInterval,
) []expr.Any {
	return joinExpressions(
		matchInputInterface(source.hostVeth),
		matchOutputInterface(destination.hostVeth),
		matchEtherType(etherTypeIPv4),
		matchIPv4Address(12, source.ipv4),
		matchIPv4Address(16, destination.ipv4),
		matchProtocol(protocolValue),
		matchDestinationPorts(ports),
		acceptVerdict(),
	)
}

func bridgeNetworkGrantExpressions(
	source networkIntent,
	destination endpointIntent,
	protocolValue protocol,
	ports portInterval,
) []expr.Any {
	return joinExpressions(
		matchOutputInterface(destination.hostVeth),
		matchEtherType(etherTypeIPv4),
		matchIPv4Prefix(12, source.subnet),
		matchIPv4Address(16, destination.ipv4),
		matchProtocol(protocolValue),
		matchDestinationPorts(ports),
		acceptVerdict(),
	)
}
