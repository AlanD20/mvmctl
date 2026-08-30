package firewall

import (
	"github.com/google/nftables/expr"
)

func (compiler *generationCompiler) compileINet(intent generationIntent) {
	forward := compiler.generation.inet.chains[0]
	input := compiler.generation.inet.chains[1]
	nat := compiler.generation.inet.chains[2]
	networks := sortedCopy(intent.networks, func(network networkIntent) string { return network.id })

	for _, network := range networks {
		compiler.addRule(&compiler.generation.inet, forward, "inet.forward.established."+network.id,
			joinExpressions(matchOutputInterface(network.hostBridge), acceptEstablishedRelated()))
		if network.masquerade {
			compiler.addRule(&compiler.generation.inet, forward, "inet.forward.egress."+network.id,
				joinExpressions(
					matchInputInterface(network.hostBridge),
					matchOutputInterface(network.uplinkInterface),
					matchIPv4Family(),
					matchIPv4Prefix(12, network.subnet),
					acceptVerdict(),
				))
		}
	}

	for _, grant := range sortedCopy(intent.vmGrants, func(grant vmTrafficGrant) string { return grant.id }) {
		source := compiler.endpoints[grant.sourceVMID]
		destination := compiler.endpoints[grant.destinationVMID]
		if source.networkID == destination.networkID {
			continue
		}
		compiler.addRule(&compiler.generation.inet, forward, "inet.forward.allow.vm."+grant.id,
			inetVMGrantExpressions(compiler.networks[source.networkID], compiler.networks[destination.networkID],
				source, destination, grant.protocol, grant.ports))
	}

	for _, grant := range sortedCopy(intent.networkGrants, func(grant networkTrafficGrant) string { return grant.id }) {
		source := compiler.networks[grant.sourceNetworkID]
		destination := compiler.endpoints[grant.destinationVMID]
		if source.id == destination.networkID {
			continue
		}
		compiler.addRule(&compiler.generation.inet, forward, "inet.forward.allow.network."+grant.id,
			inetNetworkGrantExpressions(source, compiler.networks[destination.networkID], destination,
				grant.protocol, grant.ports))
	}

	for _, network := range networks {
		compiler.addRule(&compiler.generation.inet, forward, "inet.forward.default-deny."+network.id,
			joinExpressions(matchInputInterface(network.hostBridge), dropVerdict()))
		compiler.addRule(&compiler.generation.inet, forward, "inet.forward.default-deny-inbound."+network.id,
			joinExpressions(matchOutputInterface(network.hostBridge), dropVerdict()))
		compiler.addRule(&compiler.generation.inet, input, "inet.input.established."+network.id,
			joinExpressions(matchInputInterface(network.hostBridge), acceptEstablishedRelated()))
	}

	for _, service := range sortedCopy(intent.hostServices, func(service hostServiceIntent) string { return service.id }) {
		network := compiler.networks[service.sourceNetworkID]
		compiler.addRule(&compiler.generation.inet, input, "inet.input.allow."+service.id,
			joinExpressions(
				matchInputInterface(network.hostBridge),
				matchIPv4Family(),
				matchIPv4Prefix(12, network.subnet),
				matchIPv4Address(16, network.hostIPv4),
				matchProtocol(service.protocol),
				matchDestinationPorts(service.ports),
				acceptVerdict(),
			))
	}

	for _, network := range networks {
		compiler.addRule(&compiler.generation.inet, input, "inet.input.default-deny."+network.id,
			joinExpressions(matchInputInterface(network.hostBridge), dropVerdict()))
		if network.masquerade {
			compiler.addRule(&compiler.generation.inet, nat, "inet.nat.masquerade."+network.id,
				joinExpressions(
					matchInputInterface(network.hostBridge),
					matchOutputInterface(network.uplinkInterface),
					matchIPv4Family(),
					matchIPv4Prefix(12, network.subnet),
					[]expr.Any{&expr.Masq{}},
				))
		}
	}
}

func inetVMGrantExpressions(
	sourceNetwork networkIntent,
	destinationNetwork networkIntent,
	source endpointIntent,
	destination endpointIntent,
	protocolValue protocol,
	ports portInterval,
) []expr.Any {
	return joinExpressions(
		matchInputInterface(sourceNetwork.hostBridge),
		matchOutputInterface(destinationNetwork.hostBridge),
		matchIPv4Family(),
		matchIPv4Address(12, source.ipv4),
		matchIPv4Address(16, destination.ipv4),
		matchProtocol(protocolValue),
		matchDestinationPorts(ports),
		acceptVerdict(),
	)
}

func inetNetworkGrantExpressions(
	source networkIntent,
	destinationNetwork networkIntent,
	destination endpointIntent,
	protocolValue protocol,
	ports portInterval,
) []expr.Any {
	return joinExpressions(
		matchInputInterface(source.hostBridge),
		matchOutputInterface(destinationNetwork.hostBridge),
		matchIPv4Family(),
		matchIPv4Prefix(12, source.subnet),
		matchIPv4Address(16, destination.ipv4),
		matchProtocol(protocolValue),
		matchDestinationPorts(ports),
		acceptVerdict(),
	)
}
