// Package cli — VM SSH commands, matching Python's cli/ssh.py
package cli

import (
	"slices"
	"strings"

	"mvmctl/internal/infra/crypto"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// completeVMNames provides shell completion for VM identifiers.
// Matches Python's _complete_vm_names() in cli/_completion.py.
func completeVMNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Use the existing List method with nil filter (= all VMs)
	vms := opRef.VMList(cmd.Context())
	if len(vms) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var results []string
	for _, vm := range vms {
		if vm.Name != "" && strings.HasPrefix(vm.Name, toComplete) && !slices.Contains(results, vm.Name) {
			results = append(results, vm.Name)
		}
		if vm.ID != "" {
			short := crypto.Truncate(vm.ID, 6)
			if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
				results = append(results, short)
			}
		}
		if vm.IPv4 != "" && strings.HasPrefix(vm.IPv4, toComplete) && !slices.Contains(results, vm.IPv4) {
			results = append(results, vm.IPv4)
		}
		if vm.MAC != "" && strings.HasPrefix(vm.MAC, toComplete) && !slices.Contains(results, vm.MAC) {
			results = append(results, vm.MAC)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

func NewSSHCmd(op *api.Operation) *cobra.Command {
	var userFlag string
	var key string
	var cmdStr string
	var timeout int

	cobraCmd := &cobra.Command{
		Use:   "ssh [vm-name]",
		Short: "VM SSH access",
		Long: `Open an SSH session into a VM.

Provide a VM identifier (name, ID prefix, IP, or MAC address) as the
positional argument.

Examples:
  mvm ssh my-vm
  mvm ssh my-vm --user admin
  mvm ssh my-vm --key ~/.ssh/id_rsa -c "ls -la"
  mvm ssh my-vm --timeout 30`,
		// Match Python's no_args_is_help=True: show help when no args given.
		// Use MaximumNArgs(1) instead of ExactArgs(1) so Cobra doesn't error
		// before RunE; we handle the 0-arg case by showing help.
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeVMNames,
		TraverseChildren:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Python's no_args_is_help=True: show help when no identifier provided
			if len(args) == 0 {
				return cmd.Help()
			}
			identifier := args[0]

			// Use nil semantics: only pass flag values when explicitly set by user.
			// Zero/empty values mean "not specified" (like Python's None),
			// and the API resolves defaults accordingly.
			input := inputs.SSHInput{
				Identifier: identifier,
			}
			if cmd.Flags().Changed("user") {
				input.User = &userFlag
			}
			if cmd.Flags().Changed("key") {
				input.Key = &key
			}
			if cmd.Flags().Changed("cmd") {
				input.Cmd = &cmdStr
			}
			if cmd.Flags().Changed("timeout") {
				input.Timeout = &timeout
			}

			if err := op.SSHConnect(cmd.Context(), input); err != nil {
				return err
			}
			return nil
		},
	}

	cobraCmd.Flags().StringVarP(&userFlag, "user", "u", "", "SSH user (default: from user config)")
	cobraCmd.Flags().StringVar(&key, "key", "", "SSH private key file or directory of keys")
	cobraCmd.Flags().StringVarP(&cmdStr, "cmd", "c", "", "Command to execute")
	cobraCmd.Flags().IntVarP(&timeout, "timeout", "t", 0, "SSH connection timeout in seconds")

	// Replicate Python's Typer context_settings={"allow_interspersed_args": True}.
	// Cobra/pflag intersperses flags and positional args by default, but make it
	// explicit to match Python behavior.
	cobraCmd.Flags().SetInterspersed(true)

	return cobraCmd
}
