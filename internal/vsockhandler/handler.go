// Package vsockhandler dispatches guest-initiated vsock frames that require
// host-side processing. This keeps internal/core/vsock domain-pure (no imports
// of vm, model, etc.) while allowing an extensible handler in its own package.
//
// Handler is wired once at startup in pkg/api/cp.go via Client.OnHostFrame.
package vsockhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/agent"
	"mvmctl/pkg/errs"
)

// vsockFrame is the wire format for frames exchanged over vsock.
// Used by the streaming relay loop to read all fields from any frame type.
type vsockFrame struct {
	Type   string `json:"type"`
	Status int    `json:"status,omitempty"`
	Data   string `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Handler dispatches guest-initiated vsock frames that require host processing.
// Each frame type maps to a method on Handler.
type Handler struct {
	VMResolver *vm.Resolver
	VsockRepo  vsock.Repository
}

// Handle is called by Client.OnHostFrame when Exec() receives a non-standard
// frame (not "stdout", "stderr", or "result"). Currently dispatches:
//   - vsock.ResponseTypeRemoteVM ("remote_vm")
func (h *Handler) Handle(ctx context.Context, sourceVMID string, conn net.Conn, frameType string, data string) error {
	switch frameType {
	case vsock.ResponseTypeRemoteVM:
		return h.handleRemoteVM(ctx, sourceVMID, conn, data)
	default:
		slog.Warn("vsockhandler: unknown frame type", "type", frameType)
		return nil
	}
}

// handleRemoteVM processes a "remote_vm" frame from the guest agent, which
// requests execution of a command on another VM. It performs authorization
// checks on both source and target VM, opens a new vsock connection to the
// target, sends the exec command, and streams the output frames back to the
// source.
func (h *Handler) handleRemoteVM(ctx context.Context, sourceVMID string, sourceConn net.Conn, data string) error {
	// 1. Check source VM exists and has remote_exec enabled.
	sourceVM, err := h.VMResolver.Resolve(ctx, sourceVMID)
	if err != nil {
		_ = vsock.SendFrame(sourceConn, agent.RemoteVMResponse{
			Type: vsock.ResponseTypeRemoteVM, Status: 1, Error: "source VM not found",
		})
		return errs.WrapMsg(errs.CodeVMNotFound,
			"remote exec: source VM lookup failed", err)
	}
	if !sourceVM.RemoteExec {
		slog.Warn("remote exec: source VM not authorized", "source_vm", sourceVM.Name)
		_ = vsock.SendFrame(sourceConn, agent.RemoteVMResponse{
			Type:   vsock.ResponseTypeRemoteVM,
			Status: 1,
			Error:  fmt.Sprintf("source VM '%s' is not authorized for remote exec", sourceVM.Name),
		})
		return errs.New(errs.CodeUnauthorized,
			"remote exec: source VM '"+sourceVM.Name+"' does not have remote_exec enabled")
	}

	// 2. Parse the remote exec request from the frame data.
	var req agent.RemoteVMRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		_ = vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "invalid remote exec request",
			},
		)
		return errs.New(errs.CodeValidationFailed, "remote exec: invalid request: "+err.Error())
	}

	if req.Destination == "" || req.Command == "" {
		vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "destination and command are required",
			},
		)
		return errs.New(errs.CodeValidationFailed, "remote exec: destination and command are required")
	}

	slog.Info("remote exec request",
		"source_vm", sourceVM.Name,
		"source_vm_id", sourceVMID,
		"destination", req.Destination,
		"command", req.Command,
	)

	// 3. Resolve target VM using the standard VM resolver
	targetVM, err := h.VMResolver.Resolve(ctx, req.Destination)
	if err != nil {
		vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  fmt.Sprintf("target VM '%s' not found", req.Destination),
			},
		)
		return errs.WrapMsg(errs.CodeVMNotFound,
			"remote exec: target VM lookup failed", err)
	}

	// 4. Check target VM has remote_exec enabled.
	if !targetVM.RemoteExec {
		slog.Warn("remote exec: target VM not authorized", "target_vm", targetVM.Name)
		vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "target VM is not authorized for remote exec",
			},
		)
		return errs.New(errs.CodeUnauthorized,
			"remote exec: target VM '"+targetVM.Name+"' does not have remote_exec enabled")
	}

	// 5. Check target VM is running.
	if targetVM.Status != model.VMStatusRunning {
		slog.Warn("remote exec: target VM not running", "target_vm", targetVM.Name, "status", targetVM.Status)
		vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{Type: vsock.ResponseTypeRemoteVM, Status: 1, Error: "target VM is not running"},
		)
		return errs.New(errs.CodeVMNotRunning,
			"remote exec: target VM '"+targetVM.Name+"' is not running")
	}

	// 6. Get target VM's vsock config.
	targetVsock, err := h.VsockRepo.GetByVMID(ctx, targetVM.ID)
	if err != nil {
		_ = vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "target VM has no vsock configuration",
			},
		)
		return errs.WrapMsg(errs.CodeVsockConfigNotFound,
			"remote exec: vsock config not found for target VM '"+targetVM.Name+"'", err)
	}
	if targetVsock == nil {
		_ = vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "target VM has no vsock configuration",
			},
		)
		return errs.New(errs.CodeVsockConfigNotFound,
			"remote exec: vsock config not found for target VM '"+targetVM.Name+"'")
	}

	// 7. Dial the target VM's vsock using the exported DialVM.
	targetConn, err := vsock.DialVM(ctx, targetVsock.UDSPath, targetVsock.Port)
	if err != nil {
		slog.Error("remote exec: failed to dial target VM",
			"target_vm", targetVM.Name, "error", err)
		vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "failed to connect to target VM",
			},
		)
		return errs.WrapMsg(errs.CodeVsockConnectionFailed,
			"remote exec: dial target VM '"+targetVM.Name+"' failed", err)
	}
	defer targetConn.Close()

	// 8. Send exec frame to target using the exported SendFrame.
	execReq := map[string]any{
		"id":      "remote:1",
		"type":    "exec",
		"command": req.Command,
		"token":   targetVsock.Token,
		"timeout": req.Timeout,
		"user":    req.User,
	}
	if err := vsock.SendFrame(targetConn, execReq); err != nil {
		slog.Error("remote exec: failed to send exec to target",
			"target_vm", targetVM.Name, "error", err)
		vsock.SendFrame(
			sourceConn,
			agent.RemoteVMResponse{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  "failed to execute command on target VM",
			},
		)
		return errs.WrapMsg(errs.CodeVsockExecFailed,
			"remote exec: send exec request to target failed", err)
	}

	// 9. Streaming relay loop: read frames from target via json.Decoder,
	//    forward to source via SendFrame.
	var exitCode int
	dec := json.NewDecoder(targetConn)
	for {
		var f vsockFrame
		if err := dec.Decode(&f); err != nil {
			slog.Error("remote exec: failed to read from target",
				"target_vm", targetVM.Name, "error", err)
			vsock.SendFrame(
				sourceConn,
				agent.RemoteVMResponse{
					Type:   vsock.ResponseTypeRemoteVM,
					Status: 1,
					Error:  "connection to target VM lost",
				},
			)
			return errs.WrapMsg(errs.CodeVsockExecFailed,
				"remote exec: read from target failed", err)
		}

		switch f.Type {
		case "stdout":
			if f.Data != "" {
				if err := vsock.SendFrame(sourceConn, map[string]any{
					"type": "stdout", "data": f.Data,
				}); err != nil {
					return errs.WrapMsg(errs.CodeVsockExecFailed,
						"remote exec: forward stdout to source failed", err)
				}
			}
		case "stderr":
			if f.Data != "" {
				if err := vsock.SendFrame(sourceConn, map[string]any{
					"type": "stderr", "data": f.Data,
				}); err != nil {
					return errs.WrapMsg(errs.CodeVsockExecFailed,
						"remote exec: forward stderr to source failed", err)
				}
			}
		case "result":
			if f.Error != "" {
				vsock.SendFrame(
					sourceConn,
					agent.RemoteVMResponse{Type: vsock.ResponseTypeRemoteVM, Status: 1, Error: f.Error},
				)
				return errs.New(errs.CodeVsockExecFailed,
					"remote exec: target error: "+f.Error)
			}
			exitCode = f.Status

			// Send final remote_vm frame to source with the exit code.
			vsock.SendFrame(sourceConn, agent.RemoteVMResponse{Type: vsock.ResponseTypeRemoteVM, Status: exitCode})

			slog.Info("remote exec completed",
				"target_vm", targetVM.Name,
				"command", req.Command,
				"exit_code", exitCode,
			)
			return nil
		default:
			slog.Debug("remote exec: ignoring unknown frame from target",
				"type", f.Type)
		}
	}
}
