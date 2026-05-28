// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/console_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"io"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/service/console"
	"mvmctl/pkg/api/inputs"
)

// ConsoleGetState returns console relay state for a VM.
// Matches Python's ConsoleOperation.get_state() exactly.
// Python returns a raw dict with running, pid, socket_path.
// On VM not found, raises VMNotFoundError — Go returns error.
func (op *Operation) ConsoleGetState(ctx context.Context, identifier string) (map[string]interface{}, error) {
	resolved, err := op.resolveWithRequest(ctx, identifier)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"running":     resolved.Relay.IsRunning(),
		"pid":         resolved.Relay.PID(),
		"socket_path": resolved.Relay.SocketPath(),
	}, nil
}

// ConsoleGetConnectionInfo returns connection info for VM console relay.
// Matches Python's ConsoleOperation.get_connection_info() exactly.
// Raises MVMError if console relay is not running — Go returns DomainError.
func (op *Operation) ConsoleGetConnectionInfo(ctx context.Context, identifier string) (*model.ConsoleConnectionInfo, error) {
	resolved, err := op.resolveWithRequest(ctx, identifier)
	if err != nil {
		return nil, err
	}

	if !resolved.Relay.IsRunning() {
		return nil, &errs.DomainError{
			Code:    errs.CodeConsoleRelayFailed,
			Op:      "console",
			Message: fmt.Sprintf("No console relay running for VM '%s'", identifier),
			Class:   errs.ClassValidation,
		}
	}

	return &model.ConsoleConnectionInfo{
		SocketPath: resolved.Relay.SocketPath(),
		VMName:     resolved.VM.Name,
		VMID:       resolved.VM.ID,
	}, nil
}

// ConsoleKill stops the console relay for a VM.
// Matches Python's ConsoleOperation.kill() exactly.
// Python: raises MVMError on resolution failure or returns OperationResult[bool].
// Go: returns (*OperationResult, error). Resolution errors propagate as error
// (not wrapped in OperationResult), matching Python's exception propagation.
func (op *Operation) ConsoleKill(ctx context.Context, identifier string) (*errs.OperationResult, error) {
	resolved, err := op.resolveWithRequest(ctx, identifier)
	if err != nil {
		// Python: ConsoleRequest(...).resolve() raises MVMError on resolution
		// failure. In Go, this propagates as a Go error — not wrapped in
		// an OperationResult, matching Python's exception behavior.
		return nil, err
	}

	if !resolved.Relay.IsRunning() {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "console.not_running",
			Message: fmt.Sprintf("No console relay running for '%s'", identifier),
			Item:    false,
		}, nil
	}

	killed := resolved.Relay.Stop(true)
	if killed {
		auditLog := logging.NewAuditLog(op.CacheDir)
		// Python: AuditLog.log("console.kill", changes={"name": identifier})
		_ = auditLog.LogOperation("console.kill", map[string]interface{}{"name": identifier}, "")
		return &errs.OperationResult{
			Status:  "success",
			Code:    "console.killed",
			Message: fmt.Sprintf("Console relay stopped for '%s'", identifier),
			Item:    true,
		}, nil
	}
	// Python: code="console.kill_failed"
	return &errs.OperationResult{
		Status:  "error",
		Code:    "console.kill_failed",
		Message: fmt.Sprintf("Failed to stop console relay for '%s'", identifier),
		Item:    false,
	}, nil
}

// ConsoleAttachConsole attaches to a running console relay in interactive mode.
// Matches Python's CLI _interact() — sets terminal to raw mode, forwards
// stdin→relay and relay→stdout, detaches on Ctrl+X then D.
func (op *Operation) ConsoleAttachConsole(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error {
	return console.InteractiveAttach(ctx, socketPath, stdin, stdout)
}

// resolveWithRequest resolves a VM identifier and creates a console relay.
// Matches Python's ConsoleRequest(inputs=ConsoleInput(...)).resolve() pattern:
//
//	resolved = ConsoleRequest(inputs=ConsoleInput(identifier=identifier)).resolve()
//	vm = resolved.vm
//	relay = resolved.relay
//
// Raises VMNotFoundError if VM cannot be found — Go returns DomainError with CodeVMNotFound.
// The relay creation is done here (API layer) rather than in the input resolver
// to avoid importing internal/service/console from the inputs package.
func (op *Operation) resolveWithRequest(ctx context.Context, identifier string) (*inputs.ResolvedConsoleInput, error) {
	rawInput := inputs.ConsoleInput{Identifier: identifier}
	req := inputs.NewConsoleRequest(rawInput, op.DB)

	// Resolve VM first to get ID and name required for relay creation
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{Identifiers: []string{identifier}},
		op.DB, op.Repos.VM, nil,
	)
	vmResolved, err := vmRequest.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	vmEntity := vmResolved.VMs[0]

	// Create relay manager — Python: ConsoleRelayManager(id=vm.id, path=CacheUtils.get_vm_dir(vm.id), name=vm.name)
	relay := console.NewRelayManager(
		vmEntity.ID,
		infra.GetVmDir(vmEntity.ID),
		vmEntity.Name,
		"", // pidFilename — defaults to "console.pid"
		"", // socketFilename — defaults to "console.sock"
		"", // logFilename — defaults to "firecracker.console.log"
	)

	return req.Resolve(ctx, vmEntity, relay)
}
