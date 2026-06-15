package vsockagent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// handleExec runs a shell command and streams its output to the vsock
// connection as JSON frames. Stdout is sent as "stdout" frames, stderr as
// "stderr" frames. A final "result" frame carries the exit code and duration.
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

	// Merge provided environment with the current environment.
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("create stdout pipe", "id", req.ID, "error", err)
		_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error()})
		return
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		slog.Error("create stderr pipe", "id", req.ID, "error", err)
		_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error()})
		return
	}

	if err := cmd.Start(); err != nil {
		slog.Error("command start failed", "id", req.ID, "error", err)
		_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error()})
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stdoutPipe.Read(buf)
			if n > 0 {
				_ = writeFrame(conn, &execResponse{
					ID:   req.ID,
					Type: responseTypeStdout,
					Data: string(buf[:n]),
				})
			}
			if readErr != nil {
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				_ = writeFrame(conn, &execResponse{
					ID:   req.ID,
					Type: responseTypeStderr,
					Data: string(buf[:n]),
				})
			}
			if readErr != nil {
				return
			}
		}
	}()

	exitCode := 0
	err = cmd.Wait()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Timeout or system error.
			slog.Error("command execution failed", "id", req.ID, "error", err)
			wg.Wait()
			_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error()})
			return
		}
	}

	// Both pipes are closed at this point — wait for goroutines to flush.
	wg.Wait()

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
