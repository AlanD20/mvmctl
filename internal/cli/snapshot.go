package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/event"
	infraptr "mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// snapshotColumns defines the listing columns for snapshots.
var snapshotColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.SnapshotItem).ID) }},
	{Header: "Name", Extract: func(v any) string { return v.(*model.SnapshotItem).Name }},
	{Header: "Source VM", Extract: func(v any) string { return v.(*model.SnapshotItem).SourceVMName }},
	{Header: "Resources", Extract: func(v any) string {
		s := v.(*model.SnapshotItem)
		return fmt.Sprintf("%d vCPU / %d MiB / %d MiB", s.VCPUCount, s.MemSizeMiB, s.DiskSizeMiB)
	}},
	{Header: "Created", Extract: func(v any) string {
		return common.Cli.FormatTimestamp(v.(*model.SnapshotItem).CreatedAt, "relative")
	}},
}

// NewSnapshotCmd creates the snapshot command tree.
func NewSnapshotCmd(snapshotAPI api.SnapshotAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshot",
		Aliases: []string{"ss"},
		Short:   "Snapshot lifecycle management (create, list, restore, remove)",
	}

	cmd.AddCommand(newSnapshotCreateCmd(snapshotAPI))
	cmd.AddCommand(newSnapshotListCmd(snapshotAPI))
	cmd.AddCommand(newSnapshotInspectCmd(snapshotAPI))
	cmd.AddCommand(newSnapshotRestoreCmd(snapshotAPI))
	cmd.AddCommand(newSnapshotRemoveCmd(snapshotAPI))
	return cmd
}

// --- create ---

func newSnapshotCreateCmd(snapshotAPI api.SnapshotAPI) *cobra.Command {
	var (
		name  string
		pause bool
	)

	cmd := &cobra.Command{
		Use:               "create <vm>",
		Short:             "Snapshot a running VM.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmIdentifier := args[0]

			if err := system.CheckPrivileges("/usr/sbin/ip", "create snapshot"); err != nil {
				return fmt.Errorf("privilege check failed: %w", err)
			}

			input := inputs.SnapshotCreateInput{
				Identifier: vmIdentifier,
				Pause:      pause,
			}
			if cmd.Flags().Changed("name") {
				input.Name = infraptr.Ptr(name)
			}

			prog := common.NewProgress()
			prog.Start("Creating snapshot...")
			defer prog.Stop()

			snapItem, err := snapshotAPI.SnapshotCreate(cmd.Context(), input, func(e event.Progress) {
				if e.Message != "" {
					prog.UpdateText(e.Message)
				}
			})
			if err != nil {
				return fmt.Errorf("snapshot create failed: %w", err)
			}

			common.Cli.Success(
				fmt.Sprintf("Snapshot created: %s (ID: %s)", snapItem.Name, common.Cli.FormatID(snapItem.ID)),
			)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Optional snapshot name")
	cmd.Flags().BoolVar(&pause, "pause", false, "Leave VM paused after snapshot")
	return cmd
}

// --- ls (list all snapshots) ---

func newSnapshotListCmd(snapshotAPI api.SnapshotAPI) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all snapshots.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return common.HandleErrors(func() error {
				snapshots := snapshotAPI.SnapshotList(cmd.Context())

				if jsonOutput {
					if snapshots == nil {
						snapshots = []*model.SnapshotItem{}
					}
					b, _ := json.MarshalIndent(snapshots, "", "  ")
					fmt.Println(string(b))
					return nil
				}

				common.RenderListing(snapshots, snapshotColumns, "short")
				return nil
			})()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// --- get ---

func newSnapshotInspectCmd(snapshotAPI api.SnapshotAPI) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect <identifier>",
		Short:             "Show details of a single snapshot.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSnapshotIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			snap, err := snapshotAPI.SnapshotInspect(cmd.Context(), inputs.SnapshotInput{Identifiers: []string{id}})
			if err != nil {
				return fmt.Errorf("get snapshot: %w", err)
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(snap, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			common.Cli.PrintDictTree(common.Cli.ToMap(snap), fmt.Sprintf("Snapshot: %s", snap.Snapshot.Name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// --- restore ---

func newSnapshotRestoreCmd(snapshotAPI api.SnapshotAPI) *cobra.Command {
	var (
		count   int
		network string
		resume  bool
	)

	cmd := &cobra.Command{
		Use:               "restore <identifier> <name>",
		Short:             "Restore one or more VMs from a snapshot.",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeSnapshotThenName,
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshotID := args[0]
			vmName := args[1]

			if err := system.CheckPrivileges("/usr/sbin/ip", "restore snapshot"); err != nil {
				return fmt.Errorf("privilege check failed: %w", err)
			}

			input := inputs.SnapshotRestoreInput{
				SnapshotID: snapshotID,
				Name:       vmName,
				Count:      count,
				Resume:     resume,
			}
			if cmd.Flags().Changed("network") {
				input.Network = &network
			}

			prog := common.NewProgress()
			prog.Start("Restoring snapshot...")
			defer prog.Stop()

			vms, err := snapshotAPI.SnapshotRestore(cmd.Context(), input)
			if err != nil {
				return fmt.Errorf("snapshot restore failed: %w", err)
			}

			names := make([]string, len(vms))
			for i, v := range vms {
				names[i] = v.Name
			}
			common.Cli.Success(fmt.Sprintf("Restored: %s", strings.Join(names, ", ")))
			return nil
		},
	}

	cmd.Flags().IntVarP(&count, "count", "c", 1, "Number of VMs to create from snapshot")
	cmd.Flags().StringVar(&network, "network", "", "Network to use (defaults to snapshot's original network)")
	cmd.Flags().BoolVar(&resume, "resume", false, "Resume VMs after loading snapshot")
	return cmd
}

// --- rm (remove) ---

func newSnapshotRemoveCmd(snapshotAPI api.SnapshotAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm <identifier>",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove a snapshot.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSnapshotIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			input := inputs.SnapshotInput{
				Identifiers: []string{id},
				Force:       force,
			}
			result := snapshotAPI.SnapshotRemove(cmd.Context(), input)
			if result.HasErrors() {
				for _, r := range result.Items {
					if r.IsOK() {
						if snap, ok := r.Item.(*model.SnapshotItem); ok && snap != nil {
							common.Cli.Success(fmt.Sprintf("Removed snapshot: %s", snap.Name))
						}
					} else {
						msg := r.Message
						if msg == "" {
							msg = "Failed to remove snapshot"
						}
						common.Cli.Error(msg)
					}
				}
				return fmt.Errorf("one or more removals failed")
			}

			common.Cli.Success(fmt.Sprintf("Removed snapshot: %s", id))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force removal")
	return cmd
}

// --- Shell completion helpers ---

func completeSnapshotIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	items := opRef.SnapshotList(cmd.Context())
	var completions []string
	for _, snap := range items {
		if strings.HasPrefix(snap.ID, toComplete) || strings.HasPrefix(snap.Name, toComplete) {
			completions = append(completions, fmt.Sprintf("%s\t%s", snap.ID, snap.Name))
		}
	}
	if len(completions) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

func completeSnapshotThenName(
	cmd *cobra.Command,
	args []string,
	toComplete string,
) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeSnapshotIDs(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
