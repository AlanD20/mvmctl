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

// ── TimingLog (Python: mvmctl.utils.timinglog.TimingLog) ──

// timingEnabled is set via SetTimingEnabled (formerly from MVM_TIMING_ENABLED env var in init()).
// Python: _TIMING_ENABLED: bool = env.get("TIMING_ENABLED") is not None
var timingEnabled bool

// SetTimingEnabled enables or disables timing logging.
// TODO: call SetTimingEnabled() from app/app.go explicitly
func SetTimingEnabled(enabled bool) {
	timingEnabled = enabled
}

// TimingLog provides centralized timing logging for VM creation phases.
// Mirrors Python's mvmctl.utils.timinglog.TimingLog.
type TimingLog struct {
	logger *slog.Logger
	once   sync.Once
}

var globalTimingLog = &TimingLog{}

// getLogger lazily creates the timing logger handler.
// Python: creates logging.FileHandler on first call, gated by _TIMING_ENABLED.
func (tl *TimingLog) getLogger() *slog.Logger {
	tl.once.Do(func() {
		if !timingEnabled {
			// When disabled, use a no-op handler (like Python's NullHandler).
			tl.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
			return
		}
		logPath := GetTimingLogPath()
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, DirPerm); err != nil {
			// Fall back to no-op (like Python's NullHandler)
			tl.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
			return
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			tl.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
			return
		}
		// Python format: "%(asctime)s %(message)s" with datefmt="%Y-%m-%dT%H:%M:%S"
		// We use a custom handler for the formatting.
		handler := &timingLogHandler{w: f}
		tl.logger = slog.New(handler)
	})
	return tl.logger
}

// timingLogHandler writes log entries in Python's timing log format.
// Format: "2026-05-24T12:34:56 phase=... elapsed_ms=... vm_name=... vm_id=..."
type timingLogHandler struct {
	w     *os.File
	mu    sync.Mutex
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

	// Build message with key=value pairs (Python format)
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
// Mirrors Python's mvmctl.utils.timinglog.timed() context manager.
// When MVM_TIMING_ENABLED is not set, this is a no-op.
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
