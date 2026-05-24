package system

import (
	"os"
	"syscall"
)

// POSIX access mode constants matching Python's os.R_OK, os.W_OK.
const (
	rOk = 4
	wOk = 2
)

// AccessRW checks if a path exists and is readable+writable, matching Python's
// os.access(path, os.R_OK | os.W_OK) behavior which checks the REAL UID
// (not effective UID).
func AccessRW(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return syscall.Access(path, rOk|wOk) == nil
}
