// Package cli — host configuration commands, matching Python's cli/host.py
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
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
func abortIfVMsRunning(ctx context.Context, op *api.Operation, action string) error {
	running, err := op.HostGetRunningVMs(ctx)
	if err != nil {
		return nil
	}
	if len(running) > 0 {
		names := make([]string, 0, len(running))
		for _, v := range running {
			names = append(names, v.Name)
		}
		common.Cli.Error(fmt.Sprintf("%s blocked: VMs still running: %s", action, strings.Join(names, ", ")))
		common.Cli.Error("Stop all VMs first: mvm vm stop <name>")
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

	// Hidden help subcommand matching Python's hidden `help` command.
	cmd.AddCommand(&cobra.Command{
		Use:    "help",
		Short:  "Show help for the host command group.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Parent().Help()
		},
	})

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
			cacheDir, err := infra.GetCacheDir()
			if err != nil {
				return fmt.Errorf("cannot resolve cache directory: %w", err)
			}

			rawResult := op.HostInit(cmd.Context(), cacheDir, nil)

			switch v := rawResult.(type) {
			case *errs.NeedsInteraction:
				if v.Code == "privilege.sudo_required" {
					common.Cli.Warning("Root privileges required for: mvm host init")
					common.Cli.Info("Run with sudo: sudo mvm host init")
					// Python: typer.confirm("Run 'sudo mvm host init' now?", default=False)
					// Default is No (Enter = reject). Re-prompts on invalid input.
					confirmed := false
					for {
						fmt.Fprintf(os.Stderr, "Run 'sudo mvm host init' now? [y/N]: ")
						var response string
						_, err := fmt.Scanln(&response)
						if err != nil {
							break
						}
						response = strings.TrimSpace(strings.ToLower(response))
						if response == "y" || response == "yes" {
							confirmed = true
							break
						} else if response == "n" || response == "no" || response == "" {
							// Default (empty/Enter) = No
							break
						}
					}
					if confirmed {
						if sudoRestart, _ := infra.EnvGet("SUDO_RESTART"); sudoRestart != "" {
							common.Cli.Error("Recursive sudo restart detected. Aborting to prevent lockout.")
							common.Cli.Info("Please run 'sudo mvm host init' manually.")
							return fmt.Errorf("recursive sudo restart")
						}

						envAssignments := []string{
							infra.EnvKey("SUDO_RESTART") + "=1",
							infra.EnvKey("ESCALATED") + "=1",
						}
						for _, key := range []string{"MVM_CONFIG_DIR", "MVM_CACHE_DIR", "HOME", "PATH"} {
							if val := os.Getenv(key); val != "" {
								envAssignments = append(envAssignments, key+"="+val)
							}
						}

						sudoArgs := append([]string{"env"}, append(envAssignments, os.Args...)...)
						result := system.RunCmdCompat(cmd.Context(), append([]string{"sudo"}, sudoArgs...), system.RunCmdOptions{
							Capture: false,
							Check:   false,
						})
						if !result.Success && result.Err != nil {
							common.Cli.Error(fmt.Sprintf("sudo command failed: %s", result.Err.Error()))
						}
					}
					return fmt.Errorf("needs sudo")
				}

				// Other NeedsInteraction (not sudo_required)
				common.Cli.Error(v.Message)
				if detailsCtx, ok := v.Context["details"].(map[string]interface{}); ok {
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
				return fmt.Errorf("%s", v.Message)

			case *errs.OperationResult:
				if v.IsError() {
					common.Cli.Error(fmt.Sprintf("Host init failed: %s", v.Message))
					return fmt.Errorf("%s", v.Message)
				}

				if v.Status == "skipped" {
					common.Cli.Info(v.Message)
					return nil
				}

				// Show change summary matching Python's flow
				if v.Status == "success" {
					var changes []*model.HostStateChangeItem
					if metadata, ok := v.Metadata["changes"].([]*model.HostStateChangeItem); ok {
						changes = metadata
					}

					appliedChanges := 0
					for _, change := range changes {
						origVal := ""
						if change.OriginalValue != nil {
							origVal = *change.OriginalValue
						}
						if change.Mechanism == "noop" && change.Setting == "iptables_chains" {
							common.Cli.Warning(formatChange(change.Mechanism, change.Setting, change.AppliedValue, origVal))
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

					if wasUserAdded, ok := v.Metadata["user_added_to_group"].(bool); ok && wasUserAdded {
						common.Cli.Warning("Log out and back in for group membership to take effect")
						common.Cli.Info(fmt.Sprintf("Or run immediately: newgrp %s", infra.MVMUnixGroup))
					}
				}

			default:
				// Python: if not isinstance(result, OperationResult):
				//         mvm_cli.error(f"Unexpected result type: {type(result).__name__}")
				//         raise typer.Exit(code=1)
				common.Cli.Error(fmt.Sprintf("Unexpected result type: %T", v))
				return fmt.Errorf("unexpected result type: %T", v)
			}

			// Chown cache dir to real user (matching Python's FsUtils.chown_to_real_user)
			infra.ChownToRealUser(cacheDir)

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
			kvmOK := op.HostCheckKVMAccess()
			missing := op.HostCheckRequiredBinaries()

			ipFwd, err := op.HostGetIPForwardStatus(cmd.Context())
			if err != nil {
				ipFwd = "unknown"
			}
			fwdOK := ipFwd == "1"

			state, _ := op.HostGetState(cmd.Context())

			// Resource / virtualization checks
			resources, _ := op.HostDetectResources(cmd.Context())

			if jsonOutput {
				// Format state_snapshot timestamp matching Python's CommonUtils.human_readable_datetime
				var timestamp interface{}
				if state != nil && state.InitializedAt != "" {
					timestamp = common.Cli.FormatTimestamp(state.InitializedAt, "full")
				}

				data := map[string]interface{}{
					"kvm_accessible":    kvmOK,
					"required_binaries": map[string]interface{}{"ok": len(missing) == 0, "missing": missing},
					"ip_forward":        map[string]interface{}{"value": ipFwd, "ok": fwdOK},
					"state_snapshot": map[string]interface{}{
						"exists":    state != nil,
						"timestamp": timestamp,
					},
				}
				if resources != nil {
					data["virtualization"] = map[string]interface{}{
						"modules_loaded":    resources.ModulesLoaded,
						"nested_virt":       resources.ModulesLoaded["kvm_intel"] || resources.ModulesLoaded["kvm_amd"],
						"dev_net_tun":       resources.DevNetTUNAccessible,
						"user_in_kvm_group": resources.UserInKVMGroup,
					}
				}
				b, _ := json.MarshalIndent(data, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			rows := make([][]string, 0)

			kvmStatus := "ok"
			kvmDetail := "accessible"
			if !kvmOK {
				kvmStatus = "FAIL"
				kvmDetail = "not accessible"
			}
			rows = append(rows, []string{"/dev/kvm", kvmStatus, kvmDetail})

			binStatus := "ok"
			binDetail := "all found"
			if len(missing) > 0 {
				binStatus = "FAIL"
				binDetail = fmt.Sprintf("missing: %s", strings.Join(missing, ", "))
			}
			rows = append(rows, []string{"required binaries", binStatus, binDetail})

			fwdStatus := "ok"
			fwdDetail := fmt.Sprintf("value=%s", ipFwd)
			if !fwdOK {
				fwdStatus = "off"
			}
			rows = append(rows, []string{"ip_forward", fwdStatus, fwdDetail})

			// Format state snapshot timestamp matching Python's CommonUtils.human_readable_datetime
			stateStatus := "none"
			stateDetail := "no snapshot"
			if state != nil {
				stateStatus = "saved"
				if state.InitializedAt != "" {
					stateDetail = common.Cli.FormatTimestamp(state.InitializedAt, "full")
				} else {
					stateDetail = "no snapshot"
				}
			}
			rows = append(rows, []string{"state snapshot", stateStatus, stateDetail})

			if resources != nil {
				nestedOK := resources.ModulesLoaded["kvm_intel"] || resources.ModulesLoaded["kvm_amd"]
				nestedStatus := "-"
				nestedDetail := "not loaded"
				if nestedOK {
					nestedStatus = "ok"
					nestedDetail = "supported"
				}
				rows = append(rows, []string{"nested virt", nestedStatus, nestedDetail})

				kvmMod := resources.ModulesLoaded["kvm"]
				kvmModStatus := "FAIL"
				kvmModDetail := "not loaded"
				if kvmMod {
					kvmModStatus = "ok"
					kvmModDetail = "loaded"
				}
				rows = append(rows, []string{"kvm module", kvmModStatus, kvmModDetail})

				tunStatus := "FAIL"
				tunDetail := "not accessible"
				if resources.DevNetTUNAccessible {
					tunStatus = "ok"
					tunDetail = "accessible"
				}
				rows = append(rows, []string{"/dev/net/tun", tunStatus, tunDetail})

				userKVMStatus := "-"
				userKVMDetail := "not member"
				if resources.UserInKVMGroup {
					userKVMStatus = "ok"
					userKVMDetail = "member"
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
			var result *errs.OperationResult
			if refresh {
				result = op.HostRefreshCapacity(cmd.Context())
			} else {
				result = op.HostInfo(cmd.Context())
			}

			if result.IsError() {
				common.Cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}

			if result.Item == nil {
				common.Cli.Error("No host info available.")
				return fmt.Errorf("no host info available")
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(result.Item, "", "  ")
				fmt.Println(string(b))
			} else {
				printHostInfo(result.Item)
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
			if err := abortIfVMsRunning(cmd.Context(), op, "clean"); err != nil {
				return err
			}

			if !force {
				common.Cli.Warning("This will remove all VM networking: bridges, TAP devices, iptables rules, and the default network configuration.")
				common.Cli.Info(fmt.Sprintf("Sysctl settings, sudoers, and the '%s' group will NOT be affected.", infra.MVMUnixGroup))
				common.Cli.Info("")
				if !confirmRePrompt("Proceed with host clean?") {
					common.Cli.Info("Aborted")
					return nil
				}
			}

			cacheDir, err := infra.GetCacheDir()
			if err != nil {
				return fmt.Errorf("cannot resolve cache directory: %w", err)
			}
			result := op.HostClean(cmd.Context(), cacheDir)
			if result.IsError() {
				common.Cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}

			// Show per-item lines matching Python
			if result.Item != nil {
				if summary, ok := result.Item.([]string); ok {
					for _, item := range summary {
						if strings.HasPrefix(item, "Warning:") {
							remainder := strings.TrimSpace(item[len("Warning:"):])
							common.Cli.Warning(fmt.Sprintf("  %s", remainder))
						} else {
							common.Cli.Info(fmt.Sprintf("  %s", item))
						}
					}
				}
			}

			common.Cli.Success(result.Message)
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
			if err := abortIfVMsRunning(cmd.Context(), op, "reset"); err != nil {
				return err
			}

			if !force {
				common.Cli.Warning("This will tear down all networking, revert sysctl changes, remove the sudoers drop-in, and remove the project group. This is a full rollback to pre-init state.")
				common.Cli.Info("")
				if !confirmRePrompt("Proceed with host reset?") {
					common.Cli.Info("Aborted")
					return nil
				}
			}

			cacheDir, err := infra.GetCacheDir()
			if err != nil {
				return fmt.Errorf("cannot resolve cache directory: %w", err)
			}
			result := op.HostReset(cmd.Context(), cacheDir)
			if result.IsError() {
				common.Cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}

			// Show per-item lines matching Python
			if result.Item != nil {
				if summary, ok := result.Item.([]string); ok {
					for _, item := range summary {
						if strings.HasPrefix(item, "Warning:") {
							remainder := strings.TrimSpace(item[len("Warning:"):])
							common.Cli.Warning(fmt.Sprintf("  %s", remainder))
						} else {
							common.Cli.Info(fmt.Sprintf("  %s", item))
						}
					}
				}
			}

			common.Cli.Success(result.Message)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

// printHostInfo pretty-prints host info data matching Python's print_dict_tree.
func printHostInfo(item interface{}) {
	common.Cli.PrintDictTree(item, "Host Info")
}

// confirmRePrompt loops until the user enters y/n, matching Python's typer.confirm()
// with no default argument (which uses click.Choice(["y", "n"]) and re-prompts on empty input).
// Python's confirm prompt format: "Proceed with host clean? [y/N]: " (with colon+space).
func confirmRePrompt(prompt string) bool {
	for {
		fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
		var response string
		_, err := fmt.Scanln(&response)
		if err != nil {
			return false
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response == "y" || response == "yes" {
			return true
		}
		if response == "n" || response == "no" {
			return false
		}
		// Empty or invalid input → re-prompt (matching Python's behavior)
	}
}
