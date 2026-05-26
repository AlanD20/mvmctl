package system

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// IsProcessRunning checks if a process with the given PID is currently running.
// Matches Python's: os.kill(pid, 0) → True if no error.
// Returns false for pid <= 0 or any error (including process not found).
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// SetupSignalHandler sets up a goroutine that calls cancel() on SIGINT/SIGTERM.
// Matches Python's signal.signal(signal.SIGTERM, lambda: cancel()) pattern.
func SetupSignalHandler(cancel func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		slog.Warn("Received shutdown signal")
		cancel()
	}()
}
