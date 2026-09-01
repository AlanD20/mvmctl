package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"mvmctl/internal/lib/version"
	"mvmctl/internal/lib/vmexecwire"
)

// handleConnection reads JSON frames from the vsock connection and dispatches
// them to the appropriate handler. For TTY sessions, the connection is taken
// over and this function does not return until the session ends.
func (a *Agent) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetReadDeadline(time.Now())
		case <-connectionDone:
		}
	}()

	prefix := make([]byte, len(vmexecwire.FrameMagic))
	if _, err := io.ReadFull(conn, prefix); err != nil {
		return
	}
	reader := io.MultiReader(bytes.NewReader(prefix), conn)
	if string(prefix) == vmexecwire.SourceSessionMagic {
		sourceDeadline := time.Now().Add(vmexecwire.MaxExecutionDuration)
		if err := conn.SetDeadline(sourceDeadline); err != nil {
			return
		}
		a.handleSourceConnection(ctx, reader, conn, sourceDeadline)
		return
	}
	if string(prefix) == vmexecwire.FrameMagic {
		if err := conn.SetDeadline(time.Now().Add(vmexecwire.MaxExecutionDuration)); err != nil {
			return
		}
		a.handleDestinationConnection(ctx, reader, conn)
		return
	}
	legacyDone := make(chan struct{})
	defer close(legacyDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-legacyDone:
		}
	}()
	a.handleLegacyConnection(ctx, reader, conn)
}

func (a *Agent) handleLegacyConnection(ctx context.Context, reader io.Reader, conn net.Conn) {
	connMu := &sync.Mutex{}
	for {
		req, err := readFrame(reader)
		if err != nil {
			return
		}

		// Auth check: if the agent has a token configured, require it.
		// Ping requests are exempt from auth.
		if req.Type != requestTypePing && a.token != "" &&
			subtle.ConstantTimeCompare([]byte(req.Token), []byte(a.token)) != 1 {
			slog.Warn("auth rejected", "id", req.ID, "type", req.Type)
			connMu.Lock()
			_ = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeError, Error: "invalid auth token",
			})
			connMu.Unlock()
			continue
		}

		switch req.Type {
		case requestTypeExec:
			if req.SourceSessionID == "" {
				a.handleExec(ctx, req, conn, connMu)
				continue
			}
			execCtx, cancel := context.WithCancel(ctx)
			go func() {
				var unexpectedInput [1]byte
				_, _ = conn.Read(unexpectedInput[:])
				cancel()
			}()
			a.handleExec(execCtx, req, conn, connMu)
			cancel()
			return

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
			connMu.Lock()
			err = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypePong,
			})
			connMu.Unlock()
			if err != nil {
				slog.Error("write pong", "id", req.ID, "error", err)
				return
			}

		case requestTypeVersion:
			agentVersion := version.VersionString()
			data, _ := json.Marshal(map[string]string{
				"agent_version": agentVersion,
			})
			connMu.Lock()
			err = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeVersion, Data: string(data),
			})
			connMu.Unlock()
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
			connMu.Lock()
			_ = writeFrame(conn, &execResponse{
				ID: req.ID, Type: responseTypeError, Error: "unknown request type: " + req.Type,
			})
			connMu.Unlock()
		}
	}
}
