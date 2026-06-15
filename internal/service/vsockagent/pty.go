package vsockagent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// configurePTY sets the PTY slave's termios for proper interactive shell
// behavior on all kernel versions (v5+). Ensures ICRNL converts \r→\n,
// ICANON provides line editing, ECHO echoes input, and ISIG enables
// Ctrl+C/Z processing.
func configurePTY(slave *os.File) {
	tio, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		slog.Warn("ptty: failed to get termios, using kernel defaults", "error", err)
		return
	}
	tio.Iflag |= unix.ICRNL
	tio.Lflag |= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ISIG
	tio.Oflag |= unix.OPOST | unix.ONLCR
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, tio); err != nil {
		slog.Warn("ptty: failed to set termios, using kernel defaults", "error", err)
	}
}

// handleTTY allocates a PTY, forks a shell on the slave side, and relays
// bytes between the vsock connection and the PTY master.
// This call is blocking: it runs until the connection or shell terminates.
// The context is used for cancellation (e.g. agent shutdown).
func handleTTY(ctx context.Context, conn net.Conn, req *execRequest) {
	master, slave, err := openPTY()
	if err != nil {
		slog.Error("failed to open PTY", "error", err)
		return
	}
	defer master.Close()
	defer slave.Close()

	var cmd *exec.Cmd
	if req.User != "" && req.User != "root" {
		cmd = exec.CommandContext(ctx, "su", "-", req.User)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-i")
	}

	// Merge environment if provided.
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}

	configurePTY(slave)

	if err := cmd.Start(); err != nil {
		slog.Error("failed to start shell", "error", err)
		return
	}
	slog.Debug("tty: shell started")

	// NOTE: We intentionally do NOT call term.MakeRaw(master) here.
	// On Linux, the PTY master and slave share a single tty_struct.
	// MakeRaw on the master would disable ICRNL on that shared struct,
	// which means the user's Enter key (\r from the raw host terminal)
	// is NOT converted to \n — the shell would never receive a newline
	// and appears "blocked". The shell handles its own terminal settings.

	// ── Shell exit monitor ──
	//
	// When the user types "exit" or Ctrl+D, the shell process terminates.
	// The PTY slave closes, causing master reads to return EOF. But the
	// conn→PTY relay (reading from vsock) would hang forever because vsock
	// remains open — the host side doesn't know the shell has exited.
	// We monitor cmd.Wait() and close vsock when the shell exits.
	// If the user types "exit\n" (or logout) and the shell doesn't exit
	// within 5 seconds, we force-kill it — this prevents hangs on VMs
	// where the shell doesn't process the exit command.
	shellDone := make(chan struct{})
	waitDone := make(chan struct{})
	exitTimer := time.NewTimer(0) // Stopped initially — armed when "exit" is detected
	if !exitTimer.Stop() {
		<-exitTimer.C
	}

	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	// Normal exit path: shell exits, we clean up.
	var closeShellDone sync.Once
	go func() {
		<-waitDone
		slog.Debug("tty: monitor — shell exited normally")
		exitTimer.Stop()
		closeShellDone.Do(func() { close(shellDone) })
		slave.Close()
		conn.Close()
	}()

	// Kill switch: user typed "exit" but shell didn't exit within 5s.
	// Directly force-closes everything, bypassing the normal path.
	go func() {
		<-exitTimer.C
		slog.Warn("tty: kill switch — exit timer expired, force-closing")
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		closeShellDone.Do(func() { close(shellDone) })
		slave.Close()
		conn.Close()
	}()

	// ── Bidirectional relay ──
	//
	// PTY→conn runs in a goroutine. conn→PTY runs in the main goroutine:
	// when it returns (host disconnected or shell exited via monitor
	// closing conn), we close the master to unblock the PTY→conn
	// goroutine. The conn→PTY loop also scans for exit commands.
	var wg sync.WaitGroup

	wg.Go(func() {
		// PTY master → vsock connection: send shell output to host.
		slog.Debug("tty: relay goroutine (master→conn) starting")
		relayErr := error(nil)
		relayBuf := make([]byte, 32*1024)
		for {
			n, err := master.Read(relayBuf)
			if n > 0 {
				if _, werr := conn.Write(relayBuf[:n]); werr != nil {
					relayErr = werr
					break
				}
			}
			if err != nil {
				relayErr = err
				break
			}
		}
		if relayErr != nil && relayErr != io.EOF {
			slog.Debug("tty: PTY→conn copy ended", "error", relayErr)
		} else {
			slog.Debug("tty: PTY→conn copy ended (EOF)")
		}
	})

	// vsock connection → PTY master: forward host input to shell.
	// Custom loop instead of io.Copy so we can detect "exit" commands.
	slog.Debug("tty: main goroutine — starting host input relay")
	relayErr := error(nil)
	relayBuf := make([]byte, 32*1024)
	var partialLine []byte
	for {
		n, err := conn.Read(relayBuf)
		if n > 0 {
			// Scan for \n (line terminators) and check if a complete
			// line matches exit/logout — enables auto-kill on stuck shells.
			for i := 0; i < n; i++ {
				b := relayBuf[i]
				if b == '\n' {
					line := strings.TrimSpace(string(partialLine))
					partialLine = partialLine[:0]
					if line == "exit" || strings.HasPrefix(line, "exit ") || line == "logout" {
						slog.Debug("tty: exit command detected, starting 5s kill timer")
						exitTimer.Reset(5 * time.Second)
					}
				} else {
					partialLine = append(partialLine, b)
				}
			}
			if _, werr := master.Write(relayBuf[:n]); werr != nil {
				relayErr = werr
				break
			}
		}
		if err != nil {
			relayErr = err
			break
		}
	}
	if relayErr != nil && relayErr != io.EOF {
		slog.Debug("tty: conn→PTY relay ended", "error", relayErr)
	} else {
		slog.Debug("tty: conn→PTY relay ended (EOF)")
	}

	// conn→PTY returned (host disconnected or shell exited). Close the
	// master to unblock the PTY→conn goroutine, then wait for it.
	slog.Debug("tty: master.Close()")
	master.Close()
	slog.Debug("tty: wg.Wait()")
	wg.Wait()
	slog.Debug("tty: relay cleanup done")

	// If the shell is still running (host disconnected before shell
	// exited), kill it and wait for the monitor goroutine to reap it.
	select {
	case <-shellDone:
		// Shell already exited — monitor already called cmd.Wait().
	default:
		slog.Debug("tty: killing shell (host disconnected)")
		_ = cmd.Process.Kill()
		<-shellDone // Wait for monitor to reap the zombie process.
	}

	// Terminate the shell process.
	if cmd.Process != nil {
		_ = cmd.Process.Kill() // best-effort: process exits on its own after PTY close
		_ = cmd.Wait()         // best-effort: exit status already handled
	}

	slog.Debug("tty: TTY session ended")
}

// openPTY allocates a new PTY pair and returns the master and slave ends.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// Grant access to the slave. On modern devpts with pt_chown, this is
	// a no-op and the ioctl may return ENOSYS — we tolerate that.
	if err := grantpt(master); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}

	// Unlock the slave.
	if err := unlockpt(master); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}

	// Get the slave device path.
	slavePath, err := ptsname(master)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}

	slave, err = os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slavePath, err)
	}

	return master, slave, nil
}

// ptsname returns the slave PTY path for a given master PTY fd.
// It uses the TIOCGPTN ioctl to get the PTY index.
func ptsname(f *os.File) (string, error) {
	n, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPTN)
	if err != nil {
		return "", fmt.Errorf("TIOCGPTN: %w", err)
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

// grantpt grants access to the slave PTY. On modern devpts with automatic
// ownership, this is a no-op. We implement it as a no-op since the Go
// unix.Grantpt is not available on Linux.
func grantpt(f *os.File) error {
	// On modern Linux devpts filesystems, slave access is managed by the
	// filesystem itself — grantpt(3) is a documented no-op. We skip the
	// actual ioctl since it may fail with ENOSYS on recent kernels.
	return nil
}

// unlockpt unlocks the slave PTY so it can be opened.
func unlockpt(f *os.File) error {
	return unix.IoctlSetPointerInt(int(f.Fd()), unix.TIOCSPTLCK, 0)
}
