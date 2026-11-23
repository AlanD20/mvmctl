package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func newLabelTrafficPolicyCmd(policyAPI api.LabelTrafficPolicyAPI) *cobra.Command {
	cmd := &cobra.Command{Use: "labels", Short: "Manage label-selected VM traffic policies"}
	cmd.AddCommand(newLabelTrafficPolicyCreateCmd(policyAPI), newLabelTrafficPolicyListCmd(policyAPI),
		newLabelTrafficPolicyRemoveCmd(policyAPI))
	return cmd
}

func newLabelTrafficPolicyCreateCmd(policyAPI api.LabelTrafficPolicyAPI) *cobra.Command {
	var input inputs.LabelTrafficPolicyCreateInput
	var sourceLabels, destinationLabels []string
	cmd := &cobra.Command{
		Use: "create", Short: "Allow directional TCP or UDP traffic using VM labels", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			input.Source.Labels, err = parsePolicyLabels(sourceLabels)
			if err != nil {
				return err
			}
			input.Destination.Labels, err = parsePolicyLabels(destinationLabels)
			if err != nil {
				return err
			}
			policy, err := policyAPI.LabelTrafficPolicyCreate(cmd.Context(), input)
			if err != nil {
				return err
			}
			if !common.RecordResult(policy) {
				common.Cli.PrintDictTree(common.Cli.ToMap(policy), "Label traffic policy")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Source.VM, "from-vm", "", "Exact source VM name or ID")
	cmd.Flags().StringArrayVar(&sourceLabels, "from-label", nil, "Source label key=value, repeat for AND")
	cmd.Flags().StringVar(&input.Destination.VM, "to-vm", "", "Exact destination VM name or ID")
	cmd.Flags().StringArrayVar(&destinationLabels, "to-label", nil, "Destination label key=value, repeat for AND")
	cmd.Flags().StringVar(&input.Protocol, "protocol", "", "Transport protocol (tcp or udp)")
	cmd.Flags().StringVar(&input.DestinationPort, "port", "", "Destination port or inclusive range")
	return cmd
}

func parsePolicyLabels(arguments []string) (model.VMLabelSelector, error) {
	if len(arguments) == 0 {
		return nil, nil
	}
	labels, err := parseVMLabels(arguments)
	return model.VMLabelSelector(labels), err
}

func newLabelTrafficPolicyListCmd(policyAPI api.LabelTrafficPolicyAPI) *cobra.Command {
	return &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List label-selected traffic policies", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policies, err := policyAPI.LabelTrafficPolicyList(cmd.Context())
			if err != nil {
				return err
			}
			if common.RecordListResult(policies) {
				return nil
			}
			rows := make([]trafficPolicyRow, 0, len(policies))
			for _, policy := range policies {

				rows = append(rows, trafficPolicyRow{
					id: policy.ID, sourceKind: "vm", source: formatPolicySelector(policy.Source),
					destination: formatPolicySelector(
						policy.Destination,
					), protocol: string(policy.Protocol),
					port: trafficPolicyPorts(int(policy.DestinationPortStart), int(policy.DestinationPortEnd)),
				})
			}
			common.RenderListing(rows, trafficPolicyColumns, "short")
			return nil
		},
	}
}

func formatPolicySelector(endpoint results.VMPolicySelector) string {
	if endpoint.VMID != "" {
		return common.Cli.FormatID(endpoint.VMID)
	}
	pairs := make([]string, 0, len(endpoint.Labels))
	for _, key := range slices.Sorted(maps.Keys(endpoint.Labels)) {
		pairs = append(pairs, key+"="+endpoint.Labels[key])
	}
	return strings.Join(pairs, ", ")
}

func newLabelTrafficPolicyRemoveCmd(policyAPI api.LabelTrafficPolicyAPI) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use: "rm [policies...]", Aliases: []string{"remove"}, Short: "Remove label-selected traffic policies",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := common.Cli.PromptConfirm(
					cmd.Context(), fmt.Sprintf("Remove %d label traffic policy(s)?", len(args)), false,
				)
				if err != nil || !confirmed {
					return err
				}
			}
			input := inputs.LabelTrafficPolicyInput{Identifiers: args}
			if err := policyAPI.LabelTrafficPolicyRemove(cmd.Context(), input); err != nil {
				return err
			}
			if !common.StructuredOutput() {
				common.Cli.Success(fmt.Sprintf("Removed %d label traffic policy(s)", len(args)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}
