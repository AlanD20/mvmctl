package network

import (
	"fmt"
	"slices"

	"mvmctl/internal/lib/model"
)

// CompileServiceAccessPolicyRules compiles current resource identities into ordered backend-neutral rules.
func CompileServiceAccessPolicyRules(
	policies []*model.ResolvedServiceAccessPolicy,
	networks []*model.NetworkItem,
) []model.FirewallRule {
	sortedPolicies := append([]*model.ResolvedServiceAccessPolicy(nil), policies...)
	sortedNetworks := append([]*model.NetworkItem(nil), networks...)
	slices.SortFunc(sortedPolicies, func(a, b *model.ResolvedServiceAccessPolicy) int {
		return comparePolicy(a.ServiceAccessPolicy, b.ServiceAccessPolicy)
	})
	slices.SortFunc(sortedNetworks, func(a, b *model.NetworkItem) int {
		if a.Bridge < b.Bridge {
			return -1
		}
		if a.Bridge > b.Bridge {
			return 1
		}
		return 0
	})

	rules := make([]model.FirewallRule, 0, len(sortedPolicies)+2*len(sortedNetworks))
	for _, policy := range sortedPolicies {
		policyID := policy.ID
		comment := fmt.Sprintf("mvm:policy:%s", policy.ID)
		rules = append(rules, model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    model.FirewallChainMVMRoutedPolicy,
			RuleType:     model.FirewallRuleTypePolicyAllow,
			Protocol:     model.FirewallProtocol(policy.Protocol),
			Source:       string(model.FirewallWildcardAnyCIDR),
			Destination:  policy.DestinationIPv4,
			InInterface:  policy.SourceNetworkBridge,
			OutInterface: policy.DestinationNetworkBridge,
			Target:       model.FirewallTargetAccept,
			SPort:        model.FirewallPortAny,
			DPort:        policy.DestinationPortStart,
			DPortEnd:     policy.DestinationPortEnd,
			NetworkID:    policy.SourceNetworkID,
			IsActive:     true,
			PolicyID:     &policyID,
			CommentTag:   &comment,
		})
	}

	for _, source := range sortedNetworks {
		comment := fmt.Sprintf("mvm:routed-deny:%s", source.ID)
		rules = append(rules, model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    model.FirewallChainMVMRoutedPolicy,
			RuleType:     model.FirewallRuleTypeRoutedDrop,
			Protocol:     model.FirewallProtocolAll,
			Source:       string(model.FirewallWildcardAnyCIDR),
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  source.Bridge,
			OutInterface: string(model.FirewallWildcardManagedInterface),
			Target:       model.FirewallTargetDrop,
			SPort:        model.FirewallPortAny,
			DPort:        model.FirewallPortAny,
			DPortEnd:     model.FirewallPortAny,
			NetworkID:    source.ID,
			IsActive:     true,
			CommentTag:   &comment,
		})
	}

	for _, source := range sortedNetworks {
		comment := fmt.Sprintf("mvm:host-deny:%s", source.ID)
		rules = append(rules, model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    model.FirewallChainMVMHostInput,
			RuleType:     model.FirewallRuleTypeHostInputDrop,
			Protocol:     model.FirewallProtocolAll,
			Source:       string(model.FirewallWildcardAnyCIDR),
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  source.Bridge,
			OutInterface: string(model.FirewallWildcardAnyInterface),
			Target:       model.FirewallTargetDrop,
			SPort:        model.FirewallPortAny,
			DPort:        model.FirewallPortAny,
			DPortEnd:     model.FirewallPortAny,
			NetworkID:    source.ID,
			IsActive:     true,
			CommentTag:   &comment,
		})
	}
	return rules
}

func comparePolicy(a, b model.ServiceAccessPolicy) int {
	left := fmt.Sprintf("%s:%s:%s:%05d:%05d:%s", a.SourceNetworkID, a.DestinationVMID,
		a.Protocol, a.DestinationPortStart, a.DestinationPortEnd, a.ID)
	right := fmt.Sprintf("%s:%s:%s:%05d:%05d:%s", b.SourceNetworkID, b.DestinationVMID,
		b.Protocol, b.DestinationPortStart, b.DestinationPortEnd, b.ID)
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
