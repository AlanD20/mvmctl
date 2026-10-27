package firewall

// compileHostTraffic emits only input-hook grants. Host-originated output and VM-to-VM forwarding are unchanged.
func (compiler *generationCompiler) compileHostTraffic(intent generationIntent) {
	input := compiler.generation.inet.chains[1]
	for _, grant := range sortedCopy(intent.vmHostGrants, func(grant vmHostTrafficGrant) string { return grant.id }) {
		source := compiler.endpoints[grant.sourceVMID]
		network := compiler.networks[source.networkID]
		compiler.addRule(&compiler.generation.inet, input, "inet.input.allow.vm-host."+grant.id,
			joinExpressions(
				matchInputInterface(network.hostBridge),
				matchIPv4Family(),
				matchIPv4Address(12, source.ipv4),
				matchIPv4Address(16, network.hostIPv4),
				matchProtocol(grant.protocol),
				matchDestinationPorts(grant.ports),
				acceptVerdict(),
			))
	}
	for _, grant := range sortedCopy(intent.networkHostGrants,
		func(grant networkHostTrafficGrant) string { return grant.id }) {
		network := compiler.networks[grant.sourceNetworkID]
		compiler.addRule(&compiler.generation.inet, input, "inet.input.allow.network-host."+grant.id,
			joinExpressions(
				matchInputInterface(network.hostBridge),
				matchIPv4Family(),
				matchIPv4Prefix(12, network.subnet),
				matchIPv4Address(16, network.hostIPv4),
				matchProtocol(grant.protocol),
				matchDestinationPorts(grant.ports),
				acceptVerdict(),
			))
	}
}
