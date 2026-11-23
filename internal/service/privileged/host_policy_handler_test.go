package privileged

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/hostpolicy"
	"mvmctl/pkg/errs"
)

func TestHostPolicyHandlerRequiresClosedRequestAndPreservesObservation(t *testing.T) {
	tests := map[string]struct {
		body        any
		payload     []byte
		trailing    bool
		observeErr  error
		wantError   bool
		wantObserve bool
	}{
		"unknown path":  {body: map[string]string{"path": "/tmp/policy"}, wantError: true},
		"payload":       {body: hostpolicy.ObserveRequest{}, payload: []byte("x"), wantError: true},
		"trailing data": {body: hostpolicy.ObserveRequest{}, trailing: true, wantError: true},
		"read failure": {body: hostpolicy.ObserveRequest{}, wantObserve: true, wantError: true,
			observeErr: errs.New(errs.CodePrivilegeSudoers, "policy read failed", errs.WithClass(errs.ClassInternal))},
		"present": {body: hostpolicy.ObserveRequest{}, wantObserve: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			frame := encodeTrustedReleaseRequest(t, hostpolicy.ObserveAction, tc.body, tc.payload)
			if tc.trailing {
				frame = append(frame, 'x')
			}
			endpoint.input = *bytes.NewReader(frame)
			observed := false
			exists := true
			observe := func(context.Context) (hostpolicy.ObserveResult, error) {
				observed = true
				return hostpolicy.ObserveResult{Exists: &exists}, tc.observeErr
			}
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = trustedReleaseControlDeps(t, endpoint)
			runtime.hostPolicy = observe
			err := run(t.Context(), []string{Marker, hostpolicy.ObserveAction}, endpoint, runtime)
			require.NoError(t, err)
			assert.Equal(t, tc.wantObserve, observed, "rejected request must not reach policy observation")
			response, err := privilegedwire.DecodeResponse[hostpolicy.ObserveResult](t.Context(),
				bytes.NewReader(endpoint.output.Bytes()), hostpolicy.ObserveAction)
			require.NoError(t, err)
			if tc.wantError {
				require.NotNil(t, response.OperationError())
				if tc.observeErr != nil {
					assert.Equal(t, errs.CodePrivilegeSudoers, response.OperationError().Code)
					assert.Equal(t, errs.ClassInternal, response.OperationError().Class)
				}
				return
			}
			require.Nil(t, response.OperationError())
			if diff := cmp.Diff(hostpolicy.ObserveResult{Exists: &exists}, response.Result()); diff != "" {
				t.Errorf("policy result (-want +got):\n%s", diff)
			}
		})
	}
}
