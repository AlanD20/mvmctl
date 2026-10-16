package hostadmission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const enabledState = "{\"version\":1,\"enabled\":true}\n"
const disabledState = "{\"version\":1,\"enabled\":false}\n"

func openDirectory(ctx context.Context, create bool) (directory *os.File, returnErr error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	defer func() {
		if current != nil {
			returnErr = errors.Join(returnErr, current.Close())
		}
	}()
	parts := []string{"var", "lib", "mvmctl", "host"}
	for index := -1; index < len(parts); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index >= 0 {
			child, err := unix.Openat(int(current.Fd()), parts[index],
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if errors.Is(err, unix.ENOENT) && create && index >= 2 {
				if err := unix.Mkdirat(
					int(current.Fd()),
					parts[index],
					0700,
				); err != nil &&
					!errors.Is(err, unix.EEXIST) {
					return nil, err
				}
				if err := current.Sync(); err != nil {
					return nil, err
				}
				child, err = unix.Openat(int(current.Fd()), parts[index],
					unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			}
			if err != nil {
				return nil, err
			}
			next := os.NewFile(uintptr(child), parts[index])
			err = current.Close()
			current = next
			if err != nil {
				return nil, err
			}
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(current.Fd()), &stat); err != nil {
			return nil, err
		}
		if stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0022 != 0 ||
			(index == len(parts)-1 && stat.Mode&07777 != 0700) {
			return nil, errors.New("unsafe host admission directory")
		}
	}
	directory, current = current, nil
	return directory, nil
}

func readEnabled(directory *os.File) (enabled bool, returnErr error) {
	fd, err := unix.Openat(int(directory.Fd()), "admission.json",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), "host admission state")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return false, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&07777 != 0600 ||
		before.Uid != 0 || before.Gid != 0 || before.Nlink != 1 || before.Size < 1 || before.Size > 64 {
		return false, errors.New("unsafe host admission state")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return false, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return false, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode ||
		before.Uid != after.Uid || before.Gid != after.Gid || before.Nlink != after.Nlink ||
		before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim ||
		int64(len(data)) != before.Size {
		return false, errors.New("host admission state changed during read")
	}
	switch string(data) {
	case enabledState:
		return true, nil
	case disabledState:
		return false, nil
	default:
		return false, errors.New("invalid host admission state")
	}
}

func writeEnabled(ctx context.Context, directory *os.File, enabled bool) (returnErr error) {
	if ctx == nil {
		return admissionError("publish host admission", errors.New("context is required"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := readEnabled(directory); err != nil && !errors.Is(err, unix.ENOENT) {
		return admissionError("inspect existing host admission", err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := ".admission-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(directory.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	published := false
	defer func() {
		if file != nil {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if !published {
			returnErr = errors.Join(returnErr, unix.Unlinkat(int(directory.Fd()), name, 0))
		}
	}()
	if err := file.Chown(0, 0); err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		return err
	}
	content := disabledState
	if enabled {
		content = enabledState
	}
	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	file = nil
	if err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.Fd()), name, int(directory.Fd()), "admission.json"); err != nil {
		return err
	}
	published = true
	if err := directory.Sync(); err != nil {
		return errs.WrapMsg(errs.CodePrivilegeRequired, "sync host admission directory", err,
			errs.WithDetails(map[string]any{"admission_replaced": true, "durability_uncertain": true}))
	}
	return nil
}
