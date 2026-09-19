package cli

import (
	"fmt"
	"os"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// networkColumns defines the local listing columns for networks.
var networkColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.NetworkItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.NetworkItem).ID) }},
	{Header: "Name", Extract: func(v any) string {
		item := v.(*model.NetworkItem)
		name := item.Name
		if item.DeletedAt != nil {
			name += " [x]"
		}
		return common.Cli.FormatName(name, item.DeletedAt != nil)
	}},
	{Header: "Subnet", Extract: func(v any) string { return v.(*model.NetworkItem).Subnet }},
	{Header: "NAT", Extract: func(v any) string {
		if v.(*model.NetworkItem).NATEnabled {
			return "yes"
		}
		return "no"
	}},
	{Header: "Bridge", Extract: func(v any) string { return v.(*model.NetworkItem).Bridge }, LongOnly: true},
	{Header: "VMs", Extract: func(v any) string {
		l := v.(*model.NetworkItem).Leases
		if l != nil {
			return fmt.Sprintf("%d", len(l))
		}
		return "0"
	}, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.NetworkItem).CreatedAt, "relative") },
	},
}

// NewNetworkCmd creates the network command and its subcommands.
func NewNetworkCmd(networkAPI api.NetworkAPI, configAPI api.ConfigAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Aliases: []string{"net"},
		Short:   "Network management",
	}

	cmd.AddCommand(newNetworkListCmd(networkAPI, configAPI))
	cmd.AddCommand(newNetworkCreateCmd(networkAPI))
	cmd.AddCommand(newNetworkRemoveCmd(networkAPI))
	cmd.AddCommand(newNetworkInspectCmd(networkAPI))
	cmd.AddCommand(newNetworkSyncCmd(networkAPI))
	cmd.AddCommand(newNetworkDefaultCmd(networkAPI))

	return cmd
}

func newNetworkListCmd(networkAPI api.NetworkAPI, configAPI api.ConfigAPI) *cobra.Command {
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all networks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			nets, err := networkAPI.NetworkListAll(cmd.Context())
			if err != nil {
				return err
			}

			if common.RecordResult(nets) {
				return nil
			}

			// Resolve listing style from --long flag or DB config
			style := common.Cli.ResolveListingStyle(cmd.Context(), configAPI, longOutput)
			common.RenderListing(nets, networkColumns, style)
			return nil
		},
	}

	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	return cmd
}

func newNetworkCreateCmd(networkAPI api.NetworkAPI) *cobra.Command {
	var subnet string
	var ipv4Gateway string
	var noNAT bool
	var setDefault bool

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a named network.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				var err error
				name, err = common.Cli.CheckArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				// Let Cobra handle the missing argument error
				return fmt.Errorf("missing required argument: name")
			}

			if subnet == "" {
				return fmt.Errorf("Missing required option '--subnet'")
			}

			var gw *string
			if ipv4Gateway != "" {
				gw = &ipv4Gateway
			}
			input := inputs.NetworkCreateInput{
				Name:        name,
				Subnet:      subnet,
				IPv4Gateway: gw,
				NATEnabled:  !noNAT,
				SetDefault:  setDefault,
			}

			net, err := networkAPI.NetworkCreate(cmd.Context(), input)
			common.RecordResult(net)
			if err != nil {
				return fmt.Errorf("create network failed: %w", err)
			}

			common.Cli.PrintDictTree(common.Cli.ToMap(net), fmt.Sprintf("Network: %s", net.Name))

			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default network set to: %s", net.Name))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&subnet, "subnet", "", "IP subnet in SUBNET notation (e.g. 192.168.100.0/24)")
	cmd.Flags().StringVar(&ipv4Gateway, "ipv4-gateway", "", "Gateway IPv4 for the bridge")
	cmd.Flags().BoolVar(&noNAT, "no-nat", false, "Disable NAT/masquerade")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default network")
	return cmd
}

func newNetworkRemoveCmd(networkAPI api.NetworkAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [selectors...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more networks by name.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}

			err := networkAPI.NetworkRemove(
				cmd.Context(),
				inputs.NetworkInput{Identifiers: args, IncludeDeleted: true},
				force,
			)
			if err != nil {
				return fmt.Errorf("remove failed: %w", err)
			}

			for _, name := range args {
				common.Cli.Success(fmt.Sprintf("Removed: %s", name))
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Remove even if referenced by VMs")
	return cmd
}

func newNetworkInspectCmd(networkAPI api.NetworkAPI) *cobra.Command {

	cmd := &cobra.Command{
		Use:               "inspect [selector]",
		Short:             "Show detailed information about a network.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				var err error
				name, err = common.Cli.CheckArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error: missing required argument\n")
				return fmt.Errorf("missing required argument")
			}

			info, err := networkAPI.NetworkInspect(cmd.Context(), inputs.NetworkInput{Identifiers: []string{name}})
			if err != nil {
				return fmt.Errorf("network not found: %s: %w", name, err)
			}

			if common.RecordResult(info) {
				return nil
			}

			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("Network: %s", info.Network.Name))
			return nil
		},
	}

	return cmd
}

func newNetworkSyncCmd(networkAPI api.NetworkAPI) *cobra.Command {

	cmd := &cobra.Command{
		Use:               "sync [selectors...]",
		Short:             "Sync iptables rules between database and host.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			var netInput inputs.NetworkInput
			if len(args) > 0 {
				netInput = inputs.NetworkInput{Identifiers: args}
			}
			results, err := networkAPI.NetworkSync(cmd.Context(), netInput)
			if err != nil {
				return fmt.Errorf("sync failed: %w", err)
			}

			if common.RecordResult(results) {
				return nil
			}

			// Build name map from all networks
			allNetworks, listErr := networkAPI.NetworkListAll(cmd.Context())
			if listErr != nil {
				return listErr
			}
			nameMap := make(map[string]string)
			for _, n := range allNetworks {
				nameMap[n.ID] = n.Name
			}

			// Build table rows
			rows := make([][]string, 0, len(results))
			for nid, counts := range results {
				shortID := common.Cli.FormatID(nid)
				name := nameMap[nid]
				if name == "" {
					name = crypto.Truncate(nid, 8)
				}
				rows = append(rows, []string{
					shortID,
					name,
					fmt.Sprintf("%d", counts["verified"]),
					fmt.Sprintf("%d", counts["added"]),
					fmt.Sprintf("%d", counts["orphaned"]),
				})
			}

			common.Cli.Table([]string{"ID", "Name", "Verified", "Added", "Orphaned"}, rows)
			return nil
		},
	}

	return cmd
}

func newNetworkDefaultCmd(networkAPI api.NetworkAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "default [selector]",
		Short: "Set a network as the default for VM creation.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				var err error
				name, err = common.Cli.CheckArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error: missing required argument\n")
				return fmt.Errorf("missing required argument")
			}

			err := networkAPI.NetworkSetDefault(cmd.Context(), inputs.NetworkInput{Identifiers: []string{name}})
			if err != nil {
				return fmt.Errorf("set default failed: %w", err)
			}

			common.Cli.Success(fmt.Sprintf("Default network set to: %s", name))
			return nil
		},
	}
	return cmd
}
