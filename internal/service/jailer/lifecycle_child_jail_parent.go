package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// File descriptors inherited from the parent still reference its mount objects
// after CLONE_NEWNS. Re-pin this fixed root-owned jail parent in the child's
// current namespace and prove it is the same directory before mounting into it.
func reopenLaunchChildJailParent(ctx context.Context, retained int) (int, error) {
	var original unix.Stat_t
	if err := unix.Fstat(retained, &original); err != nil {
		return -1, err
	}
	if original.Mode != unix.S_IFDIR|0700 || original.Uid != 0 || original.Gid != 0 {
		return -1, instanceValidationError("retained launch jail parent has unsafe metadata")
	}
	current, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	deps := realJailDestinationDeps()
	if err := inspectTrustedJailAncestor(ctx, deps, current, "/"); err != nil {
		return -1, errors.Join(err, unix.Close(current))
	}
	for _, part := range []struct {
		name  string
		exact bool
	}{
		{"var", false}, {"lib", false}, {"mvmctl", true}, {"jailer", true}, {"firecracker", true},
	} {
		if err := ctx.Err(); err != nil {
			return -1, errors.Join(err, unix.Close(current))
		}
		resolve := uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS)
		if part.exact {
			resolve |= unix.RESOLVE_NO_XDEV
		}
		next, err := unix.Openat2(current, part.name, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: resolve,
		})
		if err != nil {
			return -1, errors.Join(err, unix.Close(current))
		}
		if part.exact {
			err = inspectExactJailAncestor(ctx, deps, next, part.name)
		} else {
			err = inspectTrustedJailAncestor(ctx, deps, next, part.name)
		}
		closeErr := unix.Close(current)
		if err != nil || closeErr != nil {
			return -1, errors.Join(err, closeErr, unix.Close(next))
		}
		current = next
	}
	var reopened unix.Stat_t
	if err := unix.Fstat(current, &reopened); err != nil {
		return -1, errors.Join(err, unix.Close(current))
	}
	if original.Dev != reopened.Dev || original.Ino != reopened.Ino || original.Mode != reopened.Mode ||
		original.Uid != reopened.Uid || original.Gid != reopened.Gid {
		return -1, errors.Join(instanceValidationError("launch jail parent differs across mount namespaces"),
			unix.Close(current))
	}
	return current, nil
}
