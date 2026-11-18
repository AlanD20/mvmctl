package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

var floatingIPColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(value any) string { return common.Cli.FormatID(value.(*model.FloatingIPItem).ID) }},
	{Header: "Name", Extract: func(value any) string { return value.(*model.FloatingIPItem).Name }},
	{Header: "IPv4", Extract: func(value any) string { return value.(*model.FloatingIPItem).IPv4 }},
	{
		Header:   "Generation",
		Extract:  func(value any) string { return fmt.Sprint(value.(*model.FloatingIPItem).Generation) },
		LongOnly: true,
	},
}

// NewFloatingIPCmd manages root-owned reservations; association is a separate feature operation.
func NewFloatingIPCmd(floatingAPI api.FloatingIPAPI, configAPI api.ConfigAPI) *cobra.Command {
	var network string
	cmd := &cobra.Command{Use: "floating-ip", Short: "Manage floating IP resources"}
	cmd.PersistentFlags().StringVarP(&network, "network", "n", "", "Managed network (defaults to the default network)")
	_ = cmd.RegisterFlagCompletionFunc("network", completeNetworkNames)

	var address string
	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Reserve an explicit or automatically allocated IPv4 address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := floatingAPI.FloatingIPCreate(cmd.Context(), inputs.FloatingIPCreateInput{
				Network: network, Name: args[0], IPv4: address,
			})
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Floating IP: "+item.Name)
			}
			return nil
		},
	}
	create.Flags().StringVar(&address, "ip", "", "Usable IPv4 address in the network (omit to allocate)")
	cmd.AddCommand(create)

	var long bool
	list := &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List floating IPs in a managed network", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := floatingAPI.FloatingIPList(cmd.Context(), inputs.FloatingIPNetworkInput{Network: network})
			if err != nil {
				return err
			}
			if !common.RecordListResult(items) {
				common.RenderListing(
					items,
					floatingIPColumns,
					common.Cli.ResolveListingStyle(cmd.Context(), configAPI, long),
				)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&long, "long", false, "Show full listing with all columns")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect SELECTOR",
		Short: "Inspect a floating IP by name, ID, prefix or address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := floatingAPI.FloatingIPGet(
				cmd.Context(),
				inputs.FloatingIPInput{Network: network, Identifier: args[0]},
			)
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Floating IP: "+item.Name)
			}
			return nil
		},
	})
	var force bool
	remove := &cobra.Command{
		Use:     "rm SELECTOR",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Remove a floating IP reservation",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := common.Cli.PromptConfirm(cmd.Context(), "Remove floating IP "+args[0]+"?", false)
				if err != nil {
					return err
				}
				if !confirmed {
					return nil
				}
			}
			item, err := floatingAPI.FloatingIPRemove(
				cmd.Context(),
				inputs.FloatingIPInput{Network: network, Identifier: args[0]},
			)
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.Success("Removed floating IP: " + item.Name)
			}
			return nil
		},
	}
	remove.Flags().BoolVarP(&force, "force", "f", false, "Remove without confirmation")
	cmd.AddCommand(remove)
	cmd.AddCommand(newFloatingIPDelegationCmd(floatingAPI, configAPI, &network))
	return cmd
}
