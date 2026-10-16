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
	{Header: "Source VM", Extract: func(v any) string { return v.(*results.VMToVMExecPolicy).SourceVMName }},
	{Header: "Destination VM", Extract: func(v any) string { return v.(*results.VMToVMExecPolicy).DestinationVMName }},
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
	return &cobra.Command{
		Use:               "create [source-vm] [destination-vm] [target-user]",
		Short:             "Allow one VM to execute as one non-root user on another VM.",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeExecPolicyCreate,
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := policyAPI.ExecPolicyCreate(cmd.Context(), inputs.VMToVMExecPolicyCreateInput{
				SourceVM: args[0], DestinationVM: args[1], TargetUser: args[2],
			})
			if err != nil {
				return err
			}
			if common.RecordResult(policy) {
				return nil
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(policy), "VM-to-VM execution policy")
			return nil
		},
	}
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
		newTrafficPolicyInspectCmd(policyAPI), newTrafficPolicyRemoveCmd(policyAPI))
	return cmd
}

func newTrafficPolicyCreateCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var sourceNetwork, sourceVM, destination, protocol, port string
	cmd := &cobra.Command{
		Use: "create", Short: "Record one network-source or exact VM-source traffic allow intent.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (sourceNetwork == "") == (sourceVM == "") {
				return errs.New(errs.CodeValidationFailed, "exactly one of --from-network or --from-vm is required")
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
	cmd.Flags().StringVar(&protocol, "protocol", "", "TCP/UDP protocol: tcp or udp")
	cmd.Flags().StringVar(&port, "port", "", "Destination port or bounded range")
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
	{Header: "Destination VM", Extract: func(v any) string { return v.(trafficPolicyRow).destination }},
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
			if networks == nil {
				networks = []*results.NetworkToVMTrafficPolicy{}
			}
			if vms == nil {
				vms = []*results.VMToVMTrafficPolicy{}
			}
			if common.RecordResult(&results.TrafficPolicies{NetworkToVM: networks, VMToVM: vms}) {
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
	var networkSource, vmSource bool
	cmd := &cobra.Command{
		Use: "inspect [policy]", Short: "Inspect desired traffic allow intent.", Args: cobra.ExactArgs(1),
		ValidArgsFunction: completePolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if networkSource == vmSource {
				return errs.New(errs.CodeValidationFailed, "exactly one of --from-network or --from-vm is required")
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
	return cmd
}

func newTrafficPolicyRemoveCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var networkSource, vmSource, force bool
	cmd := &cobra.Command{
		Use: "rm [policies...]", Aliases: []string{"remove", "delete", "del"},
		Short: "Remove desired traffic allow intent after authoritative revocation.", Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: completePolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if networkSource == vmSource {
				return errs.New(errs.CodeValidationFailed, "exactly one of --from-network or --from-vm is required")
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
			if networkSource {
				if err := policyAPI.NetworkToVMTrafficPolicyRemove(cmd.Context(),
					inputs.NetworkToVMTrafficPolicyInput{Identifiers: args}); err != nil {
					return err
				}
			} else {
				if err := policyAPI.VMToVMTrafficPolicyRemove(cmd.Context(),
					inputs.VMToVMTrafficPolicyInput{Identifiers: args}); err != nil {
					return err
				}
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
	return cmd
}
