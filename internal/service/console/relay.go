package console

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// --- Relay ---
type Relay struct {
	mu         sync.Mutex // guards relayPid and PID-file state
	name       string // used for logging
	pidPath    string // path to PID file (written by spawn.go)
	socketPath string // path to Unix socket (created by subprocess)
	relayPid   int    // in-memory PID, populated from file or spawn
}

// NewRelay creates a new console relay manager.
// Caller must provide resolved pidPath and socketPath.
func NewRelay(vmName, pidPath, socketPath string) *Relay {
	return &Relay{
		name:       vmName,
		pidPath:    pidPath,
		socketPath: socketPath,
	}
}

// PID returns the relay process PID and whether it's known.
// Does NOT verify liveness — use PIDAlive for that.
func (rm *Relay) PID() (int, bool) {
	rm.mu.Lock()
	pid := rm.relayPid
	rm.mu.Unlock()
	if pid > 0 {
		return pid, true
	}
	return rm.readPIDFromFile()
}

// PIDAlive returns the relay process PID if known and the process is alive.
func (rm *Relay) PIDAlive() (int, bool) {
	pid, ok := rm.PID()
	if !ok {
		return 0, false
	}
	if syscall.Kill(pid, 0) == nil {
		return pid, true
	}
	return 0, false
}

// SocketPath returns the relay's socket path.
func (rm *Relay) SocketPath() string { return rm.socketPath }

// Stop stops the relay and cleans up.
// force=true: immediate SIGKILL.
// force=false: SIGTERM then poll for up to 2s, escalate to SIGKILL if still alive.
func (rm *Relay) Stop(force bool) bool {
	pid := rm.resolvePID()
	if pid <= 0 {
		return false
	}

	// Single cleanup path — runs regardless of how the process exits.
	defer func() {
		rm.mu.Lock()
		rm.cleanupFiles()
		rm.relayPid = 0
		rm.mu.Unlock()
		slog.Debug("Terminated console relay", "name", rm.name)
	}()

	if force {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			slog.Debug("SIGKILL failed", "pid", pid, "error", err)
		}
		return true
	}

	// Graceful: SIGTERM → poll → escalate
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		slog.Debug("SIGTERM failed", "pid", pid, "error", err)
		return true
	}

	deadline := time.After(2 * time.Second)
	for {
		if syscall.Kill(pid, 0) != nil {
			return true // process exited
		}
		select {
		case <-deadline:
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				slog.Debug("SIGKILL escalation failed", "pid", pid, "error", err)
			}
			return true
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (rm *Relay) cleanupFiles() {
	if err := os.Remove(rm.pidPath); err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to remove PID file", "path", rm.pidPath, "error", err)
	}
	if err := os.Remove(rm.socketPath); err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to remove socket file", "path", rm.socketPath, "error", err)
	}
}

// resolvePID returns the PID from memory or PID file. Not safe for concurrent calls.
func (rm *Relay) resolvePID() int {
	rm.mu.Lock()
	pid := rm.relayPid
	rm.mu.Unlock()
	if pid > 0 {
		return pid
	}
	if p, ok := rm.readPIDFromFile(); ok {
		rm.mu.Lock()
		rm.relayPid = p
		rm.mu.Unlock()
		return p
	}
	return 0
}

// readPIDFromFile reads the PID from the PID file without liveness verification.
func (rm *Relay) readPIDFromFile() (int, bool) {
	data, err := os.ReadFile(rm.pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// IsRunning checks if the relay is currently running.
func (rm *Relay) IsRunning() bool {
	_, ok := rm.PIDAlive()
	return ok
}
