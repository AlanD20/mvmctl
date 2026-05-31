package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// relayPID stores the OS PID of the current process when running as a
// goroutine-based relay. Since Go uses goroutines (not subprocesses),
// all relays share the mvm process's PID. This PID is used for liveness
// checks (via os.FindProcess/kill(0)) in CleanupOrphans.
var relayPID = os.Getpid()

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
	listener   net.Listener
	cancel     context.CancelFunc
	relayPid   int
	// ptyFD is the PTY controller file descriptor used by relayLoop.
	// Stored so Stop() can close it to force-unblock a stuck read goroutine
	// (SIGKILL equivalent, matching Python's _send_signal(pid, signal.SIGKILL)).
	ptyFD int
	// doneCh is closed when the relay goroutine fully exits.
	// Used by Stop() to poll for goroutine completion (matching Python's os.kill(pid, 0) liveness check).
	doneCh chan struct{}
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

// Start begins the console relay goroutine with the given PTY controller FD.
// Uses the caller's context so SIGINT/SIGTERM propagate properly.
// Returns (socketPath, pid, error). Matches Python's ConsoleRelayManager.start().
func (rm *RelayManager) Start(ctx context.Context, ptyControllerFD int) (string, int, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.cancel != nil {
		return "", 0, ErrAlreadyRunning(rm.id)
	}
	if err := os.MkdirAll(rm.path, 0755); err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	rm.cancel = cancel
	// Store PTY FD for potential SIGKILL-equivalent force-close in Stop()
	rm.ptyFD = ptyControllerFD
	// Set PID to the real OS PID of the current process. Since Go uses
	// goroutines (not subprocesses), all relays share the mvm process PID.
	// This ensures CleanupOrphans can correctly verify liveness using
	// syscall.Kill(pid, 0) — the mvm process is alive so orphans are skipped.
	rm.relayPid = relayPID
	// Write PID file in Start() rather than relayLoop() to avoid racing
	// on rm.relayPid access (relayLoop runs in a separate goroutine).
	// Matches Python's process.py _write_pid_file() which runs before the select loop.
	if err := os.MkdirAll(filepath.Dir(rm.pidPath), 0755); err == nil {
		_ = os.WriteFile(rm.pidPath, []byte(strconv.Itoa(relayPID)), 0644)
	}
	// doneCh allows Stop() to poll for goroutine completion, matching
	// Python's os.kill(pid, 0) liveness check in the graceful stop timeout loop.
	rm.doneCh = make(chan struct{})
	ready := make(chan error, 1)
	go rm.relayLoop(ctx, ptyControllerFD, ready, rm.doneCh)
	// Wait for socket to be ready or for error
	err := <-ready
	if err != nil {
		cancel()
		rm.cancel = nil
		rm.relayPid = 0
		return "", 0, ErrProcessFailed(rm.id, err)
	}
	return rm.socketPath, rm.relayPid, nil
}

// relayLoop is the main goroutine implementing the relay logic.
// Matches Python's process.py main() — reads from PTY, writes to log file,
// listens on Unix socket, forwards bidirectionally between PTY and connected client.
func (rm *RelayManager) relayLoop(ctx context.Context, ptyFD int, ready chan<- error, doneCh chan struct{}) {
	// doneCh must always be closed on exit so Stop() can detect completion.
	defer close(doneCh)
	// Create log file
	logFile, err := os.OpenFile(rm.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		ready <- err
		return
	}
	defer logFile.Close()
	// Create PTY file handle
	ptyFile := os.NewFile(uintptr(ptyFD), "pty")
	if ptyFile == nil {
		ready <- fmt.Errorf("failed to open PTY FD %d", ptyFD)
		return
	}
	defer ptyFile.Close()
	// Remove old socket if present, then create new one
	os.Remove(rm.socketPath)
	listener, err := net.Listen("unix", rm.socketPath)
	if err != nil {
		ready <- err
		return
	}
	// Set the listener for Stop() to close
	rm.mu.Lock()
	rm.listener = listener
	rm.mu.Unlock()
	// Cleanup on exit — reset all shared state so Stop() can detect completion.
	defer func() {
		listener.Close()
		os.Remove(rm.socketPath)
		os.Remove(rm.pidPath)
		rm.mu.Lock()
		rm.listener = nil
		rm.cancel = nil
		rm.relayPid = 0
		rm.mu.Unlock()
	}()
	// Signal that we're ready
	ready <- nil
	// Channel for PTY reads
	ptyCh := make(chan []byte, 32)
	// Goroutine: read from PTY, send to ptyCh
	go func() {
		defer close(ptyCh)
		buf := make([]byte, consoleReadBufferSize)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := ptyFile.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case ptyCh <- data:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// Goroutine: accept connections
	acceptCh := make(chan net.Conn)
	go func() {
		defer close(acceptCh)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case acceptCh <- conn:
			case <-ctx.Done():
				conn.Close()
				return
			}
		}
	}()
	// Watcher: close listener when context is cancelled, so accept goroutine returns
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	// ── Main relay loop ──
	// Matches Python's process.py main select loop exactly.
	var client net.Conn
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-ptyCh:
			if !ok {
				return
			}
			// Write to log file (matches process.py _write_to_log)
			if _, err := logFile.Write(data); err == nil {
				logFile.Sync()
			}
			// Forward to connected client (matches process.py _forward_to_client)
			if client != nil {
				if _, err := client.Write(data); err != nil {
					// Connection broken — close client (matches process.py line 169-177)
					client.Close()
					client = nil
				}
			}
		case newConn, ok := <-acceptCh:
			if !ok {
				return
			}
			// Only accept one client at a time (matching backlog=1)
			if client != nil {
				client.Close()
			}
			client = newConn
			// Start client read goroutine — reads from client, forwards to PTY
			// (matches process.py _read_from_client + _forward_to_pty)
			go func(conn net.Conn) {
				buf := make([]byte, consoleReadBufferSize)
				for {
					if err := conn.SetReadDeadline(time.Now().Add(
						time.Duration(consoleSelectTimeoutS * float64(time.Second)),
					)); err != nil {
						return
					}
					n, err := conn.Read(buf)
					if n > 0 {
						// Forward to PTY (matches process.py _forward_to_pty)
						ptyFile.Write(buf[:n]) //nolint:errcheck
					}
					if err != nil {
						if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
							continue
						}
						return
					}
				}
			}(client)
		case <-time.After(time.Duration(consoleSelectTimeoutS * float64(time.Second))):
			// Periodic timeout to keep select looping (matches Python's select timeout)
		}
	}
}

// Stop stops the relay and cleans up.
// force=true: immediate stop (matches Python's force=True: SIGTERM + cleanup_files + _pid = None).
// force=false: graceful stop with kill escalation timeout (matches Python's graceful stop).
// Python graceful flow:
//  1. Send SIGTERM
//  2. If SIGTERM fails (process dead) → cleanup, return
//  3. Loop CONST_CONSOLE_KILL_TIMEOUT_S * 10 times, sleep 0.1s each, signal 0 to check liveness
//  4. If loop exhausted → send SIGKILL
//  5. Cleanup files, reset _pid
//  6. Log "Terminated console relay for %s" on both paths
func (rm *RelayManager) Stop(force bool) bool {
	rm.mu.Lock()
	cancel := rm.cancel
	listener := rm.listener
	doneCh := rm.doneCh
	if cancel == nil {
		rm.mu.Unlock()
		return false
	}
	rm.mu.Unlock()
	if force {
		// Abrupt: cancel context (SIGTERM equivalent), clean up immediately.
		// Matches Python's force=True:
		//   self._send_signal(pid, signal.SIGTERM)
		//   self._cleanup_files()
		//   self._pid = None
		//   logger.info("Terminated console relay for %s", self._name)
		//   return True
		cancel()
		if listener != nil {
			listener.Close()
		}
		rm.mu.Lock()
		rm.cleanupFiles()
		rm.cancel = nil
		rm.listener = nil
		rm.relayPid = 0
		rm.mu.Unlock()
		slog.Info("Terminated console relay", "name", rm.name)
		return true
	}
	// Graceful: cancel context (SIGTERM equivalent), then poll for completion
	// Matches Python's graceful stop pattern:
	//   if not self._send_signal(pid, signal.SIGTERM): → cleanup, return True
	cancel()
	if listener != nil {
		listener.Close()
	}
	// Equivalent to Python's: for _ in range(int(CONST_CONSOLE_KILL_TIMEOUT_S * 10)):
	//   time.sleep(0.1)
	//   if not self._send_signal(pid, 0): break
	// else: self._send_signal(pid, signal.SIGKILL)
	//
	// doneCh is closed by relayLoop's deferred cleanup, so we poll it
	// instead of checking rm.cancel (which Go's goroutine resets to nil).
	if doneCh != nil {
		stillAlive := true
		for i := 0; i < int(consoleKillTimeoutS*10); i++ {
			time.Sleep(100 * time.Millisecond)
			select {
			case <-doneCh:
				stillAlive = false
			default:
			}
			if !stillAlive {
				break
			}
		}
		// SIGKILL equivalent: goroutine might still be alive after timeout.
		// Force-close the PTY FD to unblock any stuck read goroutine,
		// matching Python's _send_signal(pid, signal.SIGKILL).
		if stillAlive {
			if rm.ptyFD > 0 {
				syscall.Close(rm.ptyFD)
			}
			// Wait briefly for goroutine to exit after force-close.
			if doneCh != nil {
				select {
				case <-doneCh:
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
	}
	rm.mu.Lock()
	rm.cleanupFiles()
	rm.cancel = nil
	rm.listener = nil
	rm.relayPid = 0
	rm.mu.Unlock()
	slog.Info("Terminated console relay", "name", rm.name)
	return true
}
func (rm *RelayManager) cleanupFiles() {
	os.Remove(rm.pidPath)
	os.Remove(rm.socketPath)
}

// GetPID returns the PID of the running relay, verifying liveness via doneCh.
// Matches Python's ConsoleRelayManager.get_pid() which uses os.kill(pid, 0)
// to confirm the process is alive before returning the PID.
func (rm *RelayManager) GetPID() *int {
	rm.mu.Lock()
	if rm.cancel == nil && rm.relayPid <= 0 {
		rm.mu.Unlock()
		// No in-memory PID — try PID file (matching Python fallback)
		return rm.readPIDFromFile()
	}
	pid := rm.relayPid
	doneCh := rm.doneCh
	cancel := rm.cancel
	rm.mu.Unlock()
	// Verify liveness: doneCh is closed when relayLoop fully exits.
	// This is the Go equivalent of Python's os.kill(pid, 0) — if the
	// goroutine has exited, doneCh is closed and we return nil.
	if cancel != nil && doneCh != nil {
		select {
		case <-doneCh:
			return nil // goroutine has exited
		default:
			return &pid // goroutine is still alive
		}
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

	for i := 0; i < len(args); i++ {
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
	if err := os.MkdirAll(filepath.Dir(rm.pidPath), 0755); err == nil {
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
// This is the subprocess version — blocking, no context cancellation.
func (rm *RelayManager) runSubprocessRelay(ptyFile *os.File) {
	// Create log file
	logFile, err := os.OpenFile(rm.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// Wait for client connection on Unix socket
	listener, err := net.Listen("unix", rm.socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listening on socket %s: %v\n", rm.socketPath, err)
		os.Exit(1)
	}
	defer listener.Close()

	conn, err := listener.Accept()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error accepting connection: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Notify parent that socket is ready
	fmt.Fprintf(os.Stdout, "ready\n")

	// Relay loop: PTY → log + socket, socket → PTY
	// Matches internal/relayLoop but for subprocess
	relayLoopImpl(ptyFile, logFile, conn, conn)
}

// relayLoopImpl implements the core PTY relay logic.
// Reads from PTY, writes to log and client. Reads from client, writes to PTY.
func relayLoopImpl(pty io.ReadWriteCloser, logFile io.Writer, client io.ReadWriteCloser, clientReader io.Reader) {
	// PTY → client + log (goroutine)
	ptyCh := make(chan []byte, 256)
	go func() {
		defer close(ptyCh)
		buf := make([]byte, 4096)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				ptyCh <- data
			}
			if err != nil {
				return
			}
		}
	}()

	// Client → PTY (main loop)
	inputBuf := make([]byte, 4096)
	for {
		select {
		case data, ok := <-ptyCh:
			if !ok {
				return
			}
			logFile.Write(data)
			client.Write(data)
		default:
			n, err := clientReader.Read(inputBuf)
			if n > 0 {
				data := inputBuf[:n]
				// Check for detach sequence Ctrl+X d
				if len(data) == 2 && data[0] == 0x18 && data[1] == 'd' {
					return
				}
				pty.Write(data)
			}
			if err != nil {
				return
			}
		}
	}
}
