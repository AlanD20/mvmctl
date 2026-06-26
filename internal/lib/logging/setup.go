package logging

import (
	"log/slog"
	"os"
	"strings"
	"sync"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/logging/rotating"
)

var setupLoggingOnce sync.Once

// SetupLogging configures the root slog logger with console-style format and
// continuous file rotation.
//
// A rotating file handler is always created with maxBytes=10MB, backupCount=3,
// and level=DEBUG — regardless of the console level.
// The console handler respects the configured level (DEBUG/INFO/WARNING).
// The file handler always logs at DEBUG level for persistent debugging without
// requiring --debug flags.
//
// Priority (highest first):
// 1. debug=true  → DEBUG level
// 2. verbose=true → INFO level
// 3. MVM_LOG_LEVEL env var → parsed level (default WARNING)
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

		// Stderr text handler at configured level
		stderrH := &textHandler{writer: os.Stderr, level: level}
		handlers := []slog.Handler{stderrH}

		// File handler always at DEBUG — captures everything without --debug flags.
		logPath := infra.GetLogPath()
		rw, err := rotating.NewRotatingFileWriter(logPath)
		if err == nil {
			fileH := &textHandler{writer: rw, level: slog.LevelDebug}
			handlers = append(handlers, fileH)
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
