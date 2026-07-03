package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"

	"mvmctl/internal/lib/version"
)

// handleConnection reads JSON frames from the vsock connection and dispatches
// them to the appropriate handler. For TTY sessions, the connection is taken
// over and this function does not return until the session ends.
func (a *Agent) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	for {
		// Serialize reads with handleLocalConn to prevent two goroutines
		// reading from the same vsock conn concurrently.
		a.readMu.Lock()
		req, err := readFrame(conn)
		a.readMu.Unlock()
		if err != nil {
			return
		}

		// Auth check: if the agent has a token configured, require it.
		// Ping requests are exempt from auth.
		if req.Type != requestTypePing && a.token != "" &&
			subtle.ConstantTimeCompare([]byte(req.Token), []byte(a.token)) != 1 {
			slog.Warn("auth rejected", "id", req.ID, "type", req.Type)
			a.connMu.Lock()
			_ = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeError, Error: "invalid auth token",
			})
			a.connMu.Unlock()
			continue
		}

		// For TTY sessions, don't register activeConn so handleLocalConn
		// can't write JSON into a raw binary stream.
		if req.Type == requestTypeExec {
			a.activeConnMu.Lock()
			a.activeConn = conn
			a.activeConnMu.Unlock()
		}

		switch req.Type {
		case requestTypeExec:
			handleExec(ctx, req, conn, &a.connMu)
			a.activeConnMu.Lock()
			if a.activeConn == conn {
				a.activeConn = nil
			}
			a.activeConnMu.Unlock()

		case requestTypeExecTTY:
			// Send TTY acknowledgement before switching to raw relay.
			if err := writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeTTY,
			}); err != nil {
				slog.Error("write TTY ack", "id", req.ID, "error", err)
				return
			}
			// TTY session takes over the connection. No more JSON framing.
			handleTTY(ctx, conn, req)
			return

		case requestTypePing:
			a.connMu.Lock()
			err = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypePong,
			})
			a.connMu.Unlock()
			if err != nil {
				slog.Error("write pong", "id", req.ID, "error", err)
				return
			}

		case requestTypeVersion:
			agentVersion := version.VersionString()
			data, _ := json.Marshal(map[string]string{
				"agent_version": agentVersion,
			})
			a.connMu.Lock()
			err = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeVersion, Data: string(data),
			})
			a.connMu.Unlock()
			if err != nil {
				slog.Error("write version response", "id", req.ID, "error", err)
				return
			}

		case requestTypeFileTransfer:
			ackPayload := fmt.Sprintf(`{"buf":%d}`, ftBufferSize)
			if err := writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeFTReady, Data: ackPayload,
			}); err != nil {
				slog.Error("ft: write ready ack", "id", req.ID, "error", err)
				return
			}
			handleFileTransfer(ctx, conn, req)
			return

		default:
			slog.Warn("unknown request type", "id", req.ID, "type", req.Type)
			a.connMu.Lock()
			_ = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeError, Error: "unknown request type: " + req.Type,
			})
			a.connMu.Unlock()
		}
	}
}
