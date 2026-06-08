package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"mvmctl/pkg/errs"

	"golang.org/x/term"
)

const (
	consoleSocketTimeout = 2 * time.Second // CONST_CONSOLE_SOCKET_TIMEOUT_S
	consolePollIntervalS = 0.05            // polling interval used by CLI _interact
)

// defaultDetachSequence is the default byte sequence that triggers detach:
// Ctrl+X (0x18) followed by 'd' (0x64).
// Matches Python's CONST_CONSOLE_DETACH_SEQUENCE = b"\x18d".
var defaultDetachSequence = []byte{0x18, 'd'}

// DefaultDetachSequence returns a copy of the default detach sequence.
func DefaultDetachSequence() []byte {
	seq := make([]byte, len(defaultDetachSequence))
	copy(seq, defaultDetachSequence)
	return seq
}

// ── RelayClient connects to a console relay Unix socket ─────────────────
// Matches Python's ConsoleRelayClient exactly.
// RelayClient provides a high-level client for bidirectional console
// communication with detach keybind support.
type RelayClient struct {
	mu         sync.Mutex
	socketPath string
	detachSeq  []byte
	conn       net.Conn
}

// NewRelayClient creates a console relay client.
// Matches Python's ConsoleRelayClient.__init__().
func NewRelayClient(socketPath string, detachSequence []byte) *RelayClient {
	if len(detachSequence) == 0 {
		detachSequence = DefaultDetachSequence()
	}
	seq := make([]byte, len(detachSequence))
	copy(seq, detachSequence)
	return &RelayClient{
		socketPath: socketPath,
		detachSeq:  seq,
	}
}

// Connect connects to the console relay socket.
// Matches Python's ConsoleRelayClient.connect() exactly:
//
//	Python creates a socket, connects with timeout, then calls setblocking(False).
//	Go does the same via net.DialTimeout.
func (c *RelayClient) Connect() error {
	conn, err := net.DialTimeout("unix", c.socketPath, consoleSocketTimeout)
	if err != nil {
		return errs.WrapMsg(
			errs.CodeConsoleRelayFailed,
			fmt.Sprintf("Failed to connect to console relay at %s: %s", c.socketPath, err),
			err,
		)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

// IsConnected checks if client is currently connected.
// Matches Python's ConsoleRelayClient.is_connected().
func (c *RelayClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Disconnect disconnects from the relay socket.
// Matches Python's ConsoleRelayClient.disconnect().
func (c *RelayClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
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
func (c *RelayClient) Send(data []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	if len(data) == 0 {
		return nil
	}
	_, err := conn.Write(data)
	return err
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
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	ch := make(chan []byte, 16)
	go func() {
		defer close(ch)
		if conn == nil {
			return
		}
		if bufferSize <= 0 {
			bufferSize = 4096
		}
		buf := make([]byte, bufferSize)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Set read deadline for poll-like timeout behavior
			if err := conn.SetReadDeadline(time.Now().Add(
				time.Duration(consolePollIntervalS * float64(time.Second)),
			)); err != nil {
				return
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
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout — no data available, retry.
					continue
				}
				// Connection reset or closed
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

// DetachSequence returns a copy of the detach sequence. Matches Python's property.
func (c *RelayClient) DetachSequence() []byte {
	seq := make([]byte, len(c.detachSeq))
	copy(seq, c.detachSeq)
	return seq
}

// SocketPath returns the socket path. Matches Python's property.
func (c *RelayClient) SocketPath() string {
	return c.socketPath
}

// GetSocket returns the underlying connected socket for advanced use.
// Matches Python's ConsoleRelayClient.get_socket() which raises
// RuntimeError("Not connected - call connect() first") when not connected.
func (c *RelayClient) GetSocket() (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("not connected - call Connect() first")
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

	sock, err := client.GetSocket()
	if err != nil {
		return err
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
		defer close(socketCh)
		buf := make([]byte, 4096)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := sock.SetReadDeadline(time.Now().Add(
				time.Duration(consolePollIntervalS * float64(time.Second)),
			)); err != nil {
				socketErrCh <- err
				return
			}
			n, err := sock.Read(buf)
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
				return nil
			}
			if _, err := stdout.Write(data); err != nil {
				return err
			}
		case err := <-socketErrCh:
			return fmt.Errorf("console relay: %w", err)
		case b := <-stdinCh:
			// Append byte to input buffer (matches Python: input_buffer.extend(char))
			inputBuf = append(inputBuf, b)
			// ── Detach check: bytes(input_buffer[-2:]) == b"\x18d" ──
			if len(inputBuf) >= 2 {
				if inputBuf[len(inputBuf)-2] == 0x18 && inputBuf[len(inputBuf)-1] == 'd' {
					// Send any remaining data before the detach sequence
					if len(inputBuf) > 2 {
						if err := client.Send(inputBuf[:len(inputBuf)-2]); err != nil {
							return fmt.Errorf("console relay: %w", err)
						}
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
				if err := client.Send(inputBuf); err != nil {
					return fmt.Errorf("console relay: %w", err)
				}
				inputBuf = inputBuf[:0]
			} else if len(inputBuf) >= 2 {
				// First byte is Ctrl+X, we have 2+ bytes
				// Check: if bytes(input_buffer) != b"\x18d":
				if !(inputBuf[0] == 0x18 && inputBuf[1] == 'd') {
					// Not the detach sequence — send all and clear
					if err := client.Send(inputBuf); err != nil {
						return fmt.Errorf("console relay: %w", err)
					}
					inputBuf = inputBuf[:0]
				}
				// Otherwise (it IS the detach sequence), the earlier check caught it
			}
			// else: first byte is 0x18, buffer < 2 bytes — wait for more
		}
	}
}
