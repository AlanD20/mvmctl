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
		statx: func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Statx(fd, "", flags, mask, stat)
		},
		fchmod: func(ctx context.Context, fd int, mode uint32) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fchmod(fd, mode)
		},
		fchown: func(ctx context.Context, fd int, uid, gid int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fchown(fd, uid, gid)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}
