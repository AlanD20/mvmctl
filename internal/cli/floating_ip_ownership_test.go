package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

type ownershipCLIRecorder struct {
	api.FloatingIPAPI
	associate inputs.FloatingIPAssociateInput
	transfer  inputs.FloatingIPTransferInput
	release   inputs.FloatingIPReleaseInput
}

func (r *ownershipCLIRecorder) FloatingIPAssociate(
	_ context.Context,
	input inputs.FloatingIPAssociateInput,
) (*model.FloatingIPItem, error) {
	r.associate = input
	return &model.FloatingIPItem{Name: "vip"}, nil
}

func (r *ownershipCLIRecorder) FloatingIPTransfer(
	_ context.Context,
	input inputs.FloatingIPTransferInput,
) (*model.FloatingIPItem, error) {
	r.transfer = input
	return &model.FloatingIPItem{Name: "vip"}, nil
}

func (r *ownershipCLIRecorder) FloatingIPRelease(
	_ context.Context,
	input inputs.FloatingIPReleaseInput,
) (*model.FloatingIPItem, error) {
	r.release = input
	return &model.FloatingIPItem{Name: "vip"}, nil
}
func TestFloatingIPOwnershipCLIUsesNamedOperatorMethodsAndGeneration(t *testing.T) {
	selected := "managed"
	recorder := &ownershipCLIRecorder{}
	associate := newFloatingIPAssociateCmd(recorder, &selected)
	associate.SetArgs([]string{"vip", "node-a", "--generation", "7"})
	require.NoError(t, associate.ExecuteContext(t.Context()))
	require.Equal(
		t,
		inputs.FloatingIPAssociateInput{Network: "managed", Identifier: "vip", VM: "node-a", ExpectedGeneration: 7},
		recorder.associate,
	)
	transfer := newFloatingIPTransferCmd(recorder, &selected)
	transfer.SetArgs([]string{"vip", "node-b"})
	require.NoError(t, transfer.ExecuteContext(t.Context()))
	require.Equal(
		t,
		inputs.FloatingIPTransferInput{Network: "managed", Identifier: "vip", VM: "node-b"},
		recorder.transfer,
	)
	release := newFloatingIPReleaseCmd(recorder, &selected)
	release.SetArgs([]string{"vip", "--generation", "8"})
	require.NoError(t, release.ExecuteContext(t.Context()))
	require.Equal(
		t,
		inputs.FloatingIPReleaseInput{Network: "managed", Identifier: "vip", ExpectedGeneration: 8},
		recorder.release,
	)
}
