package infra

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// --- Safe file I/O (symlink-attack resistant) ---

// OpenNoFollow opens a file with O_RDONLY | O_CLOEXEC | O_NOFOLLOW.
func OpenNoFollow(path string) (*os.File, error) {
	// O_NOFOLLOW is always available on Linux (Go target platform)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open nofollow %s: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// SecureMkdir creates a directory, refusing if it or any ancestor is a symlink.
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

// --- Real user ID resolution (for sudo chown) ---

// GetRealUserIDs returns (uid, gid) of the real invoking user when running
// under sudo. Returns nil if not running as root, or if SUDO_USER is not set
// or cannot be resolved.
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

// --- Generic file I/O helpers ---

// ReadRaw reads the raw text content of a file with O_NOFOLLOW protection.
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
func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Failed to read file %s: %w", path, err)
	}
	return string(data), nil
}

// ReadYAML reads and parses a YAML file with O_NOFOLLOW protection.
// Returns a map or list, not just a map. Returns {} (empty map) for empty files.
func ReadYAML(path string) (any, error) {
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
	var result any
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("Failed to read YAML from %s: %w", path, err)
	}
	if result == nil {
		return make(map[string]any), nil
	}
	return result, nil
}

// --- Existing helpers ---

// EnsureDir creates a directory and all parents with the given permissions.
func EnsureDir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// DirSize returns the total size in bytes of all files within a directory tree.
// Inaccessible files are silently skipped.
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
// Uses O_NOFOLLOW protection to prevent symlink attacks.
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

// --- Integer reading from /proc files ---

// ReadInt reads an integer from the first whitespace-delimited field of a file.
// If the file cannot be read or parsed, returns defaultVal.
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

// CopyPreservingMetadata copies a file preserving both permissions and timestamps.
// Uses io.Copy for streaming (no full memory load) and is appropriate for large files.
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

// SafeMove moves a file with cross-filesystem fallback (os.Rename + copy+delete).
func SafeMove(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := CopyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// CopyFile copies a file from src to dst.
func CopyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", dst, err)
	}
	defer d.Close()

	if _, err := io.Copy(d, s); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return d.Sync()
}

// IsSubDir checks whether path is under parent using proper path hierarchy comparison.
// Uses filepath.Rel() to avoid false positives with string prefix matching
// (e.g., "/home/user1" incorrectly matching "/home/user").
// Returns true when path == parent (exact match counts as "under").
func IsSubDir(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// FindFreePort finds a free TCP port in [start, end] by probing.
// Returns 0 and an error if no port is available in the range.
func FindFreePort(host string, start, end int) (int, error) {
	for port := start; port <= end; port++ {
		addr := fmt.Sprintf("%s:%d", host, port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in range %d-%d", start, end)
}
