// Package cli — volume management commands, matching Python's cli/volume.py
package cli

import (
	"encoding/json"
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// volumeColumns defines the listing columns for volumes.
// Matches Python's _VOLUME_COLUMNS in cli/volume.py.
var volumeColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.VolumeItem).ID) }},
	{Header: "Name", Extract: func(v any) string { return v.(*model.VolumeItem).Name }},
	{Header: "Size", Extract: func(v any) string { return common.Cli.FormatSize(v.(*model.VolumeItem).SizeBytes) }},
	{Header: "Status", Extract: func(v any) string { return string(v.(*model.VolumeItem).Status) }},
	{Header: "Format", Extract: func(v any) string { return string(v.(*model.VolumeItem).Format) }, LongOnly: true},
	{Header: "Attached To", Extract: func(v any) string {
		vmID := v.(*model.VolumeItem).VMID
		if vmID != nil && *vmID != "" {
			return *vmID
		}
		return "-"
	}, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.VolumeItem).CreatedAt, "relative") },
	},
}

func NewVolumeCmd(op *api.Operation, configAPI *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Aliases: []string{"vol"},
		Short:   "Volume management",
		Long:    "Manage persistent volumes — list, create, remove, inspect, resize.",
	}

	cmd.AddCommand(newVolumeListCmd(op, configAPI))
	cmd.AddCommand(newVolumeCreateCmd(op))
	cmd.AddCommand(newVolumeRemoveCmd(op))
	cmd.AddCommand(newVolumeInspectCmd(op))
	cmd.AddCommand(newVolumeResizeCmd(op))

	return cmd
}

func newVolumeListCmd(op *api.Operation, configAPI *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all volumes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			volumes := op.VolumeListAll(cmd.Context())

			if jsonOutput {
				jsonBytes, _ := json.MarshalIndent(volumes, "", "  ")
				fmt.Println(string(jsonBytes))
				return nil
			}

			style := common.Cli.ResolveListingStyle(cmd.Context(), configAPI, longOutput)

			common.RenderListing(volumes, volumeColumns, style)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	return cmd
}

func newVolumeCreateCmd(op *api.Operation) *cobra.Command {
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
			input := inputs.VolumeCreateInput{
				Name:     name,
				Size:     sizeStr,
				Format:   formatPtr,
				ReadOnly: readOnlyPtr,
			}
			vol, err := op.VolumeCreate(cmd.Context(), input)
			if err != nil {
				return err
			}
			// Match Python: mvm_cli.success(result.message)
			common.Cli.Success(fmt.Sprintf("Volume '%s' created", name))
			for _, col := range volumeColumns {
				switch col.Header {
				case "ID", "Format", "Size":
					common.Cli.KeyValue(col.Header, col.Extract(vol), 2, 12)
				}
			}
			mode := "rw"
			if vol.IsReadOnly {
				mode = "ro"
			}
			common.Cli.KeyValue("Mode", mode, 2, 12)
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Disk format: raw or qcow2 (default: raw)")
	// Accept both --read-only and --readonly (matching Python's Typer alias)
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Mount volume as read-only (default: writable)")
	cmd.Flags().BoolVar(&readOnly, "readonly", false, "Mount volume as read-only (default: writable)")
	cmd.Flags().BoolVar(&readOnly, "ro", false, "Mount volume as read-only (default: writable)")
	return cmd
}

func newVolumeRemoveCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [identifiers...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more volumes",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			removeResult := op.VolumeRemove(cmd.Context(), inputs.VolumeInput{Identifiers: args}, force)
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
					common.Cli.Success(fmt.Sprintf("Removed: %s", itemName))
				} else {
					msg := r.Message
					if msg == "" {
						msg = fmt.Sprintf("Remove failed: %s", itemName)
					}
					common.Cli.Error(msg)
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

func newVolumeInspectCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [identifier]",
		Short:             "Show detailed information about a volume",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			info, err := op.VolumeInspect(cmd.Context(), inputs.VolumeInput{Identifiers: []string{identifier}})
			if err != nil {
				// Match Python: @handle_errors decorator — pass through actual error message
				return err
			}

			if jsonOutput {
				// Match Python's json.dumps(info, indent=2, default=str)
				fmt.Println(common.MarshalJSONDefaultStr(info))
				return nil
			}

			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("Volume: %s", info.Volume.Name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newVolumeResizeCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "resize [identifier] [size]",
		Short:             "Resize a volume",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			sizeArg := args[1]

			if err := op.VolumeResize(
				cmd.Context(),
				inputs.VolumeCreateInput{Name: identifier, Size: sizeArg},
			); err != nil {
				return err
			}
			// Match Python: mvm_cli.success(result.message)
			common.Cli.Success(fmt.Sprintf("Volume '%s' resized", identifier))
			return nil
		},
	}

	return cmd
}
