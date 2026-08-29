package cli

import (
	"encoding/json"
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"

	"github.com/spf13/cobra"
)

var policyColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*results.Policy).ID) }},
	{Header: "Source Network", Extract: func(v any) string { return v.(*results.Policy).SourceNetworkName }},
	{Header: "Destination VM", Extract: func(v any) string { return v.(*results.Policy).DestinationVMName }},
	{Header: "Protocol", Extract: func(v any) string { return string(v.(*results.Policy).Protocol) }},
	{Header: "Destination Port", Extract: func(v any) string {
		policy := v.(*results.Policy)
		if policy.DestinationPortStart == policy.DestinationPortEnd {
			return fmt.Sprintf("%d", policy.DestinationPortStart)
		}
		return fmt.Sprintf("%d-%d", policy.DestinationPortStart, policy.DestinationPortEnd)
	}},
}

// NewPolicyCmd creates the top-level routed service-access policy command.
func NewPolicyCmd(policyAPI api.PolicyAPI) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Routed service-access policy management"}
	cmd.AddCommand(newPolicyCreateCmd(policyAPI))
	cmd.AddCommand(newPolicyListCmd(policyAPI))
	cmd.AddCommand(newPolicyInspectCmd(policyAPI))
	cmd.AddCommand(newPolicyRemoveCmd(policyAPI))
	cmd.AddCommand(newPolicySyncCmd(policyAPI))
	return cmd
}

func newPolicyCreateCmd(policyAPI api.PolicyAPI) *cobra.Command {
	return &cobra.Command{
		Use:               "create [source-network] [destination-vm] [tcp|udp] [destination-port]",
		Short:             "Allow routed access from a network to one exact VM service.",
		Args:              cobra.ExactArgs(4),
		ValidArgsFunction: completePolicyCreate,
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := policyAPI.PolicyCreate(cmd.Context(), inputs.PolicyCreateInput{
				SourceNetwork: args[0], DestinationVM: args[1], Protocol: args[2], DestinationPort: args[3],
			})
			if err != nil {
				return err
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Service-access policy")
			return nil
		},
	}
}

func newPolicyListCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List service-access policies.",
		RunE: func(cmd *cobra.Command, args []string) error {
			policies, err := policyAPI.PolicyList(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOutput {
				if policies == nil {
					policies = []*results.Policy{}
				}
				data, err := json.MarshalIndent(policies, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			common.RenderListing(policies, policyColumns, "short")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newPolicyInspectCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:               "inspect [policy]",
		Short:             "Show one service-access policy.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completePolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := policyAPI.PolicyInspect(cmd.Context(), inputs.PolicyInput{Identifiers: args})
			if err != nil {
				return err
			}
			if jsonOutput {
				data, err := json.MarshalIndent(policy, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Service-access policy")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newPolicyRemoveCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:               "rm [policies...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove service-access policies.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completePolicyIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := common.Cli.PromptConfirm(
					cmd.Context(), fmt.Sprintf("Remove %d service-access policy(s)?", len(args)), false)
				if err != nil {
					return err
				}
				if !confirmed {
					return nil
				}
			}
			if err := policyAPI.PolicyRemove(cmd.Context(), inputs.PolicyInput{Identifiers: args}); err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Removed %d service-access policy(s)", len(args)))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

func newPolicySyncCmd(policyAPI api.PolicyAPI) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Regenerate and reconcile all service-access policy rules.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := policyAPI.PolicySync(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOutput {
				data, err := json.MarshalIndent(result, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			common.Cli.Success(fmt.Sprintf("Reconciled %d service-access policy(s)", result.Policies))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}
