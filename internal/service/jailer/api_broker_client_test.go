package jailer

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

func TestVMAPIBrokerResumeRequiresAuthoritativeProofForPartialSuccess(t *testing.T) {
	request := VMAPIRequest{VMID: testVMID}
	for name, tc := range map[string]struct {
		code     errs.Code
		entity   string
		state    string
		eof      bool
		applied  bool
		wantCode errs.Code
	}{
		"confirmed with worker failure": {
			code: errs.CodeVMResumeGuestControlFailed, entity: testVMID, state: "running", eof: true, applied: true,
			wantCode: errs.CodeVMResumeGuestControlFailed,
		},
		"worker failure for another VM": {
			code: errs.CodeVMResumeGuestControlFailed, entity: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			state: "running", eof: true, wantCode: errs.CodeVMResumeGuestControlFailed,
		},
		"unconfirmed transition": {
			code: errs.CodeVMResumeGuestControlFailed, entity: testVMID, state: "unknown", eof: true,
			wantCode: errs.CodeVMResumeGuestControlFailed,
		},
		"ordinary root refusal": {
			code: errs.CodeVMStateInvalid, entity: testVMID, state: "running", eof: true, wantCode: errs.CodeVMStateInvalid,
		},
		"missing transport EOF": {
			code: errs.CodeVMResumeGuestControlFailed, entity: testVMID, state: "running", wantCode: errs.CodeProcessError,
		},
	} {
		t.Run(name, func(t *testing.T) {
			failure := errs.New(tc.code, "VM operation failed", errs.WithEntity(tc.entity),
				errs.WithDetails(map[string]any{"vm_state": tc.state}))
			var encoded bytes.Buffer
			require.NoError(t, privilegedwire.EncodeError(t.Context(), &encoded, VMResumeAction, failure))
			client := &VMAPIBrokerClient{transport: newTrustedReleaseClient(&fakeTrustedReleaseExchange{
				outcome: trustedReleaseExchangeOutcome{response: encoded.Bytes(), started: true, responseEOF: tc.eof},
			})}

			result, err := client.Resume(t.Context(), request)
			require.Error(t, err)
			domain := errs.AsDomainError(err)
			require.NotNil(t, domain)
			assert.Equal(t, tc.wantCode, domain.Code)
			if tc.applied {
				assert.NoError(t, result.Validate(request))
			} else {
				assert.Equal(t, VMAPIResult{}, result)
			}
		})
	}
}
