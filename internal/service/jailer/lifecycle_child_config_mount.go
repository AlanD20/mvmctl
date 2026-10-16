package jailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// A sealed memfd cannot be cloned with open_tree. Materialize its verified bytes in
// a detached, private tmpfs mount; only that root-built mount crosses CLONE_NEWNS.
func prepareLaunchChildConfigMount(ctx context.Context, sealedFD int, metrics bool) (_ int, returnErr error) {
	data, err := readSealedLaunchDeviceConfig(ctx, sealedFD)
	if err != nil {
		return -1, err
	}
	fs, err := unix.Fsopen("tmpfs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("open detached launch config filesystem: %w", err)
	}
	if err := unix.FsconfigSetString(fs, "size", "2m"); err != nil {
		return -1, errors.Join(err, unix.Close(fs))
	}
	if err := unix.FsconfigCreate(fs); err != nil {
		return -1, errors.Join(err, unix.Close(fs))
	}
	mount, err := unix.Fsmount(fs, unix.FSMOUNT_CLOEXEC, 0)
	closeErr := unix.Close(fs)
	if err != nil {
		return -1, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return -1, errors.Join(closeErr, unix.Close(mount))
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(mount))
		}
	}()
	directory, err := unix.Openat(mount, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := unix.Fchmod(directory, 0755); err != nil {
		return -1, errors.Join(err, unix.Close(directory))
	}
	var root unix.Stat_t
	if err := unix.Fstat(directory, &root); err != nil {
		return -1, errors.Join(err, unix.Close(directory))
	}
	if root.Mode != unix.S_IFDIR|0755 || root.Uid != 0 || root.Gid != 0 {
		return -1, errors.Join(instanceValidationError("detached launch config root has unsafe metadata"),
			unix.Close(directory))
	}
	if err := unix.Close(directory); err != nil {
		return -1, err
	}
	file, err := unix.Openat(mount, infra.VMFirecrackerConfigFilename,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return -1, err
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return -1, errors.Join(err, unix.Close(file))
		}
		count, err := unix.Pwrite(file, data[offset:], int64(offset))
		if err != nil || count <= 0 || count > len(data)-offset {
			if err == nil {
				err = io.ErrShortWrite
			}
			return -1, errors.Join(fmt.Errorf("write detached launch config: %w", err), unix.Close(file))
		}
		offset += count
	}
	if err := unix.Fsync(file); err != nil {
		return -1, errors.Join(err, unix.Close(file))
	}
	if err := unix.Fchmod(file, 0444); err != nil {
		return -1, errors.Join(err, unix.Close(file))
	}
	var copied unix.Stat_t
	if err := unix.Fstat(file, &copied); err != nil {
		return -1, errors.Join(err, unix.Close(file))
	}
	if copied.Mode != unix.S_IFREG|0444 || copied.Uid != 0 || copied.Gid != 0 || copied.Nlink != 1 ||
		copied.Size != int64(len(data)) {
		return -1, errors.Join(instanceValidationError("detached launch config has unsafe metadata"),
			unix.Close(file))
	}
	readback, err := readLaunchDeviceBytes(ctx, file, len(data))
	if err != nil || !bytes.Equal(readback, data) {
		return -1, errors.Join(instanceValidationError("detached launch config differs from sealed authority"),
			err, unix.Close(file))
	}
	if err := unix.Close(file); err != nil {
		return -1, err
	}
	for _, name := range []string{infra.VMFirecrackerLogFilename, infra.VMFirecrackerMetricsFilename} {
		if name == infra.VMFirecrackerMetricsFilename && !metrics {
			break
		}
		placeholder, err := unix.Openat(mount, name,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return -1, err
		}
		if err := unix.Close(placeholder); err != nil {
			return -1, err
		}
	}
	attributes := uint64(unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV |
		unix.MOUNT_ATTR_NOEXEC)
	if err := unix.MountSetattr(mount, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: attributes}); err != nil {
		return -1, fmt.Errorf("seal detached launch config mount: %w", err)
	}
	return mount, nil
}
