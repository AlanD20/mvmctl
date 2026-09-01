package agent

import (
	"context"
	"io"
	"net"

	"mvmctl/internal/lib/vmexecwire"
)

func (a *Agent) handleLocalConnection(ctx context.Context, localConn net.Conn) {
	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = localConn.Close()
		case <-connectionDone:
		}
	}()
	a.handleLocalConn(ctx, localConn)
}

// handleLocalConn admits one process-bound local peer and relays one closed VM-exec request.
func (a *Agent) handleLocalConn(ctx context.Context, localConn net.Conn) {
	defer localConn.Close()
	request, err := vmexecwire.DecodeSourceRequest(ctx, localConn)
	if err != nil {
		return
	}
	binding, err := a.claimSourceForPeer(ctx, localConn)
	if err != nil {
		writeLocalSourceError(ctx, localConn, request.RequestID)
		return
	}
	defer a.cleanupSourceBinding(binding)

	if err := vmexecwire.EncodeSourceRequest(ctx, binding.transport.conn, request); err != nil {
		writeLocalSourceError(ctx, localConn, request.RequestID)
		return
	}
	relaySourceConnections(localConn, binding.transport)
}

func writeLocalSourceError(ctx context.Context, conn net.Conn, requestID string) {
	writer, err := vmexecwire.NewResponseWriter(conn, requestID)
	if err != nil {
		return
	}
	_ = writer.WriteError(ctx, "source execution is unavailable")
}

func relaySourceConnections(localConn net.Conn, transport *sourceTransport) {
	completed := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(transport.conn, localConn)
		completed <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(localConn, transport.conn)
		completed <- struct{}{}
	}()
	<-completed
	transport.close()
	_ = localConn.Close()
	<-completed
}
