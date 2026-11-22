package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

type recordingVMLabelsAPI struct {
	update *inputs.VMLabelUpdateInput
}

func (*recordingVMLabelsAPI) VMLabelInspect(context.Context, inputs.VMLabelInput) (results.VMLabels, error) {
	return results.VMLabels{}, nil
}

func (api *recordingVMLabelsAPI) VMLabelUpdate(
	_ context.Context, input inputs.VMLabelUpdateInput,
) (results.VMLabels, error) {
	api.update = &input
	return results.VMLabels{Revision: input.ExpectedRevision + 1, Labels: input.Labels}, nil
}

func TestVMLabelsSetRequiresRevisionAndRejectsDuplicateKeys(t *testing.T) {
	for _, arguments := range [][]string{
		{"set", "worker-1", "role=worker"},
		{"set", "worker-1", "role=worker", "role=controller", "--revision", "0"},
	} {
		api := &recordingVMLabelsAPI{}
		command := newVMLabelsCmd(api)
		command.SetArgs(arguments)
		require.Error(t, command.ExecuteContext(t.Context()))
		assert.Nil(t, api.update)
	}
}

func TestVMLabelsSetClearsLabelsAtExplicitRevision(t *testing.T) {
	api := &recordingVMLabelsAPI{}
	command := newVMLabelsCmd(api)
	command.SetArgs([]string{"set", "worker-1", "--revision", "4"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.NotNil(t, api.update)
	assert.Equal(t, "worker-1", api.update.VM)
	assert.EqualValues(t, 4, api.update.ExpectedRevision)
	assert.Equal(t, model.VMLabels{}, api.update.Labels)
}
