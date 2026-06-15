// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/console_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/console"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// ConsoleAPI defines the public interface for console relay operations.
type ConsoleAPI interface {
	ConsoleGetState(ctx context.Context, identifier string) (*results.ConsoleStateResult, error)
	ConsoleGetConnectionInfo(ctx context.Context, identifier string) (*model.ConsoleConnectionInfo, error)
	ConsoleKill(ctx context.Context, identifier string) error
	ConsoleAttachConsole(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error
}

// ConsoleGetState returns console relay state for a VM.
// Matches Python's ConsoleOperation.get_state() exactly.
// Python returns a raw dict with running, pid, socket_path.
// On VM not found, raises VMNotFoundError — Go returns error.
func (op *Operation) ConsoleGetState(ctx context.Context, identifier string) (*results.ConsoleStateResult, error) {
	resolved, err := op.resolveWithRequest(ctx, identifier)
	if err != nil {
		return nil, err
	}

	pidVal, pidOK := resolved.Relay.PID()
	var pidPtr *int
	if pidOK {
		pidPtr = &pidVal
	}
	return &results.ConsoleStateResult{
		Running:    resolved.Relay.IsRunning(),
		PID:        pidPtr,
		SocketPath: resolved.Relay.SocketPath(),
	}, nil
}

// ConsoleGetConnectionInfo returns connection info for VM console relay.
// Matches Python's ConsoleOperation.get_connection_info() exactly.
// Raises MVMError if console relay is not running — Go returns DomainError.
func (op *Operation) ConsoleGetConnectionInfo(
	ctx context.Context,
	identifier string,
) (*model.ConsoleConnectionInfo, error) {
	resolved, err := op.resolveWithRequest(ctx, identifier)
	if err != nil {
		return nil, err
	}

	if !resolved.Relay.IsRunning() {
		return nil, errs.New(
			errs.CodeConsoleRelayFailed,
			fmt.Sprintf("No console relay running for VM '%s'", identifier),
			errs.WithClass(errs.ClassValidation),
		)
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
// Go: returns error. Resolution errors propagate as error (not wrapped in
// OperationResult), matching Python's exception propagation.
func (op *Operation) ConsoleKill(ctx context.Context, identifier string) error {
	resolved, err := op.resolveWithRequest(ctx, identifier)
	if err != nil {
		return err
	}

	if !resolved.Relay.IsRunning() {
		return errs.New(errs.CodeConsoleNotRunning, fmt.Sprintf("No console relay running for '%s'", identifier))
	}

	killed := resolved.Relay.Stop(true)
	if killed {
		op.AuditLog.LogOperation("console.kill", map[string]any{"name": identifier}, "")
		return nil
	}
	return errs.New(errs.CodeConsoleKillFailed, fmt.Sprintf("Failed to stop console relay for '%s'", identifier))
}

// ConsoleAttachConsole attaches to a running console relay in interactive mode.
// Matches Python's CLI _interact() — sets terminal to raw mode, forwards
// stdin→relay and relay→stdout, detaches on Ctrl+X then D.
func (op *Operation) ConsoleAttachConsole(
	ctx context.Context,
	socketPath string,
	stdin io.Reader,
	stdout io.Writer,
) error {
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
	req := inputs.NewConsoleRequest(rawInput, op.Connection.DB())

	// Resolve VM first to get ID and name required for relay creation
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{Identifiers: []string{identifier}},
		op.Connection.DB(), op.Repos.VM,
	)
	vmResolved, err := vmRequest.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	vmEntity := vmResolved.VMs[0]

	// Create relay manager.
	vmDir := infra.GetVMDirByID(vmEntity.ID)
	pidPath := filepath.Join(vmDir, console.DefaultConsolePIDFilename)
	socketPath := filepath.Join(vmDir, console.DefaultConsoleSocketFilename)
	relay := console.NewRelay(vmEntity.Name, pidPath, socketPath)

	return req.Resolve(ctx, vmEntity, relay)
}
