package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Agent manages the vsock listener and dispatches incoming connections.
type Agent struct {
	port         int
	token        string
	localSocket  string
	activeConn   net.Conn
	activeConnMu sync.Mutex
	connMu       sync.Mutex // serializes writes to vsock conn between streamingWriter and handleLocalConn
	readMu       sync.Mutex // serializes reads from vsock conn between handleConnection and handleLocalConn
}

// New creates a new Agent with the given configuration.
// localSocket is the path to the local Unix socket for in-VM IPC
// (e.g., /var/run/mvm-agent.sock). If empty, defaults to
// /var/run/mvm-agent.sock.
func New(port int, token string, localSocket string) *Agent {
	if localSocket == "" {
		localSocket = "/var/run/mvm-agent.sock"
	}
	return &Agent{
		port:        port,
		token:       token,
		localSocket: localSocket,
	}
}

// Run starts the vsock listener and local UDS listener, accepting connections
// until ctx is cancelled. This is a blocking call. It returns nil on clean
// shutdown.
func (a *Agent) Run(ctx context.Context) error {
	vsockListener, err := listenVsock(a.port)
	if err != nil {
		return fmt.Errorf("cannot listen on vsock port %d: %w", a.port, err)
	}

	// Clean up stale local socket file before listening.
	if err := os.Remove(a.localSocket); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove stale local socket", "path", a.localSocket, "error", err)
	}

	localListener, err := net.Listen("unix", a.localSocket)
	if err != nil {
		vsockListener.Close()
		return fmt.Errorf("cannot listen on local socket %s: %w", a.localSocket, err)
	}

	slog.Info("guest agent started", "port", a.port, "local_socket", a.localSocket)

	// Close listeners when context is cancelled so Accept() unblocks.
	go func() {
		<-ctx.Done()
		slog.Info("shutting down listeners")
		vsockListener.Close()
		localListener.Close()
	}()

	var wg sync.WaitGroup

	// Vsock acceptor goroutine.
	wg.Go(func() {
		for {
			conn, err := vsockListener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("vsock accept failed", "error", err)
				time.Sleep(time.Second)
				continue
			}

			wg.Go(func() {
				a.handleConnection(ctx, conn)
			})
		}
	})

	// Local UDS acceptor goroutine.
	wg.Go(func() {
		for {
			conn, err := localListener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("local accept failed", "error", err)
				time.Sleep(time.Second)
				continue
			}

			wg.Go(func() {
				a.handleLocalConn(conn)
			})
		}
	})

	// Block until context is cancelled, then wait for all goroutines.
	<-ctx.Done()
	slog.Info("shutting down agent")
	wg.Wait()
	return nil
}

// --- vsock types ---

// vsockListener implements net.Listener for AF_VSOCK sockets (Firecracker vsock).
type vsockListener struct {
	fd   int
	port int
}

// listenVsock creates and binds a vsock listener on the given port.
func listenVsock(port int) (*vsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("socket(AF_VSOCK): %w", err)
	}

	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("setsockopt(SO_REUSEADDR): %w", err)
	}

	addr := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: uint32(port),
	}
	if err := unix.Bind(fd, addr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind(port=%d): %w", port, err)
	}

	if err := unix.Listen(fd, 10); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("listen(port=%d): %w", port, err)
	}

	return &vsockListener{fd: fd, port: port}, nil
}

// Accept accepts an incoming vsock connection.
func (l *vsockListener) Accept() (net.Conn, error) {
	connFd, sa, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}

	var remoteCID, remotePort uint32
	if vmAddr, ok := sa.(*unix.SockaddrVM); ok {
		remoteCID = vmAddr.CID
		remotePort = vmAddr.Port
	}

	return newVSockConn(connFd, remoteCID, remotePort), nil
}

// Close closes the vsock listener.
func (l *vsockListener) Close() error {
	return unix.Close(l.fd)
}

// Addr returns the listener's network address.
func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{
		net:  "vsock",
		cid:  unix.VMADDR_CID_ANY,
		port: l.port,
	}
}

// vsockConn implements net.Conn for an accepted vsock stream.
type vsockConn struct {
	fd         int
	file       *os.File
	remoteCID  uint32
	remotePort uint32
}

func newVSockConn(fd int, remoteCID, remotePort uint32) *vsockConn {
	return &vsockConn{
		fd:         fd,
		file:       os.NewFile(uintptr(fd), "vsock"),
		remoteCID:  remoteCID,
		remotePort: remotePort,
	}
}

func (c *vsockConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error) { return c.file.Write(b) }

// Close performs a graceful vsock shutdown before closing the fd.
// A raw close(fd) on virtio-vsock may not reliably transmit the
// VIRTIO_VSOCK_OP_SHUTDOWN packet to the Firecracker proxy, leaving
// the host-side UDS connection open and causing the host's conn.Read()
// to block indefinitely. shutdown(SHUT_RDWR) ensures the shutdown
// packet is sent before the socket is freed.
func (c *vsockConn) Close() error {
	_ = unix.Shutdown(c.fd, unix.SHUT_RDWR) // best-effort: ensure clean SHUTDOWN
	return c.file.Close()
}

func (c *vsockConn) LocalAddr() net.Addr {
	return &vsockAddr{net: "vsock", cid: unix.VMADDR_CID_ANY, port: 0}
}

func (c *vsockConn) RemoteAddr() net.Addr {
	return &vsockAddr{net: "vsock", cid: c.remoteCID, port: int(c.remotePort)}
}

// SetDeadline is not supported for vsock connections.
func (c *vsockConn) SetDeadline(t time.Time) error { return nil }

// SetReadDeadline is not supported for vsock connections.
func (c *vsockConn) SetReadDeadline(t time.Time) error { return nil }

// SetWriteDeadline is not supported for vsock connections.
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return nil }

// vsockAddr implements net.Addr for vsock addresses.
type vsockAddr struct {
	net  string
	cid  uint32
	port int
}

func (a *vsockAddr) Network() string { return a.net }
func (a *vsockAddr) String() string  { return fmt.Sprintf("cid=%d,port=%d", a.cid, a.port) }

// VMADDR_CID_ANY (0xffffffff) accepts connections from any guest CID.
// The unix package constant is the canonical value.
