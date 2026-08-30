package jailer

import (
	"context"

	"golang.org/x/sys/unix"
)

func realJailDestinationDeps() jailDestinationDeps {
	return jailDestinationDeps{
		open: func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.Open(name, flags, mode)
		},
		openAt: func(ctx context.Context, directoryFD int, name string, flags int, mode uint32) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.Openat(directoryFD, name, flags, mode)
		},
		fstat: func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fstat(fd, stat)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}
