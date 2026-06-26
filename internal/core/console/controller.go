// Package console provides VM serial console output streaming.
// Layer: Core domain — never imports other core/* packages.
package console

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	consolesvc "mvmctl/internal/service/console"
)

// Controller manages the console lifecycle for a single VM.
//
// The Controller owns:
// - PTY pair creation/teardown (master → relay, slave → Firecracker)
// - Relay subprocess spawning/management (Start, Stop, IsRunning, GetPID)
// - Relay client connection (Connect, Disconnect)
//
// The API layer creates a Controller and calls these methods — it never
// touches the service package directly.
//
// A Controller is not safe for concurrent use. The API layer serializes
// calls per-VM through its own execution model.
type Controller struct {
	vmID, vmPath, vmName        string
	pidFilename, socketFilename string

	// PTY state
	masterFD int // PTY master → relay subprocess
	clientFD int // PTY slave → Firecracker
	hasPTY   bool

	// Relay (set by Start, used by Stop/IsRunning/PID/SocketPath/Connect)
	relayManager *consolesvc.Relay

	// Client (set by Connect)
	client *consolesvc.RelayClient
}

// NewController creates a new Controller for the given VM.
func NewController(vmID, vmPath, vmName, pidFilename, socketFilename string) *Controller {
	if vmName == "" {
		vmName = vmID
	}
	return &Controller{
		vmID:           vmID,
		vmPath:         vmPath,
		vmName:         vmName,
		pidFilename:    pidFilename,
		socketFilename: socketFilename,
	}
}

// CreatePTY creates a PTY pair and returns the slave/client FD.
func (cc *Controller) CreatePTY() (int, error) {
	if cc.hasPTY {
		return cc.clientFD, nil
	}

	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("failed to open /dev/ptmx: %w", err)
	}

	masterFD := int(ptmx.Fd())
	ptyno, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		ptmx.Close()
		return 0, fmt.Errorf("failed to get PTY slave number: %w", err)
	}
	slaveName := fmt.Sprintf("/dev/pts/%d", ptyno)

	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		ptmx.Close()
		return 0, fmt.Errorf("failed to unlock PTY: %w", err)
	}

	slave, err := os.OpenFile(slaveName, os.O_RDWR, 0)
	if err != nil {
		ptmx.Close()
		return 0, fmt.Errorf("failed to open slave PTY %s: %w", slaveName, err)
	}

	cc.masterFD = masterFD
	cc.clientFD = int(slave.Fd())
	cc.hasPTY = true

	if cc.clientFD == 0 {
		ptmx.Close()
		slave.Close()
		cc.masterFD = 0
		cc.hasPTY = false
		return 0, errors.New("PTY allocation failed: client FD is None after creation")
	}

	return cc.clientFD, nil
}

// Start starts the console relay for this controller's VM.
func (cc *Controller) Start(ctx context.Context) (string, *int, error) {
	if !cc.hasPTY {
		return "", nil, errors.New("Must call create_pty() before start()")
	}

	ptyFile := os.NewFile(uintptr(cc.masterFD), "pty")
	if ptyFile == nil {
		return "", nil, fmt.Errorf("invalid PTY controller FD %d", cc.masterFD)
	}

	cfg := consolesvc.Config{
		VMID:           cc.vmID,
		VMPath:         cc.vmPath,
		VMName:         cc.vmName,
		PIDFilename:    cc.pidFilename,
		SocketFilename: cc.socketFilename,
	}

	result, err := consolesvc.Spawn(ctx, cfg, ptyFile)
	if err != nil {
		return "", nil, err
	}

	// Spawn closed ptyFile (parent's copy of the PTY master fd). Clear
	// masterFD to prevent ClosePTY/Cleanup from double-closing.
	cc.masterFD = 0

	// Create the relay manager now that the subprocess is alive.
	// Spawn already wrote the PID file, so the Relay can find the PID.
	pidPath := filepath.Join(cc.vmPath, cc.pidFilename)
	sockPath := filepath.Join(cc.vmPath, cc.socketFilename)
	cc.relayManager = consolesvc.NewRelay(cc.vmName, pidPath, sockPath)

	return result.SocketPath, &result.PID, nil
}

// SocketPath returns the relay socket path (set after Start).
func (cc *Controller) SocketPath() string {
	if cc.relayManager == nil {
		return ""
	}
	return cc.relayManager.SocketPath()
}

// Connect connects to the console relay and returns a RelayClient.
func (cc *Controller) Connect() (*consolesvc.RelayClient, error) {
	if cc.client != nil && cc.client.IsConnected() {
		return cc.client, nil
	}

	if cc.relayManager == nil {
		return nil, errors.New("relay not started: call Start() before Connect()")
	}

	socketPath := cc.relayManager.SocketPath()
	client := consolesvc.NewRelayClient(socketPath, nil)
	if err := client.Connect(); err != nil {
		return nil, err
	}

	cc.client = client
	return client, nil
}

// Disconnect disconnects from the console relay.
func (cc *Controller) Disconnect() error {
	if cc.client != nil {
		cc.client.Disconnect()
		cc.client = nil
	}
	return nil
}

// Stop stops the console relay.
func (cc *Controller) Stop(force bool) bool {
	if cc.relayManager == nil {
		return false
	}
	return cc.relayManager.Stop(force)
}

// IsRunning returns true if the relay is currently running.
func (cc *Controller) IsRunning() bool {
	if cc.relayManager == nil {
		return false
	}
	return cc.relayManager.IsRunning()
}

// GetPID returns the relay PID and whether it's running.
func (cc *Controller) GetPID() (int, bool) {
	if cc.relayManager == nil {
		return 0, false
	}
	return cc.relayManager.PIDAlive()
}

// CloseClientFD closes the PTY slave/client file descriptor.
func (cc *Controller) CloseClientFD() {
	if cc.clientFD != 0 {
		_ = unix.Close(cc.clientFD)
		cc.clientFD = 0
	}
}

// ClosePTY closes both PTY file descriptors.
func (cc *Controller) ClosePTY() {
	if cc.clientFD != 0 {
		_ = unix.Close(cc.clientFD)
		cc.clientFD = 0
	}
	if cc.masterFD != 0 {
		_ = unix.Close(cc.masterFD)
		cc.masterFD = 0
	}
	cc.hasPTY = false
}

// Cleanup stops the relay gracefully and closes PTY FDs.
func (cc *Controller) Cleanup() {
	if cc.relayManager != nil {
		cc.relayManager.Stop(false)
	}

	if cc.clientFD != 0 {
		_ = unix.Close(cc.clientFD)
		cc.clientFD = 0
	}
	if cc.masterFD != 0 {
		_ = unix.Close(cc.masterFD)
		cc.masterFD = 0
	}
	cc.hasPTY = false
}
