package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"

	"golang.org/x/term"
)

// isAlive checks if a process with the given PID is still running.
func isAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TODO(verdict #32): The goroutine-based relay logic has been migrated to
// internal/service/console/. New code should prefer that package.
// This file is kept for backward compatibility.
// ── Console relay defaults (matching Python's console_relay._defaults.py) ──
const (
	DefaultConsolePIDFilename    = "console.pid"
	DefaultConsoleSocketFilename = "console.sock"
	DefaultConsoleLogFilename    = "firecracker.console.log"
	consoleKillTimeoutS          = 2.0             // CONST_CONSOLE_KILL_TIMEOUT_S (from _defaults.py)
	consoleReadBufferSize        = 4096            // CONST_CONSOLE_READ_BUFFER_SIZE
	consoleSelectTimeoutS        = 0.1             // CONST_CONSOLE_SELECT_TIMEOUT_S
	consoleSocketBacklog         = 1               // CONST_CONSOLE_SOCKET_BACKLOG
	consoleSocketTimeout         = 2 * time.Second // CONST_CONSOLE_SOCKET_TIMEOUT_S (from constants.py)
	consolePollIntervalS         = 0.05            // polling interval used by CLI _interact
)

// DetachSequence is the byte sequence that triggers detach: Ctrl+X (0x18) followed by 'd' (0x64).
// Matches Python's CONST_CONSOLE_DETACH_SEQUENCE = b"\x18d".
var DetachSequence = []byte{0x18, 'd'}

// ── RelayManager — Manages a console relay instance ─────────────────────
// Matches Python's ConsoleRelayManager exactly in behavior.
// Python spawns a subprocess; Go uses a goroutine + Unix socket.
// RelayManager manages the lifecycle of a console relay.
type RelayManager struct {
	mu         sync.Mutex
	id         string
	path       string
	name       string
	pidFile    string
	sockFile   string
	logFile    string
	pidPath    string
	socketPath string
	logPath    string
	relayPid   int
}

// NewRelayManager creates a new console relay manager.
// Matches Python's ConsoleRelayManager.__init__().
func NewRelayManager(
	id string,
	path string,
	name string,
	pidFilename, socketFilename, logFilename string,
) *RelayManager {
	if pidFilename == "" {
		pidFilename = DefaultConsolePIDFilename
	}
	if socketFilename == "" {
		socketFilename = DefaultConsoleSocketFilename
	}
	if logFilename == "" {
		logFilename = DefaultConsoleLogFilename
	}
	if name == "" {
		name = id
	}
	return &RelayManager{
		id:         id,
		path:       path,
		name:       name,
		pidFile:    pidFilename,
		sockFile:   socketFilename,
		logFile:    logFilename,
		pidPath:    filepath.Join(path, pidFilename),
		socketPath: filepath.Join(path, socketFilename),
		logPath:    filepath.Join(path, logFilename),
	}
}

// ID returns the relay's unique identifier. Matches Python's property.
func (rm *RelayManager) ID() string { return rm.id }

// Name returns the relay's human-readable name. Matches Python's property.
func (rm *RelayManager) Name() string { return rm.name }

// PID returns the relay process PID, or nil if not running.
// Matches Python's pid property.
func (rm *RelayManager) PID() *int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.relayPid > 0 {
		pid := rm.relayPid
		return &pid
	}
	if data, err := os.ReadFile(rm.pidPath); err == nil {
		pid, err := strconv.Atoi(string(data))
		if err == nil {
			return &pid
		}
	}
	return nil
}

// PIDPath returns the path to the PID file. Matches Python's property.
func (rm *RelayManager) PIDPath() string { return rm.pidPath }

// SocketPath returns the relay's socket path. Matches Python's property.
func (rm *RelayManager) SocketPath() string { return rm.socketPath }

// LogPath returns the relay's log path. Matches Python's property.
func (rm *RelayManager) LogPath() string { return rm.logPath }

// Start begins the console relay subprocess with the given PTY controller FD.
// Spawns "mvm run console-relay" as a detached subprocess.
// Returns (socketPath, pid, error). Matches Python's ConsoleRelayManager.start().
func (rm *RelayManager) Start(ctx context.Context, ptyControllerFD int) (string, int, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.relayPid > 0 {
		return "", 0, ErrAlreadyRunning(rm.id)
	}
	if err := os.MkdirAll(rm.path, infra.DirPerm); err != nil {
		return "", 0, err
	}

	// Convert PTY controller FD to *os.File for passing to subprocess.
	// The subprocess inherits this FD (as ExtraFiles[0] = FD 3).
	ptyFile := os.NewFile(uintptr(ptyControllerFD), "pty")
	if ptyFile == nil {
		return "", 0, fmt.Errorf("invalid PTY controller FD %d", ptyControllerFD)
	}

	// Spawn "mvm run console-relay" subprocess
	args := []string{
		"--vm-id", rm.id,
		"--vm-path", rm.path,
		"--vm-name", rm.name,
		"--pty-fd", "3",
	}
	cmd, err := system.SpawnService("console-relay", []*os.File{ptyFile}, args...)
	if err != nil {
		return "", 0, ErrProcessFailed(rm.id, err)
	}

	pid := cmd.Process.Pid
	rm.relayPid = pid

	// Poll for socket to appear (subprocess creates it in net.Listen)
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(rm.socketPath); err == nil {
			return rm.socketPath, pid, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	return "", 0, ErrProcessFailed(rm.id, fmt.Errorf("console relay socket %s did not appear within 2.5s", rm.socketPath))
}

// relayLoop is the main goroutine implementing the relay logic.
// Matches Python's process.py main() — reads from PTY, writes to log file,
// listens on Unix socket, forwards bidirectionally between PTY and connected client.
// Stop stops the relay and cleans up.
// force=true: immediate stop (matches Python's force=True: SIGTERM + cleanup_files + _pid = None).
// force=false: graceful stop with kill escalation timeout (matches Python's graceful stop).
func (rm *RelayManager) Stop(force bool) bool {
	rm.mu.Lock()
	pid := rm.relayPid
	if pid <= 0 {
		rm.mu.Unlock()
		return false
	}
	rm.mu.Unlock()

	// On Unix, os.FindProcess always succeeds — use syscall.Kill for all signaling.
	// Matches Python's _send_signal() which uses os.kill(pid, sig) and handles
	// ProcessLookupError (ESRCH) and PermissionError (EPERM).

	if force {
		// Abrupt: SIGKILL, clean up immediately (matches Python's force=True).
		syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck
		rm.mu.Lock()
		rm.cleanupFiles()
		rm.relayPid = 0
		rm.mu.Unlock()
		slog.Info("Terminated console relay", "name", rm.name)
		return true
	}

	// Graceful: send SIGTERM, then poll for completion
	// Matches Python's: if not self._send_signal(pid, signal.SIGTERM): cleanup + return
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		// Process already dead — matches Python's ProcessLookupError
		rm.mu.Lock()
		rm.cleanupFiles()
		rm.relayPid = 0
		rm.mu.Unlock()
		return true
	}

	// Poll for process death (matches Python's loop with signal 0)
	for i := 0; i < int(consoleKillTimeoutS*10); i++ {
		time.Sleep(100 * time.Millisecond)
		if !isAlive(pid) {
			rm.mu.Lock()
			rm.cleanupFiles()
			rm.relayPid = 0
			rm.mu.Unlock()
			slog.Info("Terminated console relay", "name", rm.name)
			return true
		}
	}
	// Timeout — escalate to SIGKILL (matches Python's else: self._send_signal(pid, signal.SIGKILL))
	syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck
	rm.mu.Lock()
	rm.cleanupFiles()
	rm.relayPid = 0
	rm.mu.Unlock()
	slog.Info("Terminated console relay", "name", rm.name)
	return true
}

func (rm *RelayManager) cleanupFiles() {
	os.Remove(rm.pidPath)
	os.Remove(rm.socketPath)
}

// GetPID returns the PID of the running relay, verifying liveness.
// Matches Python's ConsoleRelayManager.get_pid() which uses os.kill(pid, 0).
func (rm *RelayManager) GetPID() *int {
	rm.mu.Lock()
	pid := rm.relayPid
	if pid <= 0 {
		rm.mu.Unlock()
		return rm.readPIDFromFile()
	}
	rm.mu.Unlock()
	// Verify liveness (Python: os.kill(pid, 0))
	if isAlive(pid) {
		return &pid
	}
	return nil
}

// readPIDFromFile reads the PID from the PID file without liveness verification.
// This is the Go fallback for when no in-memory PID is available, matching
// Python's fallback path in get_pid().
func (rm *RelayManager) readPIDFromFile() *int {
	data, err := os.ReadFile(rm.pidPath)
	if err != nil {
		return nil
	}
	// Use TrimSpace to match Python's .strip() in int(pid_file.read_text().strip())
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return nil
	}
	return &pid
}

// IsRunning checks if the relay is currently running.
// Matches Python's ConsoleRelayManager.is_running() which calls get_pid()
// and returns True only if get_pid() returns a non-None PID.
func (rm *RelayManager) IsRunning() bool {
	return rm.GetPID() != nil
}

// CleanupOrphans scans for stale console PID files from previous crashed sessions
// and cleans them up. Matches Python's ConsoleRelayManager.cleanup_orphans() exactly.
// vmsDir is the path to the directory containing per-VM directories.
//
// Python logic:
//
//	logger.debug("Running console relay orphan cleanup check")
//	for each VM directory:
//	  pid_file = entry / self.pid_path
//	  if not pid_file.exists(): continue
//	  try: pid = int(pid_file.read_text()); os.kill(pid, 0)
//	  except ProcessLookupError → clean up stale PID file and socket
//	    logger.info("Cleaned up stale PID file for %s (process terminated)", id)
//	  except PermissionError → skip, just log
//	    logger.debug("Skipping orphan cleanup for %s - permission denied on process %s", id, pid_str)
//	  except (ValueError, OSError) → clean up PID file (invalid PID)
//	    logger.info("Cleaned up invalid PID file for %s", id)
func (rm *RelayManager) CleanupOrphans(vmsDir string) {
	slog.Debug("Running console relay orphan cleanup check")
	entries, err := os.ReadDir(vmsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		vmID := entry.Name()
		pidFilePath := filepath.Join(vmsDir, vmID, rm.pidFile)
		pidData, err := os.ReadFile(pidFilePath)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(string(pidData))
		if err != nil {
			// Invalid PID file — clean up (matches Python's ValueError branch)
			os.Remove(pidFilePath)
			slog.Info("Cleaned up invalid PID file", "vm_id", vmID)
			continue
		}
		// Check if process exists by sending signal 0 (matches Python's os.kill(pid, 0))
		err = syscall.Kill(pid, syscall.Signal(0))
		if err == nil {
			// Process still running — skip (matches Python's expected path)
			slog.Debug("Skipping orphan cleanup - process still running",
				"vm_id", vmID,
				"pid", pid,
			)
			continue
		}
		if errors.Is(err, syscall.ESRCH) {
			// ProcessLookupError — process terminated, clean up stale PID file and socket
			os.Remove(pidFilePath)
			slog.Info("Cleaned up stale PID file (process terminated)", "vm_id", vmID)
			socketFilePath := filepath.Join(vmsDir, vmID, rm.sockFile)
			os.Remove(socketFilePath)
		} else if errors.Is(err, syscall.EPERM) {
			// PermissionError — cannot signal, skip orphan cleanup for this entry
			pidStr := strings.TrimSpace(string(pidData))
			slog.Debug("Skipping orphan cleanup - permission denied",
				"vm_id", vmID,
				"pid", pidStr,
			)
		} else {
			// Other OSError — invalid PID file, clean up
			os.Remove(pidFilePath)
		}
	}
}

// ── RelayClient connects to a console relay Unix socket ─────────────────
// Matches Python's ConsoleRelayClient exactly.
// RelayClient provides a high-level client for bidirectional console
// communication with detach keybind support.
type RelayClient struct {
	socketPath string
	detachSeq  []byte
	conn       net.Conn
}

// NewRelayClient creates a console relay client.
// Matches Python's ConsoleRelayClient.__init__().
func NewRelayClient(socketPath string, detachSequence []byte) *RelayClient {
	if len(detachSequence) == 0 {
		detachSequence = DetachSequence
	}
	return &RelayClient{
		socketPath: socketPath,
		detachSeq:  detachSequence,
	}
}

// Connect connects to the console relay socket.
// Matches Python's ConsoleRelayClient.connect() exactly:
//
//	Python creates a socket, connects with timeout, then calls setblocking(False).
//	Go does the same via net.DialTimeout + SyscallConn to set non-blocking mode.
func (c *RelayClient) Connect() error {
	conn, err := net.DialTimeout("unix", c.socketPath, consoleSocketTimeout)
	if err != nil {
		return ErrConnectionFailed(c.socketPath, err)
	}
	// Set non-blocking mode to match Python's setblocking(False) after connect.
	// Python's socket starts in blocking mode for the connect() call, then
	// switches to non-blocking for the select-based receive() loop.
	if unixConn, ok := conn.(*net.UnixConn); ok {
		rawConn, err := unixConn.SyscallConn()
		if err == nil {
			rawConn.Control(func(fd uintptr) {
				syscall.SetNonblock(int(fd), true)
			})
		}
	}
	c.conn = conn
	return nil
}

// IsConnected checks if client is currently connected.
// Matches Python's ConsoleRelayClient.is_connected().
func (c *RelayClient) IsConnected() bool {
	return c.conn != nil
}

// Disconnect disconnects from the relay socket.
// Matches Python's ConsoleRelayClient.disconnect().
func (c *RelayClient) Disconnect() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// Close is the context-manager cleanup equivalent of Python's __exit__.
// Matches Python's ConsoleRelayClient.__exit__() which calls self.disconnect().
func (c *RelayClient) Close() error {
	c.Disconnect()
	return nil
}

// Send sends data to the console.
// Matches Python's ConsoleRelayClient.send().
func (c *RelayClient) Send(data []byte) bool {
	if c.conn == nil || len(data) == 0 {
		return false
	}
	_, err := c.conn.Write(data)
	return err == nil
}

// Receive returns a channel that yields data chunks as they arrive from
// the console relay socket. This is the Go equivalent of Python's
// ConsoleRelayClient.receive() generator, which yields bytes until the
// socket is closed or an error occurs.
//
// The channel is closed when:
//   - The connection is closed (remote end hung up)
//   - A non-recoverable error occurs (OSError, ConnectionResetError)
//   - The context is cancelled
//
// BlockingIOError/InterruptedError equivalents cause a retry (not a close).
// Timeouts (no data within select timeout) cause a retry (not a close).
func (c *RelayClient) Receive(ctx context.Context, bufferSize int) <-chan []byte {
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		if c.conn == nil {
			return
		}
		if bufferSize <= 0 {
			bufferSize = consoleReadBufferSize
		}
		for {
			// Set read deadline for select-like timeout behavior
			if err := c.conn.SetReadDeadline(time.Now().Add(
				time.Duration(consoleSelectTimeoutS * float64(time.Second)),
			)); err != nil {
				return
			}
			buf := make([]byte, bufferSize)
			n, err := c.conn.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout — matches Python: select times out (ready empty), no data
					// Python returns from generator on timeout.
					// Go equivalent: we also return (close channel) on timeout.
					return
				}
				// Connection reset or closed — matches Python's except (OSError, ConnectionResetError): return
				return
			}
			if n == 0 {
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case ch <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// CheckDetach checks if buffer ends with the detach sequence.
// Matches Python's ConsoleRelayClient.check_detach().
func (c *RelayClient) CheckDetach(buf []byte) bool {
	if len(buf) >= len(c.detachSeq) {
		end := buf[len(buf)-len(c.detachSeq):]
		return bytes.Equal(end, c.detachSeq)
	}
	return false
}

// DetachSequence returns the detach sequence. Matches Python's property.
func (c *RelayClient) DetachSequence() []byte {
	return c.detachSeq
}

// SocketPath returns the socket path. Matches Python's property.
func (c *RelayClient) SocketPath() string {
	return c.socketPath
}

// GetSocket returns the underlying connected socket for advanced use.
// Matches Python's ConsoleRelayClient.get_socket() which raises
// RuntimeError("Not connected - call connect() first") when not connected.
func (c *RelayClient) GetSocket() (net.Conn, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("Not connected - call connect() first")
	}
	return c.conn, nil
}

// ── Interactive Console Attach ──────────────────────────────────────────
// Matches Python's CLI _interact() exactly.
// InteractiveAttach connects to the console relay and enters interactive mode.
// Sets terminal to raw mode, forwards stdin→relay and relay→stdout,
// detaches on Ctrl+X then D. Matches the Python CLI attach behavior.
func InteractiveAttach(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error {
	client := NewRelayClient(socketPath, nil)
	if err := client.Connect(); err != nil {
		return err
	}
	defer client.Disconnect()
	if client.conn == nil {
		return fmt.Errorf("not connected")
	}
	// ── Set terminal to raw mode (matching Python's tty.setraw()) ──
	var oldState *term.State
	var stdinFD int
	if f, ok := stdin.(*os.File); ok {
		if term.IsTerminal(int(f.Fd())) {
			s, err := term.MakeRaw(int(f.Fd()))
			if err == nil {
				oldState = s
				stdinFD = int(f.Fd())
			}
		}
	}
	if oldState != nil {
		defer term.Restore(stdinFD, oldState) //nolint:errcheck
	}
	// ── Set up derived context for goroutine cancellation ──
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// ── Socket read goroutine: relay → stdout ──
	// Matches Python's: if sock in ready: data = sock.recv(4096); sys.stdout.buffer.write(data)
	socketCh := make(chan []byte, 10)
	socketErrCh := make(chan error, 1)
	go func() {
		buf := make([]byte, consoleReadBufferSize)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := client.conn.SetReadDeadline(time.Now().Add(
				time.Duration(consolePollIntervalS * float64(time.Second)),
			)); err != nil {
				socketErrCh <- err
				return
			}
			n, err := client.conn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case socketCh <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // timeout — normal polling
				}
				socketErrCh <- err
				return
			}
		}
	}()
	// ── Stdin read goroutine: stdin → stdinCh byte by byte ──
	// Matches Python's: char = sys.stdin.buffer.read(1)
	stdinCh := make(chan byte, 64)
	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := stdin.Read(buf)
			if n > 0 {
				select {
				case stdinCh <- buf[0]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// ── Main interact loop (matches Python's _interact()) ──
	// Python logic:
	//   while True:
	//     ready, _, _ = select.select([sys.stdin, sock], [], [], 0.05)
	//     for fd in ready:
	//       if fd == sock:  → read, write to stdout
	//       if fd == sys.stdin: → read 1 byte, buffer, check detach, send
	inputBuf := make([]byte, 0, 4096)
	for {
		select {
		case <-ctx.Done():
			return nil
		case data, ok := <-socketCh:
			if !ok {
				continue
			}
			if _, err := stdout.Write(data); err != nil {
				return err
			}
		case err := <-socketErrCh:
			_ = err // connection closed — return
			return nil
		case b := <-stdinCh:
			// Append byte to input buffer (matches Python: input_buffer.extend(char))
			inputBuf = append(inputBuf, b)
			// ── Detach check: bytes(input_buffer[-2:]) == b"\x18d" ──
			if len(inputBuf) >= 2 {
				if inputBuf[len(inputBuf)-2] == 0x18 && inputBuf[len(inputBuf)-1] == 'd' {
					// Send any remaining data before the detach sequence
					if len(inputBuf) > 2 {
						client.Send(inputBuf[:len(inputBuf)-2])
					}
					// Print detach confirmation matching Python's mvm_cli.info("\nDetached from console")
					fmt.Fprintf(os.Stderr, "\nDetached from console\n")
					return nil
				}
			}
			// ── Send logic matching Python's input handling ──
			// Python check: if input_buffer[0:1] != b"\x18":
			if inputBuf[0] != 0x18 {
				// First byte is not Ctrl+X — send entire buffer and clear
				client.Send(inputBuf)
				inputBuf = inputBuf[:0]
			} else if len(inputBuf) >= 2 {
				// First byte is Ctrl+X, we have 2+ bytes
				// Check: if bytes(input_buffer) != b"\x18d":
				if !(inputBuf[0] == 0x18 && inputBuf[1] == 'd') {
					// Not the detach sequence — send all and clear
					client.Send(inputBuf)
					inputBuf = inputBuf[:0]
				}
				// Otherwise (it IS the detach sequence), the earlier check caught it
			}
			// else: first byte is 0x18, buffer < 2 bytes — wait for more
		}
	}
}

// RunRelaySubprocess is the entry point for the console relay subprocess.
// Called from "mvm run console-relay". Parses args, opens the inherited PTY FD,
// writes PID file, and runs the relay loop (blocking).
func RunRelaySubprocess(args []string) {
	var vmID, vmPath, vmName string
	var ptyFD int

	for i := range args {
		switch args[i] {
		case "--vm-id":
			if i+1 < len(args) {
				vmID = args[i+1]
				i++
			}
		case "--vm-path":
			if i+1 < len(args) {
				vmPath = args[i+1]
				i++
			}
		case "--vm-name":
			if i+1 < len(args) {
				vmName = args[i+1]
				i++
			}
		case "--pty-fd":
			if i+1 < len(args) {
				ptyFD, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
	}

	if vmID == "" || vmPath == "" || ptyFD == 0 {
		fmt.Fprintf(os.Stderr, "Error: Missing required arguments for console relay. Usage: ...\n")
		os.Exit(1)
	}

	rm := NewRelayManager(vmID, vmPath, vmName, "", "", "")

	// Write PID file with our own PID (subprocess PID, not parent)
	if err := os.MkdirAll(filepath.Dir(rm.pidPath), infra.DirPerm); err == nil {
		_ = os.WriteFile(rm.pidPath, []byte(strconv.Itoa(os.Getpid())), 0644)
	}

	// Open the PTY FD inherited from parent (passed as ExtraFiles[0] = FD 3)
	ptyFile := os.NewFile(uintptr(ptyFD), "pty")
	if ptyFile == nil {
		fmt.Fprintf(os.Stderr, "Error: Invalid PTY FD %d\n", ptyFD)
		os.Exit(1)
	}
	defer ptyFile.Close()

	rm.mu.Lock()
	rm.relayPid = os.Getpid()
	rm.mu.Unlock()

	// Run relay loop synchronously (blocking)
	rm.runSubprocessRelay(ptyFile)
}

// runSubprocessRelay runs the relay loop in the current goroutine.
// Matches Python's process.py main() exactly in behavior:
//   - select loop with 0.1s timeout
//   - PTY → log file + client socket
//   - Client socket → PTY
//   - Detach = client disconnect (process stays running for reconnection)
//   - Clean shutdown on context cancellation
func (rm *RelayManager) runSubprocessRelay(ptyFile *os.File) {
	// ── Signal handling (matches Python's _setup_signal_handlers) ──
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// ── Open log file (matches Python's with open(log_file, "ab")) ──
	logFile, err := os.OpenFile(rm.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// ── Set up Unix socket (matches Python's socket + bind + listen) ──
	os.Remove(rm.socketPath)
	listener, err := net.Listen("unix", rm.socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listening on socket %s: %v\n", rm.socketPath, err)
		os.Exit(1)
	}
	defer listener.Close()

	// ── Ensure socket/PID file cleanup on exit (matches Python's finally block) ──
	cleanup := func() {
		os.Remove(rm.socketPath)
		os.Remove(rm.pidPath)
	}
	defer cleanup()

	// ── Goroutine: read PTY → channel (matches Python's select on pty_fd) ──
	// Using buffered channel to decouple PTY reads from client writes,
	// matching Python's select-based non-blocking approach.
	type ptyRead struct {
		data []byte
		err  error
	}
	ptyCh := make(chan ptyRead, 256)
	go func() {
		buf := make([]byte, consoleReadBufferSize)
		for {
			n, err := ptyFile.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
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

	// ── Main relay loop (matches Python's while + select) ──
	// Python: while not _shutdown_state["requested"]: select([pty_fd, server_sock, client_sock], timeout=0.1)
	// Go equivalent: select on ctx.Done(), ptyCh, acceptCh, client read
	var clientConn net.Conn

	for {
		// Check shutdown before blocking operations (matches Python's while-check)
		select {
		case <-ctx.Done():
			return
		default:
		}

		if clientConn == nil {
			// No client — try non-blocking accept (matches Python's _accept_client)
			// Python uses setblocking(False) + accept; Go uses SetDeadline on listener
			listener.(*net.UnixListener).SetDeadline(time.Now().Add(
				time.Duration(consoleSelectTimeoutS * float64(time.Second)),
			))
			conn, err := listener.Accept()
			if err == nil {
				clientConn = conn
				// Notify parent that socket is ready (matches Python — socket exists when Listen succeeds)
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Timeout — no client yet, continue loop to check shutdown
			} else {
				// Actual error — log and continue
				slog.Debug("Accept error", "error", err)
			}
		}

		if clientConn != nil {
			// Set read deadline for select-like timeout behavior
			// (matches Python's select with 0.1s timeout on client_sock)
			clientConn.SetReadDeadline(time.Now().Add(
				time.Duration(consoleSelectTimeoutS * float64(time.Second)),
			))
			buf := make([]byte, consoleReadBufferSize)
			n, err := clientConn.Read(buf)

			if n > 0 {
				// Forward client input to PTY (matches Python's _forward_to_pty)
				ptyFile.Write(buf[:n]) //nolint:errcheck
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout — normal, continue loop (matches Python's select timeout)
				} else {
					// Client disconnected or error — close and wait for next (matches Python)
					clientConn.Close()
					clientConn = nil
				}
			}
		}

		// Read from PTY channel (non-blocking, matches Python's select on pty_fd)
		select {
		case pr := <-ptyCh:
			if pr.err != nil {
				// PTY closed — matches Python's: if not data: _shutdown_state = True; break
				return
			}
			// Write to log file with flush (matches Python's _write_to_log with f.flush())
			if _, err := logFile.Write(pr.data); err == nil {
				logFile.Sync() // Python's f.flush()
			}
			// Forward to connected client (matches Python's _forward_to_client)
			if clientConn != nil {
				if _, err := clientConn.Write(pr.data); err != nil {
					// Connection broken — close client (matches Python's close + set None)
					clientConn.Close()
					clientConn = nil
				}
			}
		default:
			// No PTY data available — matches Python's select timeout where pty_fd not ready
		}
	}
}
