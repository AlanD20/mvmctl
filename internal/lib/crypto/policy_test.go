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
		"VM to VM exec": {
			got: crypto.VMToVMExecPolicyDigest(model.VMToVMExecAllowIntent{
				SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
			}),
			// CONTRACT: sha256sum of the documented length-delimited canonical byte vector.
			want: "961d1718c9901e2cac702f3ebc53e007aeedf1b8fbd4918833d79405515f186e",
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
	execBase := model.VMToVMExecAllowIntent{SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app"}
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
		"exec source": {base: crypto.VMToVMExecPolicyDigest(execBase), changed: func() string {
			intent := execBase
			intent.SourceVMID = "vm-c"
			return crypto.VMToVMExecPolicyDigest(intent)
		}()},
		"exec destination": {base: crypto.VMToVMExecPolicyDigest(execBase), changed: func() string {
			intent := execBase
			intent.DestinationVMID = "vm-c"
			return crypto.VMToVMExecPolicyDigest(intent)
		}()},
		"exec target user": {base: crypto.VMToVMExecPolicyDigest(execBase), changed: func() string {
			intent := execBase
			intent.TargetUser = "worker"
			return crypto.VMToVMExecPolicyDigest(intent)
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

func TestTypedPolicyDigestsExcludeRecordMetadata(t *testing.T) {
	first := model.VMToVMExecAllowIntent{
		ID: "caller-supplied", SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	second := first
	second.ID = "different"
	second.CreatedAt = "2026-02-01T00:00:00Z"
	second.UpdatedAt = "2026-02-01T00:00:00Z"

	if diff := cmp.Diff(crypto.VMToVMExecPolicyDigest(first), crypto.VMToVMExecPolicyDigest(second)); diff != "" {
		t.Errorf("record metadata changed policy identity (-first +second):\n%s", diff)
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

	first := crypto.VMToVMExecPolicyDigest(model.VMToVMExecAllowIntent{
		SourceVMID: "a", DestinationVMID: "bc", TargetUser: "app",
	})
	second := crypto.VMToVMExecPolicyDigest(model.VMToVMExecAllowIntent{
		SourceVMID: "ab", DestinationVMID: "c", TargetUser: "app",
	})
	if first == second {
		t.Fatal("length-delimited canonical fields must prevent concatenation collisions")
	}
}
