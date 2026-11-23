package crypto_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

func TestTypedPolicyDigestsUseClosedCanonicalDomains(t *testing.T) {
	tests := map[string]struct {
		got  string
		want string
	}{
		"network to VM traffic": {
			got: crypto.NetworkToVMTrafficPolicyDigest(model.NetworkToVMTrafficAllowIntent{
				SourceNetworkID: "net-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolTCP,
				DestinationPortStart: 443, DestinationPortEnd: 443,
			}),
			// CONTRACT: sha256sum of the documented length-delimited canonical byte vector.
			want: "37af4f663c66ab8fe0ad39485af9756c5c03f9ab129bdf39ffd3540d2aee8f4b",
		},
		"VM to VM traffic": {
			got: crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
				SourceVMID: "vm-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolUDP,
				DestinationPortStart: 5000, DestinationPortEnd: 5005,
			}),
			// CONTRACT: sha256sum of the documented length-delimited canonical byte vector.
			want: "ac5100b8ea39d46e493e48dbfd83b0d5831721326a4854e49e9648e2f2a059c0",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.got); diff != "" {
				t.Errorf("policy digest mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTypedPolicyDigestsIncludeEveryIdentityField(t *testing.T) {
	networkBase := model.NetworkToVMTrafficAllowIntent{
		SourceNetworkID: "net-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 443,
	}
	vmBase := model.VMToVMTrafficAllowIntent{
		SourceVMID: "vm-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolUDP,
		DestinationPortStart: 5000, DestinationPortEnd: 5005,
	}
	tests := map[string]struct {
		base    string
		changed string
	}{
		"network source": {base: crypto.NetworkToVMTrafficPolicyDigest(networkBase), changed: func() string {
			intent := networkBase
			intent.SourceNetworkID = "net-c"
			return crypto.NetworkToVMTrafficPolicyDigest(intent)
		}()},
		"network destination": {base: crypto.NetworkToVMTrafficPolicyDigest(networkBase), changed: func() string {
			intent := networkBase
			intent.DestinationVMID = "vm-c"
			return crypto.NetworkToVMTrafficPolicyDigest(intent)
		}()},
		"network protocol": {base: crypto.NetworkToVMTrafficPolicyDigest(networkBase), changed: func() string {
			intent := networkBase
			intent.Protocol = model.TrafficProtocolUDP
			return crypto.NetworkToVMTrafficPolicyDigest(intent)
		}()},
		"network port start": {base: crypto.NetworkToVMTrafficPolicyDigest(networkBase), changed: func() string {
			intent := networkBase
			intent.DestinationPortStart = 442
			return crypto.NetworkToVMTrafficPolicyDigest(intent)
		}()},
		"network port end": {base: crypto.NetworkToVMTrafficPolicyDigest(networkBase), changed: func() string {
			intent := networkBase
			intent.DestinationPortEnd = 444
			return crypto.NetworkToVMTrafficPolicyDigest(intent)
		}()},
		"VM source": {base: crypto.VMToVMTrafficPolicyDigest(vmBase), changed: func() string {
			intent := vmBase
			intent.SourceVMID = "vm-c"
			return crypto.VMToVMTrafficPolicyDigest(intent)
		}()},
		"VM destination": {base: crypto.VMToVMTrafficPolicyDigest(vmBase), changed: func() string {
			intent := vmBase
			intent.DestinationVMID = "vm-c"
			return crypto.VMToVMTrafficPolicyDigest(intent)
		}()},
		"VM protocol": {base: crypto.VMToVMTrafficPolicyDigest(vmBase), changed: func() string {
			intent := vmBase
			intent.Protocol = model.TrafficProtocolTCP
			return crypto.VMToVMTrafficPolicyDigest(intent)
		}()},
		"VM port start": {base: crypto.VMToVMTrafficPolicyDigest(vmBase), changed: func() string {
			intent := vmBase
			intent.DestinationPortStart = 4999
			return crypto.VMToVMTrafficPolicyDigest(intent)
		}()},
		"VM port end": {base: crypto.VMToVMTrafficPolicyDigest(vmBase), changed: func() string {
			intent := vmBase
			intent.DestinationPortEnd = 5006
			return crypto.VMToVMTrafficPolicyDigest(intent)
		}()},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.base == tc.changed {
				t.Fatal("changing one identity field must change the policy digest")
			}
		})
	}
}

func TestTypedPolicyDigestsSeparatePlanesAndFieldBoundaries(t *testing.T) {
	networkDigest := crypto.NetworkToVMTrafficPolicyDigest(model.NetworkToVMTrafficAllowIntent{
		SourceNetworkID: "vm-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 443,
	})
	vmDigest := crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
		SourceVMID: "vm-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 443,
	})
	if networkDigest == vmDigest {
		t.Fatal("network-source and VM-source traffic policies must have distinct identities")
	}

	first := crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
		SourceVMID: "a", DestinationVMID: "bc", Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
	})
	second := crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
		SourceVMID: "ab", DestinationVMID: "c", Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
	})
	if first == second {
		t.Fatal("length-delimited canonical fields must prevent concatenation collisions")
	}
}
