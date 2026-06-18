package system

import (
	"os"
	"syscall"
)

// POSIX access mode constants.
const (
	rOk = 4
	wOk = 2
)

// AccessRW checks if a path exists and is readable+writable.
// Uses syscall.Access which checks the real UID (not effective UID).
func AccessRW(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return syscall.Access(path, rOk|wOk) == nil
}
