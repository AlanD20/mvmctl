package cli

import (
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"

	"github.com/spf13/cobra"
)

var execPolicyColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*results.VMToVMExecPolicy).ID) }},
	{Header: "Source", Extract: func(v any) string {
		policy := v.(*results.VMToVMExecPolicy)
		if policy.SourceVMName != "" {
			return policy.SourceVMName
		}
		return formatPolicySelector(results.VMPolicySelector{VMID: policy.SourceVMID, Labels: policy.SourceLabels})
	}},
	{Header: "Destination", Extract: func(v any) string {
		policy := v.(*results.VMToVMExecPolicy)
		if policy.DestinationVMName != "" {
			return policy.DestinationVMName
		}
		return formatPolicySelector(results.VMPolicySelector{VMID: policy.DestinationVMID, Labels: policy.DestinationLabels})
	}},
	{Header: "Target User", Extract: func(v any) string { return v.(*results.VMToVMExecPolicy).TargetUser }},
}

// NewPolicyCmd creates the separate traffic and execution policy families.
func NewPolicyCmd(policyAPI api.PolicyAPI) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Traffic and VM-to-VM execution policy management"}
	cmd.AddCommand(newTrafficPolicyCmd(policyAPI), newExecPolicyCmd(policyAPI))
	return cmd
}

func newExecPolicyCmd(policyAPI api.PolicyAPI) *cobra.Command {
	cmd := &cobra.Command{Use: "exec", Short: "Directional VM-to-VM execution policy management"}
	cmd.AddCommand(
		newExecPolicyCreateCmd(policyAPI),
		newExecPolicyListCmd(policyAPI),
		newExecPolicyRemoveCmd(policyAPI),
	)
	return cmd
}

func newExecPolicyCreateCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var input inputs.VMToVMExecPolicyCreateInput
	var sourceLabels, destinationLabels []string
	cmd := &cobra.Command{
		Use:               "create [source-vm destination-vm target-user]",
		Short:             "Allow directional execution as one non-root user",
		Args:              cobra.MaximumNArgs(3),
		ValidArgsFunction: completeExecPolicyCreate,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				if len(args) != 3 || input.SourceVM != "" || input.DestinationVM != "" ||
					input.TargetUser != "" || len(sourceLabels) != 0 || len(destinationLabels) != 0 {
					return errs.New(errs.CodeValidationFailed, "use three positional arguments or endpoint flags")
				}
				input.SourceVM, input.DestinationVM, input.TargetUser = args[0], args[1], args[2]
			}
			var err error
			input.SourceLabels, err = parsePolicyLabels(sourceLabels)
			if err != nil {
				return err
			}
			input.DestinationLabels, err = parsePolicyLabels(destinationLabels)
			if err != nil {
				return err
			}
			if err := input.Validate(); err != nil {
				return err
			}
			policy, err := policyAPI.ExecPolicyCreate(cmd.Context(), input)
			if err != nil {
				return err
			}
			if !common.RecordResult(policy) {
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM-to-VM execution policy")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input.SourceVM, "from-vm", "", "Exact source VM name or ID")
	cmd.Flags().StringArrayVar(&sourceLabels, "from-label", nil, "Source label key=value, repeat for AND")
	cmd.Flags().StringVar(&input.DestinationVM, "to-vm", "", "Exact destination VM name or ID")
	cmd.Flags().StringArrayVar(&destinationLabels, "to-label", nil, "Destination label key=value, repeat for AND")
	cmd.Flags().StringVar(&input.TargetUser, "user", "", "Destination non-root account")
	return cmd
}

func newExecPolicyListCmd(policyAPI api.PolicyAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List VM-to-VM execution policies.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policies, err := policyAPI.ExecPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			if common.RecordListResult(policies) {
				return nil
			}
			common.RenderListing(policies, execPolicyColumns, "short")
			return nil
		},
	}
	return cmd
}

func newExecPolicyRemoveCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use: "rm [policies...]", Aliases: []string{"remove", "delete", "del"},
		Short: "Remove VM-to-VM execution policies.", Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: completeExecPolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := common.Cli.PromptConfirm(
					cmd.Context(), fmt.Sprintf("Remove %d VM-to-VM execution policy(s)?", len(args)), false)
				if err != nil {
					return err
				}
				if !confirmed {
					return nil
				}
			}
			if err := policyAPI.ExecPolicyRemove(
				cmd.Context(), inputs.VMToVMExecPolicyInput{Identifiers: args},
			); err != nil {
				return err
			}
			if common.StructuredOutput() {
				return nil
			}
			common.Cli.Success(fmt.Sprintf("Removed %d VM-to-VM execution policy(s)", len(args)))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

func newTrafficPolicyCmd(policyAPI api.PolicyAPI) *cobra.Command {
	cmd := &cobra.Command{Use: "traffic", Short: "Manage desired TCP/UDP allow intent"}
	cmd.AddCommand(newTrafficPolicyCreateCmd(policyAPI), newTrafficPolicyListCmd(policyAPI),
		newTrafficPolicyInspectCmd(policyAPI), newTrafficPolicyRemoveCmd(policyAPI), newLabelTrafficPolicyCmd(policyAPI))
	return cmd
}

func newTrafficPolicyCreateCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var sourceNetwork, sourceVM, destination, destinationFloatingIP, destinationNetwork, protocol, port string
	var toHost bool
	cmd := &cobra.Command{
		Use: "create", Short: "Record one network-source or exact VM-source traffic allow intent.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (sourceNetwork == "") == (sourceVM == "") {
				return errs.New(errs.CodeValidationFailed, "exactly one of --from-network or --from-vm is required")
			}
			destinationCount := 0
			if destination != "" {
				destinationCount++
			}
			if destinationFloatingIP != "" {
				destinationCount++
			}
			if toHost {
				destinationCount++
			}
			if destinationCount != 1 {
				return errs.New(
					errs.CodeValidationFailed,
					"exactly one of --to-vm, --to-floating-ip or --to-host is required",
				)
			}
			if destinationNetwork != "" && destinationFloatingIP == "" {
				return errs.New(errs.CodeValidationFailed, "--to-network requires --to-floating-ip")
			}
			if destinationFloatingIP != "" && sourceNetwork != "" {
				policy, err := policyAPI.NetworkToFloatingIPTrafficPolicyCreate(
					cmd.Context(),
					inputs.NetworkToFloatingIPTrafficPolicyCreateInput{
						SourceNetwork:         sourceNetwork,
						DestinationFloatingIP: destinationFloatingIP,
						DestinationNetwork:    destinationNetwork,
						Protocol:              protocol,
						DestinationPort:       port,
					})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Network-to-floating-IP traffic allow intent")
				return nil
			}
			if destinationFloatingIP != "" {
				policy, err := policyAPI.VMToFloatingIPTrafficPolicyCreate(
					cmd.Context(),
					inputs.VMToFloatingIPTrafficPolicyCreateInput{
						SourceVM:              sourceVM,
						DestinationFloatingIP: destinationFloatingIP,
						DestinationNetwork:    destinationNetwork,
						Protocol:              protocol,
						DestinationPort:       port,
					})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM-to-floating-IP traffic allow intent")
				return nil
			}
			if toHost && sourceNetwork != "" {
				policy, err := policyAPI.NetworkToHostTrafficPolicyCreate(cmd.Context(),
					inputs.NetworkToHostTrafficPolicyCreateInput{
						SourceNetwork: sourceNetwork, Protocol: protocol, DestinationPort: port,
					})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Network-to-host traffic allow intent")
				return nil
			}
			if toHost {
				policy, err := policyAPI.VMToHostTrafficPolicyCreate(cmd.Context(),
					inputs.VMToHostTrafficPolicyCreateInput{
						SourceVM: sourceVM, Protocol: protocol, DestinationPort: port,
					})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM-to-host traffic allow intent")
				return nil
			}
			if sourceNetwork != "" {
				policy, err := policyAPI.NetworkToVMTrafficPolicyCreate(
					cmd.Context(),
					inputs.NetworkToVMTrafficPolicyCreateInput{
						SourceNetwork:   sourceNetwork,
						DestinationVM:   destination,
						Protocol:        protocol,
						DestinationPort: port,
					},
				)
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Network traffic allow intent")
				return nil
			}
			policy, err := policyAPI.VMToVMTrafficPolicyCreate(cmd.Context(), inputs.VMToVMTrafficPolicyCreateInput{
				SourceVM: sourceVM, DestinationVM: destination, Protocol: protocol, DestinationPort: port,
			})
			if err != nil {
				return err
			}
			if common.RecordResult(policy) {
				return nil
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM traffic allow intent")
			return nil
		},
	}
	cmd.Flags().StringVar(&sourceNetwork, "from-network", "", "Source managed network")
	cmd.Flags().StringVar(&sourceVM, "from-vm", "", "Exact source VM")
	cmd.Flags().StringVar(&destination, "to-vm", "", "Exact destination VM")
	cmd.Flags().
		StringVar(&destinationFloatingIP, "to-floating-ip", "", "Destination floating-IP name, ID, prefix or IPv4")
	cmd.Flags().StringVar(
		&destinationNetwork,
		"to-network",
		"",
		"Floating-IP destination network (default: default network)",
	)
	cmd.Flags().BoolVar(&toHost, "to-host", false, "Destination is the source managed network's host gateway")
	cmd.Flags().StringVar(&protocol, "protocol", "", "TCP/UDP protocol: tcp or udp")
	cmd.Flags().StringVar(&port, "port", "", "Destination port or bounded range")
	cmd.MarkFlagsMutuallyExclusive("from-network", "from-vm")
	cmd.MarkFlagsMutuallyExclusive("to-vm", "to-host", "to-floating-ip")
	if err := cmd.RegisterFlagCompletionFunc("from-network", completeNetworkNames); err != nil {
		panic(err)
	}
	if err := cmd.RegisterFlagCompletionFunc("from-vm", completeVMNames); err != nil {
		panic(err)
	}
	if err := cmd.RegisterFlagCompletionFunc("to-vm", completeVMNames); err != nil {
		panic(err)
	}
	return cmd
}

type trafficPolicyRow struct {
	id, sourceKind, source, destination, protocol, port string
}

var trafficPolicyColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(trafficPolicyRow).id) }},
	{Header: "Source Kind", Extract: func(v any) string { return v.(trafficPolicyRow).sourceKind }},
	{Header: "Source", Extract: func(v any) string { return v.(trafficPolicyRow).source }},
	{Header: "Destination", Extract: func(v any) string { return v.(trafficPolicyRow).destination }},
	{Header: "Protocol", Extract: func(v any) string { return v.(trafficPolicyRow).protocol }},
	{Header: "Destination Port", Extract: func(v any) string { return v.(trafficPolicyRow).port }},
}

func newTrafficPolicyListCmd(policyAPI api.PolicyAPI) *cobra.Command {
	return &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List desired traffic allow intent.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			networks, err := policyAPI.NetworkToVMTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			vms, err := policyAPI.VMToVMTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			networkHosts, err := policyAPI.NetworkToHostTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			vmHosts, err := policyAPI.VMToHostTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			networkFloatingIPs, err := policyAPI.NetworkToFloatingIPTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			if networkFloatingIPs == nil {
				networkFloatingIPs = []*results.NetworkToFloatingIPTrafficPolicy{}
			}
			vmFloatingIPs, err := policyAPI.VMToFloatingIPTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			if vmFloatingIPs == nil {
				vmFloatingIPs = []*results.VMToFloatingIPTrafficPolicy{}
			}
			if networks == nil {
				networks = []*results.NetworkToVMTrafficPolicy{}
			}
			if vms == nil {
				vms = []*results.VMToVMTrafficPolicy{}
			}
			if networkHosts == nil {
				networkHosts = []*results.NetworkToHostTrafficPolicy{}
			}
			if vmHosts == nil {
				vmHosts = []*results.VMToHostTrafficPolicy{}
			}
			if common.RecordResult(&results.TrafficPolicies{
				NetworkToVM: networks, VMToVM: vms, NetworkToHost: networkHosts, VMToHost: vmHosts,
				NetworkToFloatingIP: networkFloatingIPs, VMToFloatingIP: vmFloatingIPs,
			}) {
				return nil
			}
			rows := make([]trafficPolicyRow, 0, len(networks)+len(vms))
			for _, policy := range networks {
				rows = append(rows, trafficPolicyRow{
					policy.ID,
					"network",
					policy.SourceNetworkName,
					policy.DestinationVMName,
					string(policy.Protocol),
					trafficPolicyPorts(policy.DestinationPortStart, policy.DestinationPortEnd),
				})
			}
			for _, policy := range vms {
				rows = append(rows, trafficPolicyRow{
					policy.ID,
					"vm",
					policy.SourceVMName,
					policy.DestinationVMName,
					string(policy.Protocol),
					trafficPolicyPorts(policy.DestinationPortStart, policy.DestinationPortEnd),
				})
			}
			for _, policy := range networkHosts {
				rows = append(rows, trafficPolicyRow{policy.ID, "network", policy.SourceNetworkName,
					"host (" + policy.Gateway + ")", string(policy.Protocol),
					trafficPolicyPorts(policy.DestinationPortStart, policy.DestinationPortEnd)})
			}
			for _, policy := range vmHosts {
				rows = append(rows, trafficPolicyRow{policy.ID, "vm", policy.SourceVMName,
					"host (" + policy.Gateway + ")", string(policy.Protocol),
					trafficPolicyPorts(policy.DestinationPortStart, policy.DestinationPortEnd)})
			}
			for _, policy := range networkFloatingIPs {
				rows = append(rows, trafficPolicyRow{
					policy.ID,
					"network",
					policy.SourceNetworkName,
					"floating IP " + policy.DestinationFloatingIPName + " (" + policy.DestinationIPv4 + ")",
					string(policy.Protocol),
					trafficPolicyPorts(policy.DestinationPortStart, policy.DestinationPortEnd),
				})
			}
			for _, policy := range vmFloatingIPs {
				rows = append(rows, trafficPolicyRow{
					policy.ID,
					"vm",
					policy.SourceVMName,
					"floating IP " + policy.DestinationFloatingIPName + " (" + policy.DestinationIPv4 + ")",
					string(policy.Protocol),
					trafficPolicyPorts(policy.DestinationPortStart, policy.DestinationPortEnd),
				})
			}
			common.RenderListing(rows, trafficPolicyColumns, "short")
			return nil
		},
	}
}

func trafficPolicyPorts(start, end int) string {
	if start == end {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func newTrafficPolicyInspectCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var networkSource, vmSource, toHost, toFloatingIP bool
	cmd := &cobra.Command{
		Use: "inspect [policy]", Short: "Inspect desired traffic allow intent.", Args: cobra.ExactArgs(1),
		ValidArgsFunction: completePolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if networkSource == vmSource {
				return errs.New(errs.CodeValidationFailed, "exactly one of --from-network or --from-vm is required")
			}
			if toHost && toFloatingIP {
				return errs.New(errs.CodeValidationFailed, "--to-host and --to-floating-ip are mutually exclusive")
			}
			if toFloatingIP && networkSource {
				policy, err := policyAPI.NetworkToFloatingIPTrafficPolicyInspect(
					cmd.Context(),
					inputs.NetworkToFloatingIPTrafficPolicyInput{
						Identifiers: args,
					})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Network-to-floating-IP traffic allow intent")
				return nil
			}
			if toFloatingIP {
				policy, err := policyAPI.VMToFloatingIPTrafficPolicyInspect(
					cmd.Context(),
					inputs.VMToFloatingIPTrafficPolicyInput{
						Identifiers: args,
					})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM-to-floating-IP traffic allow intent")
				return nil
			}
			if toHost && networkSource {
				policy, err := policyAPI.NetworkToHostTrafficPolicyInspect(cmd.Context(),
					inputs.NetworkToHostTrafficPolicyInput{Identifiers: args})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Network-to-host traffic allow intent")
				return nil
			}
			if toHost {
				policy, err := policyAPI.VMToHostTrafficPolicyInspect(cmd.Context(),
					inputs.VMToHostTrafficPolicyInput{Identifiers: args})
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM-to-host traffic allow intent")
				return nil
			}
			if networkSource {
				policy, err := policyAPI.NetworkToVMTrafficPolicyInspect(
					cmd.Context(),
					inputs.NetworkToVMTrafficPolicyInput{Identifiers: args},
				)
				if err != nil {
					return err
				}
				if common.RecordResult(policy) {
					return nil
				}
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Network traffic allow intent")
				return nil
			}
			policy, err := policyAPI.VMToVMTrafficPolicyInspect(
				cmd.Context(),
				inputs.VMToVMTrafficPolicyInput{Identifiers: args},
			)
			if err != nil {
				return err
			}
			if common.RecordResult(policy) {
				return nil
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM traffic allow intent")
			return nil
		},
	}
	cmd.Flags().BoolVar(&networkSource, "from-network", false, "Select the network-source policy family")
	cmd.Flags().BoolVar(&vmSource, "from-vm", false, "Select the VM-source policy family")
	cmd.Flags().BoolVar(&toHost, "to-host", false, "Select grants to the managed host gateway")
	cmd.Flags().BoolVar(&toFloatingIP, "to-floating-ip", false, "Select floating-IP destination grants")
	cmd.MarkFlagsMutuallyExclusive("to-host", "to-floating-ip")
	return cmd
}

func newTrafficPolicyRemoveCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var networkSource, vmSource, force, toHost, toFloatingIP bool
	cmd := &cobra.Command{
		Use: "rm [policies...]", Aliases: []string{"remove", "delete", "del"},
		Short: "Remove desired traffic allow intent after authoritative revocation.", Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: completePolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if networkSource == vmSource {
				return errs.New(errs.CodeValidationFailed, "exactly one of --from-network or --from-vm is required")
			}
			if toHost && toFloatingIP {
				return errs.New(errs.CodeValidationFailed, "--to-host and --to-floating-ip are mutually exclusive")
			}
			if !force {
				confirmed, err := common.Cli.PromptConfirm(cmd.Context(),
					fmt.Sprintf("Remove %d traffic policy(s)?", len(args)), false)
				if err != nil {
					return err
				}
				if !confirmed {
					return nil
				}
			}
			var removeErr error
			switch {
			case toFloatingIP && networkSource:
				removeErr = policyAPI.NetworkToFloatingIPTrafficPolicyRemove(
					cmd.Context(),
					inputs.NetworkToFloatingIPTrafficPolicyInput{
						Identifiers: args,
					})
			case toFloatingIP:
				removeErr = policyAPI.VMToFloatingIPTrafficPolicyRemove(
					cmd.Context(),
					inputs.VMToFloatingIPTrafficPolicyInput{
						Identifiers: args,
					})
			case toHost && networkSource:
				removeErr = policyAPI.NetworkToHostTrafficPolicyRemove(cmd.Context(),
					inputs.NetworkToHostTrafficPolicyInput{Identifiers: args})
			case toHost:
				removeErr = policyAPI.VMToHostTrafficPolicyRemove(cmd.Context(),
					inputs.VMToHostTrafficPolicyInput{Identifiers: args})
			case networkSource:
				removeErr = policyAPI.NetworkToVMTrafficPolicyRemove(cmd.Context(),
					inputs.NetworkToVMTrafficPolicyInput{Identifiers: args})
			default:
				removeErr = policyAPI.VMToVMTrafficPolicyRemove(cmd.Context(),
					inputs.VMToVMTrafficPolicyInput{Identifiers: args})
			}
			if removeErr != nil {
				return removeErr
			}
			if common.StructuredOutput() {
				return nil
			}
			common.Cli.Success(fmt.Sprintf("Removed %d traffic policy(s)", len(args)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&networkSource, "from-network", false, "Select the network-source policy family")
	cmd.Flags().BoolVar(&vmSource, "from-vm", false, "Select the VM-source policy family")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	cmd.Flags().BoolVar(&toHost, "to-host", false, "Select grants to the managed host gateway")
	cmd.Flags().BoolVar(&toFloatingIP, "to-floating-ip", false, "Select floating-IP destination grants")
	cmd.MarkFlagsMutuallyExclusive("to-host", "to-floating-ip")
	return cmd
}
