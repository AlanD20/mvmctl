package env

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestFloatingIPWorkflowCleanupRevokesDependentsBeforeReservation(t *testing.T) {
	// Both ephemeral cleanup and explicit removals reconstruct actual steps from saved identities.
	for _, cleanup := range []string{"ephemeral", "removes"} {
		t.Run(cleanup, func(t *testing.T) {
			op := floatingIPGrantTestAPI()
			var mu sync.Mutex
			var reservation *model.FloatingIPItem
			var grant *model.FloatingIPDelegationItem
			policyActive, grantActive, retired := false, false, false
			op.FloatingIPEnsureFunc = func(_ context.Context, input inputs.FloatingIPCreateInput, ownership inputs.EnvironmentFloatingIPOwnershipInput) (*model.FloatingIPItem, error) {
				mu.Lock()
				defer mu.Unlock()
				reservation = &model.FloatingIPItem{
					ID:        ownership.ResourceID,
					NetworkID: input.Network,
					Name:      input.Name,
					IPv4:      "172.27.0.100",
				}
				return reservation, nil
			}
			op.FloatingIPGetFunc = func(_ context.Context, input inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
				mu.Lock()
				defer mu.Unlock()
				require.NotNil(t, reservation)
				assert.Equal(t, reservation.ID, input.Identifier)
				copy := *reservation
				return &copy, nil
			}
			op.FloatingIPDelegationEnsureFunc = func(_ context.Context, input inputs.FloatingIPDelegationEnsureInput) (*model.FloatingIPDelegationItem, error) {
				mu.Lock()
				defer mu.Unlock()
				grant = &model.FloatingIPDelegationItem{
					ID:               input.ID,
					NetworkID:        input.Network,
					ResourceID:       input.Resource,
					VMID:             input.VM,
					Method:           input.Method,
					LaunchGeneration: 1,
					ExecutionUID:     42000,
					State:            "active",
					Revision:         1,
				}
				grantActive = true
				return grant, nil
			}
			op.VMToFloatingIPTrafficPolicyEnsureFunc = func(context.Context, inputs.VMToFloatingIPTrafficPolicyCreateInput,
				inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error) {
				mu.Lock()
				defer mu.Unlock()
				policyActive = true
				return &results.EnvironmentPolicyReceipt{
					PolicyID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Incarnation: "33333333-3333-4333-8333-333333333333",
					WasCreated:  true,
				}, nil
			}
			op.FloatingIPDelegationRemoveOwnedFunc = func(_ context.Context, input inputs.FloatingIPDelegationRemoveOwnedInput) (*model.FloatingIPDelegationItem, error) {
				mu.Lock()
				defer mu.Unlock()
				require.NotNil(t, grant)
				assert.Equal(t, grant.ID, input.Grant.ID)
				grantActive = false
				result := *grant
				result.State, result.Revision = "revoked", 3
				return &result, nil
			}
			op.VMToFloatingIPTrafficPolicyRemoveOwnedFunc = func(context.Context, inputs.VMToFloatingIPTrafficPolicyCreateInput,
				inputs.EnvironmentPolicyOwnershipInput) error {
				mu.Lock()
				defer mu.Unlock()
				policyActive = false
				return nil
			}
			op.FloatingIPRemoveFunc = func(_ context.Context, input inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
				mu.Lock()
				defer mu.Unlock()
				assert.False(t, grantActive, "revoke the guest grant before retiring its reservation")
				assert.False(t, policyActive, "remove destination access before retiring its reservation")
				assert.Equal(t, reservation.ID, input.Identifier)
				retired = true
				result := *reservation
				result.Retired = true
				return &result, nil
			}
			spec := `version: "1"
network:
  cluster:
    existing: cluster
vm:
  controller:
    existing: controller
    depends_on: ["@network:cluster"]
floating_ip:
  api:
    network: "@network:cluster"
    depends_on: ["@network:cluster"]
floating_ip_grant:
  inspect:
    network: "@network:cluster"
    resource: "@floating_ip:api"
    vm: "@vm:controller"
    method: inspect
    depends_on: ["@floating_ip:api", "@vm:controller"]
traffic_policy:
  api:
    source_vm: "@vm:controller"
    destination_floating_ip: "@floating_ip:api"
    destination_network: "@network:cluster"
    protocol: tcp
    port: 6443
    depends_on: ["@vm:controller", "@floating_ip:api"]
`
			if cleanup == "ephemeral" {
				spec += "ephemeral: true\n"
			} else {
				spec += `ready:
  configured:
    target: "@vm:controller"
    cmd: "true"
    depends_on: ["@floating_ip_grant:inspect", "@traffic_policy:api"]
    removes: ["@floating_ip_grant:inspect", "@traffic_policy:api", "@floating_ip:api"]
`
				op.ExecReadyFunc = func(context.Context, inputs.ReadyInput) (*results.ExecResult, error) {
					return &results.ExecResult{}, nil
				}
			}
			path := filepath.Join(t.TempDir(), "env.yaml")
			require.NoError(t, os.WriteFile(path, []byte(spec), 0600))
			require.NoError(t, Apply(t.Context(), op, path, nil, nil))
			mu.Lock()
			assert.True(t, retired)
			assert.False(t, grantActive)
			assert.False(t, policyActive)
			mu.Unlock()
		})
	}
}
