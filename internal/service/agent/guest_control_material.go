package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
)

const guestControlMaterialLimit = 16 * 1024

// LoadGuestControlAgentMaterial admits only the fixed root-private guest provision. Absence disables control.
func LoadGuestControlAgentMaterial(ctx context.Context) (guestcontrolauth.AgentMaterial, bool, error) {
	if ctx == nil {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material context is required")
	}
	if err := ctx.Err(); err != nil {
		return guestcontrolauth.AgentMaterial{}, false, err
	}
	directory, err := unix.Openat2(unix.AT_FDCWD, infra.GuestAgentConfigDirectory, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOENT) {
		return guestcontrolauth.AgentMaterial{}, false, nil
	}
	if err != nil {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material directory is unavailable")
	}
	defer unix.Close(directory)
	return loadGuestControlMaterialAt(ctx, directory, "guest-control.json")
}

func loadGuestControlMaterialAt(
	ctx context.Context, directory int, name string,
) (guestcontrolauth.AgentMaterial, bool, error) {
	var parent unix.Stat_t
	if err := unix.Fstat(directory, &parent); err != nil || parent.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parent.Uid != 0 || parent.Gid != 0 || parent.Mode&0777 != 0700 {
		return guestcontrolauth.AgentMaterial{}, false, errors.New(
			"guest control material directory is not root-private",
		)
	}
	fd, err := unix.Openat2(directory, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, unix.ENOENT) {
		return guestcontrolauth.AgentMaterial{}, false, nil
	}
	if err != nil {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material is unavailable")
	}
	file := os.NewFile(uintptr(fd), "guest-control-material")
	if file == nil {
		_ = unix.Close(fd)
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material descriptor is invalid")
	}
	defer file.Close()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 ||
		before.Gid != 0 || before.Mode&0777 != 0600 || before.Nlink != 1 || before.Size <= 0 ||
		before.Size > guestControlMaterialLimit {
		return guestcontrolauth.AgentMaterial{}, false, errors.New(
			"guest control material is not a private regular file",
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, guestControlMaterialLimit+1))
	if err != nil || len(raw) > guestControlMaterialLimit {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material read failed")
	}
	if err := ctx.Err(); err != nil {
		return guestcontrolauth.AgentMaterial{}, false, err
	}
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Mode != after.Mode ||
		before.Uid != after.Uid || before.Gid != after.Gid || before.Nlink != after.Nlink || before.Size != after.Size ||
		before.Mtim != after.Mtim || before.Ctim != after.Ctim || int64(len(raw)) != before.Size {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material changed during read")
	}
	material, err := guestcontrolauth.DecodeAgentMaterial(raw)
	if err != nil {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material is invalid")
	}
	canonical, err := material.Encode()
	if err != nil || !bytes.Equal(raw, canonical) {
		return guestcontrolauth.AgentMaterial{}, false, errors.New("guest control material is not canonical")
	}
	return material, true, nil
}
