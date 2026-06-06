package console

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mvmctl/internal/infra"
)

const (
	// DefaultConsolePIDFilename is the default PID file name for console relays.
	DefaultConsolePIDFilename = "console.pid"
	// DefaultConsoleSocketFilename is the default socket file name for console relays.
	DefaultConsoleSocketFilename = "console.sock"
	// DefaultConsoleLogFilename is the default log file name for console relays.
	DefaultConsoleLogFilename = "firecracker.console.log"

	consoleReadBufferSize = 4096 // CONST_CONSOLE_READ_BUFFER_SIZE
	consoleSelectTimeoutS = 0.1  // CONST_CONSOLE_SELECT_TIMEOUT_S
)

// Config holds configuration for the console relay subprocess.
type Config struct {
	VMID   string
	VMPath string
	VMName string
	PtyFD  int
}

// Run starts the console relay with the given config.
// This is the canonical entry point called by the CLI layer.
func Run(ctx context.Context, cfg Config) error {
	if cfg.VMID == "" || cfg.VMPath == "" || cfg.PtyFD == 0 {
		return fmt.Errorf("missing required arguments for console relay")
	}

	// Compute paths from VM directory.
	pidPath := filepath.Join(cfg.VMPath, DefaultConsolePIDFilename)
	socketPath := filepath.Join(cfg.VMPath, DefaultConsoleSocketFilename)
	logPath := filepath.Join(cfg.VMPath, DefaultConsoleLogFilename)

	// Open the PTY FD inherited from parent (passed as ExtraFiles[0] = FD 3).
	ptyFile := os.NewFile(uintptr(cfg.PtyFD), "pty")
	if ptyFile == nil {
		return fmt.Errorf("invalid PTY FD %d", cfg.PtyFD)
	}
	defer ptyFile.Close()

	// ── Signal handling for graceful shutdown ──
	relayCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// ── Open log file ──
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("cannot create log file %s: %w", logPath, err)
	}
	defer logFile.Close()

	// ── Set up Unix socket ──
	os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("cannot listen on socket %s: %w", socketPath, err)
	}

	// ── Ensure socket/PID file cleanup on exit ──
	cleanup := func() {
		listener.Close()
		os.Remove(socketPath)
		os.Remove(pidPath)
	}
	defer cleanup()

	// Ensure VM directory exists for PID file.
	os.MkdirAll(filepath.Dir(pidPath), infra.DirPerm) //nolint:errcheck

	// Run relay I/O loop (blocking).
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		return fmt.Errorf("expected Unix listener, got %T", listener)
	}
	return runRelayIO(relayCtx, ptyFile, logFile, unixListener)
}

type ptyRead struct {
	data []byte
	err  error
}

// runRelayIO runs the PTY↔socket relay I/O loop.
// Reads from PTY, writes to log file and connected Unix socket clients.
// Blocks until context cancellation or PTY closure.
//
// On ctx cancellation, ptyFile is closed to unblock the PTY reader goroutine.
// The caller must close listener after this returns.
func runRelayIO(ctx context.Context, ptyFile *os.File, logFile *os.File, listener *net.UnixListener) error {
	// Close ptyFile on ctx cancellation to unblock the PTY reader goroutine.
	// This causes Read() to return an error, which propagates to the main loop.
	go func() {
		<-ctx.Done()
		ptyFile.Close()
	}()

	// PTY reader goroutine.
	readBuf := make([]byte, consoleReadBufferSize)
	ptyCh := make(chan ptyRead, 256)
	go func() {
		for {
			n, err := ptyFile.Read(readBuf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, readBuf[:n])
				select {
				case ptyCh <- ptyRead{data: data}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case ptyCh <- ptyRead{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	// Main select loop: accept clients, forward PTY↔client.
	var clientConn net.Conn
	clientBuf := make([]byte, consoleReadBufferSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if clientConn == nil {
			listener.SetDeadline(time.Now().Add(
				time.Duration(consoleSelectTimeoutS * float64(time.Second)),
			))
			conn, err := listener.Accept()
			if err == nil {
				clientConn = conn
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// No client yet — continue.
			} else {
				slog.Debug("Accept error", "error", err)
			}
		}

		if clientConn != nil {
			clientConn.SetReadDeadline(time.Now().Add(
				time.Duration(consoleSelectTimeoutS * float64(time.Second)),
			))
			n, err := clientConn.Read(clientBuf)
			if n > 0 {
				if _, wErr := ptyFile.Write(clientBuf[:n]); wErr != nil {
					slog.Warn("Failed to forward client input to PTY", "error", wErr)
				}
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout — normal.
				} else {
					clientConn.Close()
					clientConn = nil
				}
			}
		}

		select {
		case pr := <-ptyCh:
			if pr.err != nil {
				if pr.err != io.EOF {
					slog.Warn("PTY read error", "error", pr.err)
				}
				return nil
			}
			if _, err := logFile.Write(pr.data); err != nil {
				slog.Warn("Failed to write PTY data to log", "error", err)
			}
			if clientConn != nil {
				if _, err := clientConn.Write(pr.data); err != nil {
					clientConn.Close()
					clientConn = nil
				}
			}
		default:
		}
	}
}
