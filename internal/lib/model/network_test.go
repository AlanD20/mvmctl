package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
)

func TestFirewallRuleKeyIncludesEveryPolicyIdentityField(t *testing.T) {
	t.Parallel()
	policy := "policy-a"
	base := model.FirewallRule{
		TableName: model.FirewallTableFilter, ChainName: model.FirewallChainMVMRoutedPolicy,
		RuleType: model.FirewallRuleTypePolicyAllow, Protocol: model.FirewallProtocolTCP,
		Source: "0.0.0.0/0", Destination: "10.0.0.2", InInterface: "mvm-a", OutInterface: "mvm-b",
		Target: model.FirewallTargetAccept, SPort: 0, DPort: 443, DPortEnd: 443,
		NetworkID: "net-a", PolicyID: &policy,
	}
	mutations := map[string]func(*model.FirewallRule){
		"table":            func(r *model.FirewallRule) { r.TableName = model.FirewallTableNat },
		"chain":            func(r *model.FirewallRule) { r.ChainName = model.FirewallChainMVMForward },
		"type":             func(r *model.FirewallRule) { r.RuleType = model.FirewallRuleTypeRoutedDrop },
		"protocol":         func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolUDP },
		"source":           func(r *model.FirewallRule) { r.Source = "10.1.0.0/24" },
		"destination":      func(r *model.FirewallRule) { r.Destination = "10.0.0.3" },
		"input":            func(r *model.FirewallRule) { r.InInterface = "mvm-c" },
		"output":           func(r *model.FirewallRule) { r.OutInterface = "mvm-d" },
		"target":           func(r *model.FirewallRule) { r.Target = model.FirewallTargetDrop },
		"source port":      func(r *model.FirewallRule) { r.SPort = 1 },
		"destination port": func(r *model.FirewallRule) { r.DPort = 444 },
		"range end":        func(r *model.FirewallRule) { r.DPortEnd = 450 },
		"network":          func(r *model.FirewallRule) { r.NetworkID = "net-b" },
		"policy":           func(r *model.FirewallRule) { other := "policy-b"; r.PolicyID = &other },
	}
	seen := map[string]string{base.Key(): "base"}
	for name, mutate := range mutations {
		rule := base
		mutate(&rule)
		if prior, exists := seen[rule.Key()]; exists {
			t.Errorf("%s key collided with %s", name, prior)
		}
		seen[rule.Key()] = name
	}
	assert.Len(t, seen, len(mutations)+1)
}
