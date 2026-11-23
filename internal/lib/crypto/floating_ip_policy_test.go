package crypto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

func TestFloatingIPPolicyDigestsUseIndependentDomainsAndStableResourceIdentity(t *testing.T) {
	vm := model.VMToFloatingIPTrafficAllowIntent{
		SourceVMID: "source-a", DestinationFloatingIPID: "resource-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 443,
	}
	network := model.NetworkToFloatingIPTrafficAllowIntent{
		SourceNetworkID: "source-a", DestinationFloatingIPID: "resource-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 443,
	}
	// CONTRACT: independently hashed length-delimited mvmctl-policy-v1 vectors, with distinct source domains.
	assert.Equal(t, "d9d2b379c5555f8948fa8b71dbbf98162549d5ad680725d8a66258c898855ceb",
		crypto.VMToFloatingIPTrafficPolicyDigest(vm))
	assert.Equal(t, "94ca30920d152ced02cd73e18f3acd817b09dd48e3f40a796dce392aee1ef56f",
		crypto.NetworkToFloatingIPTrafficPolicyDigest(network))
	assert.NotEqual(t, crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
		SourceVMID: "source-a", DestinationVMID: "resource-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 443,
	}),
		crypto.VMToFloatingIPTrafficPolicyDigest(vm))
	for name, mutate := range map[string]func(*model.VMToFloatingIPTrafficAllowIntent){
		"source":     func(v *model.VMToFloatingIPTrafficAllowIntent) { v.SourceVMID = "source-c" },
		"resource":   func(v *model.VMToFloatingIPTrafficAllowIntent) { v.DestinationFloatingIPID = "resource-c" },
		"protocol":   func(v *model.VMToFloatingIPTrafficAllowIntent) { v.Protocol = model.TrafficProtocolUDP },
		"first port": func(v *model.VMToFloatingIPTrafficAllowIntent) { v.DestinationPortStart = 442 },
		"last port":  func(v *model.VMToFloatingIPTrafficAllowIntent) { v.DestinationPortEnd = 444 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := vm
			mutate(&changed)
			assert.NotEqual(
				t,
				crypto.VMToFloatingIPTrafficPolicyDigest(vm),
				crypto.VMToFloatingIPTrafficPolicyDigest(changed),
			)
		})
	}
	changed := vm
	changed.ID = "record-id"
	changed.ClaimID = "claim"
	changed.Incarnation = "incarnation"
	changed.CreatedAt = "created"
	changed.UpdatedAt = "updated"
	assert.Equal(t, crypto.VMToFloatingIPTrafficPolicyDigest(vm), crypto.VMToFloatingIPTrafficPolicyDigest(changed))
	networkChanged := network
	networkChanged.ID = "record-id"
	networkChanged.ClaimID = "claim"
	networkChanged.Incarnation = "incarnation"
	networkChanged.CreatedAt = "created"
	networkChanged.UpdatedAt = "updated"
	assert.Equal(t, crypto.NetworkToFloatingIPTrafficPolicyDigest(network),
		crypto.NetworkToFloatingIPTrafficPolicyDigest(networkChanged))
}
