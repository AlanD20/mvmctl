package agent

import (
	"context"
	"io"
	"net"
	"time"

	"mvmctl/internal/lib/vmexecwire"
)

const (
	maxSourceAdmissions    = 8
	sourceRequestReadLimit = 2 * time.Second
)

func (a *Agent) handleLocalConnection(ctx context.Context, localConn net.Conn) {
	if !a.beginSourceAdmission() {
		_ = localConn.Close()
		return
	}
	defer func() { <-a.sourceAdmission }()
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
	binding, err := a.claimSourceForPeer(ctx, localConn)
	if err != nil {
		return
	}
	defer a.cleanupSourceBinding(binding)
	if err := binding.transport.handoffReadOwner(); err != nil {
		return
	}

	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-binding.transport.done:
			// Outer cleanup must interrupt a partial local request.
			_ = localConn.Close()
		case <-connectionDone:
		}
	}()
	requestDeadline := time.Now().Add(sourceRequestReadLimit)
	if binding.transport.deadline.Before(requestDeadline) {
		requestDeadline = binding.transport.deadline
	}
	if err := localConn.SetReadDeadline(requestDeadline); err != nil {
		return
	}
	request, err := vmexecwire.DecodeSourceRequest(ctx, localConn)
	if err != nil {
		return
	}
	if err := localConn.SetDeadline(binding.transport.deadline); err != nil {
		return
	}

	if err := vmexecwire.EncodeSourceRequest(ctx, binding.transport.conn, request); err != nil {
		writeLocalSourceError(ctx, localConn, request.RequestID)
		return
	}
	relaySourceConnections(localConn, binding.transport)
}

func (a *Agent) beginSourceAdmission() bool {
	a.sourceMu.Lock()
	a.ensureSourceStateLocked()
	admission := a.sourceAdmission
	a.sourceMu.Unlock()
	select {
	case admission <- struct{}{}:
		return true
	default:
		return false
	}
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
