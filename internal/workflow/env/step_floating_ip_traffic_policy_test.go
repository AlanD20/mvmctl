package env

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestFloatingIPTrafficPolicyRetainsExactDestinationAndOwnedReceipt(t *testing.T) {
	for _, source := range []string{"vm", "network"} {
		t.Run(source, func(t *testing.T) {
			op := floatingIPGrantTestAPI()
			spec := model.ResourceMap{
				"destination_floating_ip": "@floating_ip:api",
				"destination_network":     "@network:cluster",
				"protocol":                "tcp",
				"port":                    "6443",
			}
			if source == "vm" {
				spec["source_vm"] = "controller"
			} else {
				spec["source_network"] = "cluster"
			}
			shared := workflow.NewSharedState()
			shared.Set("floating_ip:api", &FloatingIPState{ResourceID: "11111111111111111111111111111111"})
			shared.Set("network:cluster", &NetworkState{NetworkID: floatingIPTestNetworkID})
			var saved model.ResourceState
			write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
			projectionErr := errors.New("root policy committed but projection failed")
			receipt := &results.EnvironmentPolicyReceipt{
				PolicyID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Incarnation: "33333333-3333-4333-8333-333333333333",
				WasCreated:  true,
			}
			ensure := func(ownership inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error) {
				assert.Equal(t, ownership.ClaimID, saved.Output.GetString("claim_id"))
				assert.Equal(
					t,
					"11111111111111111111111111111111",
					saved.Output.GetString("destination_floating_ip_id"),
				)
				assert.Equal(t, floatingIPTestNetworkID, saved.Output.GetString("destination_network_id"))
				return receipt, projectionErr
			}
			op.VMToFloatingIPTrafficPolicyEnsureFunc = func(_ context.Context, input inputs.VMToFloatingIPTrafficPolicyCreateInput,
				ownership inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error) {
				assert.Equal(t, "11111111111111111111111111111111", input.DestinationFloatingIP)
				assert.Equal(t, floatingIPTestNetworkID, input.DestinationNetwork)
				return ensure(ownership)
			}
			op.NetworkToFloatingIPTrafficPolicyEnsureFunc = func(_ context.Context, input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
				ownership inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error) {
				assert.Equal(t, "11111111111111111111111111111111", input.DestinationFloatingIP)
				assert.Equal(t, floatingIPTestNetworkID, input.DestinationNetwork)
				return ensure(ownership)
			}
			step, err := newTrafficPolicyStepFromSpec("traffic_policy", "api", spec, op)
			require.NoError(t, err)
			require.ErrorIs(t, step.Apply(t.Context(), shared, model.ResourceState{}, write, nil), projectionErr)
			assert.Equal(t, receipt.Incarnation, saved.Output.GetString("incarnation"))
			assert.True(t, saved.Meta.WasCreated)
			removed := false
			remove := func(ownership inputs.EnvironmentPolicyOwnershipInput) error {
				assert.Equal(t, receipt.Incarnation, ownership.Incarnation)
				assert.Equal(t, saved.Output.GetString("claim_id"), ownership.ClaimID)
				removed = true
				return nil
			}
			op.VMToFloatingIPTrafficPolicyRemoveOwnedFunc = func(_ context.Context, input inputs.VMToFloatingIPTrafficPolicyCreateInput,
				ownership inputs.EnvironmentPolicyOwnershipInput) error {
				assert.Equal(t, "11111111111111111111111111111111", input.DestinationFloatingIP)
				return remove(ownership)
			}
			op.NetworkToFloatingIPTrafficPolicyRemoveOwnedFunc = func(_ context.Context, input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
				ownership inputs.EnvironmentPolicyOwnershipInput) error {
				assert.Equal(t, "11111111111111111111111111111111", input.DestinationFloatingIP)
				return remove(ownership)
			}
			restored, err := newTrafficPolicyStepFromState("traffic_policy", "api", saved,
				[]string{"floating_ip:api", "network:cluster"}, op)
			require.NoError(t, err)
			require.NoError(t, restored.Destroy(t.Context(), saved, write, nil))
			assert.True(t, removed)
		})
	}
}
