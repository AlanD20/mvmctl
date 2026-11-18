package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

func newFloatingIPDelegationCmd(
	delegationAPI api.FloatingIPDelegationAPI, configAPI api.ConfigAPI, network *string,
) *cobra.Command {
	cmd := &cobra.Command{Use: "delegation", Short: "Administer stored guest permissions (worker readiness is separate)"}
	var target, method, lease string
	grant := &cobra.Command{Use: "grant RESOURCE", Short: "Store one immutable permission for the current VM incarnation",
		Args: cobra.ExactArgs(1), ValidArgsFunction: completeFloatingIPResources,
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := delegationAPI.FloatingIPDelegationGrant(cmd.Context(), inputs.FloatingIPDelegationGrantInput{
				Network: *network, Resource: args[0], VM: target, Method: method, Lease: lease})
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Stored delegation; worker readiness is separate")
			}
			return nil
		}}
	grant.Flags().StringVar(&target, "vm", "", "Target VM (required)")
	grant.Flags().StringVar(&method, "method", "", "Exact guest method: inspect, associate, renew or release (required)")
	grant.Flags().StringVar(&lease, "lease", "", "Explicit finite whole-second lease for associate/renew")
	_ = grant.RegisterFlagCompletionFunc("vm", completeVMNames)
	_ = grant.RegisterFlagCompletionFunc("method", func(*cobra.Command, []string, string) ([]string,
		cobra.ShellCompDirective) {
		return []string{"inspect", "associate", "renew", "release"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.AddCommand(grant)
	var long bool
	list := &cobra.Command{Use: "ls RESOURCE", Aliases: []string{"list"},
		Short: "List stored grants, including revoked lifetimes",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: completeFloatingIPResources,
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := delegationAPI.FloatingIPDelegationList(cmd.Context(),
				inputs.FloatingIPInput{Network: *network, Identifier: args[0]})
			if err != nil {
				return err
			}
			if !common.RecordListResult(items) {
				common.RenderListing(items, floatingIPDelegationColumns,
					common.Cli.ResolveListingStyle(cmd.Context(), configAPI, long))
			}
			return nil
		}}
	list.Flags().BoolVar(&long, "long", false, "Show stored incarnation and lease policy")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{Use: "inspect GRANT_ID", Short: "Inspect an exact stored grant",
		Args: cobra.ExactArgs(1), ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := delegationAPI.FloatingIPDelegationGet(cmd.Context(),
				inputs.FloatingIPDelegationInput{Network: *network, ID: args[0]})
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Stored delegation")
			}
			return nil
		}})
	cmd.AddCommand(&cobra.Command{Use: "revoke GRANT_ID", Short: "Revoke permission and fence ownership it sustained",
		Args: cobra.ExactArgs(1), ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := delegationAPI.FloatingIPDelegationRevoke(cmd.Context(),
				inputs.FloatingIPDelegationInput{Network: *network, ID: args[0]})
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.Success("Revoked delegation: " + item.ID)
			}
			return nil
		}})
	return cmd
}

var floatingIPDelegationColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.FloatingIPDelegationItem).ID) }},
	{Header: "VM", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.FloatingIPDelegationItem).VMID) }},
	{Header: "Method", Extract: func(v any) string { return v.(*model.FloatingIPDelegationItem).Method }},
	{Header: "State", Extract: func(v any) string { return v.(*model.FloatingIPDelegationItem).State }},
	{Header: "Lease", Extract: func(v any) string { return v.(*model.FloatingIPDelegationItem).LeaseDuration.String() }},
	{
		Header:   "Revision",
		Extract:  func(v any) string { return fmt.Sprint(v.(*model.FloatingIPDelegationItem).Revision) },
		LongOnly: true,
	},
	{
		Header:   "Launch",
		Extract:  func(v any) string { return fmt.Sprint(v.(*model.FloatingIPDelegationItem).LaunchGeneration) },
		LongOnly: true,
	},
}

func completeFloatingIPResources(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	selected, _ := cmd.Flags().GetString("network")
	items, err := opRef.FloatingIPList(cmd.Context(), inputs.FloatingIPNetworkInput{Network: selected})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	result := []string{}
	for _, item := range items {
		for _, value := range []string{item.Name, item.ID} {
			if strings.HasPrefix(value, prefix) {
				result = append(result, value)
			}
		}
	}
	return result, cobra.ShellCompDirectiveNoFileComp
}
