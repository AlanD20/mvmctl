// Package timinglog provides centralized multi-phase timing logging for
// VM creation, image operations, and other measured operations.
//
// Output is written to ~/.cache/mvmctl/timing.log in key=value format.
// All operations are no-ops when timing is disabled via SetTimingEnabled.
package timinglog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mvmctl/internal/infra"
)

// --- Package-level state ---

var (
	enabled    bool
	fileMu     sync.Mutex
	fileHandle *os.File
	fileOnce   sync.Once
	fileErr    error
)

// SetTimingEnabled controls whether timing entries are written to file.
// When disabled, Start returns nil and all Log methods are no-ops.
func SetTimingEnabled(v bool) {
	enabled = v
}

// openFile lazily opens the timing log file for append. Thread-safe via sync.Once.
func openFile() error {
	fileOnce.Do(func() {
		logPath := infra.GetTimingLogPath()
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			fileErr = fmt.Errorf("create timing log directory: %w", err)
			return
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fileErr = fmt.Errorf("open timing log file: %w", err)
			return
		}
		fileHandle = f
	})
	return fileErr
}

// writeLine writes a single line to the timing log file.
// The line format is: "timestamp k1=v1 k2=v2 ...\n"
// Thread-safe via fileMu. Silently drops errors (file I/O is non-fatal).
func writeLine(kv ...string) {
	if len(kv) == 0 {
		return
	}
	if len(kv)%2 != 0 {
		return // odd number of args is a bug — skip
	}

	fileMu.Lock()
	defer fileMu.Unlock()

	if err := openFile(); err != nil {
		return
	}

	ts := time.Now().Format(time.RFC3339)
	var b strings.Builder
	b.WriteString(ts)
	for i := 0; i < len(kv); i += 2 {
		b.WriteByte(' ')
		b.WriteString(kv[i])
		b.WriteByte('=')
		b.WriteString(kv[i+1])
	}
	b.WriteByte('\n')
	_, _ = fileHandle.WriteString(b.String())
}

// --- Log (multi-phase) ---

// Logger tracks sequential phases of an operation.
// Created via Start. All methods are safe to call on a nil *Logger (no-op).
// Exporting the type name allows callers to declare variables, though typically
// they use := with Start's return value.
type Logger struct {
	op    string
	attrs []string // ordered "key1","val1","key2","val2",...
	start time.Time
	last  time.Time
}

// Start begins a multi-phase timing operation.
// attrs are optional key-value pairs included in EVERY log line for all stages.
// Returns nil when timing is disabled — safe to call methods on nil *Logger.
func Start(op string, attrs ...any) *Logger {
	if !enabled {
		return nil
	}
	now := time.Now()
	l := &Logger{
		op:    op,
		start: now,
		last:  now,
	}
	// Convert attrs to string pairs
	for i := 0; i < len(attrs); i += 2 {
		if i+1 >= len(attrs) {
			break
		}
		key := fmt.Sprintf("%v", attrs[i])
		val := fmt.Sprintf("%v", attrs[i+1])
		l.attrs = append(l.attrs, key, val)
	}

	// Log start line: "ts op=XXX stage=start"
	writeLine(append([]string{"op", op, "stage", "start"}, l.attrs...)...)
	return l
}

// Stage marks the end of one phase. Logs stage_s + elapsed_s.
// No-op when l is nil or timing is disabled.
func (l *Logger) Stage(stage string) {
	if l == nil || !enabled {
		return
	}
	now := time.Now()
	stageDur := now.Sub(l.last).Seconds()
	elapsed := now.Sub(l.start).Seconds()

	kv := []string{
		"op", l.op,
		"stage", stage,
		"stage_s", fmt.Sprintf("%.3f", stageDur),
		"elapsed_s", fmt.Sprintf("%.3f", elapsed),
	}
	kv = append(kv, l.attrs...)
	writeLine(kv...)
	l.last = now
}

// StageFunc wraps a closure as a timed phase. Same as Stage but the
// stage duration = time spent in fn. Caller should check errors after.
// fn always runs, even when timing is disabled.
func (l *Logger) StageFunc(stage string, fn func()) {
	if fn == nil {
		return
	}
	if l == nil || !enabled {
		fn()
		return
	}
	now := time.Now()
	fn()
	stageDur := time.Since(now).Seconds()
	elapsed := time.Since(l.start).Seconds()

	kv := []string{
		"op", l.op,
		"stage", stage,
		"stage_s", fmt.Sprintf("%.3f", stageDur),
		"elapsed_s", fmt.Sprintf("%.3f", elapsed),
	}
	kv = append(kv, l.attrs...)
	writeLine(kv...)
	l.last = time.Now()
}

// Complete logs total duration for the operation.
// No-op when l is nil or timing is disabled.
func (l *Logger) Complete() {
	if l == nil || !enabled {
		return
	}
	total := time.Since(l.start).Seconds()

	kv := []string{
		"op", l.op,
		"stage", "total",
		"total_s", fmt.Sprintf("%.3f", total),
	}
	kv = append(kv, l.attrs...)
	writeLine(kv...)
}

// --- Standalone Log ---

// Log is a standalone one-shot timing entry. No Logger struct needed.
// Use for retry loops or single measurements. No-op when timing disabled.
func Log(op string, elapsedMs float64, attrs ...any) {
	if !enabled {
		return
	}
	kv := []string{"op", op, "elapsed_ms", fmt.Sprintf("%.3f", elapsedMs)}
	for i := 0; i < len(attrs); i += 2 {
		if i+1 >= len(attrs) {
			break
		}
		key := fmt.Sprintf("%v", attrs[i])
		val := fmt.Sprintf("%v", attrs[i+1])
		kv = append(kv, key, val)
	}
	writeLine(kv...)
}
