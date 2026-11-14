package env_test

import (
	"context"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/internal/testutil"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

func TestReadyRetriesStartupHandshakeButNotAdmittedCommandOrAuthorizationFailure(t *testing.T) {
	// Rationale: a freshly created VM can close CONNECT before the agent listens.
	// The workflow must reach a second successful check, but never retry a rejected
	// identity or an admitted command whose result was lost. Exercise Apply, not its classifier.
	for _, tc := range []struct {
		name    string
		failure error
		retry   bool
	}{
		{"CONNECT EOF", errs.WrapMsg(errs.CodeVsockHandshakeFailed, "failed to read CONNECT response", io.EOF), true},
		{"CONNECT reset", errs.Wrap(errs.CodeVsockHandshakeFailed, &net.OpError{Op: "read", Net: "unix", Err: syscall.ECONNRESET}), true},
		{"CONNECT broken pipe", errs.Wrap(errs.CodeVsockHandshakeFailed, syscall.EPIPE), true},
		{"admitted command EOF", errs.Wrap(errs.CodeVsockExecFailed, io.EOF), false},
		{"malformed CONNECT", errs.New(errs.CodeVsockHandshakeFailed, "invalid acknowledgment"), false},
		{"authorization overrides transport", errs.Wrap(errs.CodeVsockHandshakeFailed, errs.Wrap(errs.CodeUnauthorized, syscall.ECONNRESET)), false},
		{"identity overrides transport", errs.Wrap(errs.CodeVsockHandshakeFailed, errs.Wrap(errs.CodeVMNotFound, io.EOF, errs.WithClass(errs.ClassValidation))), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			op := &testutil.MockOperation{}
			op.ExecReadyFunc = func(ctx context.Context, _ inputs.ReadyInput) (*results.ExecResult, error) {
				_, bounded := ctx.Deadline()
				assert.True(t, bounded, "each attempt retains the readiness total deadline")
				attempts++
				if attempts == 1 {
					return nil, tc.failure
				}
				return &results.ExecResult{ExitCode: 0}, nil
			}
			step, err := envpkg.Registry["ready"].FromSpec("ready", "health", model.ResourceMap{
				"target": "abcdef0123456789abcdef0123456789", "cmd": "true",
			}, op)
			require.NoError(t, err)
			writes := 0
			err = step.Apply(t.Context(), workflow.NewSharedState(), model.ResourceState{},
				func(context.Context, model.ResourceState) error { writes++; return nil }, nil)
			if tc.retry {
				require.NoError(t, err)
				assert.Equal(t, 2, attempts)
				assert.Equal(t, 1, writes)
			} else {
				require.ErrorIs(t, err, tc.failure)
				assert.Equal(t, 1, attempts)
				assert.Zero(t, writes)
			}
		})
	}
}
