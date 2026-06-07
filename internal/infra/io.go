package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ──────────────────────────────────────────────
// Logging
// ──────────────────────────────────────────────

var (
	setupLoggingOnce sync.Once
)

// pythonLogHandler implements slog.Handler producing Python-style log output:
//
//	LEVEL: name: message
//
// Matching Python's formatter: "%(levelname)s: %(name)s: %(message)s"
// Example: "INFO: mvmctl.core.vm: Starting VM my-vm"
//
// The "name" is extracted from a "name" slog attribute (set via GetLogger or slog.With).
// When no name attribute is present, "root" is used as fallback.
type pythonLogHandler struct {
	writer io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	mu     sync.Mutex
}

func (h *pythonLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *pythonLogHandler) Handle(_ context.Context, r slog.Record) error {
	// Collect all attrs: handler-level attrs (from WithAttrs) + record-level attrs
	allAttrs := make([]slog.Attr, len(h.attrs))
	copy(allAttrs, h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		allAttrs = append(allAttrs, a)
		return true
	})

	// Extract name from attrs — matches %(name)s in Python
	name := "root"
	for _, a := range allAttrs {
		if a.Key == "name" && a.Value.Kind() == slog.KindString {
			name = a.Value.String()
			break
		}
	}

	level := r.Level.String()
	// slog uses "WARN" but Python uses "WARNING" — normalize
	if level == "WARN" {
		level = "WARNING"
	}

	line := fmt.Sprintf("%s: %s: %s\n", level, name, r.Message)

	h.mu.Lock()
	_, err := h.writer.Write([]byte(line))
	h.mu.Unlock()
	return err
}

func (h *pythonLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &pythonLogHandler{
		writer: h.writer,
		level:  h.level,
		attrs:  newAttrs,
	}
}

func (h *pythonLogHandler) WithGroup(_ string) slog.Handler {
	// Python-style format doesn't support groups. Silently ignore.
	return h
}

// GetLogger returns a logger with the given name, matching Python's
// get_logger(__name__) pattern. The name appears in log output as:
//
//	LEVEL: name: message
//
// Example: GetLogger("mvmctl.core.vm") produces "INFO: mvmctl.core.vm: ..."
func GetLogger(name string) *slog.Logger {
	return slog.Default().With("name", name)
}

// rotatingFileWriter implements io.Writer with continuous log file rotation,
// matching Python's RotatingFileHandler(maxBytes=10MB, backupCount=3).
//
// Unlike Go's old rotateLogIfNeeded (which only checked at startup), this
// writer checks file size BEFORE every write and rotates automatically,
// exactly like Python's logging.handlers.RotatingFileHandler.
type rotatingFileWriter struct {
	path        string
	maxBytes    int64
	backupCount int
	file        *os.File
	size        int64
	mu          sync.Mutex
}

// newRotatingFileWriter opens (or creates) the log file and returns a writer
// that rotates automatically. Returns an error if the file cannot be opened
// at construction time (silently skipped by SetupLogging, matching Python's
// "try: RotatingFileHandler(...) except Exception: pass").
func newRotatingFileWriter(path string) (*rotatingFileWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	fi, _ := f.Stat()
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	return &rotatingFileWriter{
		path:        path,
		maxBytes:    10 * 1024 * 1024, // 10 MB
		backupCount: 3,
		file:        f,
		size:        size,
	}, nil
}

// Write implements io.Writer with pre-write rotation check.
// Before writing, checks if adding p would exceed maxBytes. If so, rotates
// the log file first (rename .1 → .2, rename current → .1, create new).
// This matches Python's RotatingFileHandler which checks on every emit().
func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Ensure file is open (re-open if rotate closed it)
	if w.file == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			// File can't be opened — silently drop like Python's NullHandler fallback
			return len(p), nil
		}
		w.file = f
		w.size = 0
	}

	// Rotate if writing would exceed maxBytes (checked BEFORE write, like Python)
	if w.maxBytes > 0 && w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
		if w.file == nil {
			// rotate failed to open new file — silently drop
			return len(p), nil
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate performs log file rotation:
//   - Shifts .2 → .3, .1 → .2
//   - Renames current → .1
//   - Opens new empty log file
func (w *rotatingFileWriter) rotate() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}

	// Shift backups: .2 → .3, .1 → .2
	for i := w.backupCount - 1; i >= 1; i-- {
		oldPath := w.path + fmt.Sprintf(".%d", i)
		newPath := w.path + fmt.Sprintf(".%d", i+1)
		if _, err := os.Stat(oldPath); err == nil {
			os.Rename(oldPath, newPath)
		}
	}

	// Rename current log to .1
	os.Rename(w.path, w.path+".1")

	// Open new file
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	w.file = f
	w.size = 0
}

// SetupLogging configures the root slog logger with Python-style format and
// continuous file rotation. Mirrors Python's mvmctl.utils._io.setup_logging().
//
// Python always creates a RotatingFileHandler at CacheUtils.get_log_path() with
// maxBytes=10MB, backupCount=3, and level=DEBUG — regardless of the console level.
// The console handler respects the configured level (DEBUG/INFO/WARNING).
// The file handler always logs at DEBUG level for persistent debugging without
// requiring --debug flags.
//
// Priority (highest first):
//  1. debug=true  → DEBUG level
//  2. verbose=true → INFO level
//  3. MVM_LOG_LEVEL env var → parsed level (default WARNING)
func SetupLogging(verbose, debug bool) {
	setupLoggingOnce.Do(func() {
		var level slog.Level
		switch {
		case debug:
			level = slog.LevelDebug
		case verbose:
			level = slog.LevelInfo
		default:
			envLevel, _ := EnvGet("LOG_LEVEL")
			envLevel = strings.ToUpper(envLevel)
			switch envLevel {
			case "DEBUG":
				level = slog.LevelDebug
			case "INFO":
				level = slog.LevelInfo
			case "WARN", "WARNING":
				level = slog.LevelWarn
			case "ERROR":
				level = slog.LevelError
			default:
				level = slog.LevelWarn
			}
		}

		// Console handler (stderr) at configured level
		consoleHandler := &pythonLogHandler{
			writer: os.Stderr,
			level:  level,
		}

		handlers := []slog.Handler{consoleHandler}

		// File handler always at DEBUG — captures everything without --debug flags.
		// Mirror's Python's "try: RotatingFileHandler(...) except Exception: pass"
		logPath := GetLogPath()
		rw, err := newRotatingFileWriter(logPath)
		if err == nil {
			fileHandler := &pythonLogHandler{
				writer: rw,
				level:  slog.LevelDebug,
			}
			handlers = append(handlers, fileHandler)
		}

		var handler slog.Handler
		if len(handlers) == 1 {
			handler = handlers[0]
		} else {
			handler = slog.NewMultiHandler(handlers...)
		}

		logger := slog.New(handler)
		slog.SetDefault(logger)
	})
}

// LogException logs an error, matching Python's log_exception().
//
// Python behavior:
//   - At DEBUG level: log with full traceback via logger.exception()
//     (uses the Python exception stack trace from sys.exc_info())
//   - At other levels: log concise ERROR message via logger.error()
//
// At DEBUG level, Go captures the current goroutine stack via runtime/debug.Stack()
// to provide equivalent traceback visibility. At non-DEBUG levels, the error is
// logged as a structured attribute without stack.
func LogException(logger *slog.Logger, msg string, err error) {
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		stack := string(debug.Stack())
		logger.Error(msg, "error", err, "stack", stack)
	} else {
		logger.Error(msg, "error", err)
	}
}

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
