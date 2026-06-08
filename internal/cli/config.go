package cli

import (
	"fmt"
	"sort"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
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

	return cmd
}

func newConfigGetCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "get [category] [key]",
		Short:             "Get a config value.",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeConfigGet,
		RunE: func(cmd *cobra.Command, args []string) error {
			category := args[0]
			key := ""
			if len(args) == 2 {
				key = args[1]
			}

			val, err := op.ConfigGet(cmd.Context(), category, key)
			if err != nil {
				common.Cli.Error(err.Error())
				return err
			}

			// Category-only: dict of SettingInfo per key
			if settings, ok := val.(map[string]model.SettingInfo); ok {
				keys := make([]string, 0, len(settings))
				for k := range settings {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					info := settings[k]
					if info.Override != nil {
						common.Cli.Text(
							fmt.Sprintf("%s = %v (override: %v, type: %s)", k, info.Override, info.Override, info.Type),
						)
					} else {
						common.Cli.Text(
							fmt.Sprintf(
								"%s = %v (default: %v, type: %s)",
								k,
								common.Cli.FormatSettingValue(info.Default, k),
								common.Cli.FormatSettingValue(info.Default, k),
								info.Type,
							),
						)
					}
				}
			} else if val == nil {
				common.Cli.Text(fmt.Sprintf("%s.%s = (default)", category, key))
			} else {
				common.Cli.Text(fmt.Sprintf("%s.%s = %v", category, key, val))
			}

			return nil
		},
	}
}

func newConfigSetCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "set [category] [key] [value]",
		Short:             "Set a config value.",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeConfigSet,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := op.ConfigSet(cmd.Context(), args[0], args[1], args[2]); err != nil {
				common.Cli.Error(err.Error())
				return err
			}
			common.Cli.Success(fmt.Sprintf("Set %s.%s = %s", args[0], args[1], args[2]))
			return nil
		},
	}
}

func newConfigResetCmd(op *api.Operation) *cobra.Command {
	var allOverrides bool
	var force bool

	cc := &cobra.Command{
		Use:               "reset [category] [key]",
		ValidArgsFunction: completeConfigGet,
		Short:             "Reset a config value to its default.",
		Args:              cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allOverrides {
				if !force {
					confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Reset all overrides globally?", false)
					if pErr != nil {
						return pErr
					}
					if !confirmed {
						common.Cli.Text("Cancelled")
						return nil
					}
				}
				deleted, err := op.ConfigReset(cmd.Context(), "", "", true)
				if err != nil {
					return err
				}
				common.Cli.Success(fmt.Sprintf("Reset: %v override(s) globally", deleted))
				return nil
			}

			switch len(args) {
			case 0:
				common.Cli.Text("Provide a category, category and key, or use --all")
			case 1:
				category := args[0]
				deleted, err := op.ConfigReset(cmd.Context(), category, "", false)
				if err != nil {
					return err
				}
				common.Cli.Success(fmt.Sprintf("Reset: %v override(s) in %s", deleted, category))
			case 2:
				category := args[0]
				key := args[1]
				deleted, err := op.ConfigReset(cmd.Context(), category, key, false)
				if err != nil {
					return err
				}
				if deleted > 0 {
					common.Cli.Success(fmt.Sprintf("Reset: %s.%s", category, key))
				} else {
					common.Cli.Text(fmt.Sprintf("%s.%s was already at default", category, key))
				}
			}
			return nil
		},
	}

	cc.Flags().BoolVarP(&allOverrides, "all", "a", false, "Reset all overrides globally")
	cc.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cc
}

func newConfigListCmd(configAPI *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all overridable settings and their current values.",
		RunE: func(cmd *cobra.Command, args []string) error {
			allSettings, err := configAPI.ConfigListAll(cmd.Context())
			if err != nil {
				common.Cli.Error(err.Error())
				return err
			}

			categories := make([]string, 0, len(allSettings))
			for cat := range allSettings {
				categories = append(categories, cat)
			}
			sort.Strings(categories)

			for _, category := range categories {
				common.Cli.Text(fmt.Sprintf("\n[%s]", category))
				settings := allSettings[category]

				keys := make([]string, 0, len(settings))
				for k := range settings {
					keys = append(keys, k)
				}
				sort.Strings(keys)

				for _, key := range keys {
					info := settings[key]
					override := info.Override
					default_ := info.Default
					if override != nil {
						common.Cli.Text(
							fmt.Sprintf(
								"  %s = %v (default: %v, type: %s)",
								key,
								override,
								common.Cli.FormatSettingValue(default_, key),
								info.Type,
							),
						)
					} else {
						common.Cli.Text(
							fmt.Sprintf(
								"  %s = %v (type: %s)",
								key,
								common.Cli.FormatSettingValue(default_, key),
								info.Type,
							),
						)
					}
				}
			}
			return nil
		},
	}
}
