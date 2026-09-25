package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/execpolicy"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type memoryExecPolicyRepo struct {
	items []*model.VMToVMExecAllowIntent
}

func (r *memoryExecPolicyRepo) Create(_ context.Context, intent *model.VMToVMExecAllowIntent) error {
	r.items = append(r.items, intent)
	return nil
}

func (r *memoryExecPolicyRepo) Get(_ context.Context, id string) (*model.VMToVMExecAllowIntent, error) {
	for _, intent := range r.items {
		if intent.ID == id {
			return intent, nil
		}
	}
	return nil, nil
}

func (r *memoryExecPolicyRepo) GetByIdentity(
	_ context.Context,
	identity *model.VMToVMExecAllowIntent,
) (*model.VMToVMExecAllowIntent, error) {
	for _, intent := range r.items {
		if intent.SourceVMID == identity.SourceVMID && intent.DestinationVMID == identity.DestinationVMID &&
			intent.TargetUser == identity.TargetUser {
			return intent, nil
		}
	}
	return nil, nil
}

func (r *memoryExecPolicyRepo) FindByPrefix(
	_ context.Context,
	prefix string,
) ([]*model.VMToVMExecAllowIntent, error) {
	var found []*model.VMToVMExecAllowIntent
	for _, intent := range r.items {
		if strings.HasPrefix(intent.ID, prefix) {
			found = append(found, intent)
		}
	}
	return found, nil
}

func (r *memoryExecPolicyRepo) List(context.Context) ([]*model.VMToVMExecAllowIntent, error) {
	return r.items, nil
}

func (r *memoryExecPolicyRepo) DeleteMany(_ context.Context, ids []string) error {
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	kept := r.items[:0]
	for _, intent := range r.items {
		if !remove[intent.ID] {
			kept = append(kept, intent)
		}
	}
	r.items = kept
	return nil
}

func (r *memoryExecPolicyRepo) DeleteByVM(_ context.Context, vmID string) error {
	kept := r.items[:0]
	for _, intent := range r.items {
		if intent.SourceVMID != vmID && intent.DestinationVMID != vmID {
			kept = append(kept, intent)
		}
	}
	r.items = kept
	return nil
}

func (r *memoryExecPolicyRepo) Allows(
	_ context.Context,
	sourceVMID string,
	destinationVMID string,
	targetUser string,
) (bool, error) {
	for _, intent := range r.items {
		if intent.SourceVMID == sourceVMID && intent.DestinationVMID == destinationVMID &&
			intent.TargetUser == targetUser {
			return true, nil
		}
	}
	return false, nil
}

var _ execpolicy.Repository = (*memoryExecPolicyRepo)(nil)

func TestExecPolicyAPIIsDirectionalDeterministicAndNetworkIndependent(t *testing.T) {
	ctx := t.Context()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-a", Name: "source", NetworkID: ""}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-b", Name: "destination", NetworkID: "net-b"}))
	repo := &memoryExecPolicyRepo{}
	op := &api.Operation{
		Repos:    api.Repos{VM: vmRepo, ExecPolicy: repo},
		Services: api.Services{ExecPolicy: execpolicy.NewService(repo)},
		AuditLog: &logging.AuditLog{},
	}

	created, err := op.ExecPolicyCreate(ctx, inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "source", DestinationVM: "destination", TargetUser: "app",
	})
	require.NoError(t, err)
	wantID := crypto.VMToVMExecPolicyDigest(model.VMToVMExecAllowIntent{
		SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
	})
	assert.Equal(t, wantID, created.ID)
	assert.Equal(t, "source", created.SourceVMName)
	assert.Equal(t, "destination", created.DestinationVMName)

	_, err = op.ExecPolicyCreate(ctx, inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "source", DestinationVM: "destination", TargetUser: "app",
	})
	require.Error(t, err)
	assert.Equal(t, errs.CodePolicyAlreadyExists, errs.AsDomainError(err).Code)

	reverse, err := op.ExecPolicyCreate(ctx, inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "destination", DestinationVM: "source", TargetUser: "app",
	})
	require.NoError(t, err)
	assert.NotEqual(t, created.ID, reverse.ID)

	listed, err := op.ExecPolicyList(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.NoError(t, op.ExecPolicyRemove(ctx, inputs.VMToVMExecPolicyInput{
		Identifiers: []string{created.ID[:12], created.ID[:12]},
	}))
	listed, err = op.ExecPolicyList(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, reverse.ID, listed[0].ID)
}
