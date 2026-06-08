package model

// ── ConsoleRelay (interface) ──

// ConsoleRelay is the interface for console relay operations.
// Matches the public API of Python's ConsoleRelayManager (now console.Relay).
type ConsoleRelay interface {
	IsRunning() bool
	PID() (int, bool)
	SocketPath() string
	Stop(force bool) bool
}

// ── ConsoleConnectionInfo ──

// ConsoleConnectionInfo matches Python's ConsoleConnectionInfo dataclass.
type ConsoleConnectionInfo struct {
	SocketPath string `json:"socket_path"`
	VMName     string `json:"vm_name"`
	VMID       string `json:"vm_id"`
}
