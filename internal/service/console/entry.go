package console

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"mvmctl/internal/infra"

	"golang.org/x/sys/unix"
)

const (
	// DefaultConsolePIDFilename is the default PID file name for console relays.
	DefaultConsolePIDFilename = "console.pid"
	// DefaultConsoleSocketFilename is the default socket file name for console relays.
	DefaultConsoleSocketFilename = "console.sock"
	// DefaultConsoleLogFilename is the default log file name for console relays.
	DefaultConsoleLogFilename = "firecracker.console.log"

	consoleReadBufferSize = 4096 // CONST_CONSOLE_READ_BUFFER_SIZE

	// Wire protocol constants for the console relay control header.
	// First 8 bytes of every connection: magic(3) + version(1) + rows(2) + cols(2).
	wsMagic      = "MVM"   // 3-byte magic identifier
	wsVersion    = byte(1) // current protocol version
	wsHeaderSize = 8       // total control header bytes
)

// Config holds configuration for the console relay subprocess.
type Config struct {
	VMID           string
	VMPath         string
	VMName         string
	PtyFD          int
	PIDFilename    string // optional, defaults to DefaultConsolePIDFilename
	SocketFilename string // optional, defaults to DefaultConsoleSocketFilename
	LogFilename    string // optional, defaults to DefaultConsoleLogFilename
}

// Run starts the console relay with the given config.
// This is the canonical entry point called by the CLI layer.
func Run(ctx context.Context, cfg Config) error {
	if cfg.VMID == "" || cfg.VMPath == "" || cfg.PtyFD == 0 {
		return fmt.Errorf("missing required arguments for console relay")
	}

	// Compute paths from VM directory, using provided filenames or defaults.
	pidName := cfg.PIDFilename
	if pidName == "" {
		pidName = DefaultConsolePIDFilename
	}
	sockName := cfg.SocketFilename
	if sockName == "" {
		sockName = DefaultConsoleSocketFilename
	}
	logName := cfg.LogFilename
	if logName == "" {
		logName = DefaultConsoleLogFilename
	}
	pidPath := filepath.Join(cfg.VMPath, pidName)
	socketPath := filepath.Join(cfg.VMPath, sockName)
	logPath := filepath.Join(cfg.VMPath, logName)

	// Open the PTY FD inherited from parent (passed as ExtraFiles[0] = FD 3).
	ptyFile := os.NewFile(uintptr(cfg.PtyFD), "pty")
	if ptyFile == nil {
		return fmt.Errorf("invalid PTY FD %d", cfg.PtyFD)
	}
	defer ptyFile.Close()

	// --- Signal handling ---
	relayCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// --- Open log file ---
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("cannot create log file %s: %w", logPath, err)
	}
	defer logFile.Close()

	// --- Set up Unix socket ---
	os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("cannot listen on socket %s: %w", socketPath, err)
	}

	// --- Ensure cleanup on exit ---
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

	// Accept runs in a background goroutine so it never blocks the
	// main event loop. Accepted connections are delivered via acceptCh.
	acceptCh := make(chan net.Conn, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err == nil {
				select {
				case acceptCh <- conn:
				default:
					conn.Close()
				}
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	var (
		clientConn net.Conn
		clientCh   <-chan []byte // nil channel blocks in select forever
	)

	for {
		// Main select: PTY and client data handled symmetrically.
		// Neither direction blocks the other anymore.
		select {
		case <-ctx.Done():
			return nil

		case conn := <-acceptCh:
			// Accept a new client connection (only one at a time).
			if clientConn == nil {
				// Read and validate window size header (wsHeaderSize bytes:
				// magic(3) + version(1) + rows(2) + cols(2)).
				var ws [wsHeaderSize]byte
				if _, err := io.ReadFull(conn, ws[:]); err != nil {
					slog.Warn("Failed to read window size header", "error", err)
					conn.Close()
					break
				}
				if string(ws[:3]) != wsMagic {
					slog.Warn("Invalid window size magic", "got", fmt.Sprintf("%q", string(ws[:3])))
					conn.Close()
					break
				}
				if ws[3] != wsVersion {
					slog.Warn("Unsupported window size version", "got", ws[3])
					conn.Close()
					break
				}
				rows := binary.LittleEndian.Uint16(ws[4:6])
				cols := binary.LittleEndian.Uint16(ws[6:8])
				if err := unix.IoctlSetWinsize(int(ptyFile.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
					Row: rows,
					Col: cols,
				}); err != nil {
					slog.Warn("Failed to set PTY window size", "error", err)
				}
				clientConn = conn
				clientCh = startClientReader(ctx, conn)
			} else {
				conn.Close()
			}

		case pr, ok := <-ptyCh:
			if !ok {
				return nil
			}
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
					clientCh = nil // nil channel blocks in select
				}
			}

		case data, ok := <-clientCh:
			if !ok {
				// Client disconnected — clean up.
				clientConn.Close()
				clientConn = nil
				clientCh = nil // nil channel blocks in select
			} else {
				// Forward client input to PTY (firecracker serial console).
				if _, err := ptyFile.Write(data); err != nil {
					slog.Warn("Failed to forward client input to PTY", "error", err)
				}
			}
		}
	}
}

// startClientReader launches a goroutine that reads from conn and feeds data
// to the returned channel. The channel is closed when conn.Read() returns an
// error (including normal EOF on disconnect) or ctx is cancelled.
func startClientReader(ctx context.Context, conn net.Conn) <-chan []byte {
	ch := make(chan []byte, 256)
	go func() {
		defer close(ch)
		buf := make([]byte, consoleReadBufferSize)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := conn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case ch <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}
