package vsockagent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// handleExec runs a shell command and streams its output to the vsock
// connection as JSON frames. Stdout is sent as "stdout" frames, stderr as
// "stderr" frames. A final "result" frame carries the exit code and duration.
//
// Output is captured via cmd.Stdout/cmd.Stderr (bytes.Buffer), not via
// cmd.StdoutPipe()/cmd.StderrPipe(). This avoids a race where the Go runtime's
// pipe read goroutine (started internally by os/exec) might not be scheduled
// before the child process exits under extreme CPU starvation, causing
// (0, io.EOF) to be returned — i.e., no data captured and no frame sent.
func handleExec(ctx context.Context, req *execRequest, conn net.Conn) {
	start := time.Now()

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	var cmd *exec.Cmd
	if req.User != "" && req.User != "root" {
		// Run as a different user via su.
		cmd = exec.CommandContext(ctx, "su", "-", req.User, "-c", req.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", req.Command)
	}

	// Capture stdout/stderr via bytes.Buffer to avoid kernel pipe races.
	// Go's os/exec internally creates pipes and reads them in goroutines
	// when cmd.Stdout/cmd.Stderr implement io.Writer (not *os.File).
	// These internal goroutines are managed by os/exec and are reliably
	// scheduled before cmd.Wait() returns.
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Merge provided environment with the current environment.
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	if err := cmd.Start(); err != nil {
		slog.Error("command start failed", "id", req.ID, "error", err)
		_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error()})
		return
	}

	exitCode := 0
	err := cmd.Wait()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Timeout or system error.
			slog.Error("command execution failed", "id", req.ID, "error", err)
			_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error()})
			return
		}
	}

	// Flush buffered stdout as streaming frames.
	if stdoutBuf.Len() > 0 {
		_ = writeFrame(conn, &execResponse{
			ID:   req.ID,
			Type: responseTypeStdout,
			Data: stdoutBuf.String(),
		})
	}

	// Flush buffered stderr as streaming frames.
	if stderrBuf.Len() > 0 {
		_ = writeFrame(conn, &execResponse{
			ID:   req.ID,
			Type: responseTypeStderr,
			Data: stderrBuf.String(),
		})
	}

	// Sync to flush Firecracker's writeback cache. Files written by the
	// command may still be in Firecracker's host-side cache. The sync()
	// syscall triggers VIRTIO_BLK_T_FLUSH on virtio-blk devices.
	if !req.NoSync {
		slog.Debug("syncing filesystem after exec")
		unix.Sync()
	}

	elapsed := time.Since(start)
	slog.Debug("command executed",
		"id", req.ID,
		"exit_code", exitCode,
		"duration_ms", elapsed.Milliseconds(),
	)

	_ = writeFrame(conn, &execResponse{
		ID:         req.ID,
		Type:       responseTypeResult,
		Status:     exitCode,
		DurationMs: int(elapsed.Milliseconds()),
	})
}
