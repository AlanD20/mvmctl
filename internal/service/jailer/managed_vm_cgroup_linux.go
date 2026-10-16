package jailer

import (
	"context"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

func realManagedVMCgroupDeps() managedVMCgroupDeps {
	return managedVMCgroupDeps{
		openRoot: func(_ context.Context) (int, error) {
			return unix.Open(infra.CgroupV2Root, managedVMCgroupDirectoryFlags, 0)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		mkdirAt: func(_ context.Context, parentFD int, name string, mode uint32) error {
			return unix.Mkdirat(parentFD, name, mode)
		},
		fchown: func(_ context.Context, fd int, uid int, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		statx: func(_ context.Context, fd int, stat *unix.Statx_t) error {
			return unix.Statx(
				fd,
				"",
				unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT|unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_FORCE_SYNC,
				managedVMCgroupStatxMask,
				stat,
			)
		},
		statfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error { return unix.Fstatfs(fd, stat) },
		pread: func(_ context.Context, fd int, buffer []byte, offset int64) (int, error) {
			return unix.Pread(fd, buffer, offset)
		},
		write: func(ctx context.Context, fd int, value []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return unix.Write(fd, value)
		},
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		close:      func(_ context.Context, fd int) error { return unix.Close(fd) },
		trustedUID: 0,
		trustedGID: 0,
	}
}
