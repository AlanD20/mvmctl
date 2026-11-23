package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
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

func TestFloatingIPGrantDestroyRetainsBatchAfterRecoveredGrantRevokeFails(t *testing.T) {
	op := floatingIPGrantTestAPI()
	const spec = `version: "1"
floating_ip_grant:
  controller:
    network: cluster
    resource: api
    vms: [controller]
    methods:
      - method: inspect
      - method: release
`
	path := filepath.Join(t.TempDir(), "env.yaml")
	require.NoError(t, os.WriteFile(path, []byte(spec), 0600))
	ctx := t.Context()
	grants := make(map[string]model.FloatingIPDelegationItem)
	lost := errors.New("grant response lost")
	op.FloatingIPDelegationEnsureFunc = func(
		_ context.Context, input inputs.FloatingIPDelegationEnsureInput,
	) (*model.FloatingIPDelegationItem, error) {
		grant := model.FloatingIPDelegationItem{
			ID:               input.ID,
			NetworkID:        input.Network,
			ResourceID:       input.Resource,
			VMID:             input.VM,
			Method:           input.Method,
			BootID:           "33333333-3333-4333-8333-333333333333",
			LaunchGeneration: 9,
			ExecutionUID:     42000,
			Revision:         1,
			State:            "active",
		}
		grants[grant.ID] = grant
		if grant.Method == "release" {
			return nil, lost
		}
		return &grant, nil
	}
	require.ErrorIs(t, Apply(ctx, op, path, nil, nil), lost)
	require.Len(t, grants, 2)
	stateDir := infra.GetWorkflowsStateDirByID(ResolveWorkflowID(path))
	before, err := workflow.ReadWorkflowState(stateDir)
	require.NoError(t, err)
	require.Len(t, before.Resources, 1)
	batch := StateFromMap[FloatingIPGrantState](before.Resources[0].State.Output)
	require.NotNil(t, batch)
	require.Len(t, batch.Grants, 2)
	assert.Nil(t, batch.Grants[1].Receipt, "release committed without returning its receipt")

	op.VMGetFunc = func(context.Context, inputs.VMInput) (*model.VMItem, error) {
		return nil, errors.New("VM stopped")
	}
	op.FloatingIPDelegationGetFunc = func(
		_ context.Context, input inputs.FloatingIPDelegationInput,
	) (*model.FloatingIPDelegationItem, error) {
		grant, ok := grants[input.ID]
		if !ok || grant.NetworkID != input.Network {
			return nil, errs.NotFound(errs.CodeNetworkNotFound, "grant absent")
		}
		return &grant, nil
	}
	revokeErr := errors.New("grant revocation interrupted")
	op.FloatingIPDelegationRemoveOwnedFunc = func(
		_ context.Context, input inputs.FloatingIPDelegationRemoveOwnedInput,
	) (*model.FloatingIPDelegationItem, error) {
		assert.Equal(t, grants[input.Grant.ID], input.Grant)
		assert.Equal(t, "release", input.Grant.Method, "cleanup encounters the recovered grant first")
		return nil, revokeErr
	}
	require.ErrorIs(t, Destroy(ctx, op, path, nil), revokeErr)
	after, err := workflow.ReadWorkflowState(stateDir)
	require.NoError(t, err)
	testutil.AssertDiff(t, before.Resources, after.Resources)

	op.FloatingIPDelegationRemoveOwnedFunc = func(
		_ context.Context, input inputs.FloatingIPDelegationRemoveOwnedInput,
	) (*model.FloatingIPDelegationItem, error) {
		grant := grants[input.Grant.ID]
		assert.Equal(t, grant, input.Grant)
		grant.State, grant.Revision = "revoked", 3
		grants[grant.ID] = grant
		return &grant, nil
	}
	require.NoError(t, Destroy(ctx, op, path, nil))
	for id, grant := range grants {
		assert.Equal(t, "revoked", grant.State, "grant %s", id)
	}
	assert.NoDirExists(t, stateDir)
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
