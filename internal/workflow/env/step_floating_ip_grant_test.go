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
	"mvmctl/pkg/errs"
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
		require.Equal(
			t,
			input.ID,
			StateFromMap[FloatingIPGrantState](saved.Output).Grants[0].GrantID,
			"grant ID must precede effect",
		)
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
		"vms":      []string{"controller"},
		"methods":  []map[string]string{{"method": "associate", "lease": "10s"}},
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
		require.NotNil(t, persisted.Grants[0].Receipt, "recovered root receipt must precede cleanup")
		assert.Equal(t, uint64(9), persisted.Grants[0].Receipt.LaunchGeneration)
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
	spec := model.ResourceMap{
		"network":  "cluster",
		"resource": "api",
		"vms":      []string{"controller"},
		"methods":  []map[string]string{{"method": "inspect"}},
	}
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
	assert.Equal(t, uint64(1), StateFromMap[FloatingIPGrantState](saved.Output).Grants[0].Receipt.LaunchGeneration)
}

func TestFloatingIPGrantDoesNotAdoptDifferentMethodDuringCleanup(t *testing.T) {
	op := floatingIPGrantTestAPI()
	spec := model.ResourceMap{
		"network":  "cluster",
		"resource": "api",
		"vms":      []string{"controller"},
		"methods":  []map[string]string{{"method": "inspect"}},
	}
	step, err := newFloatingIPGrantStepFromSpec("floating_ip_grant", "inspect", spec, op)
	require.NoError(t, err)
	record := step.StateData()
	record.Meta.WasCreated = true
	record.Output = StructToMap(
		&FloatingIPGrantState{Grants: []floatingIPGrantState{{
			GrantID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			NetworkID:  floatingIPTestNetworkID,
			ResourceID: "11111111111111111111111111111111",
			VMID:       "22222222222222222222222222222222",
			Method:     "inspect",
		}}},
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

func TestFloatingIPGrantBatchPinsEachLaunchAndCleansPartialApply(t *testing.T) {
	op := floatingIPGrantTestAPI()
	const (
		firstVM  = "22222222222222222222222222222222"
		secondVM = "44444444444444444444444444444444"
		bootID   = "33333333-3333-4333-8333-333333333333"
	)
	op.VMGetFunc = func(_ context.Context, input inputs.VMInput) (*model.VMItem, error) {
		id := firstVM
		if input.Identifiers[0] == "controller-2" {
			id = secondVM
		}
		return &model.VMItem{ID: id, NetworkID: floatingIPTestNetworkID}, nil
	}
	spec := model.ResourceMap{
		"network": "cluster", "resource": "api",
		"vms":     []string{"controller-1", "controller-2"},
		"methods": []map[string]string{{"method": "inspect"}, {"method": "release"}},
	}
	ctx := t.Context()
	step, err := newFloatingIPGrantStepFromSpec("floating_ip_grant", "controllers", spec, op)
	require.NoError(t, err)
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	var committed model.FloatingIPDelegationItem
	launchChanged := errors.New("VM restarted")
	calls := 0
	op.FloatingIPDelegationEnsureFunc = func(
		_ context.Context, request inputs.FloatingIPDelegationEnsureInput,
	) (*model.FloatingIPDelegationItem, error) {
		batch := StateFromMap[FloatingIPGrantState](saved.Output)
		require.Len(t, batch.Grants, 4, "every pair must be persisted before any root effect")
		assert.Equal(t, request.ID, batch.Grants[calls].GrantID)
		assert.Equal(t, firstVM, request.VM)
		calls++
		if request.Method == "release" {
			require.NotNil(t, request.Expected)
			assert.True(t, request.Expected.Matches(bootID, 7, 42000))
			return nil, launchChanged
		}
		require.Nil(t, request.Expected)
		committed = model.FloatingIPDelegationItem{
			ID: request.ID, NetworkID: request.Network, ResourceID: request.Resource, VMID: request.VM,
			Method: request.Method, BootID: bootID, LaunchGeneration: 7, ExecutionUID: 42000,
			Revision: 1, State: "active",
		}
		return &committed, nil
	}
	err = step.Apply(ctx, workflow.NewSharedState(), model.ResourceState{}, write, nil)
	require.ErrorIs(t, err, launchChanged)
	assert.Equal(t, 2, calls)
	require.NotNil(t, StateFromMap[FloatingIPGrantState](saved.Output).Grants[0].Receipt)

	restored, err := newFloatingIPGrantStepFromState("floating_ip_grant", "controllers", saved, nil, op)
	require.NoError(t, err)
	op.VMGetFunc = func(context.Context, inputs.VMInput) (*model.VMItem, error) {
		t.Fatal("partial cleanup must not resolve live VMs")
		return nil, nil
	}
	op.FloatingIPDelegationGetFunc = func(
		context.Context, inputs.FloatingIPDelegationInput,
	) (*model.FloatingIPDelegationItem, error) {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "grant absent")
	}
	var removed []string
	op.FloatingIPDelegationRemoveOwnedFunc = func(
		_ context.Context, input inputs.FloatingIPDelegationRemoveOwnedInput,
	) (*model.FloatingIPDelegationItem, error) {
		assert.Equal(t, committed, input.Grant)
		removed = append(removed, input.Grant.ID)
		receipt := input.Grant
		receipt.State = "revoked"
		return &receipt, nil
	}
	require.NoError(t, restored.Destroy(ctx, saved, write, nil))
	assert.Equal(t, []string{committed.ID}, removed)
}
