package common_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
)

func TestRunHostInitWithSudoUsesClosedAdministratorRoute(t *testing.T) {
	// Rationale: ADR-0016 permits administrator init only through the fixed
	// root-owned executable, without an env wrapper or caller-selected arguments.
	t.Setenv("MVM_CACHE_DIR", "/caller-controlled/cache")
	t.Setenv("MVM_ESCALATED", "1")
	t.Setenv("HOME", "/caller-controlled/home")
	t.Setenv("PATH", "/caller-controlled/bin")
	runner := &testutil.FakeRunner{}
	originalRunner := system.DefaultRunner
	system.DefaultRunner = runner
	t.Cleanup(func() { system.DefaultRunner = originalRunner })

	got := common.RunHostInitWithSudo(context.Background())
	want := common.SudoResult{Success: true, ReturnCode: 0}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("administrator setup outcome (-want +got):\n%s", diff)
	}
	// The emitted command is the privilege boundary's contract, not mock wiring.
	require.Len(t, runner.Calls, 1)
	wantCall := testutil.FakeCall{
		Args: []string{"/usr/local/bin/mvm", "host", "init"},
		Opts: system.RunCmdOpts{Privileged: true, Interactive: true},
	}
	if diff := cmp.Diff(wantCall, runner.Calls[0]); diff != "" {
		t.Errorf("administrator invocation (-want +got):\n%s", diff)
	}
}
