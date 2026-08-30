package system

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const realRunnerRunHelperTest = "^TestRealRunnerRunHelper$"

func TestRealRunnerRunReplacesEnvironmentWithClosedSet(t *testing.T) {
	// Rationale: The privileged filesystem probe depends on ReplaceEnv preventing inherited process state from changing
	// fixed-tool behavior, so exercise the actual exec boundary rather than only the environment-building helper.
	t.Setenv("MVM_RUNNER_INHERITED_SENTINEL", "must-not-leak")

	runner := &RealRunner{}
	result, err := runner.Run(t.Context(), realRunnerRunHelperCommand("environment"), RunCmdOpts{
		Capture: true,
		Env: map[string]string{
			"MVM_RUNNER_SUPPLIED_ALPHA": "one",
			"MVM_RUNNER_SUPPLIED_BETA":  "two",
		},
		ReplaceEnv: true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(
		t,
		"MVM_RUNNER_SUPPLIED_ALPHA=one\nMVM_RUNNER_SUPPLIED_BETA=two",
		result.Stdout,
	)
	assert.NotContains(t, result.Stdout, "MVM_RUNNER_INHERITED_SENTINEL")
	assert.Empty(t, result.Stderr)
}

func TestRealRunnerRunAcceptsExactStdoutAndStderrLimits(t *testing.T) {
	t.Parallel()

	runner := &RealRunner{}
	result, err := runner.Run(
		t.Context(),
		realRunnerRunHelperCommand("output", "four", "error"),
		RunCmdOpts{Capture: true, MaxStdoutBytes: 4, MaxStderrBytes: 5},
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "four", result.Stdout)
	assert.Equal(t, "error", result.Stderr)
}

func TestRealRunnerRunRejectsOneByteCaptureOverflow(t *testing.T) {
	// Rationale: One excess byte must become a bounded process error after execution without retaining attacker-controlled
	// output beyond the configured limit; helper-only writer tests do not prove the os/exec boundary behaves correctly.
	t.Parallel()

	tests := []struct {
		name       string
		stdout     string
		stderr     string
		opts       RunCmdOpts
		wantStdout string
		wantStderr string
		wantError  string
	}{
		{
			name:       "stdout",
			stdout:     "fives",
			opts:       RunCmdOpts{Capture: true, MaxStdoutBytes: 4},
			wantStdout: "five",
			wantError:  "stdout exceeded capture limit",
		},
		{
			name:       "stderr",
			stderr:     "errors",
			opts:       RunCmdOpts{Capture: true, MaxStderrBytes: 5},
			wantStderr: "error",
			wantError:  "stderr exceeded capture limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := (&RealRunner{}).Run(
				t.Context(),
				realRunnerRunHelperCommand("output", test.stdout, test.stderr),
				test.opts,
			)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.ErrorContains(t, err, test.wantError)
			require.NotNil(t, result)
			assert.Equal(t, -1, result.ExitCode)
			assert.Equal(t, test.wantStdout, result.Stdout)
			assert.Equal(t, test.wantStderr, result.Stderr)
		})
	}
}

func TestRealRunnerRunDrainsCaptureFarBeyondPipeBuffer(t *testing.T) {
	// Rationale: Returning a short write at the capture limit stops os/exec's pipe copier and can deadlock a child whose
	// remaining output exceeds the pipe buffer, so prove both streams are fully drained while only the bounded prefix stays.
	t.Parallel()

	const (
		payloadBytes = 1 << 20
		captureBytes = 4 << 10
	)
	tests := []struct {
		name       string
		stream     string
		content    string
		opts       RunCmdOpts
		wantStdout string
		wantStderr string
		wantError  string
	}{
		{
			name:       "stdout",
			stream:     "stdout",
			content:    "o",
			opts:       RunCmdOpts{Capture: true, MaxStdoutBytes: captureBytes},
			wantStdout: strings.Repeat("o", captureBytes),
			wantError:  "stdout exceeded capture limit",
		},
		{
			name:       "stderr",
			stream:     "stderr",
			content:    "e",
			opts:       RunCmdOpts{Capture: true, MaxStderrBytes: captureBytes},
			wantStderr: strings.Repeat("e", captureBytes),
			wantError:  "stderr exceeded capture limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			result, err := (&RealRunner{}).Run(
				ctx,
				realRunnerRunHelperCommand(
					"repeated-output",
					test.stream,
					test.content,
					strconv.Itoa(payloadBytes),
				),
				test.opts,
			)
			breakerErr := ctx.Err()
			cancel()

			assert.NoError(t, breakerErr, "runner must finish before the circuit breaker")
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.ErrorContains(t, err, test.wantError)
			require.NotNil(t, result)
			assert.Equal(t, -1, result.ExitCode)
			assert.Equal(t, test.wantStdout, result.Stdout)
			assert.Equal(t, test.wantStderr, result.Stderr)
		})
	}
}

func TestRealRunnerRunRejectsInvalidCaptureLimitsBeforeStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts RunCmdOpts
		want string
	}{
		{
			name: "negative stdout limit",
			opts: RunCmdOpts{Capture: true, MaxStdoutBytes: -1},
			want: "cannot be negative",
		},
		{
			name: "negative stderr limit",
			opts: RunCmdOpts{Capture: true, MaxStderrBytes: -1},
			want: "cannot be negative",
		},
		{
			name: "stdout limit without capture",
			opts: RunCmdOpts{MaxStdoutBytes: 1},
			want: "require captured output",
		},
		{
			name: "stderr limit without capture",
			opts: RunCmdOpts{MaxStderrBytes: 1},
			want: "require captured output",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := (&RealRunner{}).Run(
				t.Context(),
				realRunnerRunHelperCommand("unexpected start"),
				test.opts,
			)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.ErrorContains(t, err, test.want)
			require.NotNil(t, result)
			assert.Equal(t, 1, result.ExitCode)
		})
	}
}

func TestRealRunnerRunPreservesTimeoutClassification(t *testing.T) {
	// Rationale: Callers must distinguish a compiled subprocess deadline from other process failures through the error
	// chain. The rendered message is diagnostic and may change without weakening recovery decisions.
	t.Parallel()

	result, err := (&RealRunner{}).Run(
		t.Context(),
		realRunnerRunHelperCommand("block"),
		RunCmdOpts{Capture: true, Timeout: 50 * time.Millisecond},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	require.NotNil(t, result)
	assert.Equal(t, -1, result.ExitCode)
}

func TestRealRunnerRunHelper(t *testing.T) {
	action, arguments, ok := realRunnerRunHelperArguments(os.Args)
	if !ok {
		return
	}

	// Helper cases exit directly so the test harness cannot append PASS to bounded stdout.
	switch action {
	case "environment":
		environment := os.Environ()
		slices.Sort(environment)
		if _, err := os.Stdout.WriteString(strings.Join(environment, "\n")); err != nil {
			os.Exit(91)
		}
	case "output":
		if len(arguments) != 2 {
			os.Exit(92)
		}
		if _, err := os.Stdout.WriteString(arguments[0]); err != nil {
			os.Exit(93)
		}
		if _, err := os.Stderr.WriteString(arguments[1]); err != nil {
			os.Exit(94)
		}
	case "repeated-output":
		if len(arguments) != 3 || len(arguments[1]) != 1 {
			os.Exit(96)
		}
		byteCount, err := strconv.Atoi(arguments[2])
		if err != nil || byteCount < 0 {
			os.Exit(97)
		}
		output := os.Stdout
		if arguments[0] == "stderr" {
			output = os.Stderr
		} else if arguments[0] != "stdout" {
			os.Exit(98)
		}
		payload := strings.Repeat(arguments[1], byteCount)
		written, err := output.WriteString(payload)
		if err != nil || written != len(payload) {
			os.Exit(99)
		}
	case "block":
		// Bound a timeout-regression failure independently from `go test`'s much larger package timeout.
		time.Sleep(2 * time.Second)
	default:
		os.Exit(95)
	}
	os.Exit(0)
}

func realRunnerRunHelperCommand(action string, arguments ...string) []string {
	command := []string{os.Args[0], "-test.run=" + realRunnerRunHelperTest, "--", action}
	return append(command, arguments...)
}

func realRunnerRunHelperArguments(arguments []string) (string, []string, bool) {
	separator := slices.Index(arguments, "--")
	if separator < 0 || separator+1 >= len(arguments) {
		return "", nil, false
	}
	return arguments[separator+1], arguments[separator+2:], true
}

func TestCommandEnvironmentReplacesInheritedValuesWithClosedSet(t *testing.T) {
	t.Parallel()

	environment := commandEnvironment(RunCmdOpts{
		Env:        map[string]string{"LC_ALL": "C", "LANG": "C"},
		ReplaceEnv: true,
	})
	want := []string{"LANG=C", "LC_ALL=C"}
	if diff := cmp.Diff(want, environment); diff != "" {
		t.Errorf("closed command environment mismatch (-want +got):\n%s", diff)
	}
}

func TestBoundedCaptureWriterDrainsOverflowWithoutRetainingExcess(t *testing.T) {
	t.Parallel()

	writer := newBoundedCaptureWriter(4)
	written, err := writer.Write([]byte("abcdef"))
	require.NoError(t, err)
	assert.Equal(t, 6, written)
	assert.Equal(t, "abcd", writer.String())
	assert.True(t, writer.Exceeded())

	written, err = writer.Write([]byte("z"))
	require.NoError(t, err)
	assert.Equal(t, 1, written)
	assert.Equal(t, "abcd", writer.String())
}

func TestCommandOutputLimitErrorTakesPriorityOverProcessFailure(t *testing.T) {
	t.Parallel()

	stdout := newBoundedCaptureWriter(4)
	_, writeErr := stdout.Write([]byte("oversized"))
	require.NoError(t, writeErr)
	stderr := newBoundedCaptureWriter(8)

	result, err := capturedRunResult(stdout, stderr, errors.New("process stopped"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "stdout exceeded")
	assert.Equal(t, -1, result.ExitCode)
	assert.Equal(t, "over", result.Stdout)
	assert.Empty(t, result.Stderr)
}
