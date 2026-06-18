package infra

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- TimingLog ---

// timingEnabled is set via SetTimingEnabled.
var timingEnabled bool

// SetTimingEnabled enables or disables timing logging.
func SetTimingEnabled(enabled bool) {
	timingEnabled = enabled
}

// TimingLog provides centralized timing logging for VM creation phases.
type TimingLog struct {
	logger *slog.Logger
	once   sync.Once
}

var globalTimingLog = &TimingLog{}

// getLogger lazily creates the timing logger handler.
func (tl *TimingLog) getLogger() *slog.Logger {
	tl.once.Do(func() {
		if !timingEnabled {
			// When disabled, use a no-op handler.
			tl.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
			return
		}
		logPath := GetTimingLogPath()
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, DirPerm); err != nil {
			// Fall back to no-op handler.
			tl.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
			return
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			tl.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
			return
		}
		// Custom handler for key=value formatting.
		handler := &timingLogHandler{w: f}
		tl.logger = slog.New(handler)
	})
	return tl.logger
}

// timingLogHandler writes log entries in key=value format:
// "2026-05-24T12:34:56 phase=... elapsed_ms=... vm_name=... vm_id=..."
type timingLogHandler struct {
	w     *os.File
	mu    sync.Mutex // guards writes to the underlying file
	attrs []slog.Attr
}

func (h *timingLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *timingLogHandler) Handle(_ context.Context, r slog.Record) error {
	// Collect all attrs
	allAttrs := make([]slog.Attr, len(h.attrs))
	copy(allAttrs, h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		allAttrs = append(allAttrs, a)
		return true
	})

	// Build message with key=value pairs
	var b strings.Builder
	b.WriteString(r.Message)
	for _, a := range allAttrs {
		b.WriteString(fmt.Sprintf(" %s=%s", a.Key, a.Value.String()))
	}
	msg := b.String()

	// Timestamp in RFC3339 format
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("%s %s\n", ts, msg)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write([]byte(line))
	return err
}

func (h *timingLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &timingLogHandler{w: h.w, attrs: newAttrs}
}

func (h *timingLogHandler) WithGroup(_ string) slog.Handler {
	return h
}

// Timed measures and logs elapsed time for a phase.
// When timing is disabled, this is a no-op.
// Each log line is machine-parseable: phase, elapsed_ms, vm_name, vm_id.
func Timed(phase, vmName, vmID string, fn func()) {
	if !timingEnabled {
		fn()
		return
	}
	start := time.Now()
	fn()
	elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0

	logger := globalTimingLog.getLogger()
	logger.Info("",
		"phase", phase,
		"elapsed_ms", fmt.Sprintf("%.3f", elapsedMs),
		"vm_name", vmName,
		"vm_id", vmID,
	)
}

// LogTiming writes a timing entry directly to the timing log with the given
// elapsed time and optional extra key-value pairs. No-op when timing is disabled.
// This is the programmatic equivalent of Timed for cases where wrapping a
// closure is not practical (e.g., per-attempt timing inside a loop).
// Extra args follow the slog key-value pattern: "attempts", 5, "error", err.Error().
func LogTiming(phase, vmName, vmID string, elapsedMs float64, extra ...any) {
	if !timingEnabled {
		return
	}
	logger := globalTimingLog.getLogger()
	args := []any{
		"phase", phase,
		"elapsed_ms", fmt.Sprintf("%.3f", elapsedMs),
		"vm_name", vmName,
		"vm_id", vmID,
	}
	args = append(args, extra...)
	logger.Info("", args...)
}
