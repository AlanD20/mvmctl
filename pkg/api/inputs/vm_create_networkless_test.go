package inputs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Rationale: a networkless selection must reject managed endpoint and NoCloud NET inputs
// before the create operation looks up resources or allocates a network lease.
func TestVMCreateInputValidatesNetworklessConflicts(t *testing.T) {
	value := func(s string) *string { return &s }
	tests := map[string]struct {
		change  func(*VMCreateInput)
		wantErr bool
	}{
		"managed default": {},
		"networkless": {
			change: func(input *VMCreateInput) { input.NoNetwork = true },
		},
		"network selector": {
			change:  func(input *VMCreateInput) { input.NoNetwork, input.NetworkID = true, value("net-a") },
			wantErr: true,
		},
		"guest IP": {
			change:  func(input *VMCreateInput) { input.NoNetwork, input.RequestedGuestIP = true, value("192.0.2.10") },
			wantErr: true,
		},
		"guest MAC": {
			change: func(input *VMCreateInput) {
				input.NoNetwork, input.RequestedGuestMAC = true, value("06:00:00:00:00:01")
			},
			wantErr: true,
		},
		"NoCloud NET": {
			change: func(input *VMCreateInput) {
				input.NoNetwork, input.CloudInitMode = true, value(string(model.CloudInitModeNET))
			},
			wantErr: true,
		},
		"IP boot argument": {
			change:  func(input *VMCreateInput) { input.NoNetwork, input.BootArgs = true, "console=ttyS0 ip=dhcp" },
			wantErr: true,
		},
		"NoCloud boot argument": {
			change:  func(input *VMCreateInput) { input.NoNetwork, input.BootArgs = true, "ds=nocloud-net" },
			wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			input := VMCreateInput{Name: "offline-qa"}
			if tc.change != nil {
				tc.change(&input)
			}
			err := input.Validate()
			if tc.wantErr {
				require.Error(t, err)
				domain := errs.AsDomainError(err)
				require.NotNil(t, domain)
				assert.Equal(t, errs.CodeValidationFailed, domain.Code)
				return
			}
			assert.NoError(t, err)
		})
	}
}
