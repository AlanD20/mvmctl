package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestFloatingIPTrafficPolicyCLISelectsReservationFamily(t *testing.T) {
	var created inputs.VMToFloatingIPTrafficPolicyCreateInput
	var inspected, removed []string
	mock := &testutil.MockPolicyAPI{
		VMToFloatingIPTrafficPolicyCreateFunc: func(
			_ context.Context,
			input inputs.VMToFloatingIPTrafficPolicyCreateInput,
		) (*results.VMToFloatingIPTrafficPolicy, error) {
			created = input
			return &results.VMToFloatingIPTrafficPolicy{}, nil
		},
		NetworkToFloatingIPTrafficPolicyInspectFunc: func(
			_ context.Context,
			input inputs.NetworkToFloatingIPTrafficPolicyInput,
		) (*results.NetworkToFloatingIPTrafficPolicy, error) {
			inspected = input.Identifiers
			return &results.NetworkToFloatingIPTrafficPolicy{}, nil
		},
		VMToFloatingIPTrafficPolicyRemoveFunc: func(_ context.Context, input inputs.VMToFloatingIPTrafficPolicyInput) error {
			removed = input.Identifiers
			return nil
		},
	}
	cmd := cli.NewPolicyCmd(mock)
	create, _, err := cmd.Find([]string{"traffic", "create"})
	require.NoError(t, err)
	for name, value := range map[string]string{
		"from-vm":        "client",
		"to-floating-ip": "service",
		"to-network":     "blue",
		"protocol":       "tcp",
		"port":           "443",
	} {
		require.NoError(t, create.Flags().Set(name, value))
	}
	require.NoError(t, create.RunE(create, nil))
	require.Equal(t, inputs.VMToFloatingIPTrafficPolicyCreateInput{
		SourceVM:              "client",
		DestinationFloatingIP: "service",
		DestinationNetwork:    "blue",
		Protocol:              "tcp",
		DestinationPort:       "443",
	}, created)
	inspect, _, err := cmd.Find([]string{"traffic", "inspect"})
	require.NoError(t, err)
	require.NoError(t, inspect.Flags().Set("from-network", "true"))
	require.NoError(t, inspect.Flags().Set("to-floating-ip", "true"))
	require.NoError(t, inspect.RunE(inspect, []string{"abc"}))
	require.Equal(t, []string{"abc"}, inspected)
	remove, _, err := cmd.Find([]string{"traffic", "rm"})
	require.NoError(t, err)
	for _, flag := range []string{"from-vm", "to-floating-ip", "force"} {
		require.NoError(t, remove.Flags().Set(flag, "true"))
	}
	require.NoError(t, remove.RunE(remove, []string{"def"}))
	require.Equal(t, []string{"def"}, removed)
}

func TestFloatingIPTrafficPolicyCLIRejectsConflictingDestinationSelectors(t *testing.T) {
	for name, flags := range map[string]map[string]string{
		"two destinations":            {"from-vm": "vm", "to-vm": "dest", "to-floating-ip": "resource"},
		"host and reservation":        {"from-vm": "vm", "to-host": "true", "to-floating-ip": "resource"},
		"network without reservation": {"from-vm": "vm", "to-vm": "dest", "to-network": "blue"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _, err := cli.NewPolicyCmd(&testutil.MockPolicyAPI{}).Find([]string{"traffic", "create"})
			require.NoError(t, err)
			for flag, value := range flags {
				require.NoError(t, cmd.Flags().Set(flag, value))
			}
			require.Error(t, cmd.RunE(cmd, nil))
		})
	}
}
