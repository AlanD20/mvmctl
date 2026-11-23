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
			item, err := floatingAPI.FloatingIPInspect(
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
	cmd.AddCommand(
		newFloatingIPAssociateCmd(floatingAPI, &network),
		newFloatingIPTransferCmd(floatingAPI, &network),
		newFloatingIPReleaseCmd(floatingAPI, &network),
	)
	cmd.AddCommand(newFloatingIPDelegationCmd(floatingAPI, configAPI, &network))
	return cmd
}

func newFloatingIPAssociateCmd(floatingAPI api.FloatingIPAPI, network *string) *cobra.Command {
	var generation uint64
	cmd := &cobra.Command{
		Use:               "associate SELECTOR VM",
		Short:             "Associate a floating IP until release, transfer or VM stop",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeFloatingIPThenVM,
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := floatingAPI.FloatingIPAssociate(
				cmd.Context(),
				inputs.FloatingIPAssociateInput{
					Network:            *network,
					Identifier:         args[0],
					VM:                 args[1],
					ExpectedGeneration: generation,
				},
			)
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Floating IP: "+item.Name)
			}
			return nil
		},
	}
	cmd.Flags().
		Uint64Var(&generation, "generation", 0, "Expected ownership generation (omit to observe immediately before mutation)")
	return cmd
}
func newFloatingIPTransferCmd(floatingAPI api.FloatingIPAPI, network *string) *cobra.Command {
	var generation uint64
	cmd := &cobra.Command{
		Use:               "transfer SELECTOR VM",
		Short:             "Fence the current owner and transfer the floating IP",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeFloatingIPThenVM,
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := floatingAPI.FloatingIPTransfer(
				cmd.Context(),
				inputs.FloatingIPTransferInput{
					Network:            *network,
					Identifier:         args[0],
					VM:                 args[1],
					ExpectedGeneration: generation,
				},
			)
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Floating IP: "+item.Name)
			}
			return nil
		},
	}
	cmd.Flags().
		Uint64Var(&generation, "generation", 0, "Expected ownership generation (omit to observe immediately before mutation)")
	return cmd
}
func newFloatingIPReleaseCmd(floatingAPI api.FloatingIPAPI, network *string) *cobra.Command {
	var generation uint64
	cmd := &cobra.Command{
		Use:               "release SELECTOR",
		Aliases:           []string{"disassociate"},
		Short:             "Fence and release the current floating-IP owner",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFloatingIPNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := floatingAPI.FloatingIPRelease(
				cmd.Context(),
				inputs.FloatingIPReleaseInput{Network: *network, Identifier: args[0], ExpectedGeneration: generation},
			)
			if err != nil {
				return err
			}
			if !common.RecordResult(item) {
				common.Cli.PrintDictTree(common.Cli.ToMap(item), "Floating IP: "+item.Name)
			}
			return nil
		},
	}
	cmd.Flags().
		Uint64Var(&generation, "generation", 0, "Expected ownership generation (omit to observe immediately before mutation)")
	return cmd
}

func completeFloatingIPThenVM(
	cmd *cobra.Command,
	args []string,
	toComplete string,
) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeFloatingIPNames(cmd, args, toComplete)
	}
	if len(args) == 1 {
		return completeVMNames(cmd, args[1:], toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeFloatingIPNames(
	cmd *cobra.Command,
	args []string,
	toComplete string,
) ([]string, cobra.ShellCompDirective) {
	if opRef == nil || len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	network, _ := cmd.Flags().GetString("network")
	items, err := opRef.FloatingIPList(cmd.Context(), inputs.FloatingIPNetworkInput{Network: network})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Name, toComplete) {
			names = append(names, item.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
