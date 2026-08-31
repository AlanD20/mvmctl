package inputs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestTypedTrafficPolicyInputsValidateClosedShapes(t *testing.T) {
	tests := map[string]struct {
		validate func() error
		code     errs.Code
	}{
		"network source missing": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: "443",
				}).Validate()
			},
			code: errs.CodeValidationFailed,
		},
		"VM source missing": {
			validate: func() error {
				return (&inputs.VMToVMTrafficPolicyCreateInput{
					DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: "443",
				}).Validate()
			},
			code: errs.CodeValidationFailed,
		},
		"destination missing": {
			validate: func() error {
				return (&inputs.VMToVMTrafficPolicyCreateInput{
					SourceVM: "vm-a", Protocol: "tcp", DestinationPort: "443",
				}).Validate()
			},
			code: errs.CodeValidationFailed,
		},
		"protocol case is not canonical": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "TCP", DestinationPort: "443",
				}).Validate()
			},
			code: errs.CodePolicyProtocolInvalid,
		},
		"protocol whitespace is not canonical": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: " tcp ", DestinationPort: "443",
				}).Validate()
			},
			code: errs.CodePolicyProtocolInvalid,
		},
		"unsupported protocol": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "icmp", DestinationPort: "443",
				}).Validate()
			},
			code: errs.CodePolicyProtocolInvalid,
		},
		"port whitespace is not canonical": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: " 443",
				}).Validate()
			},
			code: errs.CodePolicyPortInvalid,
		},
		"port zero": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: "0",
				}).Validate()
			},
			code: errs.CodePolicyPortInvalid,
		},
		"port with leading zero is not canonical": {
			validate: func() error {
				return (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: "0443",
				}).Validate()
			},
			code: errs.CodePolicyPortInvalid,
		},
		"range reversed": {
			validate: func() error {
				return (&inputs.VMToVMTrafficPolicyCreateInput{
					SourceVM: "vm-a", DestinationVM: "vm-b", Protocol: "udp", DestinationPort: "5005-5000",
				}).Validate()
			},
			code: errs.CodePolicyPortInvalid,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.validate()
			var domainErr *errs.DomainError
			require.ErrorAs(t, err, &domainErr)
			assert.Equal(t, tc.code, domainErr.Code)
		})
	}
}

func TestTypedTrafficPolicyInputsResolveSameAndCrossNetworkExplicitly(t *testing.T) {
	ctx := context.Background()
	networkRepo := testutil.NewNetworkRepo()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "net-a", Name: "net-a", IsPresent: true}))
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "net-b", Name: "net-b", IsPresent: true}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-a", Name: "vm-a", NetworkID: "net-a"}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-b", Name: "vm-b", NetworkID: "net-a"}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-c", Name: "vm-c", NetworkID: "net-b"}))

	tests := map[string]struct {
		resolve func() (model.ManagedNetworkRelation, error)
		want    model.ManagedNetworkRelation
	}{
		"network to VM on same network": {
			resolve: func() (model.ManagedNetworkRelation, error) {
				resolved, err := (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: "443",
				}).Resolve(ctx, networkRepo, vmRepo)
				if err != nil {
					return "", err
				}
				return resolved.NetworkRelation, nil
			},
			want: model.ManagedNetworkRelationSame,
		},
		"network to VM across networks": {
			resolve: func() (model.ManagedNetworkRelation, error) {
				resolved, err := (&inputs.NetworkToVMTrafficPolicyCreateInput{
					SourceNetwork: "net-a", DestinationVM: "vm-c", Protocol: "tcp", DestinationPort: "443",
				}).Resolve(ctx, networkRepo, vmRepo)
				if err != nil {
					return "", err
				}
				return resolved.NetworkRelation, nil
			},
			want: model.ManagedNetworkRelationCross,
		},
		"VM to VM on same network": {
			resolve: func() (model.ManagedNetworkRelation, error) {
				resolved, err := (&inputs.VMToVMTrafficPolicyCreateInput{
					SourceVM: "vm-a", DestinationVM: "vm-b", Protocol: "udp", DestinationPort: "53",
				}).Resolve(ctx, vmRepo, networkRepo)
				if err != nil {
					return "", err
				}
				return resolved.NetworkRelation, nil
			},
			want: model.ManagedNetworkRelationSame,
		},
		"VM to VM across networks": {
			resolve: func() (model.ManagedNetworkRelation, error) {
				resolved, err := (&inputs.VMToVMTrafficPolicyCreateInput{
					SourceVM: "vm-a", DestinationVM: "vm-c", Protocol: "udp", DestinationPort: "53",
				}).Resolve(ctx, vmRepo, networkRepo)
				if err != nil {
					return "", err
				}
				return resolved.NetworkRelation, nil
			},
			want: model.ManagedNetworkRelationCross,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tc.resolve()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTypedTrafficPolicyInputBuildsStableCanonicalIntent(t *testing.T) {
	ctx := t.Context()
	networkRepo := testutil.NewNetworkRepo()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "net-a", Name: "source", IsPresent: true}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-b", Name: "destination", NetworkID: "net-a"}))

	resolved, err := (&inputs.NetworkToVMTrafficPolicyCreateInput{
		SourceNetwork: "source", DestinationVM: "destination", Protocol: "tcp", DestinationPort: "443-444",
	}).Resolve(ctx, networkRepo, vmRepo)
	require.NoError(t, err)
	first := resolved.NetworkToVMIntent(time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("offset", 3600)))
	second := resolved.NetworkToVMIntent(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	want := &model.NetworkToVMTrafficAllowIntent{
		ID: first.ID, SourceNetworkID: "net-a", DestinationVMID: "vm-b", Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 443, DestinationPortEnd: 444,
		CreatedAt: "2025-12-31T23:00:00Z", UpdatedAt: "2025-12-31T23:00:00Z",
	}
	if diff := cmp.Diff(want, first); diff != "" {
		t.Errorf("resolved network-to-VM intent mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, first.ID, second.ID, "timestamps must not change canonical policy identity")
}

func TestExecPolicyInputIsDirectionalNonRootAndNetworkIndependent(t *testing.T) {
	ctx := t.Context()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-a", Name: "vm-a", NetworkID: ""}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-b", Name: "vm-b", NetworkID: "net-b"}))

	for name, input := range map[string]inputs.VMToVMExecPolicyCreateInput{
		"root":       {SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: "root"},
		"self":       {SourceVM: "vm-a", DestinationVM: "vm-a", TargetUser: "app"},
		"empty user": {SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: ""},
		"oversized user": {
			SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: "user_name_longer_than_thirty_two_chars",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := input.Resolve(ctx, vmRepo)
			assert.Error(t, err)
		})
	}

	resolved, err := (&inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: "app",
	}).Resolve(ctx, vmRepo)
	require.NoError(t, err)
	intent := resolved.Intent(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	want := &model.VMToVMExecAllowIntent{
		ID: intent.ID, SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	if diff := cmp.Diff(want, intent); diff != "" {
		t.Errorf("resolved VM-to-VM exec intent mismatch (-want +got):\n%s", diff)
	}

	reverse, err := (&inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "vm-b", DestinationVM: "vm-a", TargetUser: "app",
	}).Resolve(ctx, vmRepo)
	require.NoError(t, err)
	assert.NotEqual(t, intent.ID, reverse.Intent(time.Time{}).ID, "exec direction must change policy identity")
}
