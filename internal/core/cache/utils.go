package cache

import (
	"path/filepath"
	"syscall"
)

// knownMVMComms lists mvm-managed process comm names.
// Only Firecracker has its own binary — service subprocesses are identified
// by the MVM_BACKGROUND_SERVICE=1 environment variable set in SpawnService.
var knownMVMComms = [...]string{"firecracker"}

// isMountPoint checks if a path is currently a mount point by comparing
// device numbers (st_dev) of path and its parent — matching Python's
// path.is_mount() behavior exactly.
func isMountPoint(path string) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	var parentSt syscall.Stat_t
	parent := filepath.Dir(path)
	if err := syscall.Stat(parent, &parentSt); err != nil {
		return false
	}
	return st.Dev != parentSt.Dev
}
