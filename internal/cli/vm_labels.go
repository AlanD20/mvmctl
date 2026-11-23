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
	"mvmctl/pkg/errs"
)

func newVMLabelsCmd(labelsAPI api.VMLabelsAPI) *cobra.Command {
	root := &cobra.Command{Use: "labels", Short: "Inspect or replace VM labels"}
	root.AddCommand(&cobra.Command{
		Use:               "inspect <vm>",
		Short:             "Show a VM's labels and current revision",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := labelsAPI.VMLabelInspect(cmd.Context(), inputs.VMLabelInput{VM: args[0]})
			if err != nil {
				return err
			}
			return printVMLabels(cmd, result)
		},
	})
	var revision uint64
	set := &cobra.Command{
		Use:   "set <vm> [key=value...]",
		Short: "Replace all labels at the supplied revision",
		Long:  "Replace all VM labels. Use revision 0 for initial enrollment. Omit key=value arguments to clear labels.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			labels, err := parseVMLabels(args[1:])
			if err != nil {
				return err
			}
			result, err := labelsAPI.VMLabelUpdate(cmd.Context(), inputs.VMLabelUpdateInput{
				VMLabelInput: inputs.VMLabelInput{VM: args[0]}, ExpectedRevision: revision, Labels: labels,
			})
			if err != nil {
				return err
			}
			return printVMLabels(cmd, result)
		},
	}
	set.Flags().Uint64Var(&revision, "revision", 0, "Expected label revision from labels inspect")
	_ = set.MarkFlagRequired("revision")
	root.AddCommand(set)
	return root
}

func parseVMLabels(args []string) (model.VMLabels, error) {
	labels := make(model.VMLabels)
	for _, argument := range args {
		key, value, found := strings.Cut(argument, "=")
		if !found {
			return nil, errs.New(errs.CodeValidationFailed, "VM labels must use key=value")
		}
		if _, duplicate := labels[key]; duplicate {
			return nil, errs.New(errs.CodeValidationFailed, "duplicate VM label key")
		}
		labels[key] = value
	}
	return labels, nil
}

func printVMLabels(cmd *cobra.Command, result results.VMLabels) error {
	if common.RecordResult(result) {
		return nil
	}
	output := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(output, "VM: %s\nRevision: %d\n", result.VMID, result.Revision); err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(result.Labels)) {
		if _, err := fmt.Fprintf(output, "%s=%s\n", key, result.Labels[key]); err != nil {
			return err
		}
	}
	return nil
}
