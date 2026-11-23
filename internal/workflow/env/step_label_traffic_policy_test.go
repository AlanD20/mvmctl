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

func TestLabelTrafficPolicyRetriesAndRemovesWithItsPersistedClaim(t *testing.T) {
	op := floatingIPGrantTestAPI()
	spec := model.ResourceMap{
		"source_labels":      map[string]string{"role": "worker", "cluster": "alpha"},
		"destination_labels": map[string]string{"role": "controller", "cluster": "alpha"},
		"protocol":           "tcp",
		"port":               "6443",
	}
	step, err := newTrafficPolicyStepFromSpec("traffic_policy", "api", spec, op)
	require.NoError(t, err)
	ctx := t.Context()
	shared := workflow.NewSharedState()
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	uncertain := errors.New("the root result was not observed")
	var originalClaim string
	var originalInput inputs.LabelTrafficPolicyCreateInput
	op.LabelTrafficPolicyEnsureFunc = func(
		_ context.Context, input inputs.LabelTrafficPolicyCreateInput, ownership inputs.EnvironmentPolicyOwnershipInput,
	) (*results.EnvironmentPolicyReceipt, error) {
		require.NotEmpty(t, ownership.ClaimID)
		require.Equal(t, saved.Output.GetString("claim_id"), ownership.ClaimID)
		originalClaim, originalInput = ownership.ClaimID, input
		return nil, uncertain
	}
	require.ErrorIs(t, step.Apply(ctx, shared, model.ResourceState{}, write, nil), uncertain)
	assert.Equal(t, model.VMLabelSelector{"role": "worker", "cluster": "alpha"}, originalInput.Source.Labels)
	assert.Equal(t, model.VMLabelSelector{"role": "controller", "cluster": "alpha"}, originalInput.Destination.Labels)

	receipt := &results.EnvironmentPolicyReceipt{
		PolicyID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Incarnation: "11111111-1111-4111-8111-111111111111",
		WasCreated:  true,
	}
	op.LabelTrafficPolicyEnsureFunc = func(
		_ context.Context, input inputs.LabelTrafficPolicyCreateInput, ownership inputs.EnvironmentPolicyOwnershipInput,
	) (*results.EnvironmentPolicyReceipt, error) {
		assert.Equal(t, originalInput, input)
		assert.Equal(t, originalClaim, ownership.ClaimID)
		return receipt, nil
	}
	step, err = newTrafficPolicyStepFromState("traffic_policy", "api", saved, nil, op)
	require.NoError(t, err)
	require.NoError(t, step.Apply(ctx, shared, saved, write, nil))
	assert.Equal(t, receipt.Incarnation, saved.Output.GetString("incarnation"))

	removed := false
	op.LabelTrafficPolicyRemoveOwnedFunc = func(
		_ context.Context, input inputs.LabelTrafficPolicyCreateInput, ownership inputs.EnvironmentPolicyOwnershipInput,
	) error {
		assert.Equal(t, originalInput, input)
		assert.Equal(t, originalClaim, ownership.ClaimID)
		assert.Equal(t, receipt.Incarnation, ownership.Incarnation)
		removed = true
		return nil
	}
	require.NoError(t, step.Destroy(ctx, saved, write, nil))
	assert.True(t, removed)
}
