package system

import (
	"syscall"
)

// IsProcessRunning checks if a process with the given PID is currently running.
// Matches Python's: os.kill(pid, 0) → True if no error.
// Returns false for pid <= 0 or any error (including process not found).
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := DefaultOS.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
