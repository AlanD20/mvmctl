package jailer

import (
	"context"
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

// VMExecSessionOpenRequest carries the caller-supplied distinct VM identifiers for one execution session.
type VMExecSessionOpenRequest struct {
	SourceVMID      string
	DestinationVMID string
}

// Validate checks only exact lowercase identifier syntax and distinctness. It does not prove VM identity or activity.
func (request VMExecSessionOpenRequest) Validate() error {
	return request.wire().Validate()
}

func (request VMExecSessionOpenRequest) wire() vmexecsessionwire.OpenSource {
	return vmexecsessionwire.OpenSource{
		SourceVMID: request.SourceVMID, DestinationVMID: request.DestinationVMID,
	}
}

type vmExecSessionTransport interface {
	OpenSource(context.Context, vmexecsessionwire.OpenSource, *os.File) (vmExecSessionControl, error)
}

type vmExecSessionControl interface {
	BindDestination(context.Context, *os.File) error
	Release(context.Context) (vmexecsessionwire.Outcome, error)
	Cancel(context.Context) error
}

type vmExecSessionSystemTransport struct {
	transport system.PrivilegedVMExecSessionTransport
}

func (transport vmExecSessionSystemTransport) OpenSource(
	ctx context.Context,
	request vmexecsessionwire.OpenSource,
	duplicate *os.File,
) (vmExecSessionControl, error) {
	return transport.transport.OpenSource(ctx, request, duplicate)
}

type vmExecSessionConnectionDuplicator interface {
	Duplicate(context.Context, *net.UnixConn) (*os.File, error)
}

type vmExecSessionUnixConnectionDuplicator struct{}

func (vmExecSessionUnixConnectionDuplicator) Duplicate(
	ctx context.Context,
	connection *net.UnixConn,
) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, err
	}
	duplicateFD := -1
	var duplicateErr error
	if err := raw.Control(func(fd uintptr) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			duplicateErr = ctxErr
			return
		}
		duplicateFD, duplicateErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		var closeErr error
		if duplicateFD >= 0 {
			closeErr = unix.Close(duplicateFD)
		}
		return nil, errors.Join(err, closeErr)
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}
	if duplicateFD < 0 {
		return nil, errors.New("retained VM-exec connection duplication returned no descriptor")
	}
	duplicate := os.NewFile(uintptr(duplicateFD), "mvm-vm-exec-session-connection")
	if duplicate == nil {
		closeErr := unix.Close(duplicateFD)
		return nil, errors.Join(errors.New("retained VM-exec connection duplication returned no file"), closeErr)
	}
	return duplicate, nil
}

// VMExecSessionClient exposes only the fixed retained VM-exec sidecar capability.
// Its zero value is inactive; use NewVMExecSessionClient for production transport binding.
type VMExecSessionClient struct {
	transport  vmExecSessionTransport
	duplicator vmExecSessionConnectionDuplicator
}

// NewVMExecSessionClient binds the fixed retained VM-exec sidecar transport.
func NewVMExecSessionClient() *VMExecSessionClient {
	return newVMExecSessionClient(
		vmExecSessionSystemTransport{transport: system.PrivilegedVMExecSessionTransport{}},
		vmExecSessionUnixConnectionDuplicator{},
	)
}

func newVMExecSessionClient(
	transport vmExecSessionTransport,
	duplicator vmExecSessionConnectionDuplicator,
) *VMExecSessionClient {
	return &VMExecSessionClient{transport: transport, duplicator: duplicator}
}

// Open duplicates source for the retained sidecar while the caller keeps ownership of the original connection.
func (client *VMExecSessionClient) Open(
	ctx context.Context,
	request VMExecSessionOpenRequest,
	source *net.UnixConn,
) (*VMExecSession, error) {
	if err := validateVMExecSessionClientContext(ctx, "open retained VM-exec session"); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if client == nil || client.transport == nil || client.duplicator == nil {
		return nil, vmExecSessionClientFailure("retained VM-exec client is not active", nil)
	}
	if source == nil {
		return nil, errs.New(errs.CodeValidationFailed, "retained VM-exec source connection is required")
	}
	duplicate, err := duplicateVMExecSessionConnection(ctx, client.duplicator, source)
	if err != nil {
		return nil, err
	}
	control, err := client.transport.OpenSource(ctx, request.wire(), duplicate)
	if err != nil {
		return nil, err
	}
	if control == nil {
		return nil, vmExecSessionClientFailure("retained VM-exec transport returned no session", nil)
	}
	return &VMExecSession{control: control, duplicator: client.duplicator}, nil
}

// VMExecSession is one ordered retained-sidecar control session.
// Copies share the same underlying system session.
type VMExecSession struct {
	control    vmExecSessionControl
	duplicator vmExecSessionConnectionDuplicator
}

// BindDestination duplicates destination for the sidecar while the caller retains the original connection.
func (session *VMExecSession) BindDestination(ctx context.Context, destination *net.UnixConn) error {
	if err := validateVMExecSessionClientContext(ctx, "bind retained VM-exec destination"); err != nil {
		return err
	}
	if session == nil || session.control == nil || session.duplicator == nil {
		return vmExecSessionClientFailure("retained VM-exec session is not active", nil)
	}
	if destination == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec destination connection is required")
	}
	duplicate, err := duplicateVMExecSessionConnection(ctx, session.duplicator, destination)
	if err != nil {
		return err
	}
	return session.control.BindDestination(ctx, duplicate)
}

// Release requests the exact terminal release and succeeds only for a released wire outcome.
func (session *VMExecSession) Release(ctx context.Context) error {
	if err := validateVMExecSessionClientContext(ctx, "release retained VM-exec session"); err != nil {
		return err
	}
	if session == nil || session.control == nil {
		return vmExecSessionClientFailure("retained VM-exec session is not active", nil)
	}
	outcome, transportErr := session.control.Release(ctx)
	if operationErr := outcome.OperationError(); operationErr != nil {
		return operationErr
	}
	if transportErr != nil {
		return transportErr
	}
	if !outcome.Released() {
		return vmExecSessionClientFailure("retained VM-exec terminal outcome was not released", nil)
	}
	return nil
}

// Cancel interrupts the retained sidecar session and releases its owned duplicates.
func (session *VMExecSession) Cancel(ctx context.Context) error {
	if ctx == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec session context is required")
	}
	if session == nil || session.control == nil {
		return vmExecSessionClientFailure("retained VM-exec session is not active", nil)
	}
	return session.control.Cancel(context.WithoutCancel(ctx))
}

func duplicateVMExecSessionConnection(
	ctx context.Context,
	duplicator vmExecSessionConnectionDuplicator,
	connection *net.UnixConn,
) (*os.File, error) {
	duplicate, err := duplicator.Duplicate(ctx, connection)
	if err != nil {
		var closeErr error
		if duplicate != nil {
			closeErr = duplicate.Close()
		}
		return nil, vmExecSessionClientFailure(
			"failed to duplicate retained VM-exec connection",
			errors.Join(err, closeErr),
		)
	}
	if duplicate == nil {
		return nil, vmExecSessionClientFailure("retained VM-exec connection duplication returned no file", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, vmExecSessionClientFailure(
			"retained VM-exec connection duplication was cancelled",
			errors.Join(err, duplicate.Close()),
		)
	}
	return duplicate, nil
}

func validateVMExecSessionClientContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec session context is required")
	}
	if err := ctx.Err(); err != nil {
		return vmExecSessionClientFailure(operation+" was cancelled", err)
	}
	return nil
}

func vmExecSessionClientFailure(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeProcessError, message, cause, errs.WithClass(errs.ClassInternal))
}
