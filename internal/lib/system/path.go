package system

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolvePath resolves symlinks and returns absolute path, matching Python's
// Path(path).resolve(). Falls back to filepath.Abs and then filepath.Clean
// if EvalSymlinks fails.
func ResolvePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// ExpandTilde expands ~ to the user's home directory, matching Python's Path.expanduser().
// TODO(verdict#33): moved from kernel/resolver.go — shared utility
func ExpandTilde(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	return path
}

// ExpandAndResolve expands ~ to home directory, resolves symlinks, and makes
// path absolute — matching Python's Path.expanduser().resolve() semantics.
// Python's resolve() resolves all symlinks; Go's filepath.Abs() alone does not.
// TODO(verdict#33): moved from kernel/resolver.go — shared utility
func ExpandAndResolve(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home
	}
	// filepath.EvalSymlinks resolves all symlinks in the path (matching Python's resolve())
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Abs(resolved)
	}
	return filepath.Abs(path)
}
