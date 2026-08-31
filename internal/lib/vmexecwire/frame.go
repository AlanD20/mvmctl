package vmexecwire

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"mvmctl/pkg/errs"
)

const (
	wireMagic         = "MVMEXEC1"
	wireVersion       = uint16(1)
	framePrefixBytes  = len(wireMagic) + 2 + 4
	maxDestinationLen = 64
	maxTargetUserLen  = 32
	maxErrorBytes     = 1024

	// MaxFrameBytes bounds every VM-exec JSON frame before allocation.
	MaxFrameBytes = 64 * 1024
	// MaxCommandBytes bounds the command carried across the VM-exec trust boundary.
	MaxCommandBytes = 32 * 1024
	// MaxOutputBytes bounds combined stdout and stderr for one execution.
	MaxOutputBytes = 8 * 1024 * 1024
	// MaxExecutionDuration is the fixed wall-clock limit for one VM-to-VM execution.
	MaxExecutionDuration = 5 * time.Minute
)

func encodeFrame(ctx context.Context, writer io.Writer, body []byte) error {
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write VM-exec frame", err)
	}
	if writer == nil {
		return validationError("VM-exec frame writer is required")
	}
	if len(body) == 0 {
		return validationError("VM-exec frame must not be empty")
	}
	if len(body) > MaxFrameBytes {
		return validationError(fmt.Sprintf("VM-exec frame exceeds %d bytes", MaxFrameBytes))
	}

	prefix := make([]byte, framePrefixBytes)
	copy(prefix, wireMagic)
	binary.BigEndian.PutUint16(prefix[len(wireMagic):], wireVersion)
	binary.BigEndian.PutUint32(prefix[len(wireMagic)+2:], uint32(len(body)))
	if err := writeAll(ctx, writer, prefix); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write VM-exec frame prefix", err)
	}
	if err := writeAll(ctx, writer, body); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write VM-exec frame body", err)
	}
	return nil
}

func decodeFrame(ctx context.Context, reader io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "read VM-exec frame", err)
	}
	if reader == nil {
		return nil, validationError("VM-exec frame reader is required")
	}
	prefix := make([]byte, framePrefixBytes)
	if _, err := io.ReadFull(reader, prefix); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read VM-exec frame prefix", err)
	}
	if string(prefix[:len(wireMagic)]) != wireMagic {
		return nil, validationError("invalid VM-exec frame magic")
	}
	if binary.BigEndian.Uint16(prefix[len(wireMagic):len(wireMagic)+2]) != wireVersion {
		return nil, validationError("unsupported VM-exec frame version")
	}
	bodyLength := binary.BigEndian.Uint32(prefix[len(wireMagic)+2:])
	if bodyLength == 0 {
		return nil, validationError("VM-exec frame must not be empty")
	}
	if bodyLength > MaxFrameBytes {
		return nil, validationError(fmt.Sprintf("VM-exec frame exceeds %d bytes", MaxFrameBytes))
	}
	body := make([]byte, bodyLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read VM-exec frame body", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "read VM-exec frame", err)
	}
	return body, nil
}

func writeAll(ctx context.Context, writer io.Writer, value []byte) error {
	for len(value) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}

func validationError(message string) error {
	return errs.New(errs.CodeValidationFailed, message)
}
