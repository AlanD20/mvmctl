// Package cli — host configuration commands, matching Python's cli/host.py
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api"

	"mvmctl/internal/cli/common"

	"github.com/spf13/cobra"
)

// formatChange returns a concise one-line description of a host change matching Python's _format_change.
func formatChange(mechanism, setting, appliedValue, originalValue string) string {
	switch mechanism {
	case "iptables_save":
		return fmt.Sprintf("iptables rules saved → %s", appliedValue)
	case "file_create", "file_remove":
		return fmt.Sprintf("%s: created %s", setting, appliedValue)
	case "groupadd":
		return fmt.Sprintf("group '%s' created", appliedValue)
	case "usermod":
		// Python: v.split(":") — unlimited splits. Only use 2-part form when exactly 2 parts.
		// For "user:group:extra", Python falls back to (v, v).
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
	// Fallback: Python uses repr-style formatting: f"{s}: {orig_display!r} → {v!r}"
	// In Go, use %q to reproduce repr semantics (proper escaping of quotes, backslashes, newlines).
	origDisplay := originalValue
	if len(origDisplay) > 50 {
		origDisplay = origDisplay[:50] + "…"
	}
	return fmt.Sprintf("%s: %q → %q", setting, origDisplay, appliedValue)
}

// abortIfVMsRunning exits with an error if any VMs are currently running.
func abortIfVMsRunning(ctx context.Context, op *api.Operation) error {
	running, err := op.HostGetRunningVMs(ctx)
	if err != nil {
		return nil
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
func NewHostCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Host configuration",
		Long: `Manage the host system configuration for running Firecracker microVMs.

Requires root privileges for most operations. Run with: sudo mvm host <command>`,
	}

	cmd.AddCommand(newHostInitCmd(op))
	cmd.AddCommand(newHostStatusCmd(op))
	cmd.AddCommand(newHostInfoCmd(op))
	cmd.AddCommand(newHostCleanCmd(op))
	cmd.AddCommand(newHostResetCmd(op))

	return cmd
}

func newHostInitCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Apply host configuration changes. Idempotent.",
		Long: fmt.Sprintf(`Apply host configuration changes. Idempotent.

This command must be run with sudo the first time. It performs the
following steps:

- Creates the '%s' system group and adds the current user to it.
- Installs a sudoers drop-in so group members can manage TAP devices,
  bridges, and iptables rules without a password.
- Enables IP forwarding (net.ipv4.ip_forward=1).
- Snapshots the pre-change host state so '%s host reset' can roll back.
- Creates the default network bridge.

After running, log out and back in (or run "newgrp mvm") for group
membership to take effect.

Examples:
  sudo mvm host init`, infra.MVMUnixGroup, infra.CLIName),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawResult, err := op.HostInit(cmd.Context(), nil)
			if err != nil {
				// NeedsInteraction flows through the error return
				var ni *errs.NeedsInteraction
				if errors.As(err, &ni) {
					if ni.Code == "privilege.sudo_required" {
						common.Cli.Warning("Root privileges required for: mvm host init")
						common.Cli.Info("Run with sudo: sudo mvm host init")
						confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Run 'sudo mvm host init' now?", false)
						if pErr != nil {
							return pErr
						}
						if confirmed {
							if sudoRestart, _ := infra.EnvGet("SUDO_RESTART"); sudoRestart != "" {
								common.Cli.Error("Recursive sudo restart detected. Aborting to prevent lockout.")
								common.Cli.Info("Please run 'sudo mvm host init' manually.")
								return fmt.Errorf("recursive sudo restart")
							}

							envAssignments := []string{}
							for _, env := range os.Environ() {
								if strings.HasPrefix(env, "MVM_") {
									envAssignments = append(envAssignments, env)
								}
							}
							for _, key := range []string{"HOME", "PATH"} {
								if val, ok := os.LookupEnv(key); ok {
									envAssignments = append(envAssignments, key+"="+val)
								}
							}
							envAssignments = append(envAssignments,
								infra.EnvKey("SUDO_RESTART")+"=1",
								infra.EnvKey("ESCALATED")+"=1",
							)

							sudoArgs := append([]string{"env"}, append(envAssignments, os.Args...)...)
							result := system.RunCmdCompat(
								cmd.Context(),
								append([]string{"sudo"}, sudoArgs...),
								system.RunCmdOpts{
									Capture: false,
									Check:   false,
								},
							)
							if !result.Success && result.Err != nil {
								common.Cli.Error(fmt.Sprintf("sudo command failed: %s", result.Err.Error()))
								if result.Stderr != "" {
									common.Cli.Warning(result.Stderr)
								}
								return result.Err
							}
							if !result.Success {
								return fmt.Errorf("sudo command failed with exit code %d", result.ExitCode)
							}
							common.Cli.Success("Host init completed successfully.")
						}
						return fmt.Errorf("needs sudo")
					}

					// Other NeedsInteraction (not sudo_required)
					common.Cli.Error(ni.Message)
					if detailsCtx, ok := ni.Context["details"].(map[string]any); ok {
						if detailMsg, ok := detailsCtx["message"].(string); ok && detailMsg != "" {
							common.Cli.Warning(fmt.Sprintf("Details: %s", detailMsg))
						}
						if suggestions, ok := detailsCtx["suggestions"].([]string); ok && len(suggestions) > 0 {
							common.Cli.Info("Options:")
							for _, s := range suggestions {
								common.Cli.Info(fmt.Sprintf("  - %s", s))
							}
						}
					}
					return fmt.Errorf("%s", ni.Message)
				}
				return err
			}

			// nil result = skipped (no changes)
			if rawResult == nil {
				common.Cli.Info("Host already configured — nothing to do.")
				return nil
			}

			// Success case - rawResult is a map of metadata
			meta, ok := rawResult.(map[string]any)
			if !ok {
				return fmt.Errorf("unexpected result type: %T", rawResult)
			}

			changes, _ := meta["changes"].([]*model.HostStateChangeItem)
			appliedChanges := 0
			for _, change := range changes {
				origVal := ""
				if change.OriginalValue != nil {
					origVal = *change.OriginalValue
				}
				if change.Mechanism == "noop" && change.Setting == "iptables_chains" {
					common.Cli.Warning(
						formatChange(change.Mechanism, change.Setting, change.AppliedValue, origVal),
					)
					continue
				}
				appliedChanges++
				common.Cli.Success(formatChange(change.Mechanism, change.Setting, change.AppliedValue, origVal))
			}

			if appliedChanges == 0 {
				common.Cli.Info("Host already configured — nothing to do.")
			} else {
				common.Cli.Success(fmt.Sprintf("Initialized: host (%d change(s) applied)", appliedChanges))
			}

			if wasUserAdded, ok := meta["user_added_to_group"].(bool); ok && wasUserAdded {
				common.Cli.Warning("Log out and back in for group membership to take effect")
				common.Cli.Info(fmt.Sprintf("Or run immediately: newgrp %s", infra.MVMUnixGroup))
			}

			return nil
		},
	}

	return cmd
}

func newHostStatusCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current host configuration state vs expected",
		RunE: func(cmd *cobra.Command, args []string) error {
			status := op.HostStatusCheck(cmd.Context())

			if jsonOutput {
				b, _ := json.MarshalIndent(status, "", "  ")
				fmt.Println(string(b))
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

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newHostInfoCmd(op *api.Operation) *cobra.Command {
	var refresh bool
	var jsonOutput bool

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
				result, err = op.HostRefreshCapacity(cmd.Context())
			} else {
				result, err = op.HostInfo(cmd.Context())
			}

			if err != nil {
				return err
			}

			if result == nil {
				return fmt.Errorf("no host info available")
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(result, "", "  ")
				fmt.Println(string(b))
			} else {
				printHostInfo(result)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&refresh, "refresh", false, "Re-detect host hardware and limits")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newHostCleanCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove all VM networking config (bridges, TAPs, iptables)",
		Long: fmt.Sprintf(`Remove all VM networking config (bridges, TAPs, iptables). Does not touch sysctl or group.

Sysctl settings, sudoers, and the '%s' group will NOT be affected.`, infra.MVMUnixGroup),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := abortIfVMsRunning(cmd.Context(), op); err != nil {
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

			summary, err := op.HostClean(cmd.Context())
			if err != nil {
				return err
			}

			// Show per-item lines matching Python
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

func newHostResetCmd(op *api.Operation) *cobra.Command {
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
			if err := abortIfVMsRunning(cmd.Context(), op); err != nil {
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

			summary, err := op.HostReset(cmd.Context())
			if err != nil {
				return err
			}

			// Show per-item lines matching Python
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
