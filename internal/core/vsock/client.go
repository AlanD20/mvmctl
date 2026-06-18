package vsock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/version"
	"mvmctl/internal/service/vsockagent"
	"mvmctl/pkg/errs"
)

const (
	// defaultVersion is the fallback version string used when BuildVersion
	// is empty (e.g. development builds without ldflags).
	defaultVersion = "0.0.0"

	// upgradeShellCommand replaces the running agent binary and restarts the
	// agent service after a 2-second delay. The delay allows the exec
	// response frame to be sent before the old agent is killed.
	upgradeShellCommand = `cp /usr/bin/mvm-vsock-agent /usr/bin/mvm-vsock-agent.bak 2>/dev/null || true; mv /usr/bin/mvm-vsock-agent.new /usr/bin/mvm-vsock-agent && chmod 0755 /usr/bin/mvm-vsock-agent && ( sleep 2 && systemctl restart mvm-vsock-agent ) &`

	// restoreShellCommand restores the previous agent binary from backup and
	// restarts the service. Used as a rollback if the upgrade exec fails.
	restoreShellCommand = `test -f /usr/bin/mvm-vsock-agent.bak && cp /usr/bin/mvm-vsock-agent.bak /usr/bin/mvm-vsock-agent && ( sleep 1 && systemctl restart mvm-vsock-agent ) &; true`
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

	// Internal: set during upgrade, cleared on successful retry.
	upgradeInProgress bool

	// Internal: bypasses version probe (used by upgradeAgent to avoid circular calls).
	skipVersionCheck bool

	// OnUpgradeStarted is called before the upgrade begins.
	// The callback should set the DB upgrade lock and log the event.
	OnUpgradeStarted func(ctx context.Context, fromVersion, toVersion string)

	// OnUpgradeCompleted is called after the upgrade succeeds and the
	// retry loop confirms the new agent version is running.
	OnUpgradeCompleted func(ctx context.Context, newVersion string)
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
	conn, err := c.ensureAgent(ctx)
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
	// --- Bidirectional relay ---
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

// ensureAgent retries dialAndHandshake until the guest agent responds or the
// probe timeout expires. After a successful dial, it probes the agent version
// and triggers an upgrade if the host binary is newer than the guest agent.
// ProbeTimeout must be > 0 — callers (API layer) set it from
// defaults.vm.vsock_probe_timeout (config default: 60s).
// When timing is enabled, logs per-attempt vsock_dial and overall vsock_probe timing.
func (c *Client) ensureAgent(ctx context.Context) (net.Conn, error) {
	if c.ProbeTimeout <= 0 {
		return nil, errs.New(errs.CodeVsockConnectionFailed,
			"vsock agent probe timeout not set — API layer must set ProbeTimeout from config")
	}

	start := time.Now()
	deadline := time.Now().Add(c.ProbeTimeout)
	attempts := 0

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
			infra.LogTiming("vsock_probe", c.VmName, c.item.VmID, elapsedMs,
				"attempts", attempts,
				"error", "timeout",
			)
			return nil, errs.New(
				errs.CodeVsockConnectionFailed,
				fmt.Sprintf(
					"vsock agent did not become reachable within %v after %d attempt(s)",
					c.ProbeTimeout,
					attempts,
				),
			)
		}

		attempts++

		// Per-attempt timing: wrap dialAndHandshake with vsock_dial
		dialStart := time.Now()
		conn, err := dialAndHandshake(ctx, c.item.UDSPath, c.item.Port)
		dialElapsed := float64(time.Since(dialStart).Microseconds()) / 1000.0

		if err == nil {
			elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
			infra.LogTiming("vsock_probe", c.VmName, c.item.VmID, elapsedMs,
				"attempts", attempts,
			)
			infra.LogTiming("vsock_dial", c.VmName, c.item.VmID, dialElapsed,
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
			if c.upgradeInProgress && !version.SemverGreater(hostVersion, agentVersion) {
				c.upgradeInProgress = false
				if c.OnUpgradeCompleted != nil {
					c.OnUpgradeCompleted(ctx, agentVersion)
				}
				// Agent is now current — fall through to return conn.
				c.AgentVersion = agentVersion
				return conn, nil
			}

			// If the host binary is newer than the guest agent, trigger upgrade.
			if version.SemverGreater(hostVersion, agentVersion) {
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
			return conn, nil
		}

		// Log failed dial attempt timing
		infra.LogTiming("vsock_dial", c.VmName, c.item.VmID, dialElapsed,
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

// probeVersion sends a version request to the guest agent over an established
// connection and returns the agent's version string. The agent responds with a
// JSON payload containing the agent_version field.
func (c *Client) probeVersion(ctx context.Context, conn net.Conn) (string, error) {
	req := execRequest{
		ID:    "v:1",
		Type:  requestTypeVersion,
		Token: c.item.Token,
	}
	if err := sendFrame(conn, req); err != nil {
		return "", fmt.Errorf("send version request: %w", err)
	}
	var resp execResponse
	if err := readFrame(conn, &resp); err != nil {
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
	return dialAndHandshake(ctx, c.item.UDSPath, c.item.Port)
}

// upgradeAgent upgrades the guest agent inside the VM to the version
// embedded in the current host binary. It pushes the binary, verifies it,
// replaces the running agent, and restarts the agent service.
func (c *Client) upgradeAgent(ctx context.Context, oldVersion string) error {
	hostVer := version.BuildVersion
	if hostVer == "" {
		hostVer = defaultVersion
	}
	slog.Info("upgrading vsock agent", "vm", c.VmName, "from", oldVersion, "to", hostVer)

	// Step 1: Write embedded binary to temp file on host
	embedded := vsockagent.AgentBinary()
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
	_, err := pushClient.FTCopyToVM(ctx, []string{tmpPath}, "/usr/bin/mvm-vsock-agent.new", true, nil)
	if err != nil {
		return fmt.Errorf("push agent binary to VM: %w", err)
	}

	// Step 3: Replace and restart
	// SHA-256 verification during FTCopyToVM already guarantees the binary
	// was transferred correctly. No need to exec-verify it.
	// Backup is best-effort (may not exist on first upgrade).
	// The chain continues even if cp fails.
	execClient := &Client{
		item:             c.item,
		ProbeTimeout:     c.ProbeTimeout,
		VmName:           c.VmName,
		skipVersionCheck: true,
	}
	_, err = execClient.Exec(ctx, upgradeShellCommand, "root", 30)
	if err != nil {
		// Try to restore from backup (if it exists)
		_, restoreErr := execClient.Exec(ctx, restoreShellCommand, "root", 15)
		if restoreErr != nil {
			slog.Error("failed to restore agent backup", "vm", c.VmName, "error", restoreErr)
		}
		return fmt.Errorf("upgrade exec failed: %w", err)
	}

	return nil
}
