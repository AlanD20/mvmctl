// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"io"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/console"
	"mvmctl/internal/service/jailer"
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
// Returns running status, PID, and socket path.
// On VM not found, raises VMNotFoundError — Go returns error.
func (op *Operation) ConsoleGetState(ctx context.Context, identifier string) (*results.ConsoleStateResult, error) {
	vmEntity, err := op.resolveConsoleVM(ctx, identifier)
	if err != nil {
		return nil, err
	}
	observed, err := op.consoleAuthority().Observe(ctx, jailer.ConsoleObserveRequest{VMID: vmEntity.ID})
	if err != nil {
		return nil, err
	}
	return &results.ConsoleStateResult{
		Running:    observed.State == jailer.ConsoleRunning,
		PID:        observed.PID,
		SocketPath: observed.SocketPath,
	}, nil
}

// ConsoleGetConnectionInfo returns connection info for VM console relay.
// Raises MVMError if console relay is not running — Go returns DomainError.
func (op *Operation) ConsoleGetConnectionInfo(
	ctx context.Context,
	identifier string,
) (*model.ConsoleConnectionInfo, error) {
	vmEntity, err := op.resolveConsoleVM(ctx, identifier)
	if err != nil {
		return nil, err
	}
	observed, err := op.consoleAuthority().Observe(ctx, jailer.ConsoleObserveRequest{VMID: vmEntity.ID})
	if err != nil {
		return nil, err
	}
	if observed.State != jailer.ConsoleRunning {
		return nil, errs.New(
			errs.CodeConsoleRelayFailed,
			fmt.Sprintf("No console relay running for VM '%s'", identifier),
			errs.WithClass(errs.ClassValidation),
		)
	}
	return &model.ConsoleConnectionInfo{
		SocketPath: observed.SocketPath,
		VMName:     vmEntity.Name,
		VMID:       vmEntity.ID,
	}, nil
}

// ConsoleKill stops the console relay for a VM.
// Returns error. Resolution errors propagate as error (not wrapped in
// OperationResult)
func (op *Operation) ConsoleKill(ctx context.Context, identifier string) error {
	vmEntity, err := op.resolveConsoleVM(ctx, identifier)
	if err != nil {
		return err
	}
	result, err := op.consoleAuthority().Stop(ctx, jailer.ConsoleStopRequest{VMID: vmEntity.ID})
	if err != nil {
		return err
	}
	if !result.Stopped {
		return errs.New(errs.CodeConsoleNotRunning, fmt.Sprintf("No console relay running for '%s'", identifier))
	}
	if op.AuditLog != nil {
		op.AuditLog.LogOperation("console.kill", map[string]any{"name": identifier}, "")
	}
	return nil
}

// ConsoleAttachConsole attaches to a running console relay in interactive mode.
// interact — sets terminal to raw mode, forwards
// stdin→relay and relay→stdout, detaches on Ctrl+X then D.
func (op *Operation) ConsoleAttachConsole(
	ctx context.Context,
	socketPath string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	return console.InteractiveAttach(ctx, socketPath, stdin, stdout)
}

// resolveConsoleVM resolves display identity; root owns every process and endpoint decision.
func (op *Operation) resolveConsoleVM(ctx context.Context, identifier string) (*model.VMItem, error) {
	rawInput := inputs.ConsoleInput{Identifier: identifier}
	if err := rawInput.Validate(); err != nil {
		return nil, err
	}
	vmInput := inputs.VMInput{Identifiers: []string{rawInput.Identifier}}
	vms, err := vmInput.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}
	return vms[0], nil
}

type consoleAuthorityClient interface {
	Observe(context.Context, jailer.ConsoleObserveRequest) (jailer.ConsoleObserveResult, error)
	Stop(context.Context, jailer.ConsoleStopRequest) (jailer.ConsoleStopResult, error)
}

func (op *Operation) consoleAuthority() consoleAuthorityClient {
	if op.consoleClient != nil {
		return op.consoleClient
	}
	return jailer.NewConsoleClient()
}
