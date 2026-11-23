package network

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestFloatingIPTrafficPolicyRootAdmissionAllowsOwnerlessAndRejectsForeignOrUnfinished(t *testing.T) {
	record := floatingIPContractRecord()
	require.NoError(t, admitFloatingIPPolicyRecord(record, true, record.OwnerUID, record.ID, false))
	for name, change := range map[string]func(*floatingIPRecord){
		"foreign owner":    func(r *floatingIPRecord) { r.OwnerUID++ },
		"resource rebound": func(r *floatingIPRecord) { r.ID = strings.Repeat("e", 32) },
		"retired":          func(r *floatingIPRecord) { r.Retired = true },
		"unfinished":       func(r *floatingIPRecord) { r.Phase = floatingIPFencing },
	} {
		t.Run(name, func(t *testing.T) {
			other := record
			change(&other)
			require.Error(t, admitFloatingIPPolicyRecord(other, true, record.OwnerUID, record.ID, false))
		})
	}
	require.Error(t, admitFloatingIPPolicyRecord(record, false, record.OwnerUID, record.ID, false))
	record.Retired = true
	require.NoError(
		t,
		admitFloatingIPPolicyRecord(record, true, record.OwnerUID, record.ID, true),
		"a completed tombstone permits revocation retry only",
	)
}

func TestFloatingIPTrafficPolicyRequestsRejectMutableDestinationAndInvalidTraffic(t *testing.T) {
	request := VMFloatingIPTrafficPolicyRequest{
		SourceVMID:              strings.Repeat("a", 32),
		DestinationFloatingIPID: strings.Repeat("b", 32),
		Protocol:                model.TrafficProtocolTCP,
		PortStart:               443,
		PortEnd:                 443,
	}
	require.NoError(t, request.Validate())
	request.Incarnation = "11111111-1111-4111-8111-111111111111"
	require.NoError(t, request.Validate(), "operator receipt remains removable without a claim")
	for name, change := range map[string]func(*VMFloatingIPTrafficPolicyRequest){
		"address":       func(r *VMFloatingIPTrafficPolicyRequest) { r.DestinationFloatingIPID = "10.0.0.2" },
		"name":          func(r *VMFloatingIPTrafficPolicyRequest) { r.DestinationFloatingIPID = "service" },
		"source prefix": func(r *VMFloatingIPTrafficPolicyRequest) { r.SourceVMID = "abc" },
		"protocol":      func(r *VMFloatingIPTrafficPolicyRequest) { r.Protocol = "icmp" },
		"port":          func(r *VMFloatingIPTrafficPolicyRequest) { r.PortStart = 0 },
	} {
		t.Run(name, func(t *testing.T) { other := request; change(&other); require.Error(t, other.Validate()) })
	}
	network := NetworkFloatingIPTrafficPolicyRequest{
		SourceNetworkID:         testNetworkID,
		DestinationFloatingIPID: request.DestinationFloatingIPID,
		Protocol:                request.Protocol,
		PortStart:               443,
		PortEnd:                 443,
	}
	require.NoError(t, network.Validate())
	network.SourceNetworkID = "network-name"
	require.Error(t, network.Validate())
}
