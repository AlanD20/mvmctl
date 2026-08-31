package jailer

import (
	"context"
	"crypto/rand"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

func realBlockedLaunchGateDeps() blockedLaunchGateDeps {
	return blockedLaunchGateDeps{
		random: func(ctx context.Context, target []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := io.ReadFull(rand.Reader, target)
			return err
		},
		descriptorFlags: processDescriptorFlags,
		readFull: func(ctx context.Context, fd int, target []byte) error {
			for len(target) > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				count, err := unix.Read(fd, target)
				if count > 0 {
					target = target[count:]
				}
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return err
				}
				if count == 0 {
					return unix.EPIPE
				}
			}
			return nil
		},
		writeFull: func(ctx context.Context, fd int, source []byte) error {
			for len(source) > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				count, err := unix.Write(fd, source)
				if count > 0 {
					source = source[count:]
				}
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return err
				}
				if count == 0 {
					return unix.EPIPE
				}
			}
			return nil
		},
		shutdown: func(_ context.Context, fd int) error {
			err := unix.Shutdown(fd, unix.SHUT_RDWR)
			if errors.Is(err, unix.ENOTCONN) || errors.Is(err, unix.EINVAL) {
				return nil
			}
			return err
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
	}
}
