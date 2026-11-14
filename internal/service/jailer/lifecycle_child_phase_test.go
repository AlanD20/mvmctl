package jailer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: Protected preparation happens after challenge acknowledgement. Ending that
// phase's context must not close the release wait or cancel newly admitted setup I/O.
func TestBlockedLaunchChildSetupAfterAuthenticationEnds(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"before release", "after release"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			authenticationCtx, endAuthentication := context.WithCancelCause(ctx)
			defer endAuthentication(nil)
			deps := blockedLaunchGateDeps{
				readFull: func(ctx context.Context, _ int, value []byte) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if len(value) == 1 {
						value[0] = blockedLaunchReleaseByte
						if phase == "after release" {
							endAuthentication(context.DeadlineExceeded)
						}
					}
					return nil
				},
				writeFull: func(ctx context.Context, _ int, _ []byte) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					// End the real context deterministically instead of sleeping for its timer.
					if phase == "before release" {
						endAuthentication(context.DeadlineExceeded)
					}
					return nil
				},
			}
			setupCtx, cancelSetup, err := awaitLaunchChildSetup(ctx, authenticationCtx, deps, -1)
			if !assert.NoError(t, err, "authenticated preparation may outlive authentication") {
				return
			}
			defer cancelSetup()
			require.ErrorIs(t, context.Cause(authenticationCtx), context.DeadlineExceeded)

			local, peer := newBlockedLaunchGateSocketPair(t)
			defer func() {
				require.NoError(t, unix.Close(peer))
				require.NoError(t, unix.Close(local))
			}()
			require.NoError(t, writeBlockedLaunchGateFull(t.Context(), peer, []byte{0x42}))
			value := []byte{0}
			if assert.NoError(t, readBlockedLaunchGateFull(setupCtx, local, value), "fresh setup I/O") {
				assert.Equal(t, byte(0x42), value[0])
			}
			cancelParent()
			assert.ErrorIs(t, readBlockedLaunchGateFull(setupCtx, local, value), context.Canceled)
		})
	}
}

// Rationale: Removing the preparation timer must not admit a child after caller cancellation,
// parent death, an invalid release byte, or an incomplete authenticated challenge.
func TestBlockedLaunchChildSetupRejectsInterruptedAdmission(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cancelAuthentication bool
		cancelParent         bool
		releaseErr           error
		releaseByte          byte
		wantCause            error
		wantMessage          string
	}{
		"authentication ended":                {cancelAuthentication: true, wantCause: context.Canceled},
		"caller cancelled during preparation": {cancelParent: true, wantCause: context.Canceled},
		"parent EOF": {
			releaseErr:  unix.EPIPE,
			wantMessage: "root launch parent closed the release gate",
		},
		"invalid release": {releaseByte: 0, wantMessage: "blocked launch release byte is invalid"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			authenticationCtx, cancelAuthentication := context.WithCancel(ctx)
			defer cancelAuthentication()
			if test.cancelAuthentication {
				cancelAuthentication()
			}
			deps := blockedLaunchGateDeps{
				readFull: func(ctx context.Context, _ int, value []byte) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if len(value) == 1 {
						value[0] = test.releaseByte
						return test.releaseErr
					}
					return nil
				},
				writeFull: func(ctx context.Context, _ int, _ []byte) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if test.cancelParent {
						cancelParent()
					}
					return nil
				},
			}
			_, _, err := awaitLaunchChildSetup(ctx, authenticationCtx, deps, -1)
			if assert.Error(t, err) {
				var domainErr *errs.DomainError
				if assert.ErrorAs(t, err, &domainErr) {
					assert.Equal(t, errs.CodeProcessError, domainErr.Code)
				}
				if test.wantCause != nil {
					assert.ErrorIs(t, err, test.wantCause)
				} else {
					assert.Contains(t, err.Error(), test.wantMessage)
				}
			}
		})
	}
}
