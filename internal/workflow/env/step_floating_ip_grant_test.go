package env

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

func floatingIPGrantTestAPI() *testutil.MockOperation {
	op := floatingIPTestAPI()
	op.VMGetFunc = func(context.Context, inputs.VMInput) (*model.VMItem, error) {
		return &model.VMItem{ID: "22222222222222222222222222222222", NetworkID: floatingIPTestNetworkID}, nil
	}
	op.FloatingIPGetFunc = func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
		return &model.FloatingIPItem{ID: "11111111111111111111111111111111", NetworkID: floatingIPTestNetworkID}, nil
	}
	return op
}

func TestFloatingIPGrantUnknownOutcomeCanBeRevokedAfterVMStops(t *testing.T) {
	// Destruction must recover exact grant authority without a live VM or a new delegation.
	op := floatingIPGrantTestAPI()
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	var grant *model.FloatingIPDelegationItem
	lost := errors.New("grant response lost")
	op.FloatingIPDelegationEnsureFunc = func(_ context.Context, input inputs.FloatingIPDelegationEnsureInput) (*model.FloatingIPDelegationItem, error) {
		require.Equal(t, input.ID, saved.Output.GetString("grant_id"), "grant ID must precede effect")
		grant = &model.FloatingIPDelegationItem{
			ID:               input.ID,
			NetworkID:        input.Network,
			ResourceID:       input.Resource,
			VMID:             input.VM,
			Method:           input.Method,
			LeaseDuration:    10 * time.Second,
			BootID:           "33333333-3333-4333-8333-333333333333",
			LaunchGeneration: 9,
			ExecutionUID:     42000,
			Revision:         1,
			State:            "active",
		}
		return nil, lost
	}
	spec := model.ResourceMap{
		"network":  "cluster",
		"resource": "api",
		"vm":       "controller",
		"method":   "associate",
		"lease":    "10s",
	}
	step, err := newFloatingIPGrantStepFromSpec("floating_ip_grant", "controller-associate", spec, op)
	require.NoError(t, err)
	require.ErrorIs(t, step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, nil), lost)
	require.NotNil(t, grant)
	op.VMGetFunc = func(context.Context, inputs.VMInput) (*model.VMItem, error) {
		t.Fatal("cleanup must not require a live VM")
		return nil, nil
	}
	op.FloatingIPDelegationGetFunc = func(_ context.Context, input inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error) {
		require.Equal(t, grant.ID, input.ID)
		return grant, nil
	}
	revoked := false
	op.FloatingIPDelegationRemoveOwnedFunc = func(_ context.Context, input inputs.FloatingIPDelegationRemoveOwnedInput) (*model.FloatingIPDelegationItem, error) {
		assert.Equal(t, *grant, input.Grant)
		persisted := StateFromMap[FloatingIPGrantState](saved.Output)
		require.NotNil(t, persisted.Receipt, "recovered root receipt must precede cleanup")
		assert.Equal(t, uint64(9), persisted.Receipt.LaunchGeneration)
		result := input.Grant
		result.State, result.Revision = "revoked", 3
		revoked = true
		return &result, nil
	}
	restored, err := newFloatingIPGrantStepFromState("floating_ip_grant", "controller-associate", saved,
		[]string{"vm:controller", "floating_ip:api"}, op)
	require.NoError(t, err)
	require.NoError(t, restored.Destroy(t.Context(), saved, write, nil))
	assert.True(t, revoked)
	assert.ElementsMatch(t, []string{"vm:controller", "floating_ip:api"}, restored.Dependencies())
}

func TestFloatingIPGrantReapplyRefusesChangedIncarnation(t *testing.T) {
	op := floatingIPGrantTestAPI()
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	var originalID string
	generation := uint64(1)
	op.FloatingIPDelegationEnsureFunc = func(_ context.Context, input inputs.FloatingIPDelegationEnsureInput) (*model.FloatingIPDelegationItem, error) {
		if originalID == "" {
			originalID = input.ID
		}
		assert.Equal(t, originalID, input.ID, "reapply must not create another grant")
		return &model.FloatingIPDelegationItem{ID: input.ID, NetworkID: input.Network, ResourceID: input.Resource,
			VMID: input.VM, Method: input.Method, LaunchGeneration: generation, ExecutionUID: 42000,
			BootID: "33333333-3333-4333-8333-333333333333", Revision: 1, State: "active"}, nil
	}
	spec := model.ResourceMap{"network": "cluster", "resource": "api", "vm": "controller", "method": "inspect"}
	step, err := newFloatingIPGrantStepFromSpec("floating_ip_grant", "inspect", spec, op)
	require.NoError(t, err)
	require.NoError(t, step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, nil))
	generation = 2
	retry, err := newFloatingIPGrantStepFromSpec("floating_ip_grant", "inspect", spec, op)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		retry.Apply(t.Context(), workflow.NewSharedState(), saved, write, nil),
		"refusing to adopt a replacement",
	)
	assert.Equal(t, uint64(1), StateFromMap[FloatingIPGrantState](saved.Output).Receipt.LaunchGeneration)
}

func TestFloatingIPGrantDoesNotAdoptDifferentMethodDuringCleanup(t *testing.T) {
	op := floatingIPGrantTestAPI()
	spec := model.ResourceMap{"network": "cluster", "resource": "api", "vm": "controller", "method": "inspect"}
	step, err := newFloatingIPGrantStepFromSpec("floating_ip_grant", "inspect", spec, op)
	require.NoError(t, err)
	record := step.StateData()
	record.Meta.WasCreated = true
	record.Output = StructToMap(
		&FloatingIPGrantState{
			GrantID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			NetworkID:  floatingIPTestNetworkID,
			ResourceID: "11111111111111111111111111111111",
			VMID:       "22222222222222222222222222222222",
			Method:     "inspect",
		},
	)
	op.FloatingIPDelegationGetFunc = func(context.Context, inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error) {
		return &model.FloatingIPDelegationItem{
			ID:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			NetworkID:  floatingIPTestNetworkID,
			ResourceID: "11111111111111111111111111111111",
			VMID:       "22222222222222222222222222222222",
			Method:     "release",
		}, nil
	}
	require.ErrorContains(
		t,
		step.Destroy(t.Context(), record, func(context.Context, model.ResourceState) error { return nil }, nil),
		"refusing to adopt a replacement",
	)
}
