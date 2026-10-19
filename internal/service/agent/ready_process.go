package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func newReadyCommand(ctx context.Context, command, targetUser string) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// waitReadyCommand owns cancellation and reaping; no asynchronous CommandContext
	// watcher may reap or abandon the leader before process-group cleanup.
	commandContext := context.WithoutCancel(ctx)
	var process *exec.Cmd
	if targetUser == "" || targetUser == "root" {
		process = exec.CommandContext(commandContext, "/bin/sh", "-c", command)
		process.SysProcAttr = &syscall.SysProcAttr{}
	} else {
		account, err := (systemDestinationAccountResolver{}).Resolve(ctx, targetUser)
		if err != nil {
			return nil, err
		}
		// CRITICAL: Execute the shell directly with the admitted numeric credentials.
		// su can create another session, outside the readiness process group we own.
		process = newDestinationCommand(
			commandContext,
			account,
			command,
			nil,
			nil,
			systemDestinationProcessGroupKiller{},
		)
	}
	process.SysProcAttr.Setpgid = false
	process.SysProcAttr.Setsid = true
	process.Cancel = nil
	process.WaitDelay = time.Second
	return process, nil
}

// waitReadyCommand retains the session leader until every completion path has killed its group.
// No other waiter may reap this child. WNOWAIT leaves its PID reserved even after early exit.
func waitReadyCommand(ctx context.Context, command *exec.Cmd) (waitErr, cleanupErr error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, command.Process.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			// A failed wait cannot prove the numeric process-group identity. Process.Kill uses
			// Go's retained process handle; never signal a possibly recycled group number here.
			killErr := command.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			return command.Wait(), errs.WrapMsg(errs.CodeProcessError,
				"cannot retain readiness process group for cleanup", errors.Join(err, killErr))
		}
		if info.Signo != 0 || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}

	// CRITICAL: The leader is still our unreaped child. Its process-group number cannot
	// be reused between this signal and Wait, including when the shell already exited.
	if err := unix.Kill(-command.Process.Pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		killErr := command.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		cleanupErr = errs.WrapMsg(errs.CodeProcessError, "kill readiness process group", errors.Join(err, killErr))
	}
	return command.Wait(), cleanupErr
}
