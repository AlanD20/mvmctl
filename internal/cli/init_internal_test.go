package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

func TestInitStatePreservesExplicitBinaryVersionAfterSuccessfulSudo(t *testing.T) {
	common.BeginOutput(nil)
	t.Cleanup(func() { common.BeginOutput(nil) })
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("y\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	originalStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = originalStdin
		require.NoError(t, reader.Close())
	})

	originalRunner := system.DefaultRunner
	system.DefaultRunner = &testutil.FakeRunner{}
	t.Cleanup(func() { system.DefaultRunner = originalRunner })

	statusCalls := 0
	state := &initState{
		initAPI: &testutil.MockInitAPI{
			InitCheckReadinessFunc: func(context.Context) *model.ProbeResult {
				return &model.ProbeResult{}
			},
		},
		hostAPI: &testutil.MockHostAPI{
			HostStatusCheckFunc: func(context.Context) (*results.HostStatusCheck, error) {
				statusCalls++
				if statusCalls == 1 {
					return &results.HostStatusCheck{}, nil
				}
				return &results.HostStatusCheck{
					GroupExists:   true,
					SudoersExists: true,
					UserInGroup:   true,
				}, nil
			},
		},
		downloadVersion: "1.15.2",
	}

	err = state.handleSudoRequired(context.Background(), &errs.NeedsInteraction{
		Code:    "privilege.sudo_required",
		Context: map[string]any{"command": "sudo mvm host init"},
	})
	require.NoError(t, err)
	assert.True(t, state.sudoCompleted)
	assert.Equal(t, "1.15.2", state.downloadVersion)
	assert.Equal(t, 2, statusCalls)
}

func TestInitBootstrapTreatsPrePolicyAsInformationalAndVerifiesAfter(t *testing.T) {
	policyErr := errs.New(errs.CodePrivilegeRequired, "operator policy observation is unavailable")
	tests := map[string]struct {
		preErr        error
		postErr       error
		member        bool
		postMissing   bool
		wantCompleted bool
		wantSkip      bool
		wantSudo      bool
		wantErr       error
	}{
		"new operator can bootstrap":                {preErr: policyErr, wantSudo: true, wantCompleted: true},
		"inactive enrolled session skips bootstrap": {preErr: policyErr, member: true, wantSkip: true},
		"cancellation aborts before bootstrap":      {preErr: context.Canceled, wantErr: context.Canceled},
		"post observation fails with fresh session needed": {
			preErr: policyErr, postErr: policyErr, wantSudo: true, wantErr: policyErr,
		},
		"post confirmed absence cannot succeed": {preErr: policyErr, wantSudo: true, postMissing: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			common.BeginOutput(nil)
			t.Cleanup(func() { common.BeginOutput(nil) })
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			_, err = writer.WriteString("y\n")
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			originalStdin := os.Stdin
			os.Stdin = reader
			t.Cleanup(func() {
				os.Stdin = originalStdin
				require.NoError(t, reader.Close())
			})
			members := "existing"
			if tc.member {
				members = "operator"
			}
			runner := &testutil.FakeRunner{StubRunResult: &system.RunResult{
				Stdout: "mvm:x:42:" + members + "\n",
			}}
			originalRunner := system.DefaultRunner
			system.DefaultRunner = runner
			t.Cleanup(func() { system.DefaultRunner = originalRunner })
			originalOS := system.DefaultOS
			system.DefaultOS = &testutil.FakeOS{
				GetgidVal: 1000, GetegidVal: 1000,
				LookupGroupFn: func(string) (*user.Group, error) { return &user.Group{Name: "mvm", Gid: "42"}, nil },
				CurrentFn:     func() (*user.User, error) { return &user.User{Username: "operator", Gid: "1000"}, nil },
			}
			t.Cleanup(func() { system.DefaultOS = originalOS })
			// Use the real public bootstrap producer: group/session evidence must not need the root observer.
			_, initErr := (&api.Operation{}).HostInit(t.Context(), nil)
			var interaction *errs.NeedsInteraction
			require.ErrorAs(t, initErr, &interaction)
			preflightCalls := len(runner.Calls)
			calls := 0
			state := &initState{
				initAPI: &testutil.MockInitAPI{
					InitCheckReadinessFunc: func(context.Context) *model.ProbeResult { return &model.ProbeResult{} },
				},
				hostAPI: &testutil.MockHostAPI{
					HostStatusCheckFunc: func(context.Context) (*results.HostStatusCheck, error) {
						calls++
						if calls == 1 {
							return nil, tc.preErr
						}
						return &results.HostStatusCheck{
							GroupExists: true, UserInGroup: true, SudoersExists: !tc.postMissing,
						}, tc.postErr
					},
				},
			}
			err = state.handleSudoRequired(t.Context(), interaction)
			assert.Equal(t, tc.wantCompleted, state.sudoCompleted)
			assert.Equal(t, tc.wantSkip, state.skipHost)
			if tc.wantSudo {
				assert.Greater(
					t,
					len(runner.Calls),
					preflightCalls,
					"explicitly confirmed bootstrap must remain reachable",
				)
			} else {
				assert.Equal(
					t,
					preflightCalls,
					len(runner.Calls),
					"cancellation or inactive session must avoid bootstrap",
				)
			}
			if !tc.wantCompleted {
				assert.Empty(t, state.hostSetupMsg)
			}
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			if tc.postMissing {
				require.Error(t, err)
				assert.Equal(t, errs.CodeHostInitFailed, errs.AsDomainError(err).Code)
				return
			}
			require.NoError(t, err)
			if tc.wantCompleted {
				assert.Equal(t, "Host configuration verified", state.hostSetupMsg)
				assert.Equal(t, 2, calls, "administrator setup requires a separate post observation")
			}
		})
	}
}

func TestInitNonInteractiveSudoOutcome(t *testing.T) {
	// Rationale: a failed administrator setup must stop before later init effects
	// and must never become an "all set" result through sudoCompleted.
	tests := map[string]struct {
		result    *system.RunResult
		runErr    error
		cancelled bool
		complete  bool
	}{
		"setup_exits_nonzero":  {result: &system.RunResult{ExitCode: 1}},
		"process_cannot_start": {runErr: errors.New("sudo could not start")},
		"cancelled_setup":      {runErr: context.Canceled, cancelled: true},
		"successful_setup":     {result: &system.RunResult{ExitCode: 0}, complete: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			originalRunner := system.DefaultRunner
			system.DefaultRunner = &testutil.FakeRunner{StubRunResult: tc.result, StubRunErr: tc.runErr}
			t.Cleanup(func() { system.DefaultRunner = originalRunner })
			common.BeginOutput(nil)
			t.Cleanup(func() { common.BeginOutput(nil) })

			output, err := os.CreateTemp(t.TempDir(), "init-output-")
			require.NoError(t, err)
			originalStdout, originalStderr := os.Stdout, os.Stderr
			os.Stdout, os.Stderr = output, output
			t.Cleanup(func() {
				os.Stdout, os.Stderr = originalStdout, originalStderr
				require.NoError(t, output.Close())
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			interaction := &errs.NeedsInteraction{Code: "privilege.sudo_required"}
			initAPI := &testutil.MockInitAPI{
				InitCheckReadinessFunc: func(context.Context) *model.ProbeResult {
					return &model.ProbeResult{}
				},
			}
			state := &initState{initAPI: initAPI, nonInteractive: true, downloadVersion: "1.16.2"}
			stateErr := state.handleSudoRequired(ctx, interaction)
			// Completion state remains defined after a handler failure.
			assert.Equal(t, tc.complete, state.sudoCompleted)
			assert.Equal(t, "1.16.2", state.downloadVersion)
			if tc.complete {
				require.NoError(t, stateErr)
			} else {
				require.Error(t, stateErr)
			}

			firstRun, continued := true, false
			initAPI.InitRunFullFunc = func(
				_ context.Context,
				_, _, _, sudoCompleted bool,
				_, downloadVersion string,
				_ *bool,
				_ event.OnProgressCallback,
			) *results.InitResult {
				if firstRun {
					firstRun = false
					return &results.InitResult{
						Steps: []results.InitStepResult{
							{Step: "host", Message: "administrator setup required"},
						},
						NeedsInteraction: interaction,
					}
				}
				continued = true
				assert.True(t, sudoCompleted, "only successful administrator setup can resume init")
				assert.Equal(t, "1.16.2", downloadVersion)
				return &results.InitResult{HostReady: true}
			}
			cmd := NewInitCmd(initAPI, &testutil.MockHostAPI{})
			cmd.SetArgs([]string{"--non-interactive", "--binary-version", "1.16.2"})
			err = cmd.ExecuteContext(ctx)
			os.Stdout, os.Stderr = originalStdout, originalStderr
			_, seekErr := output.Seek(0, io.SeekStart)
			require.NoError(t, seekErr)
			data, readErr := io.ReadAll(output)
			require.NoError(t, readErr)
			assert.Equal(t, tc.complete, continued, "failed setup must not advance init")
			if tc.cancelled {
				assert.ErrorIs(t, err, context.Canceled)
				assert.NotContains(t, string(data), "all set")
				return
			}
			if !tc.complete {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "setup incomplete")
				assert.Contains(t, string(data), "setup incomplete")
				assert.NotContains(t, string(data), "all set")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, string(data), "all set")
		})
	}
}

func TestInitInteractiveRequiresConfirmedInactiveSessionBeforeSkippingHost(t *testing.T) {
	// Rationale: missing session evidence must reach the administrator prompt,
	// so a user's decline cannot silently become a skipped host setup step.
	tests := map[string]struct {
		interactionContext map[string]any
		wantSkip           bool
	}{
		"absent_session_evidence": {},
		"malformed_session_evidence": {
			interactionContext: map[string]any{"session_has_group": "false"},
		},
		"active_session": {
			interactionContext: map[string]any{"session_has_group": true},
		},
		"confirmed_inactive_session": {
			interactionContext: map[string]any{"session_has_group": false},
			wantSkip:           true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			common.BeginOutput(nil)
			t.Cleanup(func() { common.BeginOutput(nil) })
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			_, err = writer.WriteString("n\n")
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			originalStdin := os.Stdin
			os.Stdin = reader
			t.Cleanup(func() {
				os.Stdin = originalStdin
				require.NoError(t, reader.Close())
			})
			runner := &testutil.FakeRunner{}
			originalRunner := system.DefaultRunner
			system.DefaultRunner = runner
			t.Cleanup(func() { system.DefaultRunner = originalRunner })

			state := &initState{
				initAPI: &testutil.MockInitAPI{
					InitCheckReadinessFunc: func(context.Context) *model.ProbeResult {
						return &model.ProbeResult{}
					},
				},
				hostAPI: &testutil.MockHostAPI{
					HostStatusCheckFunc: func(context.Context) (*results.HostStatusCheck, error) {
						return &results.HostStatusCheck{
							GroupExists: true, SudoersExists: true, UserInGroup: true,
						}, nil
					},
				},
			}
			err = state.handleSudoRequired(context.Background(), &errs.NeedsInteraction{
				Code: "privilege.sudo_required", Context: tc.interactionContext,
			})
			// Interaction state remains defined when the user declines setup.
			assert.Equal(t, tc.wantSkip, state.skipHost)
			assert.False(t, state.sudoCompleted)
			assert.Empty(t, runner.Calls, "declined or inactive-session setup must not execute sudo")
			if !tc.wantSkip {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "skipped by user")
				return
			}
			assert.NoError(t, err)
		})
	}
}
