package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

func TestResolveExecPolicyPreservesExactAndLiveSelectorEndpoints(t *testing.T) {
	ctx := t.Context()
	repo := testutil.NewVMRepo()
	id := strings.Repeat("a", 32)
	require.NoError(t, repo.Upsert(ctx, &model.VMItem{ID: id, Name: "source", NetworkID: ""}))
	op := &Operation{Repos: Repos{VM: repo}}
	labels := model.VMLabelSelector{"role": "controller"}
	request, err := op.resolveExecPolicy(ctx, inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "source", DestinationLabels: labels, TargetUser: "app",
	})
	require.NoError(t, err)
	assert.Equal(t, id, request.Source.VMID)
	assert.Nil(t, request.Source.Labels)
	assert.Empty(t, request.Destination.VMID)
	assert.Equal(t, labels, request.Destination.Labels)
	assert.Equal(t, "app", request.TargetUser)
}
