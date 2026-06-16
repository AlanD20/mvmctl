package vsockagent

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
)

// handleConnection reads JSON frames from the vsock connection and dispatches
// them to the appropriate handler. For TTY sessions, the connection is taken
// over and this function does not return until the session ends.
func (a *Agent) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	for {
		req, err := readFrame(conn)
		if err != nil {
			// Connection closed or unrecoverable error — stop handling.
			return
		}

		// Auth check: if the agent has a token configured, require it.
		// Ping requests are exempt from auth.
		if req.Type != requestTypePing && a.token != "" &&
			subtle.ConstantTimeCompare([]byte(req.Token), []byte(a.token)) != 1 {
			slog.Warn("auth rejected", "id", req.ID, "type", req.Type)
			_ = writeFrame(conn, &execResponse{ // best-effort: connection may be closed already
				ID:    req.ID,
				Type:  responseTypeError,
				Error: "invalid auth token",
			})
			continue
		}

		switch req.Type {
		case requestTypeExec:
			handleExec(ctx, req, conn)

		case requestTypeExecTTY:
			// Send TTY acknowledgement before switching to raw relay.
			if err := writeFrame(conn, &execResponse{
				ID:   req.ID,
				Type: responseTypeTTY,
			}); err != nil {
				slog.Error("write TTY ack", "id", req.ID, "error", err)
				return
			}
			// TTY session takes over the connection. No more JSON framing.
			handleTTY(ctx, conn, req)
			return

		case requestTypePing:
			if err := writeFrame(conn, &execResponse{
				ID:   req.ID,
				Type: responseTypePong,
			}); err != nil {
				slog.Error("write pong", "id", req.ID, "error", err)
				return
			}

		case requestTypeFileTransfer:
			ackPayload := fmt.Sprintf(`{"buf":%d}`, ftBufferSize)
			if err := writeFrame(conn, &execResponse{
				ID:   req.ID,
				Type: responseTypeFTReady,
				Data: ackPayload,
			}); err != nil {
				slog.Error("ft: write ready ack", "id", req.ID, "error", err)
				return
			}
			handleFileTransfer(ctx, conn, req)
			return

		default:
			slog.Warn("unknown request type", "id", req.ID, "type", req.Type)
			_ = writeFrame(conn, &execResponse{ // best-effort: connection may be closed already
				ID:    req.ID,
				Type:  responseTypeError,
				Error: "unknown request type: " + req.Type,
			})
		}
	}
}
