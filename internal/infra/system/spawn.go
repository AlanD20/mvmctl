package system

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"mvmctl/internal/infra"
)

// SpawnService starts a detached subprocess in a new process group (Setpgid).
// The child will run as "mvm run <name>" with the given extra files.
// extraFiles are passed to the child as FDs starting at 3 (stdin=0, stdout=1, stderr=2).
//
// Unlike CommandRunner.Run, this does NOT wait for the process to complete.
// The child process survives the parent's exit, making this suitable for
// long-running service subprocesses (nocloud server, console relay).
func SpawnService(name string, extraFiles []*os.File, args ...string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot determine executable path: %w", err)
	}
	cmd := exec.Command(exe, "run", name)
	cmd.Args = append(cmd.Args, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = append(os.Environ(), infra.MVMBackgroundServiceEnv)
	if len(extraFiles) > 0 {
		cmd.ExtraFiles = extraFiles
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start %s: %w", name, err)
	}
	return cmd, nil
}
