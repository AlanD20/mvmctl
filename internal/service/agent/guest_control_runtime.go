package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// Automatic renewal, health policy and failover remain separate explicit features.
// runGuestControl keeps malformed/absent provisioning and control-only port failures separate from legacy exec.
func (a *Agent) runGuestControl(ctx context.Context) {
	material, enabled, err := LoadGuestControlAgentMaterial(ctx)
	if err != nil {
		slog.Warn("guest control unavailable: invalid private provisioning")
		return
	}
	if !enabled {
		return
	}
	if a.port == infra.GuestControlVsockPort {
		slog.Warn("guest control unavailable: configured exec port conflicts with dedicated control port")
		return
	}
	rawHost, err := listenVsock(infra.GuestControlVsockPort)
	if err != nil {
		slog.Warn("guest control unavailable: dedicated vsock port cannot listen")
		return
	}
	host := &guestControlVsockListener{listener: rawHost}
	defer host.Close()
	local, cleanup, err := listenGuestControlLocal()
	if err != nil {
		_ = host.Close()
		slog.Warn("guest control unavailable: private local socket cannot listen")
		return
	}
	defer cleanup()
	gateway, err := NewGuestControlGateway(host, local, material)
	if err != nil {
		_ = host.Close()
		slog.Warn("guest control unavailable: gateway initialization failed")
		return
	}
	store, err := openGuestControlAliasStore()
	if err != nil {
		slog.Warn("guest control unavailable: alias receipts are unsafe")
		return
	}
	defer store.close()
	kernel, err := newGuestControlAliasNetlink()
	if err != nil {
		slog.Warn("guest control unavailable: alias network authority is unavailable")
		return
	}
	defer kernel.close()
	aliases, err := newGuestControlAliases(kernel, store)
	if err != nil {
		slog.Warn("guest control unavailable: alias manager initialization failed")
		return
	}
	defer func() {
		if err := aliases.close(); err != nil {
			slog.Warn("guest control alias cleanup remains pending")
		}
	}()
	recovery, cancel := context.WithTimeout(ctx, guestControlAliasIOTimeout)
	err = aliases.recover(recovery)
	cancel()
	if err != nil {
		slog.Warn("guest control unavailable: alias cleanup needs recovery")
		return
	}
	gateway.aliases = aliases
	if err := gateway.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("guest control gateway stopped")
	}
}

func listenGuestControlLocal() (*net.UnixListener, func(), error) {
	run, err := unix.Open("/run", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer unix.Close(run)
	var root unix.Stat_t
	if err := unix.Fstat(run, &root); err != nil || root.Uid != 0 || root.Gid != 0 || root.Mode&0022 != 0 {
		return nil, nil, errors.New("control runtime parent is unsafe")
	}
	if err := unix.Mkdirat(run, "mvm-agent", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, nil, err
	}
	directory, err := unix.Openat2(run, "mvm-agent", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, nil, err
	}
	listener, err := listenGuestControlLocalAt(directory)
	if err != nil {
		_ = unix.Close(directory)
		return nil, nil, err
	}
	return listener, func() { _ = listener.Close(); _ = unix.Close(directory) }, nil
}

func listenGuestControlLocalAt(directory int) (*net.UnixListener, error) {
	var parent unix.Stat_t
	if err := unix.Fstat(directory, &parent); err != nil || parent.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parent.Uid != 0 || parent.Gid != 0 || parent.Mode&0777 != 0700 {
		return nil, errors.New("control directory is not root-private")
	}
	path := fmt.Sprintf("/proc/self/fd/%d/%s", directory, infra.GuestControlLocalSocketFilename)
	var stale unix.Stat_t
	err := unix.Fstatat(directory, infra.GuestControlLocalSocketFilename, &stale, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if stale.Mode&unix.S_IFMT != unix.S_IFSOCK || stale.Uid != 0 || stale.Gid != 0 || stale.Mode&0777 != 0600 {
			return nil, errors.New("existing control socket is unsafe")
		}
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("control socket is already active")
		}
		if !errors.Is(dialErr, unix.ECONNREFUSED) {
			return nil, errors.New("control socket absence is unproven")
		}
		var current unix.Stat_t
		err := unix.Fstatat(directory, infra.GuestControlLocalSocketFilename, &current, unix.AT_SYMLINK_NOFOLLOW)
		if err != nil ||
			current.Dev != stale.Dev || current.Ino != stale.Ino {
			return nil, errors.New("control socket changed")
		}
		if err := unix.Unlinkat(directory, infra.GuestControlLocalSocketFilename, 0); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := unix.Fchmodat(directory, infra.GuestControlLocalSocketFilename, 0600, 0); err != nil {
		_ = listener.Close()
		return nil, err
	}
	var socket unix.Stat_t
	err = unix.Fstatat(directory, infra.GuestControlLocalSocketFilename, &socket, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil ||
		socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Uid != 0 || socket.Gid != 0 || socket.Mode&0777 != 0600 {
		_ = listener.Close()
		return nil, errors.New("control socket ownership differs")
	}
	return listener, nil
}

// guestControlVsockListener closes exactly once and wakes the raw blocking accept before releasing its descriptor.
// The existing configurable exec listener remains unchanged.
type guestControlVsockListener struct {
	listener *vsockListener
	once     sync.Once
	closeErr error
}

func (listener *guestControlVsockListener) Accept() (net.Conn, error) {
	return listener.listener.Accept()
}
func (listener *guestControlVsockListener) Addr() net.Addr { return listener.listener.Addr() }
func (listener *guestControlVsockListener) Close() error {
	listener.once.Do(func() {
		_ = unix.Shutdown(listener.listener.fd, unix.SHUT_RDWR)
		listener.closeErr = listener.listener.Close()
	})
	return listener.closeErr
}
