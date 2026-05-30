package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
)

func NewConfigCmd(configAPI *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management",
	}

	cmd.AddCommand(newConfigGetCmd(configAPI))
	cmd.AddCommand(newConfigSetCmd(configAPI))
	cmd.AddCommand(newConfigResetCmd(configAPI))
	cmd.AddCommand(newConfigListCmd(configAPI))

	// Hidden help subcommand matching Python's Typer "help" command
	helpCmd := &cobra.Command{
		Use:    "help",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Parent().Help()
		},
	}
	cmd.AddCommand(helpCmd)

	return cmd
}

func newConfigGetCmd(configAPI *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "get [category] [key]",
		Short:             "Get a config value.",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeConfigGet,
		RunE: func(cmd *cobra.Command, args []string) error {
			category := args[0]
			if len(args) == 2 {
				key := args[1]
				val, err := configAPI.ConfigGet(cmd.Context(), category, key)
				if err != nil {
					// Python: error propagates to @handle_errors which prints it and exits 1.
					// With SilenceErrors=true on root, we must print before returning.
					common.Cli.Error(err.Error())
					return err
				}
				if val == nil {
					common.Cli.Info(fmt.Sprintf("%s.%s = (default)", category, key))
				} else {
					common.Cli.Info(fmt.Sprintf("%s.%s = %v", category, key, val))
				}
			} else {
				// Category-only: show metadata per key matching Python
				val, err := configAPI.ConfigGet(cmd.Context(), category, "")
				if err != nil {
					common.Cli.Error(err.Error())
					return err
				}
				if settings, ok := val.(map[string]model.SettingInfo); ok {
					for k, info := range settings {
						if info.Override != nil {
							common.Cli.Info(fmt.Sprintf("%s = %v (override: %v, type: %s)", k, info.Override, info.Override, info.Type))
						} else {
							common.Cli.Info(fmt.Sprintf("%s = %v (default: %v, type: %s)", k, info.Default, info.Default, info.Type))
						}
					}
				}
			}
			return nil
		},
	}
}

func newConfigSetCmd(configAPI *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "set [category] [key] [value]",
		Short:             "Set a config value.",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeConfigSet,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := configAPI.ConfigSet(cmd.Context(), args[0], args[1], args[2])
			if err != nil {
				common.Cli.Error(err.Error())
				return err
			}
			if result.IsError() {
				// Python: mvm_cli.error(result.message); raise typer.Exit(code=1)
				common.Cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}
			common.Cli.Success(result.Message)
			return nil
		},
	}
}

func newConfigResetCmd(configAPI *api.Operation) *cobra.Command {
	var allOverrides bool

	cc := &cobra.Command{
		Use:               "reset [category] [key]",
		ValidArgsFunction: completeConfigGet,
		Short:             "Reset a config value to its default.",
		Args:              cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allOverrides {
				result := configAPI.ConfigReset(cmd.Context(), "", "", true)
				if result.IsError() {
					common.Cli.Error(result.Message)
					return fmt.Errorf("%s", result.Message)
				}
				common.Cli.Success(fmt.Sprintf("Reset: %v override(s) globally", result.Item))
				return nil
			}

			switch len(args) {
			case 0:
				common.Cli.Info("Provide a category, category and key, or use --all")
			case 1:
				category := args[0]
				result := configAPI.ConfigReset(cmd.Context(), category, "", false)
				if result.IsError() {
					common.Cli.Error(result.Message)
					return fmt.Errorf("%s", result.Message)
				}
				common.Cli.Success(fmt.Sprintf("Reset: %v override(s) in %s", result.Item, category))
			case 2:
				category := args[0]
				key := args[1]
				result := configAPI.ConfigReset(cmd.Context(), category, key, false)
				if result.IsError() {
					common.Cli.Error(result.Message)
					return fmt.Errorf("%s", result.Message)
				}
				if item, ok := result.Item.(int); ok && item > 0 {
					common.Cli.Success(fmt.Sprintf("Reset: %s.%s", category, key))
				} else {
					common.Cli.Info(fmt.Sprintf("%s.%s was already at default", category, key))
				}
			}
			return nil
		},
	}

	cc.Flags().BoolVarP(&allOverrides, "all", "a", false, "Reset all overrides globally")
	return cc
}

func newConfigListCmd(configAPI *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List all overridable settings and their current values.",
		RunE: func(cmd *cobra.Command, args []string) error {
			allSettings, err := configAPI.ConfigListAll(cmd.Context())
			if err != nil {
				// Python: error propagates to @handle_errors which prints it and exits 1.
				// With SilenceErrors=true on root, we must print before returning.
				common.Cli.Error(err.Error())
				return err
			}
			for category, settings := range allSettings {
				common.Cli.Info(fmt.Sprintf("\n[%s]", category))
				for key, info := range settings {
					override := info.Override
					default_ := info.Default
					if override != nil {
						common.Cli.Info(fmt.Sprintf("  %s = %v (default: %v, type: %s)", key, override, default_, info.Type))
					} else {
						common.Cli.Info(fmt.Sprintf("  %s = %v (type: %s)", key, default_, info.Type))
					}
				}
			}
			return nil
		},
	}
}

// escapeRichMarkup escapes text to prevent Rich markup interpretation of brackets.
