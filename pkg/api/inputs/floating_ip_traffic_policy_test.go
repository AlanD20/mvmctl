package inputs_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/pkg/api/inputs"
)

func TestFloatingIPTrafficPolicyInputKeepsReservationSelectorAndCanonicalTraffic(t *testing.T) {
	input := inputs.VMToFloatingIPTrafficPolicyCreateInput{
		SourceVM:              "client",
		DestinationFloatingIP: "service",
		DestinationNetwork:    "blue",
		Protocol:              "tcp",
		DestinationPort:       "4000-4005",
	}
	require.NoError(t, input.Validate())
	_, start, end, err := input.ResolvedTraffic()
	require.NoError(t, err)
	require.Equal(t, 4000, start)
	require.Equal(t, 4005, end)
	for name, change := range map[string]func(*inputs.VMToFloatingIPTrafficPolicyCreateInput){
		"missing source":      func(i *inputs.VMToFloatingIPTrafficPolicyCreateInput) { i.SourceVM = "" },
		"missing reservation": func(i *inputs.VMToFloatingIPTrafficPolicyCreateInput) { i.DestinationFloatingIP = "" },
		"noncanonical selector": func(
			i *inputs.VMToFloatingIPTrafficPolicyCreateInput,
		) {
			i.DestinationFloatingIP = " service"
		},
		"protocol": func(i *inputs.VMToFloatingIPTrafficPolicyCreateInput) { i.Protocol = "TCP" },
		"port":     func(i *inputs.VMToFloatingIPTrafficPolicyCreateInput) { i.DestinationPort = "00443" },
	} {
		t.Run(name, func(t *testing.T) { other := input; change(&other); require.Error(t, other.Validate()) })
	}
}
