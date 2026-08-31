// Package vsockhandler dispatches guest-initiated vsock frames that require
// host-side processing. Handler is wired into the vsock client by pkg/api.
package vsockhandler

import (
	"context"
	"log/slog"
	"net"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/service/agent"
	"mvmctl/pkg/errs"
)

const remoteExecDeniedMessage = "remote VM execution is not authorized"

// Handler dispatches guest-initiated vsock frames that require host processing.
type Handler struct{}

// Handle processes guest-initiated host frames.
func (*Handler) Handle(
	_ context.Context,
	_ string,
	conn net.Conn,
	frameType string,
	_ string,
) error {
	switch frameType {
	case vsock.ResponseTypeRemoteVM:
		// CRITICAL: Legacy remote exec has no authorization path. Reject it
		// before inspecting source identity or request data.
		responseErr := vsock.SendFrame(conn, agent.RemoteVMResponse{
			Type:   vsock.ResponseTypeRemoteVM,
			Status: 1,
			Error:  remoteExecDeniedMessage,
		})
		if responseErr != nil {
			return errs.WrapMsg(errs.CodeUnauthorized, remoteExecDeniedMessage, responseErr)
		}
		return errs.New(errs.CodeUnauthorized, remoteExecDeniedMessage)
	default:
		slog.Warn("vsockhandler: unknown frame type", "type", frameType)
		return nil
	}
}
