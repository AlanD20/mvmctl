package vmexecsessionwire

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"mvmctl/pkg/errs"
)

const (
	wireMagic        = "MVMXSES1"
	wireVersion      = uint16(1)
	framePrefixBytes = len(wireMagic) + 2 + 4

	// MaxFrameBytes bounds every retained VM-exec session JSON frame before allocation.
	MaxFrameBytes = 64 * 1024
)

func encodeFrame(ctx context.Context, writer io.Writer, body []byte) error {
	if ctx == nil {
		return validationError("retained VM-exec session context is required")
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write retained VM-exec session frame", err)
	}
	if writer == nil {
		return validationError("retained VM-exec session writer is required")
	}
	if len(body) == 0 || len(body) > MaxFrameBytes {
		return validationError(fmt.Sprintf("retained VM-exec session frame must be 1-%d bytes", MaxFrameBytes))
	}

	prefix := make([]byte, framePrefixBytes)
	copy(prefix, wireMagic)
	binary.BigEndian.PutUint16(prefix[len(wireMagic):], wireVersion)
	binary.BigEndian.PutUint32(prefix[len(wireMagic)+2:], uint32(len(body)))
	if err := writeAll(ctx, writer, prefix); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write retained VM-exec session frame prefix", err)
	}
	if err := writeAll(ctx, writer, body); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write retained VM-exec session frame body", err)
	}
	return nil
}

func decodeFrame(ctx context.Context, reader io.Reader) ([]byte, error) {
	if ctx == nil {
		return nil, validationError("retained VM-exec session context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "read retained VM-exec session frame", err)
	}
	if reader == nil {
		return nil, validationError("retained VM-exec session reader is required")
	}
	prefix := make([]byte, framePrefixBytes)
	if _, err := io.ReadFull(reader, prefix); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read retained VM-exec session frame prefix", err)
	}
	if string(prefix[:len(wireMagic)]) != wireMagic {
		return nil, validationError("invalid retained VM-exec session frame magic")
	}
	if binary.BigEndian.Uint16(prefix[len(wireMagic):len(wireMagic)+2]) != wireVersion {
		return nil, validationError("unsupported retained VM-exec session frame version")
	}
	bodyLength := binary.BigEndian.Uint32(prefix[len(wireMagic)+2:])
	if bodyLength == 0 || bodyLength > MaxFrameBytes {
		return nil, validationError(fmt.Sprintf("retained VM-exec session frame must be 1-%d bytes", MaxFrameBytes))
	}
	body := make([]byte, bodyLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read retained VM-exec session frame body", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "read retained VM-exec session frame", err)
	}
	return body, nil
}

func writeAll(ctx context.Context, writer io.Writer, value []byte) error {
	for len(value) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := writer.Write(value)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(value) {
			return io.ErrShortWrite
		}
		value = value[count:]
	}
	return nil
}

func validationError(message string) error {
	return errs.New(errs.CodeValidationFailed, message)
}
