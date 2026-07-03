package vsock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"mvmctl/internal/infra/timinglog"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/version"
	"mvmctl/internal/service/agent"
	"mvmctl/pkg/errs"
)

// termGetSize is a mockable variable for term.GetSize.
// Tests can set this to control terminal size detection.
var termGetSize = term.GetSize

const (
	// defaultVersion is the fallback version string used when BuildVersion
	// is empty (e.g. development builds without ldflags).
	defaultVersion = "0.0.0"

	// upgradeShellCommand replaces the running agent binary and restarts the
	// agent service after a 2-second delay in a fully detached background
	// process (nohup). The delay allows the exec response frame to be sent
	// before the old agent is killed. Supports both systemd and OpenRC.
	upgradeShellCommand = `cp /usr/bin/mvm-agent /usr/bin/mvm-agent.bak 2>/dev/null || true; mv /usr/bin/mvm-agent.new /usr/bin/mvm-agent && chmod 0755 /usr/bin/mvm-agent && nohup sh -c 'sleep 2; if command -v systemctl >/dev/null 2>&1; then systemctl restart mvm-agent; else rc-service mvm-agent restart; fi' >/dev/null 2>&1 </dev/null &`

	// restoreShellCommand restores the previous agent binary from backup and
	// restarts the service. Used as a rollback if the upgrade exec fails.
	// Uses nohup to fully detach the restart. Supports both systemd and OpenRC.
	restoreShellCommand = `test -f /usr/bin/mvm-agent.bak && cp /usr/bin/mvm-agent.bak /usr/bin/mvm-agent && nohup sh -c 'sleep 1; if command -v systemctl >/dev/null 2>&1; then systemctl restart mvm-agent; else rc-service mvm-agent restart; fi' >/dev/null 2>&1 </dev/null &`
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

	// VmName is the human-readable VM name, used for timing log entries.
	// Set by callers (API layer) after construction. Zero value (empty string)
	// is acceptable — timing entries will just have an empty vm_name field.
	VmName string

	// AgentVersion is set by ensureAgent after a successful version probe.
	AgentVersion string

	// OnHostFrame is called when the Exec() read loop receives a frame that
	// is not "stdout", "stderr", or "result". These are guest-initiated frames
	// that the host must process (e.g., "remote_vm"). If nil, the frame is
	// silently logged and ignored.
	// sourceVMID is the VM ID of the client that received the frame.
	OnHostFrame func(ctx context.Context, sourceVMID string, conn net.Conn, frameType string, data string) error

	// Internal: set during upgrade, cleared on successful retry.
	upgradeInProgress bool

	// Internal: bypasses version probe (used by upgradeAgent to avoid circular calls).
	skipVersionCheck bool

	// dialFn is the function used to establish a vsock connection.
	// If nil, dialAndHandshake is used. Tests can set this to return
	// mock connections without booting a real VM.
	dialFn func(ctx context.Context, udsPath string, port int, attemptNum int) (net.Conn, error)

	// OnUpgradeStarted is called before the upgrade begins.
	// The callback should set the DB upgrade lock and log the event.
	OnUpgradeStarted func(ctx context.Context, fromVersion, toVersion string)

	// OnUpgradeCompleted is called after the upgrade succeeds and the
	// retry loop confirms the new agent version is running.
	OnUpgradeCompleted func(ctx context.Context, newVersion string)

	// OnVersionKnown is called when the probed agent version is current
	// (no upgrade needed). It receives the version string from the guest
	// agent. Used by the API layer to persist the version to the DB
	// when it differs from the initial config value.
	OnVersionKnown func(ctx context.Context, version string)

	// OnUpgradeFailed is called when an upgrade attempt fails.
	// The callback should clear the DB upgrade lock and log the event.
	OnUpgradeFailed func(ctx context.Context, err error)
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
func (c *Client) Exec(
	ctx context.Context,
	command, user string,
	timeout int,
	env map[string]string,
	noSync bool,
) (*ExecResult, error) {
	conn, err := c.ensureAgent(ctx)
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
		Env:     env,
		NoSync:  noSync,
	}

	if err := SendFrame(conn, req); err != nil {
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
			// Defensive check — stdout/stderr should be caught above.
			if resp.Type == "stdout" || resp.Type == "stderr" {
				break
			}
			if c.OnHostFrame != nil {
				if err := c.OnHostFrame(ctx, c.item.VmID, conn, resp.Type, resp.Data); err != nil {
					return nil, err
				}
			} else {
				slog.Warn("vsock unknown exec response type", "type", resp.Type)
			}
		}
	}
}

// getTerminalSize tries to obtain the terminal size from stdin, stdout,
// and stderr in order, returning the first successful result.
// Returns ok=false if no terminal size could be determined (all three fds
// are non-terminal or unavailable).
func getTerminalSize() (rows, cols int, ok bool) {
	for _, fd := range []int{int(os.Stdin.Fd()), int(os.Stdout.Fd()), int(os.Stderr.Fd())} {
		w, h, err := termGetSize(fd)
		if err == nil && w > 0 && h > 0 {
			// term.GetSize returns (width, height) = (cols, rows).
			return h, w, true
		}
	}
	return 0, 0, false
}

// Shell opens an interactive PTY shell session inside the VM.
// It sets the local terminal to raw mode and performs bidirectional
// relay: local stdin → vsock, vsock → local stdout.
// SIGWINCH is forwarded to the agent as a resize JSON frame.
// The terminal is restored when the session ends.
func (c *Client) Shell(ctx context.Context, user string) error {
	conn, err := c.ensureAgent(ctx)
	if err != nil {
		slog.Error("vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return err
	}
	defer conn.Close()

	rows, cols, ok := getTerminalSize()
	if !ok {
		// If no terminal size is available (all fds are non-TTY), use
		// sensible defaults so the shell has a usable window size.
		rows, cols = 24, 80
	}

	req := execRequest{
		ID:    "1",
		Type:  "exec-tty",
		Token: c.item.Token,
		User:  user,
		Rows:  rows,
		Cols:  cols,
		Env: map[string]string{
			"TERM": os.Getenv("TERM"),
		},
	}

	slog.Debug("tty: opening shell with initial window size",
		"rows", req.Rows, "cols", req.Cols)

	if err := SendFrame(conn, req); err != nil {
		slog.Error("vsock send exec-tty request failed", "vm_id", c.item.VmID, "error", err)
		return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to send exec-tty request", err)
	}

	// Read the TTY acknowledgement frame. The agent always sends this
	// before entering raw relay mode on an exec-tty request.
	slog.Debug("tty: reading TTY ack")
	var resp execResponse
	if err := readFrameRaw(conn, &resp); err != nil {
		return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to read TTY ack", err)
	}
	if resp.Error != "" {
		return errs.New(errs.CodeVsockExecFailed,
			fmt.Sprintf("agent error: %s", resp.Error))
	}
	slog.Debug("tty: TTY ack received, sending initial resize frame")

	// Send an initial resize frame after the TTY ack to guarantee the PTY
	// window size is applied. The exec-tty request carries the dimensions,
	// but some agent versions may not honour the initial request — this
	// explicit resize frame is processed by the agent's frame scanner
	// (extractResizeFrames/TIOCSWINSZ) which always applies the size.
	if rows > 0 && cols > 0 {
		resizeReq := execRequest{
			Type: "resize",
			Rows: rows,
			Cols: cols,
		}
		slog.Debug("tty: sending initial resize frame",
			"rows", rows, "cols", cols)
		if err := SendFrame(conn, resizeReq); err != nil {
			slog.Error("tty: failed to send initial resize frame", "error", err)
			return errs.WrapMsg(errs.CodeVsockExecFailed, "failed to send initial resize frame", err)
		}
	}

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

// lockedWriteConn wraps a net.Conn with a sync.Mutex that callers can
// acquire explicitly to make a sequence of writes atomic. The Write method
// does NOT lock — use lockedWriter (for individual Write atomicity) or
// lock the mutex manually around a multi-write sequence (e.g. sendFrame).
type lockedWriteConn struct {
	net.Conn
	mu sync.Mutex
}

// lockedWriter is an io.Writer that serialises each Write call through the
// parent lockedWriteConn's mutex. This prevents interleaving of bytes from
// concurrent writers (e.g. stdin data and resize frames) at the application
// framing level.
type lockedWriter struct {
	lc *lockedWriteConn
}

func (w lockedWriter) Write(b []byte) (int, error) {
	w.lc.mu.Lock()
	defer w.lc.mu.Unlock()
	return w.lc.Conn.Write(b)
}

// relayTTY performs bidirectional relay between a vsock connection and the
// terminal (stdin/stdout). It returns when one direction completes (EOF or
// error), triggering connection close to unblock the other direction.
// SIGWINCH signals are forwarded to the guest agent as resize JSON frames
// on the same connection.
func relayTTY(conn net.Conn, stdin io.ReadCloser, stdout io.Writer) error {
	// --- SIGWINCH forwarding ---
	//
	// Subscribe to terminal resize events and send JSON resize frames
	// to the guest agent. The agent receives these in its raw relay loop
	// and updates the PTY window size via TIOCSWINSZ.
	// Wrap conn in a locked writer so stdin and resize frames cannot
	// interleave at the application framing level.
	lw := &lockedWriteConn{Conn: conn}

	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)

	resizeDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-winchCh:
				rows, cols, ok := getTerminalSize()
				if !ok {
					slog.Debug("tty: resize: getTerminalSize failed, skipping resize")
					continue
				}
				resizeReq := execRequest{
					Type: "resize",
					Rows: rows,
					Cols: cols,
				}
				slog.Debug("tty: forwarding resize", "rows", rows, "cols", cols)
				lw.mu.Lock()
				err := SendFrame(lw, resizeReq)
				lw.mu.Unlock()
				if err != nil {
					slog.Debug("tty: resize send failed (connection may be closed)", "error", err)
					return
				}
			case <-resizeDone:
				return
			}
		}
	}()

	// --- Bidirectional relay ---
	//
	// Two goroutines relay in both directions. The first one to finish
	// (EOF or error) triggers connection close, which unblocks the other.
	errChan := make(chan error, 2)

	// stdin → vsock (with \r → \n conversion)
	// Writes go through lockedWriter to prevent interleaving with resize frames.
	go func() {
		slog.Debug("tty: relay goroutine (stdin→conn) starting")
		_, err := io.Copy(lockedWriter{lc: lw}, &crFilter{rd: stdin})
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

	// Stop the resize goroutine.
	signal.Stop(winchCh)
	close(resizeDone)

	return nil
}

// ensureAgent retries dialAndHandshake until the guest agent responds or the
// probe timeout expires. After a successful dial, it probes the agent version
// and triggers an upgrade if the host binary is newer than the guest agent.
// ProbeTimeout must be > 0 — callers (API layer) set it from
// defaults.vm.vsock_probe_timeout (config default: 60s).
// When timing is enabled, logs per-attempt vsock_dial and overall vsock_probe timing.
func (c *Client) ensureAgent(ctx context.Context) (net.Conn, error) {
	if c.ProbeTimeout <= 0 {
		return nil, errs.New(errs.CodeVsockConnectionFailed,
			"agent probe timeout not set — API layer must set ProbeTimeout from config")
	}

	start := time.Now()
	deadline := time.Now().Add(c.ProbeTimeout)
	attempts := 0

	for {
		if time.Until(deadline) <= 0 {
			elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
			timinglog.Log("vsock_probe", elapsedMs,
				"vm_name", c.VmName,
				"vm_id", c.item.VmID,
				"attempts", attempts,
				"error", "timeout",
			)
			slog.Debug("vsock probe timeout",
				"vm_id", c.item.VmID,
				"uds_path", c.item.UDSPath,
				"port", c.item.Port,
				"attempts", attempts,
				"timeout", c.ProbeTimeout,
			)
			return nil, errs.New(
				errs.CodeVsockConnectionFailed,
				fmt.Sprintf(
					"agent did not become reachable within %v after %d attempt(s)",
					c.ProbeTimeout,
					attempts,
				),
			)
		}

		attempts++

		// Per-attempt debug logging
		remaining := time.Until(deadline)
		slog.Debug("vsock probe attempt",
			"vm_id", c.item.VmID,
			"uds_path", c.item.UDSPath,
			"port", c.item.Port,
			"attempt", attempts,
			"remaining", remaining,
		)

		// Per-attempt timing: wrap dialAndHandshake with vsock_dial
		dialStart := time.Now()
		dialFn := c.dialFn
		if dialFn == nil {
			dialFn = dialAndHandshake
		}
		conn, err := dialFn(ctx, c.item.UDSPath, c.item.Port, attempts)
		dialElapsed := float64(time.Since(dialStart).Microseconds()) / 1000.0

		if err == nil {
			elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
			timinglog.Log("vsock_probe", elapsedMs,
				"vm_name", c.VmName,
				"vm_id", c.item.VmID,
				"attempts", attempts,
			)
			timinglog.Log("vsock_dial", dialElapsed,
				"vm_name", c.VmName,
				"vm_id", c.item.VmID,
				"attempt", attempts,
			)

			// If skipVersionCheck is set, skip version probe and return the
			// connection directly. Used by upgradeAgent to avoid circular calls.
			if c.skipVersionCheck {
				return conn, nil
			}

			// Probe agent version
			agentVersion, probeErr := c.probeVersion(ctx, conn)
			if probeErr != nil {
				conn.Close()
				if c.upgradeInProgress {
					// During upgrade retry, probe may fail because the agent
					// is still restarting — just continue the probe loop.
					continue
				}
				return nil, fmt.Errorf("version probe failed: %w", probeErr)
			}

			hostVersion := version.BuildVersion
			if hostVersion == "" {
				hostVersion = defaultVersion
			}

			// After a successful upgrade attempt, check if the new agent
			// version is at least as new as the host binary.
			if c.upgradeInProgress && version.Compare(hostVersion, agentVersion) <= 0 {
				c.upgradeInProgress = false
				if c.OnUpgradeCompleted != nil {
					c.OnUpgradeCompleted(ctx, agentVersion)
				}
				// Agent is now current — fall through to return conn.
				c.AgentVersion = agentVersion
				return conn, nil
			}

			// If the host binary is newer than the guest agent, trigger upgrade.
			if version.Compare(hostVersion, agentVersion) > 0 {
				conn.Close()
				if c.upgradeInProgress {
					return nil, fmt.Errorf("upgrade already in progress for VM %s", c.VmName)
				}
				c.upgradeInProgress = true
				if c.OnUpgradeStarted != nil {
					c.OnUpgradeStarted(ctx, agentVersion, hostVersion)
				}
				if upgradeErr := c.upgradeAgent(ctx, agentVersion); upgradeErr != nil {
					c.upgradeInProgress = false
					if c.OnUpgradeFailed != nil {
						c.OnUpgradeFailed(ctx, upgradeErr)
					}
					return nil, fmt.Errorf("agent upgrade failed: %w", upgradeErr)
				}
				// Wait for agent to restart before retrying the dial loop.
				// The upgrade exec uses a delayed restart (sleep 1 + systemctl)
				// so the exec result frame is sent before the agent dies.
				// Without this wait, the retry connects to the still-running
				// old agent and tries to upgrade again.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(3 * time.Second):
				}
				// Reset deadline: agent restart takes time.
				deadline = time.Now().Add(c.ProbeTimeout)
				continue
			}

			// Agent version is current — use this connection.
			c.AgentVersion = agentVersion
			if c.OnVersionKnown != nil {
				c.OnVersionKnown(ctx, agentVersion)
			}
			return conn, nil
		}

		// Log failed dial attempt timing
		timinglog.Log("vsock_dial", dialElapsed,
			"vm_name", c.VmName,
			"vm_id", c.item.VmID,
			"attempt", attempts,
			"error", err.Error(),
		)

		// Connection failed (agent not ready yet) — probe again after interval.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(vsockProbeInterval):
		}
	}
}

// RescanPCI triggers a PCI bus rescan inside the guest so that devices
// hotplugged by the VMM are discovered by the kernel.
func (c *Client) RescanPCI(ctx context.Context) error {
	_, err := c.Exec(ctx, "echo 1 > /sys/bus/pci/rescan", "root", 10, nil, false)
	return err
}

// RemoveHotpluggedPCIDevice removes the last non-root virtio block device from
// the guest PCI bus. This is required before the host asks Firecracker to
// hot-unplug the corresponding drive.
func (c *Client) RemoveHotpluggedPCIDevice(ctx context.Context) error {
	cmd := "last_dev=$(ls /sys/block | grep '^vd[b-z]' | sort | tail -1); " +
		"if [ -n \"$last_dev\" ]; then " +
		"bdf=$(basename \"$(readlink -f /sys/block/$last_dev/device/..)\"); " +
		"echo 1 > /sys/bus/pci/devices/$bdf/remove; " +
		"fi"
	_, err := c.Exec(ctx, cmd, "root", 10, nil, false)
	return err
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

// upgradeExec sends a raw exec command for agent upgrade purposes.
// Unlike the public Exec method, this helper treats io.EOF during response
// reading as success because the upgrade command restarts the agent, which
// may close the connection before a result frame is sent.
// It returns an error only if a result frame explicitly indicates failure.
// The method dials a fresh connection (via dialRaw) — independent of the
// caller's connection.
func (c *Client) upgradeExec(ctx context.Context, command, user string, timeout int) error {
	conn, err := c.dialRaw(ctx)
	if err != nil {
		return fmt.Errorf("dial for upgrade exec: %w", err)
	}
	defer conn.Close()

	// Apply deadline from context if set.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	req := execRequest{
		ID:      "upgrade:1",
		Type:    "exec",
		Command: command,
		Token:   c.item.Token,
		Timeout: timeout,
		User:    user,
	}

	if err := SendFrame(conn, req); err != nil {
		return fmt.Errorf("send upgrade exec request: %w", err)
	}

	// Read frames until result or EOF.
	// EOF is treated as success because the upgrade command restarts the
	// agent, which may close the connection before a result frame is sent.
	dec := json.NewDecoder(conn)
	for {
		var resp execResponse
		if err := dec.Decode(&resp); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				// Agent restarted — command succeeded.
				return nil
			}
			return fmt.Errorf("read upgrade exec response: %w", err)
		}

		switch resp.Type {
		case "stdout", "stderr":
			if resp.Data != "" {
				slog.Debug("upgrade exec output", "vm_id", c.item.VmID, "type", resp.Type, "data", resp.Data)
			}
		case "result":
			if resp.Error != "" {
				return fmt.Errorf("upgrade exec failed: %s", resp.Error)
			}
			if resp.Status != 0 {
				return fmt.Errorf("upgrade exec exited with code %d", resp.Status)
			}
			return nil
		default:
			slog.Debug("upgrade exec ignoring frame", "type", resp.Type)
		}
	}
}

// probeVersion sends a version request to the guest agent over an established
// connection and returns the agent's version string. The agent responds with a
// JSON payload containing the agent_version field.
func (c *Client) probeVersion(ctx context.Context, conn net.Conn) (string, error) {
	req := execRequest{
		ID:    "v:1",
		Type:  requestTypeVersion,
		Token: c.item.Token,
	}
	if err := SendFrame(conn, req); err != nil {
		return "", fmt.Errorf("send version request: %w", err)
	}
	var resp execResponse
	if err := readFrameRaw(conn, &resp); err != nil {
		return "", fmt.Errorf("read version response: %w", err)
	}
	if resp.Type != responseTypeVersion {
		return "", fmt.Errorf("unexpected version response type: %s", resp.Type)
	}
	var data struct {
		AgentVersion string `json:"agent_version"`
	}
	if err := json.Unmarshal([]byte(resp.Data), &data); err != nil {
		return "", fmt.Errorf("parse version response: %w", err)
	}
	if data.AgentVersion == "" {
		return "", fmt.Errorf("empty agent version in response")
	}
	return data.AgentVersion, nil
}

// dialRaw dials the UDS socket and performs the CONNECT handshake only.
// No version probe or upgrade check. Used by upgradeAgent to avoid
// circular calls back into ensureAgent.
func (c *Client) dialRaw(ctx context.Context) (net.Conn, error) {
	return dialAndHandshake(ctx, c.item.UDSPath, c.item.Port, 0)
}

// upgradeAgent upgrades the guest agent inside the VM to the version
// embedded in the current host binary. It pushes the binary, verifies it,
// replaces the running agent, and restarts the agent service.
func (c *Client) upgradeAgent(ctx context.Context, oldVersion string) error {
	hostVer := version.BuildVersion
	if hostVer == "" {
		hostVer = defaultVersion
	}
	slog.Info("upgrading agent", "vm", c.VmName, "from", oldVersion, "to", hostVer)

	// Step 1: Write embedded binary to temp file on host
	embedded := agent.AgentBinary()
	if len(embedded) == 0 {
		return fmt.Errorf("embedded agent binary is empty — check build process")
	}
	tmpPath := filepath.Join(os.TempDir(), "mvm-agent-upgrade-"+c.item.VmID)
	if err := os.WriteFile(tmpPath, embedded, 0755); err != nil {
		return fmt.Errorf("write agent binary to temp: %w", err)
	}
	defer os.Remove(tmpPath)

	// Step 2: Push to VM via file transfer (skip version check)
	pushClient := &Client{
		item:             c.item,
		ProbeTimeout:     c.ProbeTimeout,
		VmName:           c.VmName,
		skipVersionCheck: true,
	}
	_, err := pushClient.FTCopyToVM(ctx, []string{tmpPath}, "/usr/bin/mvm-agent.new", true, false, nil)
	if err != nil {
		return fmt.Errorf("push agent binary to VM: %w", err)
	}

	// Step 3: Replace and restart
	// SHA-256 verification during FTCopyToVM already guarantees the binary
	// was transferred correctly. No need to exec-verify it.
	// Backup is best-effort (may not exist on first upgrade).
	// The chain continues even if cp fails.
	// upgradeExec handles the case where the connection drops mid-response
	// (agent restarted) by treating EOF as success.
	err = c.upgradeExec(ctx, upgradeShellCommand, "root", 30)
	if err != nil {
		// Try to restore from backup (if it exists)
		restoreErr := c.upgradeExec(ctx, restoreShellCommand, "root", 15)
		if restoreErr != nil {
			slog.Error("failed to restore agent backup", "vm", c.VmName, "error", restoreErr)
		}
		return fmt.Errorf("upgrade exec failed: %w", err)
	}

	return nil
}
