// Package cli — guided onboarding wizard — thin CLI wrapper around InitOperation
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

func NewInitCmd(op *api.Operation) *cobra.Command {
	var nonInteractive bool
	var skipHost bool
	var skipNetwork bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: fmt.Sprintf("Initialize %s", infra.CLIName),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInitWizard(cmd.Context(), op, nonInteractive, skipHost, skipNetwork)
		},
	}

	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Use defaults, skip prompts")
	cmd.Flags().BoolVar(&skipHost, "skip-host", false, "Skip host init step")
	cmd.Flags().BoolVar(&skipNetwork, "skip-network", false, "Skip default network creation")

	// Hidden help subcommand matching Python's Typer "help" command
	helpCmd := &cobra.Command{
		Use:    "help",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Parent().Help()
		},
	}
	cmd.AddCommand(helpCmd)

	return cmd
}

// runInitWizard drives the init wizard, handling sudo and download prompts.
func runInitWizard(ctx context.Context, op *api.Operation, nonInteractive, skipHost, skipNetwork bool) error {
	// Match Python: mvm_cli.info("")
	common.Cli.Info("")
	// Match Python: mvm_cli.info(f"{CLI_NAME} init — first-time setup")
	common.Cli.Info(fmt.Sprintf("%s init — first-time setup", infra.CLIName))
	// Match Python: mvm_cli.info("─" * 40)
	common.Cli.Info(strings.Repeat("─", 40))

	// Call the Python-style _handle_interactive_flow logic
	result, err := handleInteractiveFlow(ctx, op, nonInteractive, skipHost, skipNetwork)
	if err != nil {
		return err
	}

	// Match Python init_run step display (after _handle_interactive_flow returns)
	stepLabels := map[string]string{
		"local_state":      "Local State",
		"service_binaries": "Service Binaries",
		"host":             fmt.Sprintf("sudoers / %s group", infra.MVMUnixGroup),
		"network_setup":    "Network Setup (Sync + Default)",
		"guestfs":          "libguestfs",
		"cache":            "Cache Directories",
		"binary":           "Firecracker Binary",
	}

	common.Cli.Info("")
	for _, step := range result.Steps {
		label := stepLabels[step.Step]
		if label == "" {
			label = step.Step
		}
		if step.Success {
			if step.Message != "" {
				common.Cli.Success(fmt.Sprintf("%s  (%s)", label, step.Message))
			} else {
				common.Cli.Success(label)
			}
		} else {
			common.Cli.Warning(fmt.Sprintf("%s — %s", label, step.Message))
		}
	}

	// Missing steps
	present := make(map[string]bool)
	for _, s := range result.Steps {
		present[s.Step] = true
	}
	for key, label := range stepLabels {
		if !present[key] {
			common.Cli.Warning(fmt.Sprintf("%s — not checked", label))
		}
	}

	common.Cli.Info("")
	if result.HostReady {
		common.Cli.Success("all set")
	} else {
		common.Cli.Warning(fmt.Sprintf("setup incomplete — run '%s init' again", infra.CLIName))
		return fmt.Errorf("setup incomplete")
	}
	return nil
}

// handleInteractiveFlow drives the init wizard with interaction handling.
// Matches Python's _handle_interactive_flow() — always returns the last
// InitResult (never nil), even when the loop breaks early.
func handleInteractiveFlow(
	ctx context.Context,
	op *api.Operation,
	nonInteractive, skipHost, skipNetwork bool,
) (*api.InitResult, error) {
	sudoCompleted := false
	var downloadVersion string
	hostSetupMessage := ""
	var guestfsEnabled *bool

	// Declare result at function scope so it's always returned,
	// matching Python: result: InitResult at the top of the function.
	var result *api.InitResult

	for {
		// Call InitOperation.Run with current state
		// Python: result = InitOperation.run(...)
		result = op.InitRunFull(ctx, skipHost, skipNetwork, nonInteractive, sudoCompleted, hostSetupMessage, downloadVersion, guestfsEnabled, nil)

		if result.NeedsInteraction == nil {
			return result, nil
		}

		interaction := result.NeedsInteraction

		// ── Handle sudo escalation ─────────────────────────────────────
		// Python: interaction.code == "privilege.sudo_required"
		if interaction.Code == "privilege.sudo_required" {
			// Run pre-flight probes before prompting for sudo
			// Python: from mvmctl.api.host_operations import HostOperation
			//         probe_result = HostOperation.check_readiness()
			probeResult := op.InitCheckReadiness(ctx)
			if len(probeResult.Critical) > 0 {
				common.Cli.Warning("Pre-flight checks found issues:")
				for _, c := range probeResult.Critical {
					common.Cli.Warning(fmt.Sprintf("  %s: %s", c.Name, c.Message))
				}
				// Python: if not typer.confirm(...): mvm_cli.info("Aborted"); raise typer.Exit(code=1)
				if !promptYesNo("Continue with host init? Some features may not work.", false) {
					common.Cli.Info("Aborted")
					break
				}
			}
			if len(probeResult.Warnings) > 0 {
				for _, w := range probeResult.Warnings {
					common.Cli.Info(fmt.Sprintf("  %s: %s", w.Name, w.Message))
				}
			}

			hostStateBefore := checkHostState()

			// Python: session_has_group = interaction.context.get("session_has_group", False)
			sessionHasGroup, _ := interaction.Context["session_has_group"].(bool)

			// Python: if group_exists and user_in_group and not session_has_group:
			//     show message about logout/newgrp, skip_host = True, continue
			if hostStateBefore["group_exists"] && hostStateBefore["user_in_group"] && !sessionHasGroup {
				common.Cli.Warning(fmt.Sprintf(
					"mvm group — session not active (log out and back in, or run: newgrp %s)",
					infra.MVMUnixGroup,
				))
				skipHost = true
				continue
			}

			if hostStateBefore["group_exists"] {
				common.Cli.Warning("sudoers file is missing")
				common.Cli.Info(fmt.Sprintf("run:  sudo %s host init", infra.CLIName))
			} else {
				common.Cli.Warning("this requires sudo once")
				common.Cli.Info(fmt.Sprintf("creates the %s group and sudoers drop-in for passwordless sudo on future runs", infra.MVMUnixGroup))
			}

			if nonInteractive {
				common.Cli.Info(fmt.Sprintf("Run 'sudo %s host init' manually.", infra.CLIName))
				break
			}

			if promptYesNo(fmt.Sprintf("Run 'sudo %s host init' now?", infra.CLIName), true) {
				proc := runWithSudo(ctx)
				if !proc.Success {
					common.Cli.Warning(fmt.Sprintf("host init failed. Run 'sudo %s host init' manually.", infra.CLIName))
					break
				}
				hostStateAfter := checkHostState()
				hostSetupMessage = composeHostSetupMessage(hostStateBefore, hostStateAfter)
				sudoCompleted = true
				downloadVersion = ""
				continue
			} else {
				common.Cli.Info(fmt.Sprintf("skipped. Run 'sudo %s host init' manually when ready.", infra.CLIName))
				break
			}
		}

		// ── Handle binary download confirmation ────────────────────────
		// Python: interaction.code == "binary.confirm_download"
		if interaction.Code == "binary.confirm_download" {
			latest, _ := interaction.Context["latest_version"].(string)
			if latest == "" {
				common.Cli.Warning("no Firecracker binary found and no remote versions available.")
				break
			}

			common.Cli.Info(fmt.Sprintf("latest available: v%s", latest))
			if nonInteractive || promptYesNo(fmt.Sprintf("Download v%s?", latest), true) {
				common.Cli.Info("")
				common.Cli.Info(fmt.Sprintf("downloading Firecracker v%s ...", latest))
				downloadVersion = latest
				continue
			} else {
				common.Cli.Info(fmt.Sprintf("skipped. Run '%s bin pull <version>' manually.", infra.CLIName))
				break
			}
		}

		// ── Handle guestfs enable prompt ──────────────────────────────
		// Python: interaction.code == "guestfs.confirm_enable"
		if interaction.Code == "guestfs.confirm_enable" {
			if nonInteractive {
				guestfsEnabled = ptr.Bool(false)
			} else {
				enabled := promptYesNo("Enable libguestfs as a provisioning fallback?", false)
				guestfsEnabled = &enabled
			}
			continue
		}

		common.Cli.Warning(fmt.Sprintf("unhandled interaction: %s", interaction.Code))
		break
	}

	// Python always returns the last result object, even when breaking early.
	// This ensures runInitWizard can display steps even on cancelled init.
	return result, nil
}

// sudoResult carries the outcome of a sudo subprocess.
type sudoResult struct {
	Success    bool
	ReturnCode int
}

// runWithSudo spawns "sudo host init" with elevated privileges.
// Matches Python: shutil.which(CLI_NAME) or sys.argv[0]
func runWithSudo(ctx context.Context) sudoResult {
	mvmBin, err := exec.LookPath(infra.CLIName)
	if err != nil {
		mvmBin, err = os.Executable()
		if err != nil {
			mvmBin = infra.CLIName
		}
	}

	// Build env var assignments — passed via the 'env' utility to sudo
	envAssignments := []string{infra.EnvKey("ESCALATED") + "=1"}
	for _, key := range []string{"MVM_CONFIG_DIR", "MVM_CACHE_DIR", "HOME", "PATH"} {
		if val := os.Getenv(key); val != "" {
			envAssignments = append(envAssignments, fmt.Sprintf("%s=%s", key, val))
		}
	}

	common.Cli.Info("")
	common.Cli.Info("Running host init with sudo...")

	// Use system.RunCmdCompat with the env utility to properly pass environment
	// variables through sudo's env_reset.
	runArgs := append([]string{"env"}, append(envAssignments, mvmBin, "host", "init")...)
	result := system.RunCmdCompat(ctx, runArgs, system.RunCmdOptions{
		Capture:    false,
		Check:      false,
		Privileged: true,
	})
	if !result.Success {
		return sudoResult{Success: false, ReturnCode: result.ExitCode}
	}
	return sudoResult{Success: true, ReturnCode: 0}
}

// checkHostState checks current host setup state, matching Python's
// _check_host_state() which uses grp.getgrnam() + g.gr_mem.
// Uses os/user.LookupGroup for group existence (NSS-compatible), and
// parses /etc/group for member list (matching Python's gr_mem behavior).
func checkHostState() map[string]bool {
	state := map[string]bool{
		"group_exists":   false,
		"sudoers_exists": false,
		"user_in_group":  false,
	}

	// Get current username (matching Python: pwd.getpwuid(os.getuid()).pw_name)
	currentUser, err := user.Current()
	if err != nil {
		return state
	}
	username := currentUser.Username

	// Check group existence using os/user.LookupGroup (NSS-compatible, like Python's grp.getgrnam)
	grpInfo, err := user.LookupGroup(infra.MVMUnixGroup)
	if err == nil {
		state["group_exists"] = true

		// Check membership: parse /etc/group for the member list (gr_mem),
		// matching Python's username in g.gr_mem.
		// Also check primary group GID.
		if currentUser.Gid == grpInfo.Gid {
			state["user_in_group"] = true
		} else {
			// Parse /etc/group for membership list
			f, openErr := os.Open("/etc/group")
			if openErr == nil {
				defer f.Close()
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					line := scanner.Text()
					fields := strings.Split(line, ":")
					if len(fields) >= 4 && fields[0] == infra.MVMUnixGroup {
						// fields[3] is the comma-separated member list (gr_mem)
						if fields[3] != "" {
							members := strings.Split(fields[3], ",")
							for _, member := range members {
								if strings.TrimSpace(member) == username {
									state["user_in_group"] = true
									break
								}
							}
						}
						break
					}
				}
			}
		}
	}

	// Check sudoers file (matching Python's Path(SUDOERS_DROP_IN_PATH).exists())
	sudoersPath := fmt.Sprintf("/etc/sudoers.d/%s", infra.MVMUnixGroup)
	if _, statErr := os.Stat(sudoersPath); statErr == nil {
		state["sudoers_exists"] = true
	}

	return state
}

// composeHostSetupMessage composes a human-readable message about what changed.
func composeHostSetupMessage(before, after map[string]bool) string {
	var parts []string
	if !before["group_exists"] && after["group_exists"] {
		parts = append(parts, "group created")
	}
	if !before["sudoers_exists"] && after["sudoers_exists"] {
		parts = append(parts, "sudoers configured")
	}
	if !before["user_in_group"] && after["user_in_group"] {
		parts = append(parts, "user added to group")
	}
	if len(parts) > 0 {
		return "Host " + strings.Join(parts, ", ")
	}
	return "Host already configured"
}

// promptYesNo asks a yes/no question and returns true for yes.
// Delegates to promptConfirm (canonical implementation in cache.go).
func promptYesNo(prompt string, defaultYes bool) bool {
	return common.Cli.PromptConfirm(prompt, defaultYes)
}
