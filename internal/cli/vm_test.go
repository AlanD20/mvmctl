package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// ─── NewVMCmd ──────────────────────────────────────────────────────────────
// Rationale: Missing subcommands silently disable VM operations without error.
// NewVMCmd is the entry point for all VM CLI operations. If a subcommand is
// not registered, the user gets a "unknown command" error with no indication
// that the feature was intentionally removed. This test prevents regressions
// where a subcommand is accidentally omitted during refactoring.

func TestNewVMCmd(t *testing.T) {
	mock := &testutil.MockVMAPI{}
	cmd := cli.NewVMCmd(mock, nil)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "ls", hasAlias: true, alias: "list"},
		{use: "ps", hasAlias: false},
		{use: "create", hasAlias: false},
		{use: "rm", hasAlias: true, alias: "remove"},
		{use: "start", hasAlias: false},
		{use: "stop", hasAlias: false},
		{use: "reboot", hasAlias: false},
		{use: "pause", hasAlias: false},
		{use: "resume", hasAlias: false},
		{use: "snapshot", hasAlias: false},
		{use: "load", hasAlias: false},
		{use: "inspect", hasAlias: false},
		{use: "attach-volume", hasAlias: false},
		{use: "detach-volume", hasAlias: false},
	}

	assert.Equal(t, "vm", cmd.Use, "root command must be 'vm'")
	assert.Equal(t, "VM lifecycle management", cmd.Short)

	for _, sc := range expectedSubcommands {
		t.Run("has_subcommand_"+sc.use, func(t *testing.T) {
			sub, _, err := cmd.Find([]string{sc.use})
			require.NoError(t, err, "subcommand %q not found", sc.use)
			require.NotNil(t, sub, "subcommand %q is nil", sc.use)

			if sc.hasAlias {
				aliasCmd, _, aliasErr := cmd.Find([]string{sc.alias})
				require.NoError(t, aliasErr, "alias %q not found for %q", sc.alias, sc.use)
				require.NotNil(t, aliasCmd, "alias %q is nil for %q", sc.alias, sc.use)
			}
		})
	}

	t.Run("no_extra_subcommands", func(t *testing.T) {
		expected := make(map[string]bool)
		for _, sc := range expectedSubcommands {
			expected[sc.use] = true
			if sc.hasAlias {
				expected[sc.alias] = true
			}
		}
		for _, sub := range cmd.Commands() {
			assert.True(t, expected[sub.Name()], "unexpected subcommand: %s", sub.Name())
		}
	})
}

// ─── vm ls (via vm ls) ─────────────────────────────────────────────────────
// Rationale: VM listing is the primary user-facing output for VM operations.
// A broken list command makes the CLI unusable — users cannot see their VMs
// and cannot target them for lifecycle operations.

func TestRunVMList(t *testing.T) {
	t.Run("empty_list_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				return nil
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("single_vm_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				return []*model.VM{{
					ID: "vm-1", Name: "test-vm",
					Status: model.VMStatusRunning,
				}}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("multiple_vms_returns_no_error", func(t *testing.T) {
		vms := []*model.VM{
			{ID: "vm-1", Name: "vm-one", Status: model.VMStatusRunning},
			{ID: "vm-2", Name: "vm-two", Status: model.VMStatusStopped},
		}
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				return vms
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_with_vms_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				return []*model.VM{
					{ID: "vm-1", Name: "vm-one", Status: model.VMStatusRunning},
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_with_nil_vms_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				return nil
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("long_output_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				return nil
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("long", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("context_is_propagated_to_vmapi", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelled := false
		mock := &testutil.MockVMAPI{
			VMListFunc: func(ctx context.Context, statuses ...string) []*model.VM {
				if ctx.Err() != nil {
					cancelled = true
				}
				return nil
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err, "vm ls should not error on cancelled context — list is read-only")
		assert.True(t, cancelled, "mock VMListFunc should have received the cancelled context")
	})
}

// ─── vm start (via vm start) ───────────────────────────────────────────────
// Rationale: Start is the most common VM lifecycle operation. A broken start
// leaves users with stopped VMs and no way to recover without manual
// intervention. Both success and VM-not-found paths must work.

func TestRunVMStart(t *testing.T) {
	t.Run("success_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMStartFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return &errs.BatchResult{
					Items: []errs.OperationResult{
						{Status: "success", Code: "vm.started", Message: "VM 'vm-1' started"},
					},
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		startCmd, _, _ := cmd.Find([]string{"start"})
		err := startCmd.RunE(startCmd, []string{"vm-1"})
		assert.NoError(t, err)
	})

	t.Run("vm_not_found_returns_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMStartFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return &errs.BatchResult{
					Items: []errs.OperationResult{
						{Status: "error", Code: "vm.start_failed", Message: "VM not found: vm-999"},
					},
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		startCmd, _, _ := cmd.Find([]string{"start"})
		err := startCmd.RunE(startCmd, []string{"vm-999"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "one or more starts failed")
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockVMAPI{
			VMStartFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				select {
				case <-ctx.Done():
					return &errs.BatchResult{
						Items: []errs.OperationResult{
							{Status: "error", Code: "vm.start_failed", Message: "operation cancelled"},
						},
					}
				default:
					return &errs.BatchResult{
						Items: []errs.OperationResult{
							{Status: "success", Code: "vm.started"},
						},
					}
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		startCmd, _, _ := cmd.Find([]string{"start"})
		startCmd.SetContext(ctx)
		err := startCmd.RunE(startCmd, []string{"vm-1"})
		assert.Error(t, err)
	})
}

// ─── vm stop (via vm stop) ────────────────────────────────────────────────
// Rationale: Stop must gracefully handle already-stopped VMs. An idempotent
// stop is critical for cleanup scripts that call stop before remove.

func TestRunVMStop(t *testing.T) {
	t.Run("success_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMStopFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return &errs.BatchResult{
					Items: []errs.OperationResult{
						{Status: "success", Code: "vm.stopped", Message: "VM 'vm-1' stopped"},
					},
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		stopCmd, _, _ := cmd.Find([]string{"stop"})
		err := stopCmd.RunE(stopCmd, []string{"vm-1"})
		assert.NoError(t, err)
	})

	t.Run("stop_failed_returns_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMStopFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return &errs.BatchResult{
					Items: []errs.OperationResult{
						{Status: "error", Code: "vm.stop_failed", Message: "VM not found: vm-999"},
					},
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		stopCmd, _, _ := cmd.Find([]string{"stop"})
		err := stopCmd.RunE(stopCmd, []string{"vm-999"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "one or more stops failed")
	})

	t.Run("already_stopped_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVMAPI{
			VMStopFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return &errs.BatchResult{
					Items: []errs.OperationResult{
						{Status: "skipped", Code: "vm.already_stopped", Message: "VM 'vm-1' already stopped"},
					},
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		stopCmd, _, _ := cmd.Find([]string{"stop"})
		err := stopCmd.RunE(stopCmd, []string{"vm-1"})
		assert.NoError(t, err)
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockVMAPI{
			VMStopFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				select {
				case <-ctx.Done():
					return &errs.BatchResult{
						Items: []errs.OperationResult{
							{Status: "error", Code: "vm.stop_failed", Message: "operation cancelled"},
						},
					}
				default:
					return &errs.BatchResult{
						Items: []errs.OperationResult{
							{Status: "success", Code: "vm.stopped"},
						},
					}
				}
			},
		}
		cmd := cli.NewVMCmd(mock, nil)
		stopCmd, _, _ := cmd.Find([]string{"stop"})
		stopCmd.SetContext(ctx)
		err := stopCmd.RunE(stopCmd, []string{"vm-1"})
		assert.Error(t, err)
	})
}
