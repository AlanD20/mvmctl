package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

// ── Columns for env list ──

var envListColumns = []common.ListingColumn{
	{Header: "Workflow ID", Extract: func(v any) string { return common.Cli.FormatID(v.(env.ListSummary).WorkflowID) }},
	{Header: "Spec", Extract: func(v any) string { return v.(env.ListSummary).SpecPath }},
	{Header: "Resources", Extract: func(v any) string { return fmt.Sprintf("%d", v.(env.ListSummary).Resources) }},
	{Header: "Created", Extract: func(v any) string {
		return common.Cli.FormatTimestamp(v.(env.ListSummary).CreatedAt, "relative")
	}},
	{Header: "Updated", Extract: func(v any) string {
		return common.Cli.FormatTimestamp(v.(env.ListSummary).UpdatedAt, "relative")
	}},
}

// NewEnvCmd creates the env command group for managing environments.
func NewEnvCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Environment workflow management",
		Long: `Manage environments defined as YAML specs.

An environment spec declares the full set of resources (networks, keys, images,
kernels, binaries, VMs) needed for a project. The 'env apply' command resolves
dependencies and provisions resources in the correct order. The 'env destroy'
command tears everything down.

Resources are checked for existence before creation — existing resources are
skipped (idempotent apply).

Workflow state is persisted in ~/.cache/mvmctl/states/ so you can inspect and
destroy environments even after reboots.`,
	}

	cmd.AddCommand(newEnvApplyCmd(op))
	cmd.AddCommand(newEnvListCmd(op))
	cmd.AddCommand(newEnvDestroyCmd(op))
	cmd.AddCommand(newEnvDiffCmd(op))

	return cmd
}

// newEnvApplyCmd creates the "env apply" subcommand.
func newEnvApplyCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apply [spec-path]",
		Aliases: []string{"up"},
		Short:   "Apply an environment spec (create or reconcile resources)",
		Args:    cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return []string{"yaml", "yml"}, cobra.ShellCompDirectiveFilterFileExt
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			specPath := ""
			if len(args) > 0 {
				var err error
				specPath, err = common.Cli.CheckArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("missing required argument: spec-path")
			}

			// Verify the spec file exists.
			if _, err := os.Stat(specPath); os.IsNotExist(err) {
				return fmt.Errorf("spec file not found: %s", specPath)
			}

			// Use a simple inline progress handler that prints to stderr
			// so stdout stays clean for potential future --json output.
			onProgress := func(ev event.Progress) {
				if ev.Message != "" {
					fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", ev.Phase, ev.Status, ev.Message)
				} else {
					fmt.Fprintf(os.Stderr, "  [%s] %s\n", ev.Phase, ev.Status)
				}
			}

			if err := env.Apply(cmd.Context(), op, specPath, onProgress); err != nil {
				return fmt.Errorf("env apply failed: %w", err)
			}

			common.Cli.Success("Environment applied successfully")
			return nil
		},
	}
	return cmd
}

// newEnvListCmd creates the "env ls" subcommand.
func newEnvListCmd(_ *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all saved environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			summaries, err := env.List(cmd.Context())
			if err != nil {
				return fmt.Errorf("list envs failed: %w", err)
			}

			if len(summaries) == 0 {
				common.Cli.Info("No saved environments found")
				return nil
			}

			common.RenderListing(summaries, envListColumns, "grid")
			return nil
		},
	}
	return cmd
}

// newEnvDiffCmd creates the "env diff" subcommand.
func newEnvDiffCmd(_ *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff [spec-path]",
		Short: "Show differences between a spec and the saved workflow state",
		Long: `Compare an environment spec file against the saved workflow state and show
which resources would be new, removed, or already exist.

This is a read-only operation — nothing is created or destroyed.`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return []string{"yaml", "yml"}, cobra.ShellCompDirectiveFilterFileExt
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			specPath := ""
			if len(args) > 0 {
				var err error
				specPath, err = common.Cli.CheckArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("missing required argument: spec-path")
			}

			if _, err := os.Stat(specPath); os.IsNotExist(err) {
				return fmt.Errorf("spec file not found: %s", specPath)
			}

			// Resolve the workflow state directory (may not exist yet).
			wfID := env.ResolveWorkflowID(specPath)
			stateDir := filepath.Join(infra.GetWorkflowsStateDir(), wfID)

			result, err := env.Diff(cmd.Context(), specPath, stateDir)
			if err != nil {
				return fmt.Errorf("env diff failed: %w", err)
			}

			if len(result.New) == 0 && len(result.Removed) == 0 {
				common.Cli.Success("No differences — spec matches saved state")
				return nil
			}

			if len(result.New) > 0 {
				fmt.Fprintf(os.Stderr, "New resources (in spec, not in state):\n")
				for _, name := range result.New {
					fmt.Fprintf(os.Stderr, "  %s+ %s%s\n", common.AnsiGreen, name, common.AnsiReset)
				}
			}
			if len(result.Removed) > 0 {
				fmt.Fprintf(os.Stderr, "Removed resources (in state, not in spec):\n")
				for _, name := range result.Removed {
					fmt.Fprintf(os.Stderr, "  %s- %s%s\n", common.AnsiRed, name, common.AnsiReset)
				}
			}
			if len(result.Existing) > 0 {
				fmt.Fprintf(os.Stderr, "Existing resources (unchanged):\n")
				for _, name := range result.Existing {
					fmt.Fprintf(os.Stderr, "    %s\n", name)
				}
			}

			return nil
		},
	}
	return cmd
}

// newEnvDestroyCmd creates the "env destroy" subcommand.
func newEnvDestroyCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destroy [workflow-id|spec-path]",
		Aliases: []string{"down"},
		Short:   "Destroy an environment (tear down all provisioned resources)",
		Long: `Destroy all resources created by a previous env apply.

The argument can be either a workflow ID (short hash shown by 'env ls') or
the path to the original spec file. Resources that were already present before
apply (not created by the workflow) are left intact.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeEnvDestroy,
		RunE: func(cmd *cobra.Command, args []string) error {
			ident := ""
			if len(args) > 0 {
				var err error
				ident, err = common.Cli.CheckArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("missing required argument: workflow-id or spec-path")
			}

			onProgress := func(ev event.Progress) {
				if ev.Message != "" {
					fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", ev.Phase, ev.Status, ev.Message)
				} else {
					fmt.Fprintf(os.Stderr, "  [%s] %s\n", ev.Phase, ev.Status)
				}
			}

			if err := env.Destroy(cmd.Context(), op, ident, onProgress); err != nil {
				return fmt.Errorf("env destroy failed: %w", err)
			}

			common.Cli.Success("Environment destroyed")
			return nil
		},
	}
	return cmd
}
