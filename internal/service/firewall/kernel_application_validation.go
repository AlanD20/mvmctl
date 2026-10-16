package firewall

import (
	"slices"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

func validateKernelApplicationReplacement(
	record generationRecord,
	observedBefore kernelObservation,
	expectedBefore compiledGeneration,
	target compiledGeneration,
) error {
	if err := validateKernelApplicationTarget(expectedBefore); err != nil {
		return err
	}
	if err := validateKernelApplicationTarget(target); err != nil {
		return err
	}
	if err := validateGenerationRecord(record); err != nil {
		return err
	}
	beforeIdentity := kernelApplicationGenerationIdentity(expectedBefore)
	targetIdentity := kernelApplicationGenerationIdentity(target)
	if record.lifecycle != generationRecordApplying || record.before == nil || record.target == nil ||
		record.committed != beforeIdentity || *record.before != beforeIdentity || *record.target != targetIdentity {
		return generationAuthorityError(
			"kernel application generations do not match admitted applying authority",
			nil,
			nil,
		)
	}
	if err := compareKernelObservation(observedBefore, expectedBefore); err != nil {
		return err
	}
	if observedBefore.empty {
		if len(observedBefore.tables) != 0 {
			return invalidKernelObservation("empty pre-application snapshot contains managed tables")
		}
	} else if len(observedBefore.tables) != len(managedTableIdentities) {
		return invalidKernelObservation("pre-application snapshot does not contain both managed tables")
	}
	for index, observed := range observedBefore.tables {
		if index >= len(managedTableIdentities) || observed.family != managedTableIdentities[index].family ||
			observed.name != managedTableIdentities[index].name {
			return invalidKernelObservation("pre-application snapshot contains a foreign managed-table identity")
		}
	}
	return nil
}

func kernelApplicationGenerationIdentity(generation compiledGeneration) generationRecordIdentity {
	return generationRecordIdentity{empty: generation.empty, id: generation.id}
}

func validateKernelApplicationTarget(target compiledGeneration) error {
	if err := validateGenerationID(target.id); err != nil {
		return err
	}
	if err := validateKernelApplicationInventory(target.endpointIDs, "endpoint"); err != nil {
		return err
	}
	if err := validateKernelApplicationInventory(target.networkIDs, "network"); err != nil {
		return err
	}
	seal, err := deriveGenerationApplicationSeal(target)
	if err != nil {
		return err
	}
	if seal != target.applicationSeal {
		return invalidGeneration("kernel application target integrity seal does not match compiler output")
	}
	if target.empty {
		if target.bridge.table != nil || target.inet.table != nil || len(target.bridge.chains) != 0 ||
			len(target.inet.chains) != 0 || len(target.bridge.rules) != 0 || len(target.inet.rules) != 0 ||
			len(target.ingressGuards) != 0 || len(target.endpointIDs) != 0 || len(target.networkIDs) != 0 {
			return invalidGeneration("empty kernel application target contains managed state")
		}
		return nil
	}
	if len(target.networkIDs) == 0 {
		return invalidGeneration("kernel application target has no authoritative managed-network inventory")
	}
	if target.bridge.table == nil || target.inet.table == nil ||
		target.bridge.table.Family != managedTableIdentities[0].family ||
		target.bridge.table.Name != managedTableIdentities[0].name ||
		target.inet.table.Family != managedTableIdentities[1].family ||
		target.inet.table.Name != managedTableIdentities[1].name {
		return invalidGeneration("kernel application target table identities are invalid")
	}
	if target.bridge.table.Flags != 0 || target.bridge.table.Use != 0 ||
		target.inet.table.Flags != 0 || target.inet.table.Use != 0 {
		return invalidGeneration("kernel application target tables contain non-compiler state")
	}
	if err := validateKernelApplicationChains(target); err != nil {
		return err
	}
	rules, err := validateKernelApplicationRules(target)
	if err != nil {
		return err
	}
	return validateKernelApplicationDefaultDeny(target, rules)
}

func validateKernelApplicationInventory(identities []string, resource string) error {
	for index, identity := range identities {
		if !validResourceID(identity) || index > 0 && identities[index-1] >= identity {
			return invalidGeneration("kernel application target " + resource + " inventory is invalid")
		}
	}
	return nil
}

type kernelApplicationChainSpec struct {
	table         *nftables.Table
	name          string
	hook          nftables.ChainHook
	priority      nftables.ChainPriority
	typeName      nftables.ChainType
	policyPresent bool
}

func validateKernelApplicationChains(target compiledGeneration) error {
	specs := [...]kernelApplicationChainSpec{
		{target.bridge.table, managedBridgePreroutingChainName, *nftables.ChainHookPrerouting,
			*nftables.ChainPriorityRaw, nftables.ChainTypeFilter, true},
		{target.bridge.table, managedBridgeForwardChainName, *nftables.ChainHookForward,
			*nftables.ChainPriorityFilter, nftables.ChainTypeFilter, true},
		{target.inet.table, managedINetForwardChainName, *nftables.ChainHookForward,
			*nftables.ChainPriorityFilter, nftables.ChainTypeFilter, true},
		{target.inet.table, managedINetInputChainName, *nftables.ChainHookInput,
			*nftables.ChainPriorityFilter, nftables.ChainTypeFilter, true},
		{target.inet.table, managedINetNATChainName, *nftables.ChainHookPostrouting,
			*nftables.ChainPriorityNATSource, nftables.ChainTypeNAT, false},
	}
	chains := append(slices.Clone(target.bridge.chains), target.inet.chains...)
	if len(chains) != len(specs) {
		return invalidGeneration("kernel application target chain set is invalid")
	}
	for index, spec := range specs {
		chain := chains[index]
		if chain == nil || chain.Table != spec.table || chain.Name != spec.name || chain.Hooknum == nil ||
			*chain.Hooknum != spec.hook || chain.Priority == nil || *chain.Priority != spec.priority ||
			chain.Type != spec.typeName || (chain.Policy != nil) != spec.policyPresent ||
			(chain.Policy != nil && *chain.Policy != nftables.ChainPolicyAccept) || chain.Device != "" {
			return invalidGeneration("kernel application target chain set is invalid")
		}
	}
	return nil
}

func validateKernelApplicationRules(target compiledGeneration) (map[string]compiledRule, error) {
	declaredChains := make(map[*nftables.Chain]struct{}, len(target.bridge.chains)+len(target.inet.chains))
	for _, chain := range target.bridge.chains {
		declaredChains[chain] = struct{}{}
	}
	for _, chain := range target.inet.chains {
		declaredChains[chain] = struct{}{}
	}

	rules := make(map[string]compiledRule, len(target.bridge.rules)+len(target.inet.rules))
	for _, table := range [...]compiledTable{target.bridge, target.inet} {
		for _, compiled := range table.rules {
			if err := validateRuleID(compiled.identity); err != nil {
				return nil, err
			}
			if _, exists := rules[compiled.identity]; exists {
				return nil, invalidGeneration("kernel application target contains duplicate rule identities")
			}
			rule := compiled.rule
			if rule == nil || rule.Table != table.table || rule.Chain == nil || rule.Chain.Table != table.table {
				return nil, invalidGeneration("kernel application target contains an invalid rule parent")
			}
			if _, exists := declaredChains[rule.Chain]; !exists {
				return nil, invalidGeneration("kernel application target rule references an undeclared chain")
			}
			if rule.Handle != 0 || rule.Position != 0 || rule.Flags != 0 || len(rule.Exprs) == 0 {
				return nil, invalidGeneration("kernel application target rule contains non-compiler state")
			}
			identity, err := decodeRuleIdentity(rule.UserData)
			if err != nil || identity != (ruleIdentity{generation: target.id, rule: compiled.identity}) {
				return nil, invalidGeneration("kernel application target rule userdata is inconsistent")
			}
			for _, expression := range rule.Exprs {
				if !kernelExpressionEqual(expression, expression) {
					return nil, invalidGeneration("kernel application target rule contains an unsupported expression")
				}
			}
			rules[compiled.identity] = compiled
		}
	}
	return rules, nil
}

func validateKernelApplicationDefaultDeny(
	target compiledGeneration,
	rules map[string]compiledRule,
) error {
	observedEndpointIDs, err := kernelApplicationResourceIDs(rules, []string{
		"bridge.spoof.mac.",
		"bridge.spoof.ipv4.",
		"bridge.spoof.arp-header.",
		"bridge.spoof.arp-operation.",
		"bridge.spoof.arp-mac.",
		"bridge.spoof.arp-ipv4.",
		"bridge.unsupported-l3.",
		"bridge.arp.",
		"bridge.established.",
		"bridge.default-deny.",
	})
	if err != nil {
		return err
	}
	if !slices.Equal(observedEndpointIDs, target.endpointIDs) || len(target.endpointIDs) != len(target.ingressGuards) {
		return invalidGeneration("kernel application target ingress guard set is incomplete")
	}
	for _, id := range target.endpointIDs {
		guards, exists := target.ingressGuards[id]
		if !exists {
			return invalidGeneration("kernel application target ingress guard set is incomplete")
		}
		guardRules := [...]struct {
			identity string
			rule     *nftables.Rule
		}{
			{"bridge.spoof.mac." + id, guards.mac},
			{"bridge.spoof.ipv4." + id, guards.ipv4},
			{"bridge.spoof.arp-header." + id, guards.arpHeader},
			{"bridge.spoof.arp-operation." + id, guards.arpOperation},
			{"bridge.spoof.arp-mac." + id, guards.arpMAC},
			{"bridge.spoof.arp-ipv4." + id, guards.arpIPv4},
			{"bridge.unsupported-l3." + id, guards.unsupportedL3},
		}
		for _, guard := range guardRules {
			compiled, present := rules[guard.identity]
			if !present || guard.rule == nil || compiled.rule != guard.rule ||
				guard.rule.Chain != target.bridge.chains[0] {
				return invalidGeneration("kernel application target ingress guard set is incomplete")
			}
		}
		if err := requireKernelApplicationRule(rules, "bridge.arp."+id, target.bridge.chains[1], false); err != nil {
			return err
		}
		if err := requireKernelApplicationRule(
			rules,
			"bridge.established."+id,
			target.bridge.chains[1],
			false,
		); err != nil {
			return err
		}
		if err := requireKernelApplicationRule(
			rules,
			"bridge.default-deny."+id,
			target.bridge.chains[1],
			true,
		); err != nil {
			return err
		}
	}
	if !kernelApplicationRuleTailMatches(
		target.bridge.rules,
		target.bridge.chains[1],
		kernelApplicationRuleIdentities(target.endpointIDs, "bridge.default-deny."),
	) {
		return invalidGeneration("kernel application target bridge default-deny rules are not terminal")
	}

	observedNetworkIDs, err := kernelApplicationResourceIDs(rules, []string{
		"inet.forward.established.",
		"inet.forward.egress.",
		"inet.forward.default-deny.",
		"inet.forward.default-deny-inbound.",
		"inet.input.established.",
		"inet.input.default-deny.",
		"inet.nat.masquerade.",
	})
	if err != nil {
		return err
	}
	if !slices.Equal(observedNetworkIDs, target.networkIDs) {
		return invalidGeneration("kernel application target managed-network rule inventory is incomplete")
	}
	for _, id := range target.networkIDs {
		for _, required := range [...]struct {
			identity string
			chain    *nftables.Chain
			drop     bool
		}{
			{"inet.forward.established." + id, target.inet.chains[0], false},
			{"inet.forward.default-deny." + id, target.inet.chains[0], true},
			{"inet.forward.default-deny-inbound." + id, target.inet.chains[0], true},
			{"inet.input.established." + id, target.inet.chains[1], false},
			{"inet.input.default-deny." + id, target.inet.chains[1], true},
		} {
			if err := requireKernelApplicationRule(
				rules,
				required.identity,
				required.chain,
				required.drop,
			); err != nil {
				return err
			}
		}
	}
	forwardDenyIdentities := make([]string, 0, len(target.networkIDs)*2)
	for _, id := range target.networkIDs {
		forwardDenyIdentities = append(
			forwardDenyIdentities,
			"inet.forward.default-deny."+id,
			"inet.forward.default-deny-inbound."+id,
		)
	}
	if !kernelApplicationRuleTailMatches(target.inet.rules, target.inet.chains[0], forwardDenyIdentities) ||
		!kernelApplicationRuleTailMatches(
			target.inet.rules,
			target.inet.chains[1],
			kernelApplicationRuleIdentities(target.networkIDs, "inet.input.default-deny."),
		) {
		return invalidGeneration("kernel application target inet default-deny rules are not terminal")
	}
	return nil
}

func kernelApplicationResourceIDs(rules map[string]compiledRule, prefixes []string) ([]string, error) {
	identities := make(map[string]struct{})
	for identity := range rules {
		for _, prefix := range prefixes {
			if !strings.HasPrefix(identity, prefix) {
				continue
			}
			id := strings.TrimPrefix(identity, prefix)
			if !validResourceID(id) {
				return nil, invalidGeneration("kernel application target contains an invalid resource identity")
			}
			identities[id] = struct{}{}
			break
		}
	}
	result := make([]string, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	slices.Sort(result)
	return result, nil
}

func requireKernelApplicationRule(
	rules map[string]compiledRule,
	identity string,
	chain *nftables.Chain,
	drop bool,
) error {
	compiled, exists := rules[identity]
	if !exists || compiled.rule.Chain != chain || drop && !kernelApplicationRuleEndsInDrop(compiled.rule) {
		return invalidGeneration("kernel application target default-deny invariant is incomplete")
	}
	return nil
}

func kernelApplicationRuleEndsInDrop(rule *nftables.Rule) bool {
	if rule == nil || len(rule.Exprs) == 0 {
		return false
	}
	verdict, ok := rule.Exprs[len(rule.Exprs)-1].(*expr.Verdict)
	return ok && verdict != nil && verdict.Kind == expr.VerdictDrop
}

func kernelApplicationRuleIdentities(resourceIDs []string, prefix string) []string {
	result := make([]string, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		result = append(result, prefix+id)
	}
	return result
}

func kernelApplicationRuleTailMatches(
	rules []compiledRule,
	chain *nftables.Chain,
	want []string,
) bool {
	chainRules := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.rule != nil && rule.rule.Chain == chain {
			chainRules = append(chainRules, rule.identity)
		}
	}
	return len(chainRules) >= len(want) && slices.Equal(chainRules[len(chainRules)-len(want):], want)
}
