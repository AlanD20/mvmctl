package system

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"mvmctl/internal/infra"
)

// SpawnConfig configures a subprocess started via SpawnService.
type SpawnConfig struct {
	// Name is the service name — must match the Cobra "mvm run <name>" command.
	Name string

	// ExtraFiles are passed to the child as FDs starting at 3 (stdin=0, stdout=1, stderr=2).
	ExtraFiles []*os.File

	// Privileged runs the subprocess via sudo when true.
	// Needed for services that require root (e.g. loopmount provisioning).
	Privileged bool

	// Stdin is the optional reader for the subprocess's stdin.
	// When nil, the child reads from /dev/null (daemon default).
	Stdin io.Reader

	// Stdout is the optional writer for the subprocess's stdout.
	// When nil, the child inherits the parent's stdout.
	Stdout io.Writer

	// Stderr is the optional writer for the subprocess's stderr.
	// When nil, the child inherits the parent's stderr.
	Stderr io.Writer

	// Args are additional arguments to pass to "mvm run <name>".
	Args []string
}

// SpawnService starts a subprocess in a new process group (Setpgid).
// The child will run as "mvm run <name>" with the given config.
//
// For daemon services (nocloudnet server, console relay), pass nil for ctx
// or context.Background() — the process survives the caller's exit.
// For synchronous subprocesses (loopmount provisioning), pass a real
// context and call cmd.Wait() after SpawnService returns.
func SpawnService(ctx context.Context, cfg SpawnConfig) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot determine executable path: %w", err)
	}

	var cmd *exec.Cmd
	if cfg.Privileged && !IsRoot() {
		if isCancelable(ctx) {
			cmd = exec.CommandContext(ctx, "sudo", exe, "run", cfg.Name)
		} else {
			cmd = exec.Command("sudo", exe, "run", cfg.Name)
		}
	} else {
		if isCancelable(ctx) {
			cmd = exec.CommandContext(ctx, exe, "run", cfg.Name)
		} else {
			cmd = exec.Command(exe, "run", cfg.Name)
		}
	}
	cmd.Args = append(cmd.Args, cfg.Args...)

	if cfg.Stdin != nil {
		cmd.Stdin = cfg.Stdin
	}
	if cfg.Stdout != nil {
		cmd.Stdout = cfg.Stdout
	}
	if cfg.Stderr != nil {
		cmd.Stderr = cfg.Stderr
	}

	cmd.Env = append(os.Environ(), infra.MVMBackgroundServiceEnv)
	if len(cfg.ExtraFiles) > 0 {
		cmd.ExtraFiles = cfg.ExtraFiles
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start %s: %w", cfg.Name, err)
	}
	return cmd, nil
}

// isCancelable returns true if ctx is a cancellable context
// (WithCancel, WithDeadline, WithTimeout — not nil, Background, or TODO).
func isCancelable(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return ctx.Done() != nil
}
