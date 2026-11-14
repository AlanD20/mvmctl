package privileged

import (
	"context"
	"io"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type vmExecReleaseFrameReader struct {
	reader *vmExecConnectionReader
}

func newVMExecReleaseFrameReader(
	ctx context.Context,
	fd int,
	deadline time.Time,
) *vmExecReleaseFrameReader {
	return newVMExecReleaseFrameReaderWithDeps(ctx, fd, deadline, realVMExecConnectionReaderDeps())
}

func newVMExecReleaseFrameReaderWithDeps(
	ctx context.Context,
	fd int,
	deadline time.Time,
	deps vmExecConnectionReaderDeps,
) *vmExecReleaseFrameReader {
	return &vmExecReleaseFrameReader{reader: newVMExecConnectionReaderWithDeps(ctx, fd, deadline, deps)}
}

// CRITICAL: Every read remains recvmsg-based so SCM_RIGHTS cannot hide behind ordinary release-frame bytes.
func (reader *vmExecReleaseFrameReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	if reader == nil || reader.reader == nil {
		return 0, errs.New(errs.CodeValidationFailed, "VM-exec release frame reader is required")
	}
	base := reader.reader
	if base.terminalErr != nil {
		return 0, base.terminalErr
	}
	if base.closed {
		return 0, errs.New(errs.CodeValidationFailed, "VM-exec release frame reader is closed")
	}

	received := base.receive(len(target))
	control, controlErr := base.ownControl(received.control)
	if received.err != nil {
		return 0, base.fail(vmExecConnectionSystemError("receive VM-exec release frame", received.err))
	}
	if received.from != nil {
		return 0, base.fail(errs.New(
			errs.CodeValidationFailed,
			"VM-exec release frame must not have a sender address",
		))
	}
	unexpectedFlags := received.flags &^ unix.MSG_CMSG_CLOEXEC
	if unexpectedFlags != 0 {
		message := "VM-exec release frame has unexpected receive flags"
		if unexpectedFlags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			message = "VM-exec release frame is truncated"
		}
		return 0, base.fail(errs.New(errs.CodeValidationFailed, message))
	}
	if controlErr != nil {
		return 0, base.fail(controlErr)
	}
	if len(received.control) != 0 || len(control.messages) != 0 {
		return 0, base.fail(errs.New(
			errs.CodeValidationFailed,
			"VM-exec release frame contains ancillary data",
		))
	}
	if len(received.payload) == 0 {
		return 0, io.EOF
	}
	return copy(target, received.payload), nil
}

func (reader *vmExecReleaseFrameReader) Close() error {
	if reader == nil || reader.reader == nil {
		return nil
	}
	return reader.reader.Close()
}
