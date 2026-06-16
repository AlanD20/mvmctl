package vsock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Client is a per-VM vsock protocol client for communicating with the
// guest agent inside a Firecracker microVM.
//
// A Client is constructed from a VsockConfigItem and provides methods for
// executing commands (Exec), opening interactive shells (Shell), and
// cleaning up the vsock socket (Teardown).
//
// This is NOT a Controller — there is no stateful lifecycle to manage.
// Each connection is ephemeral: dial, handshake, exchange frames, close.
type Client struct {
	item *model.VsockConfigItem

	// ProbeTimeout is the maximum time to wait for the guest agent to become
	// reachable. The client probes with 20ms intervals until this timeout
	// expires or the agent responds. Set by the API layer from config
	// defaults.vm.vsock_probe_timeout (5s). Must be > 0.
	ProbeTimeout time.Duration
}

const vsockProbeInterval = 20 * time.Millisecond

// NewClient creates a new vsock Client for the given config item.
// probeTimeout is the maximum time to wait for the guest agent to become
// reachable (20ms probe interval). Set from defaults.vm.vsock_probe_timeout.
func NewClient(item *model.VsockConfigItem, probeTimeout time.Duration) *Client {
	return &Client{item: item, ProbeTimeout: probeTimeout}
}

// ExecResult holds the result of a single command execution.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Exec executes a command inside the VM and streams the output to stdout/stderr.
// The connection is established, the command is sent, frames are streamed,
// and the connection is closed — all in one shot.
// stdout/stderr data is printed directly to the terminal as it arrives,
// and also accumulated into the returned ExecResult.
func (c *Client) Exec(ctx context.Context, command, user string, timeout int) (*ExecResult, error) {
	conn, err := c.waitForAgent(ctx)
	if err != nil {
		slog.Error("vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, err
	}
	defer conn.Close()

	// Apply overall deadline from context if set
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline) // best-effort: deadline on a closing connection is harmless
	}

	req := execRequest{
		ID:      "1",
		Type:    "exec",
		Command: command,
		Token:   c.item.Token,
		Timeout: timeout,
		User:    user,
	}

	if err := sendFrame(conn, req); err != nil {
		slog.Error("vsock send exec request failed", "vm_id", c.item.VmID, "error", err)
		return nil, errs.WrapMsg(errs.CodeVsockExecFailed, "failed to send exec request", err)
	}

	// Streaming read loop — process stdout/stderr frames as they arrive,
	// and return when the final "result" frame is received.
	// Use a single json.Decoder to avoid buffering issues: creating a new
	// decoder for each frame would lose data buffered by the previous decoder.
	var result ExecResult
	dec := json.NewDecoder(conn)
	for {
		var resp execResponse
		if err := dec.Decode(&resp); err != nil {
			slog.Error("vsock read exec response failed", "vm_id", c.item.VmID, "error", err)
			return nil, errs.WrapMsg(errs.CodeVsockExecFailed, "failed to read exec response", err)
		}

		switch resp.Type {
		case "stdout":
			if resp.Data != "" {
				os.Stdout.WriteString(resp.Data)
				result.Stdout += resp.Data
			}
		case "stderr":
			if resp.Data != "" {
				os.Stderr.WriteString(resp.Data)
				result.Stderr += resp.Data
			}
		case "result":
			if resp.Error != "" {
				return nil, errs.New(errs.CodeVsockExecFailed, fmt.Sprintf("agent error: %s", resp.Error))
			}
			result.ExitCode = resp.Status
			return &result, nil
		default:
			slog.Warn("vsock unknown exec response type", "type", resp.Type)
		}
	}
}

// Shell opens an interactive PTY shell session inside the VM.
// It sets the local terminal to raw mode and performs bidirectional
// relay: local stdin → vsock, vsock → local stdout.
// SIGWINCH is forwarded to the agent as a resize JSON frame.
// The terminal is restored when the session ends.
func (c *Client) Shell(ctx context.Context, user string) error {
	conn, err := c.waitForAgent(ctx)
	if err != nil {
		slog.Error("vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return err
	}
	defer conn.Close()

	req := execRequest{
		ID:    "1",
		Type:  "exec-tty",
		Token: c.item.Token,
		User:  user,
		Env: map[string]string{
			"TERM": os.Getenv("TERM"),
		},
	}

	if err := sendFrame(conn, req); err != nil {
		slog.Error("vsock send exec-tty request failed", "vm_id", c.item.VmID, "error", err)
		return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to send exec-tty request", err)
	}

	// Read the TTY acknowledgement frame. The agent always sends this
	// before entering raw relay mode on an exec-tty request.
	slog.Debug("tty: reading TTY ack")
	var resp execResponse
	if err := readFrame(conn, &resp); err != nil {
		return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to read TTY ack", err)
	}
	if resp.Error != "" {
		return errs.New(errs.CodeVsockExecFailed,
			fmt.Sprintf("agent error: %s", resp.Error))
	}
	slog.Debug("tty: TTY ack received, starting relay")
	// agent sent TTY ack — proceed to relay

	// Set terminal to raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		slog.Error("vsock set terminal raw mode failed", "error", err)
		return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to set terminal to raw mode", err)
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState) // best-effort: terminal may already be restored
	}()

	// Dup stdin so we can close the copy to unblock the relay without
	// closing the real stdin (fd 0) which the host shell needs.
	stdinFd, err := unix.Dup(int(os.Stdin.Fd()))
	if err != nil {
		slog.Error("vsock dup stdin fd failed", "error", err)
		return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to dup stdin fd", err)
	}
	stdinFile := os.NewFile(uintptr(stdinFd), "stdin-dup")
	defer stdinFile.Close()

	return relayTTY(conn, stdinFile, os.Stdout)
}

// crFilter wraps an io.Reader and converts carriage returns (\r, 0x0D)
// to newlines (\n, 0x0A). In raw terminal mode, the Enter key produces \r
// on the host side. Without this conversion, the guest PTY's ICRNL flag
// must convert \r → \n — but this flag isn't reliably set across all VM
// kernels. By converting on the host side, Enter always terminates the line
// regardless of the guest PTY configuration.
type crFilter struct {
	rd io.Reader
}

func (f *crFilter) Read(p []byte) (int, error) {
	n, err := f.rd.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == '\r' {
			p[i] = '\n'
		}
	}
	return n, err
}

// relayTTY performs bidirectional relay between a vsock connection and the
// terminal (stdin/stdout). It returns when one direction completes (EOF or
// error), triggering connection close to unblock the other direction.
func relayTTY(conn net.Conn, stdin io.ReadCloser, stdout io.Writer) error {
	// ── Bidirectional relay ──
	//
	// Two goroutines relay in both directions. The first one to finish
	// (EOF or error) triggers connection close, which unblocks the other.
	errChan := make(chan error, 2)

	// stdin → vsock (with \r → \n conversion)
	go func() {
		slog.Debug("tty: relay goroutine (stdin→conn) starting")
		_, err := io.Copy(conn, &crFilter{rd: stdin})
		slog.Debug("tty: stdin→conn relay ended", "error", err)
		errChan <- err
	}()

	// vsock → stdout
	go func() {
		slog.Debug("tty: relay goroutine (conn→stdout) starting")
		_, err := io.Copy(stdout, conn)
		slog.Debug("tty: conn→stdout relay ended", "error", err)
		errChan <- err
	}()

	// Wait for the first goroutine to finish, then close the connection
	// to unblock the other goroutine.
	firstErr := <-errChan
	slog.Debug("tty: first relay direction finished", "error", firstErr)
	_ = conn.Close()  // best-effort: close to unblock other goroutine; error irrelevant
	_ = stdin.Close() // unblock stdin→conn relay without waiting for user input
	// Drain the other goroutine's error (typically nil on EOF).
	secondErr := <-errChan
	slog.Debug("tty: second relay direction drained", "error", secondErr)

	return nil
}

// waitForAgent retries dialAndHandshake until the guest agent responds or the
// probe timeout expires. ProbeTimeout must be > 0 — callers (API layer) set it
// from defaults.vm.vsock_probe_timeout (config default: 60s).
func (c *Client) waitForAgent(ctx context.Context) (net.Conn, error) {
	if c.ProbeTimeout <= 0 {
		return nil, errs.New(errs.CodeVsockConnectionFailed,
			"vsock agent probe timeout not set — API layer must set ProbeTimeout from config")
	}
	deadline := time.Now().Add(c.ProbeTimeout)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errs.New(errs.CodeVsockConnectionFailed,
				fmt.Sprintf("vsock agent did not become reachable within %v", c.ProbeTimeout))
		}

		conn, err := dialAndHandshake(ctx, c.item.UDSPath, c.item.Port)
		if err == nil {
			return conn, nil
		}

		// Connection failed (agent not ready yet) — probe again after interval.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(vsockProbeInterval):
		}
	}
}

// Teardown removes the vsock UDS socket file if it exists.
// This is called during VM cleanup to ensure no stale socket remains.
func (c *Client) Teardown(_ context.Context) error {
	if c.item.UDSPath != "" {
		if err := os.Remove(c.item.UDSPath); err != nil && !os.IsNotExist(err) {
			return errs.Wrap(errs.CodeVsockConnectionFailed, err)
		}
	}
	return nil
}
