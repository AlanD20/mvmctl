package agent

import (
	"encoding/json"
	"log/slog"
	"net"
)

// handleLocalConn handles an incoming connection on the local Unix socket.
// It reads a RemoteVMRequest from the client, forwards it to the host via the
// active vsock connection, and relays response frames back to the local client.
func (a *Agent) handleLocalConn(localConn net.Conn) {
	defer localConn.Close()

	// Read RemoteVMRequest from local socket.
	var req RemoteVMRequest
	if err := json.NewDecoder(localConn).Decode(&req); err != nil {
		slog.Error("local: failed to decode request", "error", err)
		return
	}

	slog.Debug("local: received remote exec request",
		"destination", req.Destination,
		"command", req.Command,
	)

	// Get the active vsock connection.
	a.activeConnMu.Lock()
	conn := a.activeConn
	a.activeConnMu.Unlock()
	if conn == nil {
		slog.Error("local: no active vsock connection")
		_ = writeFrame(localConn, &execResponse{
			Type:   responseTypeRemoteVM,
			Status: 1,
			Error:  "no active vsock connection",
		})
		return
	}

	// Marshal the request payload and write a remote_vm frame to the vsock
	// connection. This is sent to the host which will relay to the target VM.
	payload, err := json.Marshal(req)
	if err != nil {
		slog.Error("local: failed to marshal remote request", "error", err)
		return
	}

	// Lock connMu to serialize writes with streamingWriter in handleExec.
	// During the relay handleExec is blocked on cmd.Wait() and the child
	// produces no output, so contention is unlikely but the mutex guarantees
	// correctness.
	a.connMu.Lock()
	err = writeFrame(conn, &execResponse{
		Type: responseTypeRemoteVM,
		Data: string(payload),
	})
	a.connMu.Unlock()
	if err != nil {
		slog.Error("local: failed to write remote_vm frame to vsock", "error", err)
		return
	}

	// Read response frames from the vsock connection and forward them to the
	// local socket. The host relays stdout/stderr frames and sends a final
	// "remote_vm" frame with the exit status.
	decoder := json.NewDecoder(conn)
	for {
		var resp execResponse
		a.readMu.Lock()
		err := decoder.Decode(&resp)
		a.readMu.Unlock()
		if err != nil {
			slog.Error("local: failed to decode vsock response", "error", err)
			return
		}

		switch resp.Type {
		case "stdout":
			if err := writeFrame(localConn, &execResponse{
				Type: "stdout",
				Data: resp.Data,
			}); err != nil {
				slog.Error("local: failed to forward stdout frame", "error", err)
				return
			}
		case "stderr":
			if err := writeFrame(localConn, &execResponse{
				Type: "stderr",
				Data: resp.Data,
			}); err != nil {
				slog.Error("local: failed to forward stderr frame", "error", err)
				return
			}
		case responseTypeRemoteVM:
			// Final frame with exit code from the remote execution.
			if err := writeFrame(localConn, &execResponse{
				Type:   responseTypeRemoteVM,
				Status: resp.Status,
				Error:  resp.Error,
			}); err != nil {
				slog.Error("local: failed to write final remote_vm frame", "error", err)
			}
			return
		default:
			slog.Debug("local: ignoring unknown frame type", "type", resp.Type)
		}
	}
}
