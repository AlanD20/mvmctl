package cache

import (
	"path/filepath"
	"syscall"
)

// knownMVMComms lists mvmctl-managed process comm names.
// Matches Python's _KNOWN_MVM_COMMS frozenset exactly (unexported).
var knownMVMComms = map[string]struct{}{
	"firecracker":   {},
	"mvm-provision": {},
	"mvm-services":  {},
}

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
