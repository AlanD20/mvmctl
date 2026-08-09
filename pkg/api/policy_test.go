package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type memoryPolicyRepo struct {
	items   []*model.ServiceAccessPolicy
	deleted []string
}

func (r *memoryPolicyRepo) Create(_ context.Context, policy *model.ServiceAccessPolicy) error {
	r.items = append(r.items, policy)
	return nil
}
func (r *memoryPolicyRepo) Get(_ context.Context, id string) (*model.ServiceAccessPolicy, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return nil, nil
}

func (r *memoryPolicyRepo) GetByIdentity(
	_ context.Context,
	policy *model.ServiceAccessPolicy,
) (*model.ServiceAccessPolicy, error) {
	for _, item := range r.items {
		if item.SourceNetworkID == policy.SourceNetworkID && item.DestinationVMID == policy.DestinationVMID &&
			item.Protocol == policy.Protocol && item.DestinationPortStart == policy.DestinationPortStart &&
			item.DestinationPortEnd == policy.DestinationPortEnd {
			return item, nil
		}
	}
	return nil, nil
}
func (r *memoryPolicyRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.ServiceAccessPolicy, error) {
	var found []*model.ServiceAccessPolicy
	for _, item := range r.items {
		if len(item.ID) >= len(prefix) && item.ID[:len(prefix)] == prefix {
			found = append(found, item)
		}
	}
	return found, nil
}
func (r *memoryPolicyRepo) List(context.Context) ([]*model.ServiceAccessPolicy, error) {
	return r.items, nil
}
func (r *memoryPolicyRepo) Delete(ctx context.Context, id string) error {
	return r.DeleteMany(ctx, []string{id})
}
func (r *memoryPolicyRepo) DeleteMany(_ context.Context, ids []string) error {
	r.deleted = append([]string(nil), ids...)
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	kept := r.items[:0]
	for _, item := range r.items {
		if !remove[item.ID] {
			kept = append(kept, item)
		}
	}
	r.items = kept
	return nil
}
func (r *memoryPolicyRepo) DeleteByVM(_ context.Context, id string) error {
	kept := r.items[:0]
	for _, item := range r.items {
		if item.DestinationVMID != id {
			kept = append(kept, item)
		}
	}
	r.items = kept
	return nil
}
func (r *memoryPolicyRepo) DeleteBySourceNetwork(_ context.Context, id string) error {
	kept := r.items[:0]
	for _, item := range r.items {
		if item.SourceNetworkID != id {
			kept = append(kept, item)
		}
	}
	r.items = kept
	return nil
}

func newPolicyOperation(t *testing.T, sameNetwork bool) (*api.Operation, *memoryPolicyRepo) {
	t.Helper()
	ctx := context.Background()
	networkRepo := testutil.NewNetworkRepo()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "source-id", Name: "source", Bridge: "mvm-source",
		IsPresent: true}))
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "destination-id", Name: "destination",
		Bridge: "mvm-destination", IsPresent: true}))
	destinationNetwork := "destination-id"
	if sameNetwork {
		destinationNetwork = "source-id"
	}
	require.NoError(
		t,
		vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-id", Name: "destination-vm", NetworkID: destinationNetwork,
			IPv4: "10.2.0.2"}),
	)
	policyRepo := &memoryPolicyRepo{}
	service := network.NewService(networkRepo, nil, policyRepo)
	return &api.Operation{
		Repos:    api.Repos{Network: networkRepo, VM: vmRepo, Policy: policyRepo},
		Services: api.Services{Network: service},
		AuditLog: &logging.AuditLog{},
	}, policyRepo
}

func TestPolicyCreateRejectsSameNetwork(t *testing.T) {
	op, repo := newPolicyOperation(t, true)
	_, err := op.PolicyCreate(
		context.Background(),
		inputs.PolicyCreateInput{SourceNetwork: "source", DestinationVM: "destination-vm",
			Protocol: "tcp", DestinationPort: "443"},
	)
	require.Error(t, err)
	assert.Equal(t, errs.CodePolicySameNetwork, errs.AsDomainError(err).Code)
	assert.Empty(t, repo.items)
}

func TestPolicyCreateRollsBackIntentWhenKernelSyncFails(t *testing.T) {
	op, repo := newPolicyOperation(t, false)
	_, err := op.PolicyCreate(
		context.Background(),
		inputs.PolicyCreateInput{SourceNetwork: "source", DestinationVM: "destination-vm",
			Protocol: "tcp", DestinationPort: "443"},
	)
	require.Error(t, err)
	assert.Equal(t, errs.CodePolicyCreateFailed, errs.AsDomainError(err).Code)
	assert.Empty(t, repo.items, "failed reconciliation must roll back persisted intent")
	require.Len(t, repo.deleted, 1)
}

func TestPolicyRemoveResolvesAllBeforeTransactionalDelete(t *testing.T) {
	op, repo := newPolicyOperation(t, false)
	repo.items = []*model.ServiceAccessPolicy{{ID: "aaa-111"}, {ID: "bbb-222"}}
	err := op.PolicyRemove(context.Background(), inputs.PolicyInput{Identifiers: []string{"aaa", "bbb"}})
	require.Error(t, err, "nil firewall tracker makes post-delete reconciliation fail")
	assert.Equal(t, []string{"aaa-111", "bbb-222"}, repo.deleted)
	assert.Empty(t, repo.items)
}

func TestPolicySyncReportsStaleResources(t *testing.T) {
	op, repo := newPolicyOperation(t, false)
	repo.items = []*model.ServiceAccessPolicy{{ID: "stale", SourceNetworkID: "missing", DestinationVMID: "vm-id",
		Protocol: model.ServiceAccessPolicyProtocolTCP, DestinationPortStart: 80, DestinationPortEnd: 80}}
	_, err := op.PolicySync(context.Background())
	require.Error(t, err)
	assert.Equal(t, errs.CodeNetworkNotFound, errs.AsDomainError(err).Code)

	repo.items[0].SourceNetworkID = "source-id"
	repo.items[0].DestinationVMID = "missing-vm"
	_, err = op.PolicySync(context.Background())
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMNotFound, errs.AsDomainError(err).Code)
}

func TestPolicyListAndInspectResolveCurrentNames(t *testing.T) {
	op, repo := newPolicyOperation(t, false)
	repo.items = []*model.ServiceAccessPolicy{{ID: "policy-123", SourceNetworkID: "source-id", DestinationVMID: "vm-id",
		Protocol: model.ServiceAccessPolicyProtocolUDP, DestinationPortStart: 53, DestinationPortEnd: 53}}
	items, err := op.PolicyList(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "source", items[0].SourceNetworkName)
	assert.Equal(t, "destination-vm", items[0].DestinationVMName)
	assert.Equal(t, "destination", items[0].DestinationNetworkName)
	inspected, err := op.PolicyInspect(context.Background(), inputs.PolicyInput{Identifiers: []string{"policy"}})
	require.NoError(t, err)
	assert.Equal(t, items[0], inspected)
}
