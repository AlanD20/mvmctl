package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// keyColumns defines the local listing columns for SSH keys.
var keyColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.SSHKeyItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.SSHKeyItem).ID) }},
	{Header: "Name", Extract: func(v any) string {
		return common.Cli.FormatName(v.(*model.SSHKeyItem).Name, !v.(*model.SSHKeyItem).IsPresent)
	}},
	{Header: "Algorithm", Extract: func(v any) string { return v.(*model.SSHKeyItem).Algorithm }},
	{Header: "Fingerprint", Extract: func(v any) string { return v.(*model.SSHKeyItem).Fingerprint }, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.SSHKeyItem).CreatedAt, "relative") },
	},
}

func NewKeyCmd(keyAPI api.KeyAPI, configAPI api.ConfigAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "SSH key management",
	}

	cmd.AddCommand(newKeyListCmd(keyAPI, configAPI))
	cmd.AddCommand(newKeyCreateCmd(keyAPI))
	cmd.AddCommand(newKeyImportCmd(keyAPI))
	cmd.AddCommand(newKeyRemoveCmd(keyAPI))
	cmd.AddCommand(newKeyInspectCmd(keyAPI))
	cmd.AddCommand(newKeyExportCmd(keyAPI))
	cmd.AddCommand(newKeyDefaultCmd(keyAPI))

	return cmd
}

func newKeyListCmd(keyAPI api.KeyAPI, configAPI api.ConfigAPI) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all SSH keys.",
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := keyAPI.KeyListAll(cmd.Context())
			if err != nil {
				return err
			}

			if jsonOutput {
				if keys == nil {
					keys = []*model.SSHKeyItem{}
				}
				data, err := json.MarshalIndent(keys, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			style := common.Cli.ResolveListingStyle(cmd.Context(), configAPI, longOutput)
			common.RenderListing(keys, keyColumns, style)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")

	return cmd
}

func newKeyCreateCmd(keyAPI api.KeyAPI) *cobra.Command {
	var algorithm string
	var bits int
	var comment string
	var out string
	var setDefault bool
	var force bool

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Generate a new SSH keypair",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			alg := algorithm
			if alg == "" {
				if force {
					alg = "ed25519"
				} else {
					var pErr error
					alg, pErr = common.Cli.PromptSelect(
						cmd.Context(),
						"Select algorithm:",
						[]string{"ed25519", "rsa", "ecdsa"},
						0,
					)
					if pErr != nil {
						return pErr
					}
				}
			}

			apiAlg := strings.ToLower(alg)

			input := inputs.KeyCreateInput{
				Name:       name,
				Algorithm:  apiAlg,
				Bits:       bits,
				OutputDir:  out,
				Comment:    comment,
				Overwrite:  force,
				SetDefault: setDefault,
			}

			createdKey, err := keyAPI.KeyCreate(cmd.Context(), input)
			if err != nil {
				return err
			}
			if createdKey != nil {
				common.Cli.Success(fmt.Sprintf("Created: %s (ID: %s)", createdKey.Name, createdKey.Fingerprint))
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&algorithm, "algorithm", "a", "", "Key algorithm (ed25519, rsa, ecdsa)")
	cmd.Flags().IntVar(&bits, "bits", 0, "Key size in bits (RSA only; default 4096)")
	cmd.Flags().StringVar(&comment, "comment", "", "Key comment")
	cmd.Flags().StringVar(&out, "out", "", "Output directory")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default key")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing key")

	return cmd
}

func newKeyImportCmd(keyAPI api.KeyAPI) *cobra.Command {
	var force bool
	var setDefault bool

	cmd := &cobra.Command{
		Use:   "import [name] [path]",
		Short: "Import an existing public key to the cache",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			pubKeyPath := args[1]

			keyItem, err := keyAPI.KeyImport(cmd.Context(), inputs.KeyImportInput{
				Name: name, PubKeyPath: pubKeyPath, Overwrite: force, SetDefault: setDefault,
			})
			if err != nil {
				return err
			}
			if keyItem != nil {
				common.Cli.Success(fmt.Sprintf("Imported: %s (ID: %s)", keyItem.Name, common.Cli.FormatID(keyItem.ID)))
			}
			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default key set to: %s", name))
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing key")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default key")

	return cmd
}

func newKeyRemoveCmd(keyAPI api.KeyAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [selectors]...",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more SSH keys",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeKeyNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}

			// Use API-side resolution for batch removal
			removeResult := keyAPI.KeyRemove(cmd.Context(), inputs.KeyInput{Identifiers: args}, force)
			for _, r := range removeResult.Items {
				if r.Status == "success" {
					if keyItem, ok := r.Item.(*model.SSHKeyItem); ok {
						common.Cli.Success(fmt.Sprintf("Removed: %s", keyItem.Name))
					} else {
						common.Cli.Success("Removed")
					}
				} else {
					common.Cli.Error(r.Message)
				}
			}

			if removeResult.HasErrors() {
				return fmt.Errorf("one or more removals failed")
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force removal even if key is in use")
	return cmd
}

func newKeyInspectCmd(keyAPI api.KeyAPI) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [selector]",
		Short:             "Inspect an SSH key",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKeyNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier, err := common.Cli.CheckArg(cmd, args[0])
			if err != nil {
				return err
			}

			info, err := keyAPI.KeyInspect(cmd.Context(), inputs.KeyInput{Identifiers: []string{identifier}})
			if err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(info, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			keyName := info.Key.Name
			if keyName == "" {
				keyName = identifier
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("Key: %s", keyName))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newKeyExportCmd(keyAPI api.KeyAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "export [selector] [path]",
		Short:             "Export a keypair to a directory",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeKeyThenDir,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier, err := common.Cli.CheckArg(cmd, args[0])
			if err != nil {
				return err
			}

			path := args[1]

			paths, err := keyAPI.KeyExport(
				cmd.Context(),
				inputs.KeyInput{Identifiers: []string{identifier}},
				path,
				force,
			)
			if err != nil {
				return err
			}

			if len(paths) >= 2 {
				common.Cli.Success(fmt.Sprintf("Exported: %s", paths[0]))
				common.Cli.Info(fmt.Sprintf("Exported public key to %s", paths[1]))
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing files")

	return cmd
}

func newKeyDefaultCmd(keyAPI api.KeyAPI) *cobra.Command {
	var clear bool

	cmd := &cobra.Command{
		Use:               "default [selectors]...",
		Short:             "Set default SSH keys, or clear with --clear",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeKeyNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear {
				if err := keyAPI.KeyClearDefaults(cmd.Context()); err != nil {
					return err
				}
				common.Cli.Success("Cleared: all default keys")
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}

			// Single API call with ALL names.
			if err := keyAPI.KeySetDefaults(cmd.Context(), inputs.KeyInput{Identifiers: args}); err != nil {
				return fmt.Errorf("set default failed: %w", err)
			}

			common.Cli.Success(fmt.Sprintf("Default key(s) set: %s", strings.Join(args, ", ")))
			return nil
		},
	}

	cmd.Flags().BoolVar(&clear, "clear", false, "Clear all default keys")

	return cmd
}
