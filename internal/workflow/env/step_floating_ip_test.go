package env

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

const floatingIPTestNetworkID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func floatingIPTestAPI() *testutil.MockOperation {
	op := &testutil.MockOperation{}
	op.NetworkGetFunc = func(context.Context, inputs.NetworkInput) (*model.NetworkItem, error) {
		return &model.NetworkItem{ID: floatingIPTestNetworkID, Name: "cluster"}, nil
	}
	return op
}

func TestFloatingIPCreationRetainsIdentityAcrossUnknownOutcome(t *testing.T) {
	// A lost root response must not orphan the address or allocate a second reservation on retry.
	op := floatingIPTestAPI()
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	var rootItem *model.FloatingIPItem
	lost := errors.New("response lost after root commit")
	op.FloatingIPEnsureFunc = func(_ context.Context, input inputs.FloatingIPCreateInput, ownership inputs.EnvironmentFloatingIPOwnershipInput) (*model.FloatingIPItem, error) {
		require.Equal(
			t,
			ownership.ResourceID,
			saved.Output.GetString("resource_id"),
			"identity must be durable before reservation",
		)
		require.True(t, saved.Meta.WasCreated)
		if rootItem == nil {
			rootItem = &model.FloatingIPItem{
				ID:         ownership.ResourceID,
				NetworkID:  input.Network,
				Name:       input.Name,
				IPv4:       "172.27.0.100",
				Generation: 1,
			}
			return nil, lost
		}
		require.Equal(t, rootItem.ID, ownership.ResourceID, "retry must recover the same reservation")
		return rootItem, nil
	}
	spec := model.ResourceMap{"network": "cluster"}
	first, err := newFloatingIPStepFromSpec("floating_ip", "api", spec, op)
	require.NoError(t, err)
	require.ErrorIs(t, first.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, nil), lost)
	pendingID := saved.Output.GetString("resource_id")
	require.Len(t, pendingID, 32)
	retry, err := newFloatingIPStepFromSpec("floating_ip", "api", spec, op)
	require.NoError(t, err)
	shared := workflow.NewSharedState()
	require.NoError(t, retry.Apply(t.Context(), shared, saved, write, nil))
	assert.Equal(t, pendingID, saved.Output.GetString("resource_id"))
	assert.Equal(t, "172.27.0.100", saved.Output.GetString("ipv4"))
	resolved, err := resourceStepReference(shared, "@floating_ip:api", "floating_ip")
	require.NoError(t, err)
	assert.Equal(t, pendingID, resolved)
}

func TestFloatingIPRefusesEffectsWhenIdentityCannotBeSaved(t *testing.T) {
	op := floatingIPTestAPI()
	effects := false
	op.FloatingIPEnsureFunc = func(context.Context, inputs.FloatingIPCreateInput, inputs.EnvironmentFloatingIPOwnershipInput) (*model.FloatingIPItem, error) {
		effects = true
		return nil, errors.New("must not create")
	}
	step, err := newFloatingIPStepFromSpec("floating_ip", "api", model.ResourceMap{"network": "cluster"}, op)
	require.NoError(t, err)
	unavailable := errors.New("state directory unavailable")
	err = step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{},
		func(context.Context, model.ResourceState) error { return unavailable }, nil)
	require.ErrorIs(t, err, unavailable)
	assert.False(t, effects)
}

func TestFloatingIPAdoptionAndMissingRecordedIdentity(t *testing.T) {
	op := floatingIPTestAPI()
	item := &model.FloatingIPItem{
		ID:        "11111111111111111111111111111111",
		NetworkID: floatingIPTestNetworkID,
		Name:      "external",
		IPv4:      "172.27.0.100",
	}
	op.FloatingIPGetFunc = func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error) { return item, nil }
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	spec := model.ResourceMap{"existing": "external", "network": "cluster"}
	step, err := newFloatingIPStepFromSpec("floating_ip", "api", spec, op)
	require.NoError(t, err)
	require.NoError(t, step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, nil))
	assert.False(t, saved.Meta.WasCreated)
	op.FloatingIPRemoveFunc = func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
		t.Fatal("adopted reservation must survive destroy")
		return nil, nil
	}
	restored, err := newFloatingIPStepFromState("floating_ip", "api", saved, []string{"network:cluster"}, op)
	require.NoError(t, err)
	require.NoError(t, restored.Destroy(t.Context(), saved, write, nil))
	assert.Equal(t, []string{"network:cluster"}, restored.Dependencies())
	op.FloatingIPGetFunc = func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
		return nil, errs.New(errs.CodeNetworkNotFound, "recorded reservation missing")
	}
	retry, err := newFloatingIPStepFromSpec("floating_ip", "api", spec, op)
	require.NoError(t, err)
	require.Error(t, retry.Apply(t.Context(), workflow.NewSharedState(), saved, write, nil))
}

func TestFloatingIPOwnedDestroyPreservesStateOnRetirementFailure(t *testing.T) {
	op := floatingIPTestAPI()
	op.FloatingIPEnsureFunc = func(_ context.Context, input inputs.FloatingIPCreateInput, ownership inputs.EnvironmentFloatingIPOwnershipInput) (*model.FloatingIPItem, error) {
		return &model.FloatingIPItem{
			ID:        ownership.ResourceID,
			NetworkID: input.Network,
			Name:      input.Name,
			IPv4:      "172.27.0.100",
		}, nil
	}
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	step, err := newFloatingIPStepFromSpec("floating_ip", "api", model.ResourceMap{"network": "cluster"}, op)
	require.NoError(t, err)
	require.NoError(t, step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, nil))
	id := saved.Output.GetString("resource_id")
	op.FloatingIPGetFunc = func(_ context.Context, input inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
		require.Equal(t, id, input.Identifier)
		return &model.FloatingIPItem{
			ID:         id,
			NetworkID:  floatingIPTestNetworkID,
			Name:       "api",
			IPv4:       "172.27.0.100",
			Generation: 7,
		}, nil
	}
	blocked := errors.New("reservation is still owned")
	op.FloatingIPRemoveFunc = func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error) { return nil, blocked }
	restored, err := newFloatingIPStepFromState("floating_ip", "api", saved, nil, op)
	require.NoError(t, err)
	require.ErrorIs(t, restored.Destroy(t.Context(), saved, write, nil), blocked)
	assert.Equal(t, id, saved.Output.GetString("resource_id"))
	op.FloatingIPRemoveFunc = func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error) { return nil, nil }
	require.NoError(t, restored.Destroy(t.Context(), saved, write, nil))
}
