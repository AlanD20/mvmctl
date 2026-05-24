package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"sync"
)

// consoleHandler implements slog.Handler producing console-style log output:
//
//	LEVEL: name: message
//
// Example: "INFO: mvmctl.core.vm: Starting VM my-vm"
//
// The format matches Python's "%(levelname)s: %(name)s: %(message)s".
// The "name" is extracted from a "name" slog attribute (set via GetLogger or slog.With).
// When no name attribute is present, "root" is used as fallback.
type consoleHandler struct {
	writer io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	mu     sync.Mutex
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
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

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &consoleHandler{
		writer: h.writer,
		level:  h.level,
		attrs:  newAttrs,
	}
}

func (h *consoleHandler) WithGroup(_ string) slog.Handler {
	// Console-style format doesn't support groups. Silently ignore.
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
