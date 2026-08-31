package inputs_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestTypedPolicyIdentifierInputsRequireCanonicalHexPrefixes(t *testing.T) {
	families := map[string]func([]string) error{
		"network to VM traffic": func(identifiers []string) error {
			return (&inputs.NetworkToVMTrafficPolicyInput{Identifiers: identifiers}).Validate()
		},
		"VM to VM traffic": func(identifiers []string) error {
			return (&inputs.VMToVMTrafficPolicyInput{Identifiers: identifiers}).Validate()
		},
		"VM to VM exec": func(identifiers []string) error {
			return (&inputs.VMToVMExecPolicyInput{Identifiers: identifiers}).Validate()
		},
	}
	invalid := map[string][]string{
		"no identifiers": nil,
		"empty":          {""},
		"percent":        {"aa%"},
		"underscore":     {"aa_"},
		"uppercase":      {"AA"},
		"path like":      {"../aa"},
		"over 64":        {strings.Repeat("a", 65)},
	}
	for family, validate := range families {
		t.Run(family, func(t *testing.T) {
			for name, identifiers := range invalid {
				t.Run(name, func(t *testing.T) {
					err := validate(identifiers)
					var domainErr *errs.DomainError
					require.ErrorAs(t, err, &domainErr)
					assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
				})
			}
			assert.NoError(t, validate([]string{"a"}))
			assert.NoError(t, validate([]string{strings.Repeat("f", 64)}))
		})
	}
}

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

func TestTypedTrafficPolicyInputsResolveDomainFactsSeparately(t *testing.T) {
	ctx := context.Background()
	networkRepo := testutil.NewNetworkRepo()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, networkRepo.Upsert(ctx, &model.NetworkItem{ID: "net-a", Name: "net-a", IsPresent: true}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-a", Name: "vm-a", NetworkID: "net-a"}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-b", Name: "vm-b", NetworkID: "net-a"}))

	networkInput := &inputs.NetworkToVMTrafficPolicyCreateInput{
		SourceNetwork: "net-a", DestinationVM: "vm-b", Protocol: "tcp", DestinationPort: "443-444",
	}
	sourceNetwork, err := networkInput.ResolveSourceNetwork(ctx, networkRepo)
	require.NoError(t, err)
	destinationVM, err := networkInput.ResolveDestinationVM(ctx, vmRepo)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"net-a", "vm-b"}, []string{sourceNetwork.ID, destinationVM.ID}); diff != "" {
		t.Errorf("network-to-VM domain facts mismatch (-want +got):\n%s", diff)
	}
	protocol, portStart, portEnd, err := networkInput.ResolvedTraffic()
	require.NoError(t, err)
	assert.Equal(t, model.TrafficProtocolTCP, protocol)
	assert.Equal(t, 443, portStart)
	assert.Equal(t, 444, portEnd)

	vmInput := &inputs.VMToVMTrafficPolicyCreateInput{
		SourceVM: "vm-a", DestinationVM: "vm-b", Protocol: "udp", DestinationPort: "53",
	}
	sourceVM, destinationVM, err := vmInput.ResolveVMs(ctx, vmRepo)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"vm-a", "vm-b"}, []string{sourceVM.ID, destinationVM.ID}); diff != "" {
		t.Errorf("VM-to-VM domain facts mismatch (-want +got):\n%s", diff)
	}
	protocol, portStart, portEnd, err = vmInput.ResolvedTraffic()
	require.NoError(t, err)
	assert.Equal(t, model.TrafficProtocolUDP, protocol)
	assert.Equal(t, 53, portStart)
	assert.Equal(t, 53, portEnd)
}

func TestExecPolicyInputIsDirectionalNonRootAndNetworkIndependent(t *testing.T) {
	ctx := t.Context()
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-a", Name: "vm-a", NetworkID: ""}))
	require.NoError(t, vmRepo.Upsert(ctx, &model.VMItem{ID: "vm-b", Name: "vm-b", NetworkID: "net-b"}))

	for name, input := range map[string]inputs.VMToVMExecPolicyCreateInput{
		"root":       {SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: "root"},
		"empty user": {SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: ""},
		"oversized user": {
			SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: "user_name_longer_than_thirty_two_chars",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := input.Validate()
			assert.Error(t, err)
		})
	}

	input := &inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "vm-a", DestinationVM: "vm-b", TargetUser: "app",
	}
	source, destination, err := input.ResolveVMs(ctx, vmRepo)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"vm-a", "vm-b"}, []string{source.ID, destination.ID}); diff != "" {
		t.Errorf("exec VM facts mismatch (-want +got):\n%s", diff)
	}
	_, _, err = (&inputs.VMToVMExecPolicyCreateInput{
		SourceVM: "vm-a", DestinationVM: "vm-a", TargetUser: "app",
	}).ResolveVMs(ctx, vmRepo)
	assert.Error(t, err)
}
