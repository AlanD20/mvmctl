package console

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// RunDescriptors serves the existing console protocol using inherited, pinned resources.
// CRITICAL: This adapter must run with caller credentials and no supplementary groups. The
// runtime directory and log are already admitted by the root launch receiver; no cache path
// is reopened. ready is the inherited private setup channel, used only for a fixed acknowledgement.
func RunDescriptors(ctx context.Context, ptyFile, logFile, runtimeDirectory, ready *os.File) (returnErr error) {
	if ctx == nil || ptyFile == nil || logFile == nil || runtimeDirectory == nil || ready == nil ||
		os.Getuid() == 0 || os.Geteuid() == 0 || os.Getgid() == 0 || os.Getegid() == 0 {
		return errs.New(errs.CodeConsoleRelayFailed, "console descriptor admission is incomplete")
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		return errs.WrapMsg(errs.CodeConsoleRelayFailed, "console requires empty supplementary groups", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The proc descriptor reference pins the parent through bind; a caller rename cannot
	// redirect the socket into another directory. Existing leaves are never adopted or removed.
	socketPath := "/proc/self/fd/" + strconv.FormatUint(uint64(runtimeDirectory.Fd()), 10) +
		"/" + infra.VMConsoleSocketFilename
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return errs.WrapMsg(errs.CodeConsoleRelayFailed, "bind pinned console socket", err)
	}
	// Root cleanup owns the fixed runtime leaves, including partial setup. Automatic path
	// unlink would race same-owner replacement; the relay closes only its live listener.
	listener.SetUnlinkOnClose(false)
	defer func() { returnErr = errors.Join(returnErr, listener.Close()) }()
	pidFD, err := unix.Openat(int(runtimeDirectory.Fd()), infra.VMConsolePIDFilename,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errs.WrapMsg(errs.CodeConsoleRelayFailed, "create pinned console PID mirror", err)
	}
	pidFile := os.NewFile(uintptr(pidFD), "console-pid-mirror")
	_, writeErr := pidFile.WriteString(strconv.Itoa(os.Getpid()))
	if err := errors.Join(writeErr, pidFile.Close()); err != nil {
		return errs.WrapMsg(errs.CodeConsoleRelayFailed, "write console PID mirror", err)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		return errs.WrapMsg(errs.CodeConsoleRelayFailed, "acknowledge console listener readiness", err)
	}
	relayCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return runRelayIO(relayCtx, ptyFile, logFile, listener)
}
