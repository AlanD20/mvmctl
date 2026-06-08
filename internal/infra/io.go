package infra

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ──────────────────────────────────────────────
// Safe file I/O (symlink-attack resistant)
// ──────────────────────────────────────────────

// OpenNoFollow opens a file with O_RDONLY | O_CLOEXEC | O_NOFOLLOW.
// Mirrors Python's FsUtils._open_nofollow().
func OpenNoFollow(path string) (*os.File, error) {
	// O_NOFOLLOW is always available on Linux (Go target platform)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open nofollow %s: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// SecureMkdir creates a directory, refusing if it or any ancestor is a symlink.
// Mirrors Python's FsUtils.secure_mkdir().
func SecureMkdir(path, name string) error {
	fi, err := os.Lstat(path)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("'%s' path is a symlink (possible attack): %s", name, path)
		}
		return fmt.Errorf("'%s' already exists at %s", name, path)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	err = os.MkdirAll(path, DirPerm)
	if err != nil {
		// Race condition — someone created it between our check and mkdir
		fi2, err2 := os.Lstat(path)
		if err2 == nil && fi2.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("'%s' path is a symlink (race condition detected): %s", name, path)
		}
		return fmt.Errorf("mkdir %s: %w", path, err)
	}

	// Post-creation check: ensure the final directory is not a symlink
	fi, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat %s after mkdir: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("'%s' directory is a symlink (security violation): %s", name, path)
	}

	return nil
}

// WaitForSocket polls for a Unix socket file to appear with the given timeout.
// Returns nil when the socket exists, or an error if the timeout expires.
func WaitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Type()&os.ModeSocket != 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("socket %s did not appear within %v", path, timeout)
}

// WritePIDFile writes a PID to a file with flock-based exclusive locking.
// Mirrors Python's FsUtils.write_pid_file().
// The mode parameter defaults to 0600 if zero is passed.
func WritePIDFile(path string, pid int, mode ...os.FileMode) error {
	fileMode := os.FileMode(0600)
	if len(mode) > 0 && mode[0] != 0 {
		fileMode = mode[0]
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC, uint32(fileMode))
	if err != nil {
		return fmt.Errorf("open pid file %s: %w", path, err)
	}
	defer syscall.Close(fd)

	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock pid file %s: %w", path, err)
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)

	_, err = syscall.Write(fd, []byte(strconv.Itoa(pid)))
	if err != nil {
		return fmt.Errorf("write pid file %s: %w", path, err)
	}
	return nil
}

// ──────────────────────────────────────────────
// Real user ID resolution (for sudo chown)
// ──────────────────────────────────────────────

// GetRealUserIDs returns (uid, gid) of the real invoking user when running
// under sudo. Returns nil if not running as root, or if SUDO_USER is not set
// or cannot be resolved.
// Mirrors Python's FsUtils.get_real_user_ids().
func GetRealUserIDs() (uid, gid int, ok bool) {
	if os.Getuid() != 0 {
		return 0, 0, false
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return 0, 0, false
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return 0, 0, false
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, true
}

// ChownToRealUser recursively chowns a path to the real invoking user when
// running under sudo. Does nothing if not under sudo or if path doesn't exist.
// Mirrors Python's FsUtils.chown_to_real_user().
func ChownToRealUser(path string) {
	uid, gid, ok := GetRealUserIDs()
	if !ok {
		return
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return
	}
	_ = os.Chown(path, uid, gid)
	if fi.IsDir() {
		_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip errors
			}
			_ = os.Chown(p, uid, gid)
			return nil
		})
	}
}

// ──────────────────────────────────────────────
// Generic file I/O helpers
// ──────────────────────────────────────────────

// ReadRaw reads the raw text content of a file with O_NOFOLLOW protection.
// Mirrors Python's FsUtils.read_raw().
func ReadRaw(path string) (string, error) {
	f, err := OpenNoFollow(path)
	if err != nil {
		return "", fmt.Errorf("Failed to read file %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("Failed to read file %s: %w", path, err)
	}
	return string(data), nil
}

// ReadFile reads a file and returns its contents as a string.
// Convenience wrapper matching Python's Path.read_text().
func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Failed to read file %s: %w", path, err)
	}
	return string(data), nil
}

// ReadYAML reads and parses a YAML file with O_NOFOLLOW protection.
// Returns dict[str, Any] | list[Any], matching Python's FsUtils.read_yaml()
// which returns dict[str, Any] | list[Any] (union, not just map).
// Returns {} (empty map) for empty files.
func ReadYAML(path string) (interface{}, error) {
	f, err := OpenNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to read YAML from %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("Failed to read YAML from %s: %w", path, err)
	}
	if len(data) == 0 {
		return make(map[string]any), nil
	}
	var result interface{}
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("Failed to read YAML from %s: %w", path, err)
	}
	if result == nil {
		return make(map[string]any), nil
	}
	return result, nil
}

// ──────────────────────────────────────────────
// Existing helpers (preserved from prior version)
// ──────────────────────────────────────────────

// EnsureDir creates a directory and all parents with the given permissions.
func EnsureDir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// DirSize returns the total size in bytes of all files within a directory tree.
// Inaccessible files are silently skipped (matches Python's OSError pass).
func DirSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(fp string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// WriteJSON marshals v as indented JSON and writes to path.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// ReadJSON reads and unmarshals JSON from path into v.
// Uses O_NOFOLLOW protection to prevent symlink attacks, matching Python's
// _open_nofollow() usage. Error messages match Python's format:
// "Failed to read JSON from {path}: {exc}".
func ReadJSON(path string, v any) error {
	f, err := OpenNoFollow(path)
	if err != nil {
		return fmt.Errorf("Failed to read JSON from %s: %w", path, err)
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(v); err != nil {
		return fmt.Errorf("Failed to read JSON from %s: %w", path, err)
	}
	return nil
}

// ──────────────────────────────────────────────
// Integer reading from /proc files (migrated from host domain — verdict #33)
// ──────────────────────────────────────────────

// ReadInt reads an integer from the first whitespace-delimited field of a file.
// If the file cannot be read or parsed, returns defaultVal.
// Matches Python's HostDetector._read_int().
func ReadInt(path string, defaultVal int) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultVal
	}
	text := strings.TrimSpace(string(data))
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return defaultVal
	}
	val, err := strconv.Atoi(parts[0])
	if err != nil {
		return defaultVal
	}
	return val
}

// CopyPreservingMetadata copies a file preserving both permissions and timestamps,
// matching Python's shutil.copy2() behavior. Uses io.Copy for streaming (no full
// memory load) and is appropriate for large files.
func CopyPreservingMetadata(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer s.Close()

	if err := os.MkdirAll(filepath.Dir(dst), DirPerm); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, s); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	// Preserve source timestamps
	srcInfo, err := os.Stat(src)
	if err == nil {
		_ = os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())
		// Preserve source permissions
		_ = os.Chmod(dst, srcInfo.Mode().Perm())
	}
	return nil
}
