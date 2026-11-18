package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
)

type delegationCLIRecorder struct {
	grant   inputs.FloatingIPDelegationGrantInput
	revoked string
}

func (r *delegationCLIRecorder) FloatingIPDelegationGrant(_ context.Context,
	i inputs.FloatingIPDelegationGrantInput) (*model.FloatingIPDelegationItem, error) {
	r.grant = i
	return &model.FloatingIPDelegationItem{ID: "stored"}, nil
}
func (r *delegationCLIRecorder) FloatingIPDelegationList(context.Context,
	inputs.FloatingIPInput) ([]*model.FloatingIPDelegationItem, error) {
	return []*model.FloatingIPDelegationItem{}, nil
}
func (r *delegationCLIRecorder) FloatingIPDelegationGet(context.Context,
	inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error) {
	return &model.FloatingIPDelegationItem{}, nil
}
func (r *delegationCLIRecorder) FloatingIPDelegationRevoke(_ context.Context,
	i inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error) {
	r.revoked = i.ID
	return &model.FloatingIPDelegationItem{ID: i.ID}, nil
}

func TestFloatingIPDelegationCLIForwardsExplicitPermissionWithoutImplicitLease(t *testing.T) {
	recorder := &delegationCLIRecorder{}
	selected := "managed"
	command := newFloatingIPDelegationCmd(recorder, nil, &selected)
	command.SetArgs([]string{"grant", "vip", "--vm", "control-1", "--method", "renew", "--lease", "5s"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	assert.Equal(t, "managed", recorder.grant.Network)
	assert.Equal(t, "vip", recorder.grant.Resource)
	assert.Equal(t, "control-1", recorder.grant.VM)
	assert.Equal(t, "renew", recorder.grant.Method)
	assert.Equal(t, "5s", recorder.grant.Lease)
	recorder = &delegationCLIRecorder{}
	command = newFloatingIPDelegationCmd(recorder, nil, &selected)
	command.SetArgs([]string{"grant", "vip", "--vm", "control-1", "--method", "associate"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	assert.Empty(t, recorder.grant.Lease, "CLI must not invent an association lease")
}

func (r *delegationCLIRecorder) FloatingIPDelegationEnsure(context.Context,
	inputs.FloatingIPDelegationEnsureInput) (*model.FloatingIPDelegationItem, error) {
	return nil, nil
}
func (r *delegationCLIRecorder) FloatingIPDelegationRemoveOwned(context.Context,
	inputs.FloatingIPDelegationRemoveOwnedInput) (*model.FloatingIPDelegationItem, error) {
	return nil, nil
}
