package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
)

func NewKeyCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "SSH key management",
	}

	cmd.AddCommand(newKeyListCmd(op))
	cmd.AddCommand(newKeyCreateCmd(op))
	cmd.AddCommand(newKeyAddCmd(op))
	cmd.AddCommand(newKeyRemoveCmd(op))
	cmd.AddCommand(newKeyInspectCmd(op))
	cmd.AddCommand(newKeyExportCmd(op))
	cmd.AddCommand(newKeyDefaultCmd(op))

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

func newKeyListCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:                "ls",
		Short:              "List all SSH keys.",
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := op.KeyListAll(cmd.Context())
			if err != nil {
				return err
			}

			if jsonOutput {
				dicts := make([]map[string]interface{}, 0, len(keys))
				for _, k := range keys {
					dicts = append(dicts, map[string]interface{}{"id": k.ID, "name": k.Name, "algorithm": k.Algorithm})
				}
				data, err := json.MarshalIndent(dicts, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(keys) == 0 {
				cli.Info("No keys found. Use 'mvm key create <name>' or 'mvm key add <name> <path>' to add one.")
				return nil
			}

			rows := make([][]string, 0, len(keys))
			for _, k := range keys {
				marker := cli.FormatMarker(k.IsDefault)
				created := cli.FormatTimestamp(k.CreatedAt, "relative")

				if longOutput {
				rows = append(rows, []string{
					marker,
					cli.FormatID(k.ID),
					cli.FormatName(k.Name, !k.IsPresent),
					k.Algorithm,
					k.Fingerprint,
					created,
				})
			} else {
				rows = append(rows, []string{
					marker,
					cli.FormatID(k.ID),
					cli.FormatName(k.Name, !k.IsPresent),
					k.Algorithm,
					created,
				})
				}
			}

			if longOutput {
				cli.Table([]string{"", "ID", "Name", "Algorithm", "Fingerprint", "Created"}, rows)
			} else {
				cli.Table([]string{"", "ID", "Name", "Algorithm", "Created"}, rows)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	return cmd
}

func newKeyCreateCmd(op *api.Operation) *cobra.Command {
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
				cli.Info("Select algorithm:")
				cli.Info("  1. ed25519")
				cli.Info("  2. rsa")
				cli.Info("  3. ecdsa")
				fmt.Fprintf(os.Stderr, "Enter number [1]: ")
				var choice string
				_, _ = fmt.Scanln(&choice)
				choice = strings.TrimSpace(choice)
				algoMap := map[string]string{
					"1": "ed25519",
					"2": "rsa",
					"3": "ecdsa",
				}
				alg = algoMap[choice]
				if alg == "" {
					alg = "ed25519"
				}
			}

			apiAlg := strings.ToLower(alg)

			input := &api.KeyCreateInput{
				Name:       name,
				Algorithm:  apiAlg,
				Bits:       bits,
				OutputDir:  out,
				Comment:    comment,
				Overwrite:  force,
				SetDefault: setDefault,
			}

			createResult := op.KeyCreate(cmd.Context(), input)
			if createResult.Status == "error" {
				cli.Error(createResult.Message)
				return fmt.Errorf("%s", createResult.Message)
			}
			if createdKey, ok := createResult.Item.(*model.SSHKeyItem); ok && createdKey != nil {
				cli.Success(fmt.Sprintf("Created: %s (ID: %s)", createdKey.Name, createdKey.Fingerprint))
			}
			return nil
		},
	}

	// Python does NOT have -a short flag for --algorithm
	cmd.Flags().StringVar(&algorithm, "algorithm", "", "Key algorithm (ed25519, rsa, ecdsa)")
	cmd.Flags().IntVar(&bits, "bits", 0, "Key size in bits (RSA only; default 4096)")
	cmd.Flags().StringVar(&comment, "comment", "", "Key comment")
	cmd.Flags().StringVar(&out, "out", "", "Output directory")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default key")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing key")
	return cmd
}

func newKeyAddCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "add [name] [path]",
		Short: "Add an existing public key to the cache",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			pubKeyPath := args[1]

			createdKey := op.KeyAdd(cmd.Context(), name, pubKeyPath, force)
			if createdKey.Status == "error" {
				cli.Error(createdKey.Message)
				return fmt.Errorf("%s", createdKey.Message)
			}
			if keyItem, ok := createdKey.Item.(*model.SSHKeyItem); ok && keyItem != nil {
				cli.Success(fmt.Sprintf("Added: %s (ID: %s)", keyItem.Name, cli.FormatID(keyItem.ID)))
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing key")
	return cmd
}

func newKeyRemoveCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:                "rm [name]...",
		Short:              "Remove one or more SSH keys",
		Args:               cobra.ArbitraryArgs,
		ValidArgsFunction:  completeKeyNames,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			names := args
			if len(names) == 0 {
				cli.Error("Provide at least one key name to remove")
				return fmt.Errorf("usage error")
			}

			// Use API-side resolution matching Python's KeyInput(name=effective_names) + KeyOperation.remove()
			removeResult := op.KeyRemove(cmd.Context(), &api.KeyInput{Names: names}, force)
			for _, r := range removeResult.Items {
				if r.Status == "success" {
					if keyItem, ok := r.Item.(*model.SSHKeyItem); ok {
						cli.Success(fmt.Sprintf("Removed: %s", keyItem.Name))
					} else {
						cli.Success("Removed")
					}
				} else {
					cli.Error(r.Message)
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

func newKeyInspectCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:                "inspect [name]",
		Short:              "Inspect an SSH key",
		Args:               cobra.ExactArgs(1),
		ValidArgsFunction:  completeKeyNames,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// check_name_arg: "help" → show help; empty → show help with error
			if name == "help" {
				return cmd.Help()
			}
			if name == "" {
				cmd.Help()
				return fmt.Errorf("key name required")
			}

			info, err := op.KeyInspect(cmd.Context(), &api.KeyInput{Names: []string{name}})
			if err != nil {
				cli.Error(err.Error())
				return err
			}

			if jsonOutput {
				data, err := json.MarshalIndent(info, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			keyName := name
			if keyInfo, ok := info["key"].(map[string]interface{}); ok {
				if n, ok := keyInfo["name"].(string); ok {
					keyName = n
				}
			}
			cli.PrintDictTree(info, fmt.Sprintf("Key: %s", keyName))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newKeyExportCmd(op *api.Operation) *cobra.Command {
	var out string
	var force bool

	cmd := &cobra.Command{
		Use:               "export [name]",
		Short:             "Export a keypair to a directory",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKeyNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// check_name_arg: "help" → show help; empty → show help with error
			if name == "help" {
				return cmd.Help()
			}
			if name == "" {
				cmd.Help()
				return fmt.Errorf("key name required")
			}

			if out == "" {
				return fmt.Errorf("required flag \"--out\" not set")
			}

			exportResult := op.KeyExport(cmd.Context(), &api.KeyInput{Names: []string{name}}, out, force)
			if exportResult.Status == "error" {
				cli.Error(exportResult.Message)
				return fmt.Errorf("%s", exportResult.Message)
			}

			if paths, ok := exportResult.Item.([]string); ok && len(paths) >= 2 {
				cli.Success(fmt.Sprintf("Exported: %s", paths[0]))
				cli.Info(fmt.Sprintf("Exported public key to %s", paths[1]))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&out, "out", "", "Destination directory")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing files")
	cmd.MarkFlagRequired("out")
	return cmd
}

func newKeyDefaultCmd(op *api.Operation) *cobra.Command {
	var clear bool

	cmd := &cobra.Command{
		Use:                "default [name]...",
		Short:              "Set default SSH keys, or clear with --clear",
		Args:               cobra.ArbitraryArgs,
		ValidArgsFunction:  completeKeyNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear {
				clearResult := op.KeyClearDefaults(cmd.Context())
				if clearResult.Status == "error" {
					cli.Error(clearResult.Message)
					return fmt.Errorf("%s", clearResult.Message)
				}
				cli.Success("Cleared: all default keys")
				return nil
			}

			if len(args) == 0 {
				cli.Error("Provide at least one key name or use --clear")
				return fmt.Errorf("usage error")
			}

			// Python: KeyInput(name=effective_names) -> KeyOperation.set_default(inputs)
			// Single API call with ALL names, matching Python exactly.
			effectiveNames := args
			setResult := op.KeySetDefault(cmd.Context(), &api.KeyInput{Names: effectiveNames})
			if setResult.Status == "error" {
				cli.Error(setResult.Message)
				return fmt.Errorf("set default failed")
			}

			cli.Success(fmt.Sprintf("Default key(s) set: %s", strings.Join(effectiveNames, ", ")))
			return nil
		},
	}

	cmd.Flags().BoolVar(&clear, "clear", false, "Clear all default keys")
	return cmd
}
