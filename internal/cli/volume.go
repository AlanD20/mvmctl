// Package cli — volume management commands
package cli

import (
	"encoding/json"
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// volumeColumns defines the listing columns for volumes.
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

func NewVolumeCmd(volumeAPI api.VolumeAPI, configAPI api.ConfigAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Aliases: []string{"vol"},
		Short:   "Volume management",
		Long:    "Manage persistent volumes — list, create, remove, inspect, resize, attach, detach.",
	}

	cmd.AddCommand(newVolumeListCmd(volumeAPI, configAPI))
	cmd.AddCommand(newVolumeCreateCmd(volumeAPI))
	cmd.AddCommand(newVolumeRemoveCmd(volumeAPI))
	cmd.AddCommand(newVolumeInspectCmd(volumeAPI))
	cmd.AddCommand(newVolumeResizeCmd(volumeAPI))
	cmd.AddCommand(newVolumeAttachCmd(volumeAPI))
	cmd.AddCommand(newVolumeDetachCmd(volumeAPI))

	return cmd
}

func newVolumeListCmd(volumeAPI api.VolumeAPI, configAPI api.ConfigAPI) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all volumes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			volumes := volumeAPI.VolumeListAll(cmd.Context())

			if jsonOutput {
				if volumes == nil {
					volumes = []*model.VolumeItem{}
				}
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

func newVolumeCreateCmd(volumeAPI api.VolumeAPI) *cobra.Command {
	var format string
	var readOnly bool
	var shareable bool

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
			var shareablePtr *bool
			if cmd.Flags().Changed("shareable") || cmd.Flags().Changed("s") {
				shareablePtr = &shareable
			}
			input := inputs.VolumeCreateInput{
				Name:      name,
				Size:      sizeStr,
				Format:    formatPtr,
				ReadOnly:  readOnlyPtr,
				Shareable: shareablePtr,
			}
			vol, err := volumeAPI.VolumeCreate(cmd.Context(), input)
			if err != nil {
				return err
			}
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
			if vol.IsShareable {
				common.Cli.KeyValue("Shareable", "yes", 2, 12)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Disk format: raw or qcow2 (default: raw)")
	// Accept both --read-only and --readonly as aliases
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Mount volume as read-only (default: writable)")
	cmd.Flags().BoolVar(&readOnly, "readonly", false, "Mount volume as read-only (default: writable)")
	cmd.Flags().BoolVar(&readOnly, "ro", false, "Mount volume as read-only (default: writable)")
	cmd.Flags().
		BoolVarP(&shareable, "shareable", "s", false, "Allow volume to be attached to multiple VMs (requires --read-only)")
	return cmd
}

func newVolumeRemoveCmd(volumeAPI api.VolumeAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [identifiers...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more volumes",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			removeResult := volumeAPI.VolumeRemove(cmd.Context(), inputs.VolumeInput{Identifiers: args}, force)
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

func newVolumeInspectCmd(volumeAPI api.VolumeAPI) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [identifier]",
		Short:             "Show detailed information about a volume",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVolumeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			info, err := volumeAPI.VolumeInspect(cmd.Context(), inputs.VolumeInput{Identifiers: []string{identifier}})
			if err != nil {
				return err
			}

			if jsonOutput {
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

func newVolumeResizeCmd(volumeAPI api.VolumeAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "resize [identifier] [size]",
		Short:             "Resize a volume",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVolumeThenSize,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			sizeArg := args[1]

			if err := volumeAPI.VolumeResize(
				cmd.Context(),
				inputs.VolumeCreateInput{Name: identifier, Size: sizeArg},
			); err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Volume '%s' resized", identifier))
			return nil
		},
	}

	return cmd
}

func newVolumeAttachCmd(volumeAPI api.VolumeAPI) *cobra.Command {
	return &cobra.Command{
		Use:   "attach [vm_identifier] [volume_identifier]",
		Short: "Attach a volume to a VM.",
		Long: `Attach a volume to a running VM.

Arguments:
  vm_identifier      VM identifier (name, ID prefix, IP, or MAC)
  volume_identifier  Name or ID of the volume to attach`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMThenVolume,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			volumeName := args[1]

			input := inputs.VolumeInput{
				VMIdentifier: id,
				Identifiers:  []string{volumeName},
			}
			if err := volumeAPI.VolumeAttach(cmd.Context(), input); err != nil {
				return fmt.Errorf("attach volume %q: %w", volumeName, err)
			}

			common.Cli.Success(fmt.Sprintf("Volume '%s' attached", volumeName))
			return nil
		},
	}
}

func newVolumeDetachCmd(volumeAPI api.VolumeAPI) *cobra.Command {
	return &cobra.Command{
		Use:   "detach [vm_identifier] [volume_identifier]",
		Short: "Detach a volume from a VM.",
		Long: `Detach a volume from a running VM.

Arguments:
  vm_identifier      VM identifier (name, ID prefix, IP, or MAC)
  volume_identifier  Name or ID of the volume to detach`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMThenVolume,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			volumeName := args[1]

			input := inputs.VolumeInput{
				VMIdentifier: id,
				Identifiers:  []string{volumeName},
			}
			if err := volumeAPI.VolumeDetach(cmd.Context(), input); err != nil {
				return fmt.Errorf("detach volume %q: %w", volumeName, err)
			}

			common.Cli.Success(fmt.Sprintf("Volume '%s' detached", volumeName))
			return nil
		},
	}
}
