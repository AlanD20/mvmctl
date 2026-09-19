// Package cli — host configuration commands
package cli

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/pkg/api"
	"mvmctl/pkg/errs"

	"mvmctl/internal/cli/common"

	"github.com/spf13/cobra"
)

// formatChange returns a concise one-line description of a host change.
func formatChange(mechanism, setting, appliedValue, originalValue string) string {
	switch mechanism {
	case "iptables_save":
		return fmt.Sprintf("iptables rules saved → %s", appliedValue)
	case "file_create", "file_remove":
		return fmt.Sprintf("%s: created %s", setting, appliedValue)
	case "groupadd":
		return fmt.Sprintf("group '%s' created", appliedValue)
	case "usermod":
		// Only use 2-part form when exactly 2 parts.
		// For "user:group:extra", fall back to displaying the full value.
		parts := strings.Split(appliedValue, ":")
		user := appliedValue
		group := appliedValue
		if len(parts) == 2 {
			user = parts[0]
			group = parts[1]
		}
		return fmt.Sprintf("user '%s' added to group '%s'", user, group)
	case "sysctl":
		if originalValue == "" {
			originalValue = "0"
		}
		return fmt.Sprintf("%s: %s → %s", setting, originalValue, appliedValue)
	case "noop":
		if setting == "iptables_chains" && appliedValue == "MVM chains already exist" {
			return "iptables chains already exist — keeping existing chain state"
		}
	case "modprobe":
		if setting == "kernel_module_load" {
			return fmt.Sprintf("loaded kernel module '%s'", appliedValue)
		}
	case "network_create":
		return fmt.Sprintf("Default network '%s' ready", appliedValue)
	}
	// Fallback: use %q for repr-style display (proper escaping of quotes, backslashes, newlines).
	origDisplay := originalValue
	if len(origDisplay) > 50 {
		origDisplay = origDisplay[:50] + "…"
	}
	return fmt.Sprintf("%s: %q → %q", setting, origDisplay, appliedValue)
}

// abortIfVMsRunning exits with an error if any VMs are currently running.
func abortIfVMsRunning(ctx context.Context, hostAPI api.HostAPI) error {
	running, err := hostAPI.HostGetRunningVMs(ctx)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		names := make([]string, 0, len(running))
		for _, v := range running {
			names = append(names, v.Name)
		}
		return fmt.Errorf("VMs still running: %s", strings.Join(names, ", "))
	}
	return nil
}

// NewHostCmd creates the host command and its subcommands.
func NewHostCmd(hostAPI api.HostAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Host configuration",
		Long: `Manage the host system configuration for running Firecracker microVMs.

Requires root privileges for most operations. Run with: sudo mvm host <command>`,
	}

	cmd.AddCommand(newHostInstallSystemCmd(hostAPI))
	cmd.AddCommand(newHostInitCmd(hostAPI))
	cmd.AddCommand(newHostStatusCmd(hostAPI))
	cmd.AddCommand(newHostInfoCmd(hostAPI))
	cmd.AddCommand(newHostCleanCmd(hostAPI))
	cmd.AddCommand(newHostResetCmd(hostAPI))

	return cmd
}

func newHostInstallSystemCmd(hostAPI api.HostAPI) *cobra.Command {
	return &cobra.Command{
		Use:   "install-system",
		Short: "Install or upgrade the trusted system mvm binary",
		Long: fmt.Sprintf(`Install or upgrade the trusted system mvm binary at %s.

This is an explicit administrator bootstrap operation. Run the trusted
artifact you intend to install with sudo:

  sudo <trusted-mvm-binary> host install-system`, infra.SystemBinaryPath),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			changed, err := hostAPI.HostInstallSystemBinary(cmd.Context())
			if common.RecordResult(changed) {
				return err
			}
			if err != nil {
				if changed {
					common.Cli.Warning(systemInstallPartialWarning(err))
				}
				return err
			}
			if !changed {
				common.Cli.Info(fmt.Sprintf("System binary already current at %s.", infra.SystemBinaryPath))
				return nil
			}
			common.Cli.Success(fmt.Sprintf("Installed trusted system binary at %s.", infra.SystemBinaryPath))
			return nil
		},
	}
}

func systemInstallPartialWarning(err error) string {
	if domainErr := errs.AsDomainError(err); domainErr != nil {
		if durabilityUncertain, _ := domainErr.Details["durability_uncertain"].(bool); durabilityUncertain {
			return fmt.Sprintf(
				"System binary was replaced at %s, but directory durability could not be confirmed.",
				infra.SystemBinaryPath,
			)
		}
	}
	return fmt.Sprintf(
		"System binary was replaced at %s, but final completion checks failed.",
		infra.SystemBinaryPath,
	)
}

func newHostInitCmd(hostAPI api.HostAPI) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize the host through the administrator entry",
		Long:  "Run sudo /usr/local/bin/mvm host init. Direct root must add --operator <non-root-user>.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := hostAPI.HostInit(cmd.Context(), nil)
			if err != nil {
				return err
			}
			if common.RecordResult(result) {
				return nil
			}
			for _, change := range result.Changes {
				original := ""
				if change.OriginalValue != nil {
					original = *change.OriginalValue
				}
				common.Cli.Success(formatChange(change.Mechanism, change.Setting, change.AppliedValue, original))
			}
			if result.UserAddedToGroup {
				common.Cli.Warning("Log out and back in for group membership to take effect")
			}
			return nil
		},
	}
}

func newHostStatusCmd(hostAPI api.HostAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current host configuration state vs expected",
		RunE: func(cmd *cobra.Command, args []string) error {
			status := hostAPI.HostStatusCheck(cmd.Context())

			if common.RecordResult(status) {
				return nil
			}

			rows := make([][]string, 0)

			kvmStatus, kvmDetail := "ok", "accessible"
			if !status.KVMOK {
				kvmStatus, kvmDetail = "FAIL", "not accessible"
			}
			rows = append(rows, []string{"/dev/kvm", kvmStatus, kvmDetail})

			binStatus, binDetail := "ok", "all found"
			if len(status.MissingBinaries) > 0 {
				binStatus, binDetail = "FAIL", fmt.Sprintf("missing: %s", strings.Join(status.MissingBinaries, ", "))
			}
			rows = append(rows, []string{"required binaries", binStatus, binDetail})

			fwdStatus, fwdDetail := "ok", fmt.Sprintf("value=%s", status.IPForward)
			if !status.IPForwardOK {
				fwdStatus = "off"
			}
			rows = append(rows, []string{"ip_forward", fwdStatus, fwdDetail})

			stateStatus, stateDetail := "none", "no snapshot"
			if status.State != nil {
				stateStatus = "saved"
				if status.State.InitializedAt != "" {
					stateDetail = common.Cli.FormatTimestamp(status.State.InitializedAt, "full")
				}
			}
			rows = append(rows, []string{"state snapshot", stateStatus, stateDetail})

			if r := status.Resources; r != nil {
				nestedOK := r.ModulesLoaded["kvm_intel"] || r.ModulesLoaded["kvm_amd"]
				nestedStatus, nestedDetail := "-", "not loaded"
				if nestedOK {
					nestedStatus, nestedDetail = "ok", "supported"
				}
				rows = append(rows, []string{"nested virt", nestedStatus, nestedDetail})

				kvmModStatus, kvmModDetail := "FAIL", "not loaded"
				if r.ModulesLoaded["kvm"] {
					kvmModStatus, kvmModDetail = "ok", "loaded"
				}
				rows = append(rows, []string{"kvm module", kvmModStatus, kvmModDetail})

				tunStatus, tunDetail := "FAIL", "not accessible"
				if r.DevNetTUNAccessible {
					tunStatus, tunDetail = "ok", "accessible"
				}
				rows = append(rows, []string{"/dev/net/tun", tunStatus, tunDetail})

				userKVMStatus, userKVMDetail := "-", "not member"
				if r.UserInKVMGroup {
					userKVMStatus, userKVMDetail = "ok", "member"
				}
				rows = append(rows, []string{"user in kvm group", userKVMStatus, userKVMDetail})
			}

			common.Cli.Table([]string{"Check", "Status", "Detail"}, rows)
			return nil
		},
	}

	return cmd
}

func newHostInfoCmd(hostAPI api.HostAPI) *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show host hardware, limits, and VM capacity projection",
		Long: `Show host hardware, limits, and VM capacity projection.

Displays detected CPU, memory, storage, kernel limits, current resource
usage, and a recommended maximum VM count based on available resources.

Use --refresh to re-detect hardware and limits before displaying.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var result any
			var err error
			if refresh {
				result, err = hostAPI.HostRefreshCapacity(cmd.Context())
			} else {
				result, err = hostAPI.HostInfo(cmd.Context())
			}

			if err != nil {
				return err
			}

			if result == nil {
				return fmt.Errorf("no host info available")
			}

			if common.RecordResult(result) {
				return nil
			}
			printHostInfo(result)

			return nil
		},
	}

	cmd.Flags().BoolVar(&refresh, "refresh", false, "Re-detect host hardware and limits")
	return cmd
}

func newHostCleanCmd(hostAPI api.HostAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove all VM networking config (bridges, TAPs, iptables)",
		Long: fmt.Sprintf(`Remove all VM networking config (bridges, TAPs, iptables). Does not touch sysctl or group.

Sysctl settings, sudoers, and the '%s' group will NOT be affected.`, infra.MVMUnixGroup),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := abortIfVMsRunning(cmd.Context(), hostAPI); err != nil {
				return err
			}

			if !force {
				common.Cli.Warning(
					"This will remove all VM networking: bridges, TAP devices, iptables rules, and the default network configuration.",
				)
				common.Cli.Info(
					fmt.Sprintf(
						"Sysctl settings, sudoers, and the '%s' group will NOT be affected.",
						infra.MVMUnixGroup,
					),
				)
				common.Cli.Info("")
				proceed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Proceed with host clean?", false)
				if pErr != nil {
					return pErr
				}
				if !proceed {
					common.Cli.Info("Aborted")
					return nil
				}
			}

			summary, err := hostAPI.HostClean(cmd.Context())
			if common.RecordResult(summary) {
				return err
			}
			if err != nil {
				return err
			}

			// Show per-item lines
			for _, item := range summary {
				if strings.HasPrefix(item, "Warning:") {
					remainder := strings.TrimSpace(item[len("Warning:"):])
					common.Cli.Warning(fmt.Sprintf("  %s", remainder))
				} else {
					common.Cli.Info(fmt.Sprintf("  %s", item))
				}
			}

			common.Cli.Success(fmt.Sprintf("Cleaned %d networking item(s)", len(summary)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

func newHostResetCmd(hostAPI api.HostAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Full rollback: remove networking, revert sysctl, remove sudoers and group",
		Long: fmt.Sprintf(`Full rollback: remove networking, revert sysctl, remove sudoers and group.

Reverts every change made by '%s host init':

- Tears down all network bridges, TAP devices, and iptables rules.
- Restores the original sysctl ip_forward value.
- Removes the sudoers drop-in file.
- Removes the '%s' system group.

All running VMs must be stopped before running this command.

Examples:
  sudo mvm host reset --force`, infra.CLIName, infra.MVMUnixGroup),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := abortIfVMsRunning(cmd.Context(), hostAPI); err != nil {
				return err
			}

			if !force {
				common.Cli.Warning(
					"This will tear down all networking, revert sysctl changes, remove the sudoers drop-in, and remove the project group. This is a full rollback to pre-init state.",
				)
				common.Cli.Info("")
				proceed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Proceed with host reset?", false)
				if pErr != nil {
					return pErr
				}
				if !proceed {
					common.Cli.Info("Aborted")
					return nil
				}
			}

			summary, err := hostAPI.HostReset(cmd.Context())
			if common.RecordResult(summary) {
				return err
			}
			if err != nil {
				return err
			}

			// Show per-item lines
			for _, item := range summary {
				if strings.HasPrefix(item, "Warning:") {
					remainder := strings.TrimSpace(item[len("Warning:"):])
					common.Cli.Warning(fmt.Sprintf("  %s", remainder))
				} else {
					common.Cli.Info(fmt.Sprintf("  %s", item))
				}
			}

			common.Cli.Success(fmt.Sprintf("Reset %d item(s)", len(summary)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

// printHostInfo pretty-prints host info data.
func printHostInfo(item any) {
	common.Cli.PrintDictTree(common.Cli.ToMap(item), "Host Info")
}
