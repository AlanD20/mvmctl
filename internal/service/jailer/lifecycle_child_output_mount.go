package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// O_TMPFILE's original descriptor retains an anonymous dentry even after
// publication. Open only the fixed published leaf and prove its inode matches
// the admitted output before cloning a mount from that linked dentry.
func reopenLaunchChildOutput(ctx context.Context, directory int, name string, original int) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	var admitted unix.Stat_t
	if err := unix.Fstat(original, &admitted); err != nil {
		return -1, err
	}
	if admitted.Mode&unix.S_IFMT != unix.S_IFREG || admitted.Nlink != 1 || admitted.Ino == 0 {
		return -1, instanceValidationError("published launch output source is not linked regular data")
	}
	linked, err := unix.Openat2(directory, name, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return -1, err
	}
	var named unix.Stat_t
	if err := unix.Fstat(linked, &named); err != nil {
		return -1, errors.Join(err, unix.Close(linked))
	}
	if named.Dev != admitted.Dev || named.Ino != admitted.Ino || named.Mode != admitted.Mode ||
		named.Uid != admitted.Uid || named.Gid != admitted.Gid || named.Nlink != admitted.Nlink ||
		named.Size != admitted.Size {
		return -1, errors.Join(instanceValidationError("published launch output differs from admitted inode"),
			unix.Close(linked))
	}
	return linked, nil
}
