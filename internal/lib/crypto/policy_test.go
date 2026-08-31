package crypto_test

import (
	"crypto/sha256"
	"encoding/hex"
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
			want: canonicalPolicyDigestForTest(
				"mvmctl-policy-v1", "traffic-network-to-vm", "net-a", "vm-b", "tcp", "443", "443",
			),
		},
		"VM to VM traffic": {
			got: crypto.VMToVMTrafficPolicyDigest(model.VMToVMTrafficAllowIntent{
				SourceVMID: "vm-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolUDP,
				DestinationPortStart: 5000, DestinationPortEnd: 5005,
			}),
			want: canonicalPolicyDigestForTest(
				"mvmctl-policy-v1", "traffic-vm-to-vm", "vm-a", "vm-b", "udp", "5000", "5005",
			),
		},
		"VM to VM exec": {
			got: crypto.VMToVMExecPolicyDigest(model.VMToVMExecAllowIntent{
				SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
			}),
			want: canonicalPolicyDigestForTest(
				"mvmctl-policy-v1", "exec-vm-to-vm", "vm-a", "vm-b", "app",
			),
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

func canonicalPolicyDigestForTest(fields ...string) string {
	canonical := make([]byte, 0)
	for _, field := range fields {
		canonical = appendCanonicalPolicyFieldForTest(canonical, field)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func appendCanonicalPolicyFieldForTest(canonical []byte, field string) []byte {
	// CONTRACT: canonical policy fields are <base-10 byte length>:<bytes>\n.
	digits := []byte{}
	length := len(field)
	if length == 0 {
		digits = append(digits, '0')
	} else {
		for length > 0 {
			digits = append([]byte{byte('0' + length%10)}, digits...)
			length /= 10
		}
	}
	canonical = append(canonical, digits...)
	canonical = append(canonical, ':')
	canonical = append(canonical, field...)
	return append(canonical, '\n')
}
