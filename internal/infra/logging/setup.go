package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/logging/rotating"
)

var (
	setupLoggingOnce sync.Once
)

// SetupLogging configures the root slog logger with console-style format and
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
			envLevel := strings.ToUpper(infra.EnvGetDefault("LOG_LEVEL", ""))
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
		consoleH := &consoleHandler{
			writer: os.Stderr,
			level:  level,
		}

		handlers := []slog.Handler{consoleH}

		// File handler always at DEBUG — captures everything without --debug flags.
		// Mirror's Python's "try: RotatingFileHandler(...) except Exception: pass"
		logPath := GetLogPath()
		if err := ensureLogDir(logPath); err == nil {
			rw, err := rotating.NewRotatingFileWriter(logPath)
			if err == nil {
				fileH := &consoleHandler{
					writer: rw,
					level:  slog.LevelDebug,
				}
				handlers = append(handlers, fileH)
			}
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

// ensureLogDir ensures the parent directory of logPath exists.
func ensureLogDir(logPath string) error {
	dir := filepath.Dir(logPath)
	return os.MkdirAll(dir, 0755)
}

// GetLogPath returns the full path to the mvmctl log file.
// The path is derived from the cache directory (under $HOME/.cache/<project>).
func GetLogPath() string {
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		cacheDir = filepath.Join(infra.GetRealHome(), ".cache", infra.ProjectName)
	}
	return filepath.Join(cacheDir, "mvmctl.log")
}
