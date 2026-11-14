package hostpolicy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

type fakePolicyExchange struct {
	outcome policyExchangeOutcome
	called  bool
}

func (fake *fakePolicyExchange) Exchange(context.Context, []byte) policyExchangeOutcome {
	fake.called = true
	return fake.outcome
}

// EACCES in ordinary host status must select root observation, not confirmed absence.
func TestClientObserveRequiresAuthoritativeResult(t *testing.T) {
	exists := true
	var success, absent, omitted, remote bytes.Buffer
	require.NoError(t, privilegedwire.EncodeSuccess(t.Context(), &success, ObserveAction,
		ObserveResult{Exists: &exists}))
	missing := false
	require.NoError(t, privilegedwire.EncodeSuccess(t.Context(), &absent, ObserveAction,
		ObserveResult{Exists: &missing}))
	require.NoError(t, privilegedwire.EncodeSuccess(t.Context(), &omitted, ObserveAction, ObserveResult{}))
	remoteErr := errs.New(errs.CodePrivilegeRequired, "host admission is disabled",
		errs.WithClass(errs.ClassNeedsInteraction), errs.WithEntity("host"))
	require.NoError(t, privilegedwire.EncodeError(t.Context(), &remote, ObserveAction, remoteErr))
	tests := map[string]struct {
		outcome    policyExchangeOutcome
		wantErr    string
		wantExists bool
		remote     bool
	}{
		"missing EOF": {outcome: policyExchangeOutcome{started: true, response: success.Bytes()}, wantErr: "EOF"},
		"missing result": {outcome: policyExchangeOutcome{started: true, responseEOF: true,
			response: omitted.Bytes()}, wantErr: "observation is missing"},
		"bad frame": {outcome: policyExchangeOutcome{started: true, responseEOF: true,
			response: []byte("invalid")}, wantErr: "response is invalid"},
		"remote admission error": {outcome: policyExchangeOutcome{started: true, responseEOF: true,
			response: remote.Bytes()}, wantErr: "admission is disabled", remote: true},
		"permission denied but root present": {outcome: policyExchangeOutcome{started: true, responseEOF: true,
			response: success.Bytes()}, wantExists: true},
		"permission denied but root absent": {outcome: policyExchangeOutcome{started: true, responseEOF: true,
			response: absent.Bytes()}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			exchange := &fakePolicyExchange{outcome: tc.outcome}
			client := &Client{exchange: exchange, lstat: func(string) (os.FileInfo, error) {
				return nil, os.ErrPermission
			}}
			result, err := client.Observe(t.Context())
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				if tc.remote {
					domain := errs.AsDomainError(err)
					require.NotNil(t, domain)
					assert.Equal(t, remoteErr.Code, domain.Code)
					assert.Equal(t, remoteErr.Class, domain.Class)
					assert.Equal(t, remoteErr.Entity, domain.Entity)
				}
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(ObserveResult{Exists: &tc.wantExists}, result); diff != "" {
				t.Errorf("policy observation (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClientObserveOrdinaryAbsenceAndCancellationAvoidExchange(t *testing.T) {
	rootInfo, err := os.Lstat("/")
	require.NoError(t, err)
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed absence", true: "cancelled"}[cancelled], func(t *testing.T) {
			exchange := &fakePolicyExchange{}
			client := &Client{exchange: exchange, lstat: func(path string) (os.FileInfo, error) {
				if path == infra.SudoersDropInPath() {
					return nil, os.ErrNotExist
				}
				return rootInfo, nil
			}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				cancel()
			}
			result, err := client.Observe(ctx)
			assert.False(t, exchange.called, "ordinary absence and cancellation must avoid privileged work")
			if cancelled {
				assert.ErrorIs(t, err, context.Canceled)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result.Exists)
			assert.False(t, *result.Exists)
		})
	}
}

func TestClientObserveDoesNotTreatUnsafeAncestorAsAbsence(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0777))
	unsafe, err := os.Lstat(dir)
	require.NoError(t, err)
	exchange := &fakePolicyExchange{outcome: policyExchangeOutcome{diagnostic: errors.New("root unavailable")}}
	client := &Client{exchange: exchange, lstat: func(path string) (os.FileInfo, error) {
		if path == infra.SudoersDropInPath() {
			return nil, os.ErrNotExist
		}
		return unsafe, nil
	}}
	_, err = client.Observe(t.Context())
	assert.ErrorContains(t, err, "authoritative EOF")
}
