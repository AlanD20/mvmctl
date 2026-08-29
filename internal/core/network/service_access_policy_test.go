package network_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

type policyRepoStub struct {
	items      []*model.ServiceAccessPolicy
	existing   *model.ServiceAccessPolicy
	err        error
	created    *model.ServiceAccessPolicy
	deleted    []string
	deletedVM  string
	deletedNet string
}

func (r *policyRepoStub) Create(_ context.Context, p *model.ServiceAccessPolicy) error {
	r.created = p
	return r.err
}
func (r *policyRepoStub) Get(_ context.Context, id string) (*model.ServiceAccessPolicy, error) {
	for _, p := range r.items {
		if p.ID == id {
			return p, r.err
		}
	}
	return nil, r.err
}

func (r *policyRepoStub) GetByIdentity(
	context.Context,
	*model.ServiceAccessPolicy,
) (*model.ServiceAccessPolicy, error) {
	return r.existing, r.err
}
func (r *policyRepoStub) FindByPrefix(_ context.Context, prefix string) ([]*model.ServiceAccessPolicy, error) {
	var found []*model.ServiceAccessPolicy
	for _, p := range r.items {
		if len(p.ID) >= len(prefix) && p.ID[:len(prefix)] == prefix {
			found = append(found, p)
		}
	}
	return found, r.err
}
func (r *policyRepoStub) List(context.Context) ([]*model.ServiceAccessPolicy, error) {
	return r.items, r.err
}
func (r *policyRepoStub) Delete(context.Context, string) error { return r.err }
func (r *policyRepoStub) DeleteMany(_ context.Context, ids []string) error {
	r.deleted = append([]string(nil), ids...)
	return r.err
}
func (r *policyRepoStub) DeleteByVM(_ context.Context, id string) error {
	r.deletedVM = id
	return r.err
}
func (r *policyRepoStub) DeleteBySourceNetwork(_ context.Context, id string) error {
	r.deletedNet = id
	return r.err
}

func TestCompileServiceAccessPolicyRules(t *testing.T) {
	t.Parallel()
	policies := []*model.ResolvedServiceAccessPolicy{
		{ServiceAccessPolicy: model.ServiceAccessPolicy{ID: "udp", SourceNetworkID: "net-b", DestinationVMID: "vm-b",
			Protocol: model.ServiceAccessPolicyProtocolUDP, DestinationPortStart: 5000, DestinationPortEnd: 5005},
			SourceNetworkBridge: "mvm-b", DestinationIPv4: "10.2.0.8", DestinationNetworkBridge: "mvm-c"},
		{ServiceAccessPolicy: model.ServiceAccessPolicy{ID: "tcp", SourceNetworkID: "net-a", DestinationVMID: "vm-a",
			Protocol: model.ServiceAccessPolicyProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443},
			SourceNetworkBridge: "mvm-a", DestinationIPv4: "10.1.0.7", DestinationNetworkBridge: "mvm-b"},
	}
	networks := []*model.NetworkItem{{ID: "net-b", Bridge: "mvm-b"}, {ID: "net-a", Bridge: "mvm-a"}}

	got := network.CompileServiceAccessPolicyRules(policies, networks)
	require.Len(t, got, 6)
	assert.Equal(t, []model.FirewallRuleType{
		model.FirewallRuleTypePolicyAllow, model.FirewallRuleTypePolicyAllow,
		model.FirewallRuleTypeRoutedDrop, model.FirewallRuleTypeRoutedDrop,
		model.FirewallRuleTypeHostInputDrop, model.FirewallRuleTypeHostInputDrop,
	}, []model.FirewallRuleType{got[0].RuleType, got[1].RuleType, got[2].RuleType, got[3].RuleType,
		got[4].RuleType, got[5].RuleType})
	assert.Equal(t, model.FirewallProtocolTCP, got[0].Protocol)
	assert.Equal(t, "10.1.0.7", got[0].Destination)
	assert.Equal(t, "mvm-a", got[0].InInterface)
	assert.Equal(t, "mvm-b", got[0].OutInterface)
	assert.Equal(t, 443, got[0].DPort)
	assert.Equal(t, 443, got[0].DPortEnd)
	assert.Equal(t, model.FirewallProtocolUDP, got[1].Protocol)
	assert.Equal(t, 5000, got[1].DPort)
	assert.Equal(t, 5005, got[1].DPortEnd)
	assert.Equal(t, string(model.FirewallWildcardManagedInterface), got[2].OutInterface)
	assert.Equal(t, model.FirewallChainMVMHostInput, got[4].ChainName)
	assert.Equal(t, "mvm-a", got[4].InInterface)

	if diff := cmp.Diff([]string{"udp", "tcp"}, []string{policies[0].ID, policies[1].ID}); diff != "" {
		t.Errorf("compiler mutated caller policy order (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"mvm-b", "mvm-a"}, []string{networks[0].Bridge, networks[1].Bridge}); diff != "" {
		t.Errorf("compiler mutated caller network order (-want +got):\n%s", diff)
	}
}

func TestServiceAccessPolicyPersistence(t *testing.T) {
	ctx := context.Background()
	policy := &model.ServiceAccessPolicy{ID: "policy-1"}
	t.Run("create list remove and invalidate", func(t *testing.T) {
		repo := &policyRepoStub{items: []*model.ServiceAccessPolicy{policy}}
		svc := network.NewService(testutil.NewNetworkRepo(), nil, repo)
		require.NoError(t, svc.CreateServiceAccessPolicy(ctx, policy))
		assert.Same(t, policy, repo.created)
		got, err := svc.ListServiceAccessPolicies(ctx)
		require.NoError(t, err)
		assert.Equal(t, repo.items, got)
		require.NoError(t, svc.RemoveServiceAccessPolicies(ctx, []string{"a", "b"}))
		assert.Equal(t, []string{"a", "b"}, repo.deleted)
		require.NoError(t, svc.InvalidateServiceAccessPoliciesByVM(ctx, "vm-1"))
		require.NoError(t, svc.InvalidateServiceAccessPoliciesBySourceNetwork(ctx, "net-1"))
		assert.Equal(t, "vm-1", repo.deletedVM)
		assert.Equal(t, "net-1", repo.deletedNet)
	})
	t.Run("duplicate", func(t *testing.T) {
		svc := network.NewService(testutil.NewNetworkRepo(), nil, &policyRepoStub{existing: policy})
		err := svc.CreateServiceAccessPolicy(ctx, policy)
		require.Error(t, err)
		assertCode(t, err, errs.CodePolicyAlreadyExists)
	})
	t.Run("repository errors are typed", func(t *testing.T) {
		repo := &policyRepoStub{err: errors.New("locked")}
		svc := network.NewService(testutil.NewNetworkRepo(), nil, repo)
		for _, call := range []func() error{
			func() error { return svc.CreateServiceAccessPolicy(ctx, policy) },
			func() error { _, err := svc.ListServiceAccessPolicies(ctx); return err },
			func() error { return svc.RemoveServiceAccessPolicies(ctx, []string{"x"}) },
			func() error { return svc.InvalidateServiceAccessPoliciesByVM(ctx, "vm") },
			func() error { return svc.InvalidateServiceAccessPoliciesBySourceNetwork(ctx, "net") },
		} {
			assert.Error(t, call())
		}
	})
	t.Run("reconcile requires tracker", func(t *testing.T) {
		svc := network.NewService(testutil.NewNetworkRepo(), nil, &policyRepoStub{})
		err := svc.ReconcileServiceAccessPolicies(ctx, nil, nil)
		require.Error(t, err)
		assertCode(t, err, errs.CodePolicySyncFailed)
	})
}
