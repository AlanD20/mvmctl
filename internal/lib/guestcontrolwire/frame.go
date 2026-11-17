package guestcontrolwire

import (
	"context"
	"encoding/binary"
	"io"

	"mvmctl/pkg/errs"
)

const (
	frameMagic       = "MVMGCTL1"
	frameVersion     = uint16(1)
	framePrefixBytes = len(frameMagic) + 2 + 4
	maxFrameBytes    = 16 * 1024
)

func writeFrame(ctx context.Context, writer io.Writer, body []byte) error {
	if writer == nil || len(body) == 0 || len(body) > maxFrameBytes {
		return invalid("Guest Control requires a writer and a frame of 1-16384 bytes")
	}
	prefix := make([]byte, framePrefixBytes)
	copy(prefix, frameMagic)
	binary.BigEndian.PutUint16(prefix[len(frameMagic):], frameVersion)
	binary.BigEndian.PutUint32(prefix[len(frameMagic)+2:], uint32(len(body)))
	if err := writeAll(ctx, writer, prefix); err != nil {
		return err
	}
	return writeAll(ctx, writer, body)
}

func readFrame(ctx context.Context, reader io.Reader) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, invalid("Guest Control reader is required")
	}
	var prefix [framePrefixBytes]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read Guest Control frame prefix", err)
	}
	if string(prefix[:len(frameMagic)]) != frameMagic ||
		binary.BigEndian.Uint16(prefix[len(frameMagic):]) != frameVersion {
		return nil, invalid("unsupported Guest Control frame magic or version")
	}
	length := binary.BigEndian.Uint32(prefix[len(frameMagic)+2:])
	if length == 0 || length > maxFrameBytes {
		return nil, invalid("Guest Control frame length must be 1-16384 bytes")
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read Guest Control frame body", err)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return body, nil
}

func writeAll(ctx context.Context, writer io.Writer, value []byte) error {
	for len(value) > 0 {
		if err := checkContext(ctx); err != nil {
			return err
		}
		n, err := writer.Write(value)
		if err != nil {
			return errs.WrapMsg(errs.CodeProcessError, "write Guest Control frame", err)
		}
		if n <= 0 || n > len(value) {
			return errs.WrapMsg(errs.CodeProcessError, "write Guest Control frame", io.ErrShortWrite)
		}
		value = value[n:]
	}
	return checkContext(ctx)
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return invalid("Guest Control context is required")
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "Guest Control operation cancelled", err)
	}
	return nil
}

func invalid(message string) error {
	return errs.New(errs.CodeValidationFailed, message)
}
