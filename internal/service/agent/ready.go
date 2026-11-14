package agent

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// handleReady accepts only finite checks and never creates a VM-to-VM source session.
func handleReady(ctx context.Context, req *execRequest, conn net.Conn, connMu *sync.Mutex) {
	if req.Timeout <= 0 || int64(req.Timeout) > int64((1<<63-1)/time.Second) ||
		strings.TrimSpace(req.Command) == "" || req.SourceSessionID != "" {
		// The connection is exclusively owned here; failure to report is followed by close.
		_ = writeFrame(conn, &execResponse{ID: req.ID, Type: responseTypeError, Error: "invalid readiness request"})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline() // WithTimeout always provides a finite deadline.
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var unexpected [1]byte
		_, _ = conn.Read(unexpected[:]) // Disconnect or unexpected input cancels the single check.
		cancel()
	}()
	defer func() { _ = conn.Close(); <-done }()
	req.NoSync = true
	handleExecCommand(ctx, req, conn, connMu, nil, nil)
}
