package host

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type administratorJailerMount struct {
	kind uint32
	id   uint64
}

// ensureAdministratorJailerTree prepares only the fixed global parent. The
// authenticated Jailer launch still proves each VM destination absent before
// its child creates and pins the fresh per-VM tree.
func ensureAdministratorJailerTree(ctx context.Context) error {
	if infra.JailerChrootBase != "/var/lib/mvmctl/jailer" {
		return fmt.Errorf("fixed Jailer host tree is inconsistent")
	}
	base := []string{"var", "lib", "mvmctl"}
	want, err := inspectAdministratorJailerDirectory(ctx, base, -1)
	if err != nil {
		return fmt.Errorf("inspect fixed Jailer host base: %w", err)
	}
	for _, path := range []struct {
		parts      []string
		createFrom int
	}{
		{parts: []string{"var", "lib", "mvmctl", "jailer"}, createFrom: 3},
		{parts: []string{"var", "lib", "mvmctl", "jailer", "firecracker"}, createFrom: 4},
	} {
		got, inspectErr := inspectAdministratorJailerDirectory(ctx, path.parts, path.createFrom)
		if inspectErr != nil {
			return fmt.Errorf("prepare fixed Jailer host directory: %w", inspectErr)
		}
		if got != want {
			return fmt.Errorf("fixed Jailer host directory is a substituted mount")
		}
	}
	return nil
}

func inspectAdministratorJailerDirectory(
	ctx context.Context,
	parts []string,
	createFrom int,
) (identity administratorJailerMount, returnErr error) {
	directory, err := openAdministratorDirectory(ctx, parts, createFrom)
	if err != nil {
		return identity, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	return administratorJailerDirectoryMount(ctx, directory)
}

func administratorJailerDirectoryMount(ctx context.Context, directory *os.File) (administratorJailerMount, error) {
	if err := ctx.Err(); err != nil {
		return administratorJailerMount{}, err
	}
	var stat unix.Statx_t
	mask := unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
	flags := unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW | unix.AT_STATX_FORCE_SYNC
	if err := unix.Statx(int(directory.Fd()), "", flags, mask, &stat); err != nil {
		return administratorJailerMount{}, err
	}
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0 && stat.Mnt_id != 0:
		return administratorJailerMount{kind: unix.STATX_MNT_ID_UNIQUE, id: stat.Mnt_id}, nil
	case stat.Mask&unix.STATX_MNT_ID != 0 && stat.Mnt_id != 0:
		return administratorJailerMount{kind: unix.STATX_MNT_ID, id: stat.Mnt_id}, nil
	default:
		return administratorJailerMount{}, fmt.Errorf("fixed Jailer host mount identity is unavailable")
	}
}
