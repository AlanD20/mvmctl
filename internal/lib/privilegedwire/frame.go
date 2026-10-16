package privilegedwire

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"mvmctl/pkg/errs"
)

const (
	requestMagic      = "MVMREQ01"
	responseMagic     = "MVMRES01"
	maxJSONFrameBytes = 64 * 1024
	maxActionBytes    = 64
)

func writeFrame(ctx context.Context, writer io.Writer, prefix []byte, body []byte) error {
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write privileged frame", err)
	}
	if writer == nil {
		return errs.New(errs.CodeValidationFailed, "privileged frame writer is required")
	}
	if err := writeAll(ctx, writer, prefix); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write privileged frame prefix", err)
	}
	if err := writeAll(ctx, writer, body); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write privileged frame body", err)
	}
	return nil
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

func readExact(ctx context.Context, reader io.Reader, value []byte, description string) error {
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "read privileged frame", err)
	}
	if reader == nil {
		return errs.New(errs.CodeValidationFailed, "privileged frame reader is required")
	}
	if _, err := io.ReadFull(reader, value); err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "read privileged "+description, err)
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "read privileged frame", err)
	}
	return nil
}

func requireReaderEOF(ctx context.Context, reader io.Reader) error {
	var trailing [1]byte
	read, err := reader.Read(trailing[:])
	if contextErr := ctx.Err(); contextErr != nil {
		return errs.WrapMsg(errs.CodeProcessError, "read privileged frame", contextErr)
	}
	if read > 0 || err == nil {
		return errs.New(errs.CodeValidationFailed, "privileged response contains trailing input")
	}
	if err != io.EOF {
		return errs.WrapMsg(errs.CodeValidationFailed, "read trailing privileged response input", err)
	}
	return nil
}

func validateFrameLength(length uint32) error {
	if length == 0 {
		return errs.New(errs.CodeValidationFailed, "privileged JSON frame is empty")
	}
	if length > maxJSONFrameBytes {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("privileged JSON frame exceeds %d bytes", maxJSONFrameBytes),
		)
	}
	return nil
}

func appendUint32(target []byte, value uint32) {
	binary.BigEndian.PutUint32(target, value)
}

func appendUint64(target []byte, value uint64) {
	binary.BigEndian.PutUint64(target, value)
}
