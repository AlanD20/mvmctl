package vsock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"mvmctl/pkg/errs"
)

// --- Constants ---

const (
	constConnectTimeout = 5 * time.Second
)

// --- File transfer protocol constants ---

const (
	requestTypeFileTransfer = "file-transfer"
	requestTypeVersion      = "version"
	responseTypeFTReady     = "ft-ready"
	responseTypeVersion     = "version"
	ftBufferSize            = 262144
)

// --- Wire protocol types (unexported) ---

// execRequest is the JSON frame sent from host to guest agent.
type execRequest struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"` // "exec", "exec-tty", "ping", "resize"
	Command string            `json:"command,omitempty"`
	Token   string            `json:"token,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
	User    string            `json:"user,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	NoSync  bool              `json:"no_sync,omitempty"`
	Rows    int               `json:"rows,omitempty"`
	Cols    int               `json:"cols,omitempty"`
}

// execResponse is the JSON frame received from the guest agent.
type execResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"` // "result", "tty", "pong", "stdout", "stderr"
	Status     int    `json:"status,omitempty"`
	Data       string `json:"data,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// --- UDS dial and CONNECT handshake ---

// dialAndHandshake connects to the Firecracker vsock UDS and performs the
// CONNECT handshake. Returns an open connection on success.
func dialAndHandshake(ctx context.Context, udsPath string, port int, attemptNum int) (net.Conn, error) {
	slog.Debug("vsock dial attempt",
		"uds_path", udsPath,
		"port", port,
		"attempt", attemptNum,
	)

	d := net.Dialer{Timeout: constConnectTimeout}
	conn, err := d.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}

	// Send CONNECT <port>\n
	_, err = fmt.Fprintf(conn, "CONNECT %d\n", port)
	if err != nil {
		conn.Close()
		return nil, errs.WrapMsg(errs.CodeVsockHandshakeFailed,
			"failed to write CONNECT handshake", err)
	}

	// Set per-attempt read timeout to prevent blocking on a half-open
	// connection (Firecracker accepts UDS connect but never responds).
	if err := conn.SetReadDeadline(time.Now().Add(constConnectTimeout)); err != nil {
		conn.Close()
		return nil, errs.WrapMsg(errs.CodeVsockHandshakeFailed,
			"failed to set read deadline", err)
	}

	// Read response with context awareness: launch a goroutine and select
	// on both the read result and context cancellation.
	type readResult struct {
		resp string
		err  error
	}
	resCh := make(chan readResult, 1)
	go func() {
		resp, err := bufio.NewReader(conn).ReadString('\n')
		resCh <- readResult{resp, err}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			conn.Close()
			slog.Debug("vsock handshake read failed",
				"uds_path", udsPath,
				"port", port,
				"attempt", attemptNum,
				"error", res.err,
			)
			return nil, errs.WrapMsg(errs.CodeVsockHandshakeFailed,
				"failed to read CONNECT response", res.err)
		}
		// Firecracker acknowledges with "OK <assigned_hostside_port>\n" where
		// the host-side port is dynamically assigned, NOT the requested port.
		// See https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
		if len(res.resp) < 3 || res.resp[:3] != "OK " {
			conn.Close()
			// Build safe error message (avoid panic on empty/short response).
			got := res.resp
			if len(got) > 0 && got[len(got)-1] == '\n' {
				got = got[:len(got)-1]
			}
			slog.Debug("vsock handshake failed",
				"uds_path", udsPath,
				"port", port,
				"attempt", attemptNum,
				"response", got,
			)
			return nil, errs.New(errs.CodeVsockHandshakeFailed,
				fmt.Sprintf("handshake failed: got %q, expected \"OK ...\"", got))
		}
		// Clear read deadline so the caller's own deadline governs later reads.
		_ = conn.SetReadDeadline(time.Time{})
		slog.Debug("vsock handshake succeeded",
			"uds_path", udsPath,
			"port", port,
			"attempt", attemptNum,
			"response", trimTrailingNewline(res.resp),
		)
		return conn, nil

	case <-ctx.Done():
		conn.Close()
		return nil, errs.WrapMsg(errs.CodeVsockHandshakeFailed,
			"handshake cancelled by context", ctx.Err())
	}
}

// trimTrailingNewline removes a single trailing newline from s.
func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

// --- JSON framing helpers ---

// sendFrame marshals v as JSON and writes it to conn followed by a newline.
func sendFrame(conn net.Conn, v any) error {
	return json.NewEncoder(conn).Encode(v)
}

// readFrame reads a newline-delimited JSON message from conn and unmarshals
// it into v.
func readFrame(conn net.Conn, v any) error {
	return json.NewDecoder(conn).Decode(v)
}
