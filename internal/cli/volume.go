// Package cli — volume management commands, matching Python's cli/volume.py
package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

func NewVolumeCmd(volumeAPI *api.VolumeOperation, configAPI *api.ConfigOperation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Aliases: []string{"vol"},
		Short:   "Volume management",
		Long:    "Manage persistent volumes — list, create, remove, inspect, resize.",
	}

	cmd.AddCommand(newVolumeLsCmd(volumeAPI, configAPI))
	cmd.AddCommand(newVolumeCreateCmd(volumeAPI))
	cmd.AddCommand(newVolumeRmCmd(volumeAPI))
	cmd.AddCommand(newVolumeInspectCmd(volumeAPI))
	cmd.AddCommand(newVolumeResizeCmd(volumeAPI))

	return cmd
}

// resolveListingStyle resolves "short" or "long" from --long flag or user config.
// Matches Python's resolve_listing_style() in cli/_common.py exactly.
func resolveListingStyle(ctx context.Context, configAPI *api.ConfigOperation, longOutput bool) string {
	if longOutput {
		return "long"
	}
	if configAPI != nil {
		value, err := configAPI.Get(ctx, "settings", "listing_style")
		if err == nil {
			if s, ok := value.(string); ok && s != "" {
				return s
			}
		}
	}
	return "short"
}

func newVolumeLsCmd(volumeAPI *api.VolumeOperation, configAPI *api.ConfigOperation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List all volumes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			volumes := volumeAPI.ListAll(cmd.Context())

			if jsonOutput {
				var data []map[string]any
				for _, v := range volumes {
					entry := map[string]any{
						"id":           v.ID,
						"name":         v.Name,
						"size_bytes":   v.SizeBytes,
						"size":         v.SizeBytes,
						"format":       v.Format,
						"is_read_only": v.IsReadOnly,
						"status":       v.Status,
						"vm_id":        v.VMID,
						"created_at":   v.CreatedAt,
					}
					data = append(data, entry)
				}
				if data == nil {
					data = make([]map[string]interface{}, 0)
				}
				jsonBytes, _ := json.MarshalIndent(data, "", "  ")
				fmt.Println(string(jsonBytes))
				return nil
			}

			style := resolveListingStyle(cmd.Context(), configAPI, longOutput)

			// Match Python's _VOLUME_COLUMNS exactly
			type volColumn struct {
				header   string
				extract  func(*model.VolumeItem) string
				longOnly bool
			}
			allColumns := []volColumn{
				{"ID", func(v *model.VolumeItem) string { return common.FormatID(v.ID) }, false},
				{"Name", func(v *model.VolumeItem) string { return v.Name }, false},
				{"Size", func(v *model.VolumeItem) string { return common.FormatSize(v.SizeBytes) }, false},
				{"Status", func(v *model.VolumeItem) string { return string(v.Status) }, false},
				{"Format", func(v *model.VolumeItem) string { return v.Format }, true},
				{"Attached To", func(v *model.VolumeItem) string {
					if v.VMID != nil && *v.VMID != "" {
						return *v.VMID
					}
					return "-"
				}, true},
				{"Created", func(v *model.VolumeItem) string { return common.FormatTimestamp(v.CreatedAt, "relative") }, false},
			}

			visible := make([]volColumn, 0)
			for _, col := range allColumns {
				if style == "long" || !col.longOnly {
					visible = append(visible, col)
				}
			}

			headers := make([]string, len(visible))
			for i, col := range visible {
				headers[i] = col.header
			}

			rows := make([][]string, len(volumes))
			for i, v := range volumes {
				row := make([]string, len(visible))
				for j, col := range visible {
					row[j] = col.extract(v)
				}
				rows[i] = row
			}

			common.MVMCLI.Table(headers, rows)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	return cmd
}

func newVolumeCreateCmd(volumeAPI *api.VolumeOperation) *cobra.Command {
	var format string
	var readOnly bool

	cmd := &cobra.Command{
		Use:   "create [name] [size]",
		Short: "Create a new persistent volume",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sizeStr := args[1]

			var formatPtr *string
			if cmd.Flags().Changed("format") {
				formatPtr = &format
			}
			var readOnlyPtr *bool
			if cmd.Flags().Changed("read-only") || cmd.Flags().Changed("readonly") {
				readOnlyPtr = &readOnly
			}
			input := &inputs.VolumeCreateInput{
				Name:     name,
				Size:     sizeStr,
				Format:   formatPtr,
				ReadOnly: readOnlyPtr,
			}
			result := volumeAPI.Create(cmd.Context(), input)
			if result.IsError() {
				// Match Python: mvm_cli.error(result.message); raise typer.Exit(code=1)
				cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}
			// Match Python: mvm_cli.success(result.message)
			common.MVMCLI.Success(result.Message)
			if vol, ok := result.Item.(*model.VolumeItem); ok && vol != nil {
				// Match Python: mvm_cli.key_value("ID", mvm_cli.format_id(result.item.id))
				common.MVMCLI.KeyValue("ID", common.FormatID(vol.ID), 2, 12)
				// Match Python: mvm_cli.key_value("Mode", "ro" if result.item.is_read_only else "rw")
				mode := "rw"
				if vol.IsReadOnly {
					mode = "ro"
				}
				common.MVMCLI.KeyValue("Mode", mode, 2, 12)
				// Match Python: mvm_cli.key_value("Format", result.item.format)
				common.MVMCLI.KeyValue("Format", vol.Format, 2, 12)
				// Match Python: mvm_cli.key_value("Size", mvm_cli.format_size(result.item.size_bytes))
				common.MVMCLI.KeyValue("Size", common.FormatSize(vol.SizeBytes), 2, 12)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Disk format: raw or qcow2 (default: raw)")
	// Accept both --read-only and --readonly (matching Python's Typer alias)
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Mount volume as read-only (default: writable)")
	cmd.Flags().BoolVar(&readOnly, "readonly", false, "Mount volume as read-only (default: writable)")
	return cmd
}

func newVolumeRmCmd(volumeAPI *api.VolumeOperation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [identifiers...]",
		Short:             "Remove one or more volumes",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			removeResult := volumeAPI.Remove(cmd.Context(), &inputs.VolumeInput{Identifiers: args}, force)
			// Match Python: for r in result.items: if r.is_ok: mvm_cli.success("Removed: {name}")
			//              else: mvm_cli.error(r.message or "Remove failed: {name}")
			for _, r := range removeResult.Items {
				itemName := "unknown"
				if r.Item != nil {
					if vol, ok := r.Item.(*model.VolumeItem); ok {
						itemName = vol.Name
					}
				}
				if r.IsOK() {
					common.MVMCLI.Success(fmt.Sprintf("Removed: %s", itemName))
				} else {
					msg := r.Message
					if msg == "" {
						msg = fmt.Sprintf("Remove failed: %s", itemName)
					}
					common.MVMCLI.Error(msg)
				}
			}
			if removeResult.HasErrors() {
				return fmt.Errorf("one or more removals failed")
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Remove even if attached to VMs")
	return cmd
}

func newVolumeInspectCmd(volumeAPI *api.VolumeOperation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [identifier]",
		Short:             "Show detailed information about a volume",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			info, err := volumeAPI.Inspect(cmd.Context(), &inputs.VolumeInput{Identifiers: []string{identifier}})
			if err != nil {
				// Match Python: @handle_errors decorator — pass through actual error message
				return err
			}

			if jsonOutput {
				// Match Python's json.dumps(info, indent=2, default=str)
				fmt.Println(marshalJSONDefaultStr(info))
				return nil
			}

			// Match Python: mvm_cli.print_dict_tree(info, title=f"Volume: {info['volume']['name']}")
			// Python directly accesses info['volume']['name'] — if missing, KeyError
			// propagates to @handle_errors.
			vm := info["volume"].(map[string]interface{})
			name := vm["name"].(string)
			common.MVMCLI.PrintDictTree(info, fmt.Sprintf("Volume: %s", name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newVolumeResizeCmd(volumeAPI *api.VolumeOperation) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "resize [identifier] [size]",
		Short:             "Resize a volume",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			sizeArg := args[1]

			resizeResult := volumeAPI.Resize(cmd.Context(), &inputs.VolumeCreateInput{Name: identifier, Size: sizeArg})
			if resizeResult.IsError() {
				// Match Python: mvm_cli.error(result.message); raise typer.Exit(code=1)
				cli.Error(resizeResult.Message)
				return fmt.Errorf("%s", resizeResult.Message)
			}
			// Match Python: mvm_cli.success(result.message)
			common.MVMCLI.Success(resizeResult.Message)
			return nil
		},
	}

	return cmd
}
