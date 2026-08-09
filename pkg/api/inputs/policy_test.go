package inputs_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type policyResolverRepo struct{ items []*model.ServiceAccessPolicy }

func (*policyResolverRepo) Create(context.Context, *model.ServiceAccessPolicy) error { return nil }
func (r *policyResolverRepo) Get(_ context.Context, id string) (*model.ServiceAccessPolicy, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return nil, nil
}

func (*policyResolverRepo) GetByIdentity(
	context.Context,
	*model.ServiceAccessPolicy,
) (*model.ServiceAccessPolicy, error) {
	return nil, nil
}
func (r *policyResolverRepo) FindByPrefix(_ context.Context, prefix string) ([]*model.ServiceAccessPolicy, error) {
	var matches []*model.ServiceAccessPolicy
	for _, item := range r.items {
		if len(item.ID) >= len(prefix) && item.ID[:len(prefix)] == prefix {
			matches = append(matches, item)
		}
	}
	return matches, nil
}
func (r *policyResolverRepo) List(context.Context) ([]*model.ServiceAccessPolicy, error) {
	return r.items, nil
}
func (*policyResolverRepo) Delete(context.Context, string) error                { return nil }
func (*policyResolverRepo) DeleteMany(context.Context, []string) error          { return nil }
func (*policyResolverRepo) DeleteByVM(context.Context, string) error            { return nil }
func (*policyResolverRepo) DeleteBySourceNetwork(context.Context, string) error { return nil }

var _ network.ServiceAccessPolicyRepository = (*policyResolverRepo)(nil)

func TestPolicyCreateInputValidationAndPortResolution(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input      inputs.PolicyCreateInput
		protocol   model.ServiceAccessPolicyProtocol
		start, end int
		code       errs.Code
	}{
		"TCP scalar": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "TCP",
			DestinationPort: "443"}, protocol: model.ServiceAccessPolicyProtocolTCP, start: 443, end: 443},
		"UDP range": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "udp",
			DestinationPort: "5000-5005"}, protocol: model.ServiceAccessPolicyProtocolUDP, start: 5000, end: 5005},
		"minimum": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "tcp",
			DestinationPort: "1"}, protocol: model.ServiceAccessPolicyProtocolTCP, start: 1, end: 1},
		"maximum": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "udp",
			DestinationPort: "65535"}, protocol: model.ServiceAccessPolicyProtocolUDP, start: 65535, end: 65535},
		"source missing": {input: inputs.PolicyCreateInput{DestinationVM: "vm", Protocol: "tcp", DestinationPort: "1"},
			code: errs.CodeValidationFailed},
		"destination missing": {
			input: inputs.PolicyCreateInput{SourceNetwork: "a", Protocol: "tcp", DestinationPort: "1"},
			code:  errs.CodeValidationFailed,
		},
		"protocol invalid": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "icmp",
			DestinationPort: "1"}, code: errs.CodePolicyProtocolInvalid},
		"zero": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "tcp",
			DestinationPort: "0"}, code: errs.CodePolicyPortInvalid},
		"reversed": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "tcp",
			DestinationPort: "2-1"}, code: errs.CodePolicyPortInvalid},
		"too large": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "tcp",
			DestinationPort: "65536"}, code: errs.CodePolicyPortInvalid},
		"malformed": {input: inputs.PolicyCreateInput{SourceNetwork: "a", DestinationVM: "vm", Protocol: "tcp",
			DestinationPort: "1-2-3"}, code: errs.CodePolicyPortInvalid},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			protocol, start, end, err := tc.input.ResolvedPort()
			if tc.code != "" {
				require.Error(t, err)
				de := errs.AsDomainError(err)
				require.NotNil(t, de)
				assert.Equal(t, tc.code, de.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.protocol, protocol)
			assert.Equal(t, tc.start, start)
			assert.Equal(t, tc.end, end)
		})
	}
}

func TestPolicyInputResolve(t *testing.T) {
	ctx := context.Background()
	repo := &policyResolverRepo{items: []*model.ServiceAccessPolicy{{ID: "abc-111"}, {ID: "abc-222"}, {ID: "xyz-333"}}}
	tests := map[string]struct {
		identifiers []string
		want        []string
		code        errs.Code
	}{
		"exact and prefix": {identifiers: []string{"abc-111", "xyz"}, want: []string{"abc-111", "xyz-333"}},
		"deduplicates":     {identifiers: []string{"xyz", "xyz-333"}, want: []string{"xyz-333"}},
		"missing":          {identifiers: []string{"none"}, code: errs.CodePolicyNotFound},
		"ambiguous":        {identifiers: []string{"abc"}, code: errs.CodePolicyAmbiguous},
		"empty list":       {code: errs.CodeValidationFailed},
		"blank identifier": {identifiers: []string{" "}, code: errs.CodeValidationFailed},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := (&inputs.PolicyInput{Identifiers: tc.identifiers}).Resolve(ctx, repo)
			if tc.code != "" {
				require.Error(t, err)
				assert.Equal(t, tc.code, errs.AsDomainError(err).Code)
				return
			}
			require.NoError(t, err)
			ids := make([]string, len(got))
			for i := range got {
				ids[i] = got[i].ID
			}
			assert.Equal(t, tc.want, ids)
		})
	}
}

func TestPolicyCreateInputResolvesNetworkAndVM(t *testing.T) {
	ctx := context.Background()
	networkRepo := testutil.NewNetworkRepo()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "net-id", Name: "source", IsPresent: true}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-id", Name: "destination"}))
	input := &inputs.PolicyCreateInput{SourceNetwork: "source", DestinationVM: "destination", Protocol: "tcp",
		DestinationPort: "443"}
	source, err := input.ResolveSourceNetwork(ctx, networkRepo)
	require.NoError(t, err)
	destination, err := input.ResolveDestinationVM(ctx, vmRepo)
	require.NoError(t, err)
	assert.Equal(t, "net-id", source.ID)
	assert.Equal(t, "vm-id", destination.ID)
}
