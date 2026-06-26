package vsockagent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// streamFlushThreshold is the maximum bytes to buffer in streamingWriter
// before forcing a flush when no newline is present.
const streamFlushThreshold = 4096

// streamingWriter implements io.Writer for use as cmd.Stdout/cmd.Stderr.
// It buffers output and emits JSON frames ("stdout" or "stderr") to the
// vsock connection as data is produced, rather than accumulating everything
// until the command exits.
//
// Flush triggers:
//   - a newline in the buffered data
//   - buffer size exceeding streamFlushThreshold
//   - explicit Flush() call after the command exits
//
// Multiple streamingWriters sharing the same connMu (e.g., one for stdout
// and one for stderr) will not interleave their JSON output on the wire.
//
// Like bytes.Buffer, streamingWriter is an io.Writer, so Go's os/exec uses
// internal goroutines to read from the child's pipes and write to it. This
// avoids the kernel pipe race that occurs with cmd.StdoutPipe()/cmd.StderrPipe().
type streamingWriter struct {
	conn   net.Conn
	connMu *sync.Mutex
	id     string // request ID for frames
	typ    string // "stdout" or "stderr"
	buf    bytes.Buffer

	err   error
	errMu sync.Mutex
}

// Write accumulates data into the buffer and triggers a flush if a newline
// is present or the buffer exceeds the threshold. Always returns len(p), nil
// to avoid aborting Go's internal pipe goroutines. After a write error is
// recorded, subsequent writes discard data immediately to prevent unbounded
// memory growth.
func (w *streamingWriter) Write(p []byte) (int, error) {
	if w.Err() != nil {
		return len(p), nil
	}
	w.buf.Write(p)
	w.tryFlush()
	return len(p), nil
}

// tryFlush sends buffered data as a JSON frame if a flush trigger is met.
// On write error, the first error is recorded via setError. After an error,
// Write discards incoming data and tryFlush/Flush drain and discard buffered
// data without writing, preventing repeated writes to a dead connection.
func (w *streamingWriter) tryFlush() {
	if w.buf.Len() == 0 {
		return
	}

	data := w.buf.Bytes()
	lastNewline := bytes.LastIndexByte(data, '\n')

	var flushLen int
	if lastNewline >= 0 {
		flushLen = lastNewline + 1
	} else if w.buf.Len() >= streamFlushThreshold {
		flushLen = w.buf.Len()
	} else {
		return
	}

	chunk := make([]byte, flushLen)
	copy(chunk, data[:flushLen])
	w.buf.Next(flushLen)

	// After a write error, drain data from the buffer without writing.
	if w.Err() != nil {
		return
	}

	w.connMu.Lock()
	err := writeFrame(w.conn, &execResponse{
		ID:   w.id,
		Type: w.typ,
		Data: string(chunk),
	})
	w.connMu.Unlock()
	if err != nil {
		w.setError(err)
	}
}

// setError records the first write error. Subsequent calls are no-ops.
func (w *streamingWriter) setError(err error) {
	w.errMu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.errMu.Unlock()
}

// Err returns the first write error, if any.
func (w *streamingWriter) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}

// Flush sends any remaining buffered data as a JSON frame. Called after
// cmd.Wait() to drain the last partial-line output. After a write error,
// drains the buffer silently without writing to the dead connection.
func (w *streamingWriter) Flush() {
	if w.buf.Len() == 0 {
		return
	}

	data := w.buf.String()
	w.buf.Reset()

	if w.Err() != nil {
		return
	}

	w.connMu.Lock()
	err := writeFrame(w.conn, &execResponse{
		ID:   w.id,
		Type: w.typ,
		Data: data,
	})
	w.connMu.Unlock()
	if err != nil {
		w.setError(err)
	}
}

// handleExec runs a shell command and streams its output to the vsock
// connection as JSON frames. Stdout is sent as "stdout" frames, stderr as
// "stderr" frames. A final "result" frame carries the exit code and duration.
//
// Output is captured via streamingWriter (implements io.Writer), not via
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

	// Use streaming writers that emit JSON frames in real time.
	// They implement io.Writer, so Go's os/exec internally creates pipes
	// and reads them in goroutines — the same race-free mechanism as
	// bytes.Buffer, but with live frame emission.
	connMu := &sync.Mutex{}
	stdoutW := &streamingWriter{
		conn: conn, connMu: connMu, id: req.ID, typ: "stdout",
	}
	stderrW := &streamingWriter{
		conn: conn, connMu: connMu, id: req.ID, typ: "stderr",
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	// Merge provided environment with the current environment.
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	if err := cmd.Start(); err != nil {
		slog.Error("command start failed", "id", req.ID, "error", err)
		_ = writeFrame(conn, &execResponse{
			ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error(),
		})
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
			_ = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeResult, Status: -1, Error: err.Error(),
			})
			return
		}
	}

	// Flush any remaining buffered output.
	stdoutW.Flush()
	stderrW.Flush()

	// Check for write errors — they take precedence over the command exit
	// code because a broken connection invalidates the result.
	if writeErr := stdoutW.Err(); writeErr != nil {
		slog.Error("stdout write error", "id", req.ID, "error", writeErr)
		_ = writeFrame(conn, &execResponse{
			ID: req.ID, Type: responseTypeResult, Status: -1, Error: writeErr.Error(),
		})
		return
	}
	if writeErr := stderrW.Err(); writeErr != nil {
		slog.Error("stderr write error", "id", req.ID, "error", writeErr)
		_ = writeFrame(conn, &execResponse{
			ID: req.ID, Type: responseTypeResult, Status: -1, Error: writeErr.Error(),
		})
		return
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
