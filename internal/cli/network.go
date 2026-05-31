package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/model"
	infranet "mvmctl/internal/infra/network"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// networkColumns defines the local listing columns for networks.
var networkColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.Network).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.Network).ID) }},
	{Header: "Name", Extract: func(v any) string {
		return common.Cli.FormatName(v.(*model.Network).Name, !v.(*model.Network).IsPresent)
	}},
	{Header: "Subnet", Extract: func(v any) string { return v.(*model.Network).Subnet }},
	{Header: "NAT", Extract: func(v any) string {
		if v.(*model.Network).NATEnabled {
			return "yes"
		}
		return "no"
	}},
	{Header: "Bridge", Extract: func(v any) string { return v.(*model.Network).Bridge }, LongOnly: true},
	{Header: "VMs", Extract: func(v any) string {
		l := v.(*model.Network).Leases
		if l != nil {
			return fmt.Sprintf("%d", len(l))
		}
		return "0"
	}, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.Network).CreatedAt, "relative") },
	},
}

// NewNetworkCmd creates the network command and its subcommands.
func NewNetworkCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Aliases: []string{"net"},
		Short:   "Network management",
	}

	cmd.AddCommand(newNetworkListCmd(op))
	cmd.AddCommand(newNetworkCreateCmd(op))
	cmd.AddCommand(newNetworkRemoveCmd(op))
	cmd.AddCommand(newNetworkInspectCmd(op))
	cmd.AddCommand(newNetworkSyncCmd(op))
	cmd.AddCommand(newNetworkDefaultCmd(op))

	return cmd
}

func newNetworkListCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all networks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			nets, err := op.NetworkListAll(cmd.Context())
			if err != nil {
				return err
			}

			if jsonOutput {
				data, _ := json.MarshalIndent(nets, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			// Resolve listing style from --long flag or DB config (matching Python's resolve_listing_style)
			style := common.Cli.ResolveListingStyle(cmd.Context(), op, longOutput)
			items := make([]any, len(nets))
			for i, n := range nets {
				items[i] = n
			}
			common.Cli.RenderListing(items, networkColumns, style)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	return cmd
}

func newNetworkCreateCmd(op *api.Operation) *cobra.Command {
	var subnet string
	var ipv4Gateway string
	var noNAT bool
	var natGateways string
	var nonInteractive bool
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
				// Python raises typer.BadParameter — let Cobra handle the error
				return fmt.Errorf("missing required argument: name")
			}

			if subnet == "" {
				return fmt.Errorf("Missing required option '--subnet'")
			}

			if natGateways == "" && !noNAT && !nonInteractive {
				var err error
				natGateways, err = resolveUserNATGateways(cmd.Context())
				if err != nil {
					return err
				}
			}

			var natGatewaysList []string
			if natGateways != "" {
				for _, g := range strings.Split(natGateways, ",") {
					g = strings.TrimSpace(g)
					if g != "" {
						natGatewaysList = append(natGatewaysList, g)
					}
				}
			}

			var gw *string
			if ipv4Gateway != "" {
				gw = &ipv4Gateway
			}
			input := &inputs.NetworkCreateInput{
				Name:        name,
				Subnet:      subnet,
				IPv4Gateway: gw,
				NATEnabled:  !noNAT,
				NATGateways: natGatewaysList,
				SetDefault:  setDefault,
			}

			createResult := op.NetworkCreate(cmd.Context(), input)
			if createResult.IsError() {
				return fmt.Errorf("create network failed: %s", createResult.Message)
			}
			if createResult.Status == "skipped" {
				common.Cli.Info(createResult.Message)
				return nil
			}
			// NeedsInteraction fallback (matches Python's else branch)
			if createResult.Item == nil {
				return fmt.Errorf("create network failed")
			}
			net, ok := createResult.Item.(*model.Network)
			if !ok {
				return fmt.Errorf("create network failed: no network returned")
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
	cmd.Flags().
		StringVar(&natGateways, "nat-gateways", "", "Physical interfaces for NAT (comma-separated, auto-detected if not provided)")
	cmd.Flags().
		BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (auto-detect NAT interfaces)")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default network")
	return cmd
}

// resolveUserNATGateways prompts the user to select NAT gateway interfaces.
// Matches Python's _resolve_user_nat_gateways() exactly.
func resolveUserNATGateways(ctx context.Context) (string, error) {
	interfaces, err := infranet.GetPhysicalInterfaces()
	if err != nil {
		return "", fmt.Errorf("failed to list network interfaces: %w", err)
	}
	if len(interfaces) == 0 {
		return "", fmt.Errorf("no network interfaces found")
	}
	if len(interfaces) == 1 {
		return interfaces[0], nil
	}
	selected, err := common.Cli.PromptMultiSelect(ctx, "Select interface(s) for NAT (internet access):", interfaces, nil)
	if err != nil {
		return "", err
	}
	return strings.Join(selected, ","), nil
}

func newNetworkRemoveCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [names...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more networks by name.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}

			removeResult := op.NetworkRemove(cmd.Context(), &inputs.NetworkInput{Identifiers: args}, force)
			if removeResult.Status == "error" || removeResult.Status == "failure" {
				return fmt.Errorf("remove failed: %s", removeResult.Message)
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

func newNetworkInspectCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [name]",
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

			info, err := op.NetworkInspect(cmd.Context(), &inputs.NetworkInput{Identifiers: []string{name}})
			if err != nil {
				return fmt.Errorf("network not found: %s", name)
			}

			if jsonOutput {
				fmt.Println(common.MarshalJSONDefaultStr(info))
				return nil
			}

			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("Network: %s", info.Network.Name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newNetworkSyncCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "sync [names...]",
		Short:             "Sync iptables rules between database and host.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			var netInput *inputs.NetworkInput
			if len(args) > 0 {
				netInput = &inputs.NetworkInput{Identifiers: args}
			}
			syncResult := op.NetworkSync(cmd.Context(), netInput)
			if syncResult.Status == "error" || syncResult.Status == "failure" {
				return fmt.Errorf("sync failed: %s", syncResult.Message)
			}

			// results is a dict[net_id, {verified, added, orphaned}]
			// Python: if results is None → error
			results, ok := syncResult.Item.(map[string]map[string]int)
			if !ok || results == nil {
				return fmt.Errorf("sync returned no results")
			}

			if jsonOutput {
				fmt.Println(common.MarshalJSONDefaultStr(results))
				return nil
			}

			// Build name map from all networks
			allNetworks, listErr := op.NetworkListAll(cmd.Context())
			if listErr != nil {
				return listErr
			}
			nameMap := make(map[string]string)
			for _, n := range allNetworks {
				nameMap[n.ID] = n.Name
			}

			// Build table rows matching Python format
			rows := make([][]string, 0, len(results))
			for nid, counts := range results {
				shortID := common.Cli.FormatID(nid)
				name := nameMap[nid]
				if name == "" {
					if len(nid) > 8 {
						name = nid[:8]
					} else {
						name = nid
					}
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

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newNetworkDefaultCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "default [name]",
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

			defaultResult := op.NetworkSetDefault(cmd.Context(), &inputs.NetworkInput{Identifiers: []string{name}})
			if defaultResult.Status == "error" || defaultResult.Status == "failure" {
				return fmt.Errorf("set default failed: %s", defaultResult.Message)
			}

			common.Cli.Success(fmt.Sprintf("Default network set to: %s", name))
			return nil
		},
	}
	return cmd
}
