package env

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func TestHostTrafficWorkflowRetainsReceiptWhenProjectionFails(t *testing.T) {
	// Rationale: Root may grant access before SQLite fails. Saved incarnation must permit exact owned cleanup.
	const vmID = "11111111111111111111111111111111"
	const incarnation = "22222222-2222-4222-8222-222222222222"
	const policyID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	projectionErr := errors.New("local projection unavailable")
	op := &testutil.MockOperation{}
	op.VMGetFunc = func(context.Context, inputs.VMInput) (*model.VMItem, error) {
		return &model.VMItem{ID: vmID, Name: "client"}, nil
	}
	var saved model.ResourceState
	write := func(_ context.Context, value model.ResourceState) error { saved = value; return nil }
	op.VMToHostTrafficPolicyEnsureFunc = func(_ context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
		ownership inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error) {
		require.NoError(t, ownership.Validate())
		persisted := StateFromMap[TrafficPolicyState](saved.Output)
		require.NotNil(t, persisted)
		assert.Equal(t, ownership.ClaimID, persisted.ClaimID, "claim must be durable before the root mutation")
		if diff := cmp.Diff(inputs.VMToHostTrafficPolicyCreateInput{
			SourceVM: vmID, Protocol: "tcp", DestinationPort: "5432",
		}, input); diff != "" {
			t.Errorf("host intent (-want +got):\n%s", diff)
		}
		return &results.EnvironmentPolicyReceipt{
			PolicyID: policyID, Incarnation: incarnation, WasCreated: true,
		}, projectionErr
	}
	step, err := newTrafficPolicyStepFromSpec("traffic_policy", "host", model.ResourceMap{
		"source_vm": "client", "destination_host": true, "protocol": "tcp", "port": "5432",
	}, op)
	require.NoError(t, err)
	err = step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{}, write, nil)
	assert.ErrorIs(t, err, projectionErr)
	output := StateFromMap[TrafficPolicyState](saved.Output)
	require.NotNil(t, output)
	assert.True(t, output.DestinationHost)
	assert.Empty(t, output.DestinationVMID)
	assert.Equal(t, policyID, output.PolicyID)
	assert.Equal(t, incarnation, output.Incarnation)
	assert.True(t, saved.Meta.WasCreated)
	removed := false
	op.VMToHostTrafficPolicyRemoveOwnedFunc = func(_ context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
		ownership inputs.EnvironmentPolicyOwnershipInput) error {
		removed = true
		if diff := cmp.Diff(inputs.EnvironmentPolicyOwnershipInput{
			ClaimID: output.ClaimID, Incarnation: incarnation,
		}, ownership); diff != "" {
			t.Errorf("cleanup ownership (-want +got):\n%s", diff)
		}
		assert.Equal(t, vmID, input.SourceVM)
		return nil
	}
	require.NoError(t, step.Destroy(t.Context(), saved, write, nil))
	assert.True(t, removed, "a failed projection must not leak its acknowledged root grant")
}

func TestHostTrafficWorkflowRefusesEffectBeforeDurableClaim(t *testing.T) {
	op := &testutil.MockOperation{}
	op.VMGetFunc = func(context.Context, inputs.VMInput) (*model.VMItem, error) {
		return &model.VMItem{ID: "11111111111111111111111111111111"}, nil
	}
	called := false
	op.VMToHostTrafficPolicyEnsureFunc = func(context.Context, inputs.VMToHostTrafficPolicyCreateInput,
		inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error) {
		called = true
		return nil, nil
	}
	step, err := newTrafficPolicyStepFromSpec("traffic_policy", "host", model.ResourceMap{
		"source_vm": "client", "destination_host": true, "protocol": "udp", "port": "53",
	}, op)
	require.NoError(t, err)
	writeErr := errors.New("state durability failure")
	err = step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{},
		func(context.Context, model.ResourceState) error { return writeErr }, nil)
	assert.ErrorIs(t, err, writeErr)
	assert.False(t, called, "an unrecorded workflow claim must never grant host access")
}

func TestHostTrafficWorkflowRejectsAmbiguousDestination(t *testing.T) {
	for name, fields := range map[string]model.ResourceMap{
		"both destinations": {"source_vm": "client", "destination_host": true, "destination_vm": "server"},
		"no destination":    {"source_vm": "client"},
		"both sources":      {"source_vm": "client", "source_network": "network", "destination_host": true},
		"implicit ports":    {"source_vm": "client", "destination_host": true, "protocol": "tcp"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newTrafficPolicyStepFromSpec("traffic_policy", "host", fields, &testutil.MockOperation{})
			assert.Error(t, err)
		})
	}
}
