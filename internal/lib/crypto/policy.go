package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"mvmctl/internal/lib/model"
)

const typedPolicyCanonicalVersion = "mvmctl-policy-v1"

// NetworkToVMTrafficPolicyDigest returns the deterministic identity of one network-to-VM traffic grant.
func NetworkToVMTrafficPolicyDigest(intent model.NetworkToVMTrafficAllowIntent) string {
	return digestTypedPolicy(
		"traffic-network-to-vm",
		intent.SourceNetworkID,
		intent.DestinationVMID,
		string(intent.Protocol),
		strconv.Itoa(intent.DestinationPortStart),
		strconv.Itoa(intent.DestinationPortEnd),
	)
}

// VMToVMTrafficPolicyDigest returns the deterministic identity of one VM-to-VM traffic grant.
func VMToVMTrafficPolicyDigest(intent model.VMToVMTrafficAllowIntent) string {
	return digestTypedPolicy(
		"traffic-vm-to-vm",
		intent.SourceVMID,
		intent.DestinationVMID,
		string(intent.Protocol),
		strconv.Itoa(intent.DestinationPortStart),
		strconv.Itoa(intent.DestinationPortEnd),
	)
}

// VMToVMExecPolicyDigest returns the deterministic identity of one directional VM-to-VM exec grant.
func VMToVMExecPolicyDigest(intent model.VMToVMExecAllowIntent) string {
	return digestTypedPolicy(
		"exec-vm-to-vm",
		intent.SourceVMID,
		intent.DestinationVMID,
		intent.TargetUser,
	)
}

func digestTypedPolicy(domain string, identityFields ...string) string {
	canonical := make([]byte, 0, 256)
	canonical = appendTypedPolicyField(canonical, typedPolicyCanonicalVersion)
	canonical = appendTypedPolicyField(canonical, domain)
	for _, field := range identityFields {
		canonical = appendTypedPolicyField(canonical, field)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func appendTypedPolicyField(canonical []byte, value string) []byte {
	canonical = strconv.AppendInt(canonical, int64(len(value)), 10)
	canonical = append(canonical, ':')
	canonical = append(canonical, value...)
	return append(canonical, '\n')
}
