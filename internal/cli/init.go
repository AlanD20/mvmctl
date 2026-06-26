// Package cli — guided onboarding wizard — thin CLI wrapper around InitOperation
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"

	"github.com/spf13/cobra"
)

func NewInitCmd(initAPI api.InitAPI, hostAPI api.HostAPI) *cobra.Command {
	var nonInteractive bool
	var skipHost bool
	var skipNetwork bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: fmt.Sprintf("Initialize %s", infra.CLIName),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInitWizard(cmd.Context(), initAPI, hostAPI, nonInteractive, skipHost, skipNetwork)
		},
	}

	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Use defaults, skip prompts")
	cmd.Flags().BoolVar(&skipHost, "skip-host", false, "Skip host init step")
	cmd.Flags().BoolVar(&skipNetwork, "skip-network", false, "Skip default network creation")

	return cmd
}

// runInitWizard drives the init wizard, handling sudo and download prompts.
func runInitWizard(
	ctx context.Context,
	initAPI api.InitAPI,
	hostAPI api.HostAPI,
	nonInteractive, skipHost, skipNetwork bool,
) error {
	common.Cli.Info("")
	common.Cli.Info(fmt.Sprintf("%s init — first-time setup", infra.CLIName))
	common.Cli.Info(strings.Repeat("-", 40))
	result, err := handleInteractiveFlow(ctx, initAPI, hostAPI, nonInteractive, skipHost, skipNetwork)
	if err != nil {
		return err
	}

	// Display init step results
	stepLabels := map[string]string{
		"local_state":   "Local State",
		"host":          fmt.Sprintf("sudoers / %s group", infra.MVMUnixGroup),
		"network_setup": "Network Setup (Sync + Default)",
		"guestfs":       "libguestfs",
		"cache":         "Cache Directories",
		"binary":        "Firecracker Binary",
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

// initState holds mutable state for the init wizard interaction loop.
type initState struct {
	initAPI        api.InitAPI
	hostAPI        api.HostAPI
	nonInteractive bool
	skipHost       bool
	skipNetwork    bool

	// Mutable state updated by interaction handlers
	sudoCompleted   bool
	downloadVersion string
	hostSetupMsg    string
	guestfsEnabled  *bool
}

// runInit calls InitRunFull with current state.
func (s *initState) runInit(ctx context.Context) *results.InitResult {
	return s.initAPI.InitRunFull(
		ctx,
		s.skipHost,
		s.skipNetwork,
		s.nonInteractive,
		s.sudoCompleted,
		s.hostSetupMsg,
		s.downloadVersion,
		s.guestfsEnabled,
		nil,
	)
}

// dispatch routes each interaction code to its handler.
// Returns an error when the loop should abort (unhandled code, user decline).
// Returns nil to continue the loop.
func (s *initState) dispatch(ctx context.Context, interaction *errs.NeedsInteraction) error {
	switch interaction.Code {
	case "privilege.sudo_required":
		return s.handleSudoRequired(ctx, interaction)
	case "binary.confirm_download":
		return s.handleBinaryDownload(ctx, interaction)
	case "guestfs.confirm_enable":
		return s.handleGuestfs(ctx)
	default:
		return fmt.Errorf("unhandled interaction: %s", interaction.Code)
	}
}

// handleSudoRequired manages sudo escalation: pre-flight probes, prompts,
// running sudo host init, and updating loop state.
func (s *initState) handleSudoRequired(ctx context.Context, interaction *errs.NeedsInteraction) error {
	// Run pre-flight probes before prompting for sudo
	probeResult := s.initAPI.InitCheckReadiness(ctx)

	// In non-interactive mode: run host init directly (relies on passwordless sudo).
	// Mark as completed after attempt — even on probe failures (e.g. no KVM inside
	// a Firecracker VM) — to prevent an infinite loop. Degraded state is acceptable;
	// the wizard summary will report "setup incomplete".
	if s.nonInteractive {
		common.RunWithSudo(ctx, []string{"host", "init"}, infra.EnvKey("ESCALATED")+"=1")
		s.sudoCompleted = true
		return nil
	}

	if len(probeResult.Critical) > 0 {
		common.Cli.Warning("Pre-flight checks found issues:")
		for _, c := range probeResult.Critical {
			common.Cli.Warning(fmt.Sprintf("  %s: %s", c.Name, c.Message))
		}
		confirmed, pErr := common.Cli.PromptConfirm(ctx, "Continue with host init? Some features may not work.", false)
		if pErr != nil {
			return pErr
		}
		if !confirmed {
			common.Cli.Info("Aborted")
			return fmt.Errorf("aborted by user")
		}
	}
	if len(probeResult.Warnings) > 0 {
		for _, w := range probeResult.Warnings {
			common.Cli.Info(fmt.Sprintf("  %s: %s", w.Name, w.Message))
		}
	}

	hostStateBefore := s.hostAPI.HostStatusCheck(ctx)

	sessionHasGroup, _ := interaction.Context["session_has_group"].(bool)

	// Group exists, user is member, but session doesn't have it active
	if hostStateBefore.GroupExists && hostStateBefore.UserInGroup && !sessionHasGroup {
		common.Cli.Warning(fmt.Sprintf(
			"mvm group — session not active (log out and back in, or run: newgrp %s)",
			infra.MVMUnixGroup,
		))
		s.skipHost = true
		return nil
	}

	sudoCmd, _ := interaction.Context["command"].(string)
	if sudoCmd == "" {
		sudoCmd = fmt.Sprintf("sudo %s host init", infra.CLIName)
	}

	if hostStateBefore.GroupExists {
		common.Cli.Warning("sudoers file is missing")
		common.Cli.Info(fmt.Sprintf("run:  %s", sudoCmd))
	} else {
		common.Cli.Warning("this requires sudo once")
		common.Cli.Info(fmt.Sprintf(
			"creates the %s group and sudoers drop-in for passwordless sudo on future runs",
			infra.MVMUnixGroup,
		))
	}

	runInit, pErr := common.Cli.PromptConfirm(ctx, fmt.Sprintf("Run '%s' now?", sudoCmd), true)
	if pErr != nil {
		return pErr
	}
	if !runInit {
		common.Cli.Info(fmt.Sprintf("skipped. Run '%s' manually when ready.", sudoCmd))
		return fmt.Errorf("skipped by user")
	}

	proc := common.RunWithSudo(ctx, []string{"host", "init"}, infra.EnvKey("ESCALATED")+"=1")
	if !proc.Success {
		common.Cli.Warning(fmt.Sprintf("host init failed. Run '%s' manually.", sudoCmd))
		return fmt.Errorf("sudo host init failed")
	}

	hostStateAfter := s.hostAPI.HostStatusCheck(ctx)
	s.hostSetupMsg = composeHostSetupMessage(hostStateBefore, hostStateAfter)
	s.sudoCompleted = true
	s.downloadVersion = ""
	return nil
}

// handleBinaryDownload manages the binary download confirmation prompt.
func (s *initState) handleBinaryDownload(ctx context.Context, interaction *errs.NeedsInteraction) error {
	latest, _ := interaction.Context["latest_version"].(string)
	if latest == "" {
		common.Cli.Warning("no Firecracker binary found and no remote versions available.")
		return fmt.Errorf("no binary available")
	}

	common.Cli.Info(fmt.Sprintf("latest available: v%s", latest))
	if !s.nonInteractive {
		confirmed, pErr := common.Cli.PromptConfirm(ctx, fmt.Sprintf("Download v%s?", latest), true)
		if pErr != nil {
			return pErr
		}
		if !confirmed {
			common.Cli.Info(fmt.Sprintf("skipped. Run '%s bin pull <version>' manually.", infra.CLIName))
			return fmt.Errorf("skipped by user")
		}
	}
	common.Cli.Info("")
	common.Cli.Info(fmt.Sprintf("downloading Firecracker v%s ...", latest))
	s.downloadVersion = latest
	return nil
}

// handleGuestfs manages the libguestfs enable prompt.
func (s *initState) handleGuestfs(ctx context.Context) error {
	if s.nonInteractive {
		s.guestfsEnabled = new(false)
		return nil
	}
	enabled, pErr := common.Cli.PromptConfirm(ctx, "Enable libguestfs as a provisioning fallback?", false)
	if pErr != nil {
		return pErr
	}
	s.guestfsEnabled = &enabled
	return nil
}

// handleInteractiveFlow drives the init wizard with interaction handling.
// Always returns the last InitResult (never nil), even when the loop breaks early.
// Returns (nil, err) only on system failures (context cancellation);
// user-initiated breaks return (lastResult, nil) so runInitWizard can display step progress.
func handleInteractiveFlow(
	ctx context.Context,
	initAPI api.InitAPI,
	hostAPI api.HostAPI,
	nonInteractive, skipHost, skipNetwork bool,
) (*results.InitResult, error) {
	state := &initState{
		initAPI:        initAPI,
		hostAPI:        hostAPI,
		nonInteractive: nonInteractive,
		skipHost:       skipHost,
		skipNetwork:    skipNetwork,
	}

	// Always return the last result even on early exit.
	var lastResult *results.InitResult

	for {
		result := state.runInit(ctx)
		lastResult = result

		if result.NeedsInteraction == nil {
			return result, nil
		}

		if err := state.dispatch(ctx, result.NeedsInteraction); err != nil {
			// Propagate cancellation/real errors; user-aborts return lastResult.
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			return lastResult, nil
		}
	}
}

// composeHostSetupMessage composes a human-readable message about what changed.
func composeHostSetupMessage(before, after *results.HostStatusCheck) string {
	var parts []string
	if !before.GroupExists && after.GroupExists {
		parts = append(parts, "group created")
	}
	if !before.SudoersExists && after.SudoersExists {
		parts = append(parts, "sudoers configured")
	}
	if !before.UserInGroup && after.UserInGroup {
		parts = append(parts, "user added to group")
	}
	if len(parts) > 0 {
		return "Host " + strings.Join(parts, ", ")
	}
	return "Host already configured"
}
