package firewall

import (
	"testing"

	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

func TestMaterializeLabelTrafficUsesCurrentOwnedActiveEndpoints(t *testing.T) {
	intent, policy := labelTrafficTestIntent(t)
	intent.LabelTrafficPolicies = []LabelTrafficPolicy{policy}
	require.NoError(t, intent.validateDurablePolicies())
	memberships, err := intent.materializeLabelTraffic()
	require.NoError(t, err)
	require.Len(t, memberships, 1)
	assert.Equal(t, []EndpointBinding{intent.Endpoints[0], intent.Endpoints[1]}, memberships[0].sources)
	assert.Equal(t, []EndpointBinding{intent.Endpoints[2]}, memberships[0].destinations)

	changed := cloneIntent(intent)
	changed.Endpoints = changed.Endpoints[1:]
	_, err = changed.VMLabels.replace(1000, changed.Endpoints[0].VMID, 1, model.VMLabels{"role": "controller"})
	require.NoError(t, err)
	require.NoError(t, changed.validateDurablePolicies())
	memberships, err = changed.materializeLabelTraffic()
	require.NoError(t, err)
	assert.Empty(t, memberships, "the only active worker left the set")
	assert.Equal(t, "worker", intent.VMLabels[intent.Endpoints[1].VMID].Labels["role"])
}

func TestLabelTrafficFollowsAdmittedAddressesOfSelectedVMs(t *testing.T) {
	intent, policy := labelTrafficTestIntent(t)
	source, other, destination := intent.Endpoints[0], intent.Endpoints[1], intent.Endpoints[2]
	_, err := intent.VMLabels.replace(1000, other.VMID, 1, model.VMLabels{"role": "other"})
	require.NoError(t, err)
	policy.Destination = VMPolicyEndpoint{VMID: destination.VMID}
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	intent.LabelTrafficPolicies = []LabelTrafficPolicy{policy}
	resources := floatingPolicyIntent().FloatingIPs
	resources[0].OwnerVMID = source.VMID
	resources[1].OwnerVMID = destination.VMID
	intent.FloatingIPs = resources

	for _, moved := range []bool{false, true} {
		name := "associated with selected VMs"
		if moved {
			name = "destination alias moved to an unselected VM"
		}
		t.Run(name, func(t *testing.T) {
			current := cloneIntent(intent)
			if moved {
				current.FloatingIPs[1].OwnerVMID = other.VMID
				current.FloatingIPs[1].Generation++
			}
			compiled, err := current.compile()
			require.NoError(t, err)
			for _, path := range []struct {
				rule     string
				table    compiledTable
				sourceIF string
				targetIF string
			}{
				{"bridge.allow.labels.", compiled.bridge, "mvma0", "mvmc0"},
				{"inet.forward.allow.labels.", compiled.inet, "mvmbr0", "mvmbr0"},
			} {
				wantSource := []labelAddressKey{
					testLabelAddressKey(path.sourceIF, [4]byte{10, 0, 0, 2}),
					testLabelAddressKey(path.sourceIF, [4]byte{10, 0, 0, 100}),
				}
				wantDestination := []labelAddressKey{testLabelAddressKey(path.targetIF, [4]byte{10, 0, 0, 4})}
				if !moved {
					wantDestination = append(
						wantDestination,
						testLabelAddressKey(path.targetIF, [4]byte{10, 0, 0, 101}),
					)
				}
				sets := make(map[string][]labelAddressKey)
				for _, set := range path.table.labelAddressSets {
					sets[set.name] = set.keys
				}
				for _, suffix := range []string{"", ".reply"} {
					rule := requireCompiledRule(t, compiled, path.rule+policy.ID+suffix)
					var keys [][]labelAddressKey
					for _, expression := range rule.Exprs {
						if lookup, ok := expression.(*expr.Lookup); ok {
							keys = append(keys, sets[lookup.SetName])
						}
					}
					assert.Equal(t, [][]labelAddressKey{wantSource, wantDestination}, keys,
						"unexpected set membership for %s%s", path.rule, suffix)
				}
			}
		})
	}
}

func TestValidateLabelTrafficRequiresOwnedExactEndpointAndNonemptySelectors(t *testing.T) {
	for name, change := range map[string]func(*Intent, *LabelTrafficPolicy){
		"foreign endpoint": func(intent *Intent, policy *LabelTrafficPolicy) {
			policy.Destination = VMPolicyEndpoint{VMID: intent.Endpoints[3].VMID}
		},
		"ambiguous endpoint": func(intent *Intent, policy *LabelTrafficPolicy) {
			policy.Source.VMID = intent.Endpoints[0].VMID
		},
		"empty selector": func(_ *Intent, policy *LabelTrafficPolicy) {
			policy.Source.Labels = model.VMLabelSelector{}
		},
		"wildcard selector": func(_ *Intent, policy *LabelTrafficPolicy) {
			policy.Source.Labels = model.VMLabelSelector{"role": "*"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			intent, policy := labelTrafficTestIntent(t)
			change(&intent, &policy)
			var err error
			policy.ID, err = policy.identity()
			require.NoError(t, err)
			intent.LabelTrafficPolicies = []LabelTrafficPolicy{policy}
			require.Error(t, intent.validateDurablePolicies())
		})
	}
}

func labelTrafficTestIntent(t *testing.T) (Intent, LabelTrafficPolicy) {
	t.Helper()
	intent := floatingPolicyIntent()
	intent.FloatingIPs, intent.VMFloatingIPGrants, intent.NetworkFloatingIPGrants = nil, nil, nil
	intent.Endpoints[3].OwnerUID = 1001
	intent.Reservations[3].OwnerUID = 1001
	intent.Networks[1].OwnerUID = 1001
	intent.VMLabels = make(vmLabelRecords)
	for index, endpoint := range intent.Endpoints {
		role := "worker"
		if index == 2 {
			role = "controller"
		}
		_, err := intent.VMLabels.enroll(endpoint.OwnerUID, endpoint.VMID, model.VMLabels{"role": role})
		require.NoError(t, err)
	}
	policy := LabelTrafficPolicy{
		OwnerUID:    1000,
		Source:      VMPolicyEndpoint{Labels: model.VMLabelSelector{"role": "worker"}},
		Destination: VMPolicyEndpoint{Labels: model.VMLabelSelector{"role": "controller"}},
		Protocol:    TCP, Ports: PortInterval{First: 6443, Last: 6443},
		Incarnation: crypto.UUIDV4(),
	}
	var err error
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	return intent, policy
}
