package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/internal/cli/common"
)

// networkColumns defines the local listing columns for networks.
var networkColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.Network).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.Network).ID) }},
	{Header: "Name", Extract: func(v any) string { return common.Cli.FormatName(v.(*model.Network).Name, !v.(*model.Network).IsPresent) }},
	{Header: "Subnet", Extract: func(v any) string { return v.(*model.Network).Subnet }},
	{Header: "NAT", Extract: func(v any) string {
		if v.(*model.Network).NATEnabled { return "yes" }
		return "no"
	}},
	{Header: "Bridge", Extract: func(v any) string { return v.(*model.Network).Bridge }, LongOnly: true},
	{Header: "VMs", Extract: func(v any) string {
		l := v.(*model.Network).Leases
		if l != nil { return fmt.Sprintf("%d", len(l)) }
		return "0"
	}, LongOnly: true},
	{Header: "Created", Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.Network).CreatedAt, "relative") }},
}

// NewNetworkCmd creates the network command and its subcommands.
func NewNetworkCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Aliases: []string{"net"},
		Short:   "Network management",
	}

	cmd.AddCommand(newNetworkLsCmd(op))
	cmd.AddCommand(newNetworkCreateCmd(op))
	cmd.AddCommand(newNetworkRmCmd(op))
	cmd.AddCommand(newNetworkInspectCmd(op))
	cmd.AddCommand(newNetworkSyncCmd(op))
	cmd.AddCommand(newNetworkDefaultCmd(op))

	// Hidden help subcommand matching Python's @network_app.command(name="help", hidden=True)
	helpCmd := &cobra.Command{
		Use:    "help",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Show help for the parent (network) group, matching Python's ctx.parent.get_help()
			if parent := cmd.Parent(); parent != nil {
				parent.Help()
			}
		},
	}
	cmd.AddCommand(helpCmd)

	return cmd
}

func newNetworkLsCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all networks.",
		// Python's ls never takes positional arguments — no ValidArgsFunction needed
		RunE: func(cmd *cobra.Command, args []string) error {
			nets, err := op.NetworkListAll(cmd.Context())
			if err != nil {
				return err
			}

			if jsonOutput {
				data := make([]map[string]interface{}, 0, len(nets))
				for _, n := range nets {
					data = append(data, map[string]interface{}{"id": n.ID, "name": n.Name, "subnet": n.Subnet})
				}
				fmt.Println(marshalJSONDefaultStr(data))
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
		Use:                   "create [name]",
		Short:                 "Create a named network.",
		Args:                  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				var err error
				name, err = common.Cli.CheckNameArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				// Python raises typer.BadParameter — let Cobra handle the error
				return fmt.Errorf("missing required argument: name")
			}

			if subnet == "" {
				// Python: mvm_cli.error("Missing required option '--subnet'"); raise typer.Exit(code=1)
				// Return error once (Cobra prints it) — no double-print.
				return fmt.Errorf("Missing required option '--subnet'")
			}

			if natGateways == "" && !noNAT && !nonInteractive {
				var err error
				natGateways, err = resolveUserNATGateways()
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
				common.Cli.Error(createResult.Message)
				return fmt.Errorf("create network failed")
			}
			net, ok := createResult.Item.(*model.Network)
			if !ok {
				common.Cli.Error("Network created but no item returned")
				return fmt.Errorf("create network failed: no network returned")
			}

			common.Cli.Success(fmt.Sprintf("Created: %s", net.Name))
			common.Cli.Info(fmt.Sprintf("  SUBNET:    %s", net.Subnet))
			common.Cli.Info(fmt.Sprintf("  IPv4 Gateway: %s", net.IPv4Gateway))
			common.Cli.Info(fmt.Sprintf("  Bridge:  %s", net.Bridge))
			natDisplay := "False"
			if net.NATEnabled {
				natDisplay = "True"
			}
			common.Cli.Info(fmt.Sprintf("  NAT:     %s", natDisplay))
			if net.NATGateways != nil && *net.NATGateways != "" {
				natGwList := strings.Split(*net.NATGateways, ",")
				trimmed := make([]string, 0, len(natGwList))
				for _, gw := range natGwList {
					gw = strings.TrimSpace(gw)
					if gw != "" {
						trimmed = append(trimmed, gw)
					}
				}
				if len(trimmed) > 0 {
					common.Cli.Info(fmt.Sprintf("  NAT gateways: %s", strings.Join(trimmed, ", ")))
				}
			}

			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default network set to: %s", net.Name))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&subnet, "subnet", "", "IP subnet in SUBNET notation (e.g. 192.168.100.0/24)")
	cmd.Flags().StringVar(&ipv4Gateway, "ipv4-gateway", "", "Gateway IPv4 for the bridge")
	cmd.Flags().BoolVar(&noNAT, "no-nat", false, "Disable NAT/masquerade")
	cmd.Flags().StringVar(&natGateways, "nat-gateways", "", "Physical interfaces for NAT (comma-separated, auto-detected if not provided)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (auto-detect NAT interfaces)")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default network")
	return cmd
}

// resolveUserNATGateways prompts the user to select NAT gateway interfaces.
// Matches Python's _resolve_user_nat_gateways() exactly.
func resolveUserNATGateways() (string, error) {
	// Detect interfaces via /sys/class/net — matching Python's NetworkUtils.get_physical_interfaces()
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		// Python raises NetworkError("Failed to list network interfaces") which is caught by @handle_errors
		return "", fmt.Errorf("failed to list network interfaces: %w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no network interfaces found")
	}

	var interfaces []string
	for _, entry := range entries {
		name := entry.Name()
		// Match Python's _VIRTUAL_INTERFACE_PREFIXES and _EXCLUDED_INTERFACES
		if name == "lo" {
			continue
		}
		if strings.HasPrefix(name, "mvm-") || strings.HasPrefix(name, "tap") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") ||
			strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") {
			continue
		}
		interfaces = append(interfaces, name)
	}
	sort.Strings(interfaces) // Match Python's sorted(interfaces)

	if len(interfaces) == 0 {
		// Python raises typer.Exit(code=1) with "No network interfaces found"
		return "", fmt.Errorf("no network interfaces found")
	}
	if len(interfaces) == 1 {
		return interfaces[0], nil
	}

	common.Cli.Info("Select interface(s) for NAT (internet access):")
	for i, iface := range interfaces {
		common.Cli.Info(fmt.Sprintf("  [%d] %s", i+1, iface))
	}

	// Use bufio.Reader for proper line reading (matches Python's Prompt.ask which reads a full line)
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintf(os.Stderr, "Select interface number(s) [comma-separated] [1]: ")
	selected, _ := reader.ReadString('\n')
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return interfaces[0], nil
	}

	var selectedInterfaces []string
	for _, part := range strings.Split(selected, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
			return "", fmt.Errorf("invalid interface selection: %s", selected)
		}
		if idx < 1 || idx > len(interfaces) {
			return "", fmt.Errorf("invalid interface index: %d", idx)
		}
		selectedInterfaces = append(selectedInterfaces, interfaces[idx-1])
	}

	if len(selectedInterfaces) == 0 {
		return "", fmt.Errorf("no valid interface indices selected")
	}

	return strings.Join(selectedInterfaces, ","), nil
}

func newNetworkRmCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:                "rm [names...]",
		Aliases:            []string{"remove", "delete"},
		Short:              "Remove one or more networks by name.",
		Args:               cobra.ArbitraryArgs,
		ValidArgsFunction:  completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			names := args
			if len(names) == 0 {
				common.Cli.Error("Provide at least one network name")
				return fmt.Errorf("usage error")
			}

			removeResult := op.NetworkRemove(cmd.Context(), &inputs.NetworkInput{Name: names}, force)
			if removeResult.Status == "error" || removeResult.Status == "failure" {
				return fmt.Errorf("remove failed: %s", removeResult.Message)
			}

			for _, name := range names {
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
		Use:                "inspect [name]",
		Short:              "Show detailed information about a network.",
		Args:               cobra.MaximumNArgs(1),
		ValidArgsFunction:  completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				var err error
				name, err = common.Cli.CheckNameArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error: missing required argument\n")
				return fmt.Errorf("missing required argument")
			}

			info, err := op.NetworkInspect(cmd.Context(), &inputs.NetworkInput{Name: []string{name}})
				if err != nil {
					return fmt.Errorf("network not found: %s", name)
				}

			if jsonOutput {
				// Match Python's json.dumps(info, indent=2, default=str)
				fmt.Println(marshalJSONDefaultStr(info))
				return nil
			}

			netName := name
			if net, ok := info["network"].(map[string]interface{}); ok {
				if n, ok := net["name"].(string); ok {
					netName = n
				}
			}

			common.Cli.PrintDictTree(info, fmt.Sprintf("Network: %s", netName))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newNetworkSyncCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:                "sync [name]",
		Short:              "Sync iptables rules between database and host.",
		Args:               cobra.MaximumNArgs(1),
		ValidArgsFunction:  completeNetworkNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve network ID if name provided
			var networkID string
			if len(args) > 0 {
				name := args[0]
			info, err := op.NetworkInspect(cmd.Context(), &inputs.NetworkInput{Name: []string{name}})
				if err != nil {
					return fmt.Errorf("network not found: %s", name)
				}
				if n, ok := info["network"].(map[string]interface{}); ok {
					if id, ok := n["id"].(string); ok {
						networkID = id
					}
				}
			}

			// Call sync with optional network ID
			syncResult := op.NetworkSync(cmd.Context(), networkID)
			if syncResult.Status == "error" || syncResult.Status == "failure" {
				return fmt.Errorf("sync failed: %s", syncResult.Message)
			}

			// results is a dict[net_id, {verified, added, orphaned}]
			// Python: if results is None → error
			results, ok := syncResult.Item.(map[string]map[string]int)
			if !ok || results == nil {
				common.Cli.Error("Sync returned no results")
				return fmt.Errorf("sync returned no results")
			}

			if jsonOutput {
				fmt.Println(marshalJSONDefaultStr(results))
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
		Use:                "default [name]",
		Short:              "Set a network as the default for VM creation.",
		Args:               cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				var err error
				name, err = common.Cli.CheckNameArg(cmd, args[0])
				if err != nil {
					return err
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error: missing required argument\n")
				return fmt.Errorf("missing required argument")
			}

			defaultResult := op.NetworkSetDefault(cmd.Context(), &inputs.NetworkInput{Name: []string{name}})
			if defaultResult.Status == "error" || defaultResult.Status == "failure" {
				return fmt.Errorf("set default failed: %s", defaultResult.Message)
			}

			common.Cli.Success(fmt.Sprintf("Default network set to: %s", name))
			return nil
		},
	}
	return cmd
}

// marshalJSONDefaultStr marshals to JSON with Python's default=str semantics.
// On marshalling error, recursively converts non-serializable values to strings.
func marshalJSONDefaultStr(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		return string(b)
	}
	// Fallback: convert all non-serializable values to strings (default=str)
	v2 := convertToStringsRecursive(v)
	b, _ = json.MarshalIndent(v2, "", "  ")
	return string(b)
}

// convertToStringsRecursive recursively converts non-serializable Go types to strings.
// Handles the equivalent of Python's json.dumps(..., default=str).
func convertToStringsRecursive(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			out[k] = convertToStringsRecursive(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = convertToStringsRecursive(item)
		}
		return out
	default:
		// For structs, pointers, and non-serializable types, try MarshalJSON first
		if _, err := json.Marshal(v); err != nil {
			return fmt.Sprintf("%v", v)
		}
		return v
	}
}

