package privileged

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func unavailableGuestControlAgent() error {
	return errs.New(errs.CodeAgentUnreachable, "agent not listening", errs.WithClass(errs.ClassRetryable))
}

// Rationale: a newly started VMM can precede its agent. Waiting must preserve
// exact admission on every attempt and stop immediately at terminal refusals.
func TestGuestControlWorkerInitialSessionWaitsForExplicitUnavailability(t *testing.T) {
	setup := jailer.GuestControlWorkerSetup{Principal: guestControlWorkerPrincipalFixture(), Generation: 7}
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "changed incarnation"}[changed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			calls := 0
			want := &jailer.GuestControlSession{}
			got, err := waitGuestControlWorkerInitialSession(
				ctx,
				setup,
				func(attempt context.Context, selected jailer.GuestControlWorkerSetup) (*jailer.GuestControlSession, error) {
					require.Equal(t, setup, selected)
					require.Same(t, ctx, attempt)
					calls++
					if calls == 1 {
						return nil, unavailableGuestControlAgent()
					}
					actual := selected.Principal
					if changed {
						actual.LaunchGeneration++
					}
					if err := requireGuestControlWorkerSessionPrincipal(selected.Principal, actual); err != nil {
						return nil, err
					}
					return want, nil
				},
			)
			if changed {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Same(t, want, got)
			}
			require.Equal(t, 2, calls)
		})
	}
}

func TestGuestControlWorkerInitialSessionRejectsTerminalFailures(t *testing.T) {
	for name, cause := range map[string]error{
		"authentication":            errs.New(errs.CodeVsockHandshakeFailed, "pin refused"),
		"authority":                 errs.New(errs.CodeVMStateInvalid, "VM replaced"),
		"malformed acknowledgement": errs.New(errs.CodeValidationFailed, "invalid acknowledgement"),
		"unclassified EOF":          io.EOF,
		"wrong class":               errs.New(errs.CodeAgentUnreachable, "unavailable"),
		"wrong code":                errs.New(errs.CodeInternal, "unavailable", errs.WithClass(errs.ClassRetryable)),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			got, err := waitGuestControlWorkerInitialSession(t.Context(), jailer.GuestControlWorkerSetup{},
				func(context.Context, jailer.GuestControlWorkerSetup) (*jailer.GuestControlSession, error) {
					calls++
					return nil, cause
				})
			require.ErrorIs(t, err, cause)
			require.Nil(t, got)
			require.Equal(t, 1, calls)
		})
	}
}

func TestGuestControlWorkerInitialSessionHonorsCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
			}
			defer cancel()
			calls := 0
			got, err := waitGuestControlWorkerInitialSession(ctx, jailer.GuestControlWorkerSetup{},
				func(context.Context, jailer.GuestControlWorkerSetup) (*jailer.GuestControlSession, error) {
					calls++
					if !deadline {
						cancel()
					}
					return nil, unavailableGuestControlAgent()
				})
			require.Nil(t, got)
			require.Equal(t, 1, calls)
			require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
		})
	}
}
