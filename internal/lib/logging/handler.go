package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// textHandler implements slog.Handler producing text log output:
//
//	LEVEL: name: message
//
// Example: "INFO: mvmctl.core.vm: Starting VM my-vm"
//
// The "name" is extracted from a "name" slog attribute. When no name
// attribute is present, "root" is used as fallback.
type textHandler struct {
	writer io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	mu     sync.Mutex // guards writes to the underlying writer
}

func (h *textHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	// Collect all attrs: handler-level attrs + record-level attrs
	allAttrs := make([]slog.Attr, len(h.attrs))
	copy(allAttrs, h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		allAttrs = append(allAttrs, a)
		return true
	})

	// Extract name from attrs and build attr suffix for non-name attrs.
	name := "root"
	var attrParts []string
	for _, a := range allAttrs {
		if a.Key == "name" && a.Value.Kind() == slog.KindString {
			name = a.Value.String()
			continue
		}
		if formatted := formatAttrValue(a); formatted != "" {
			attrParts = append(attrParts, formatted)
		}
	}

	level := r.Level.String()
	// Normalize "WARN" to "WARNING" for consistency.
	if level == "WARN" {
		level = "WARNING"
	}

	var b strings.Builder
	b.Grow(len(level) + len(name) + len(r.Message) + 32)
	b.WriteString(level)
	b.WriteString(": ")
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(r.Message)
	for _, p := range attrParts {
		b.WriteByte(' ')
		b.WriteString(p)
	}
	b.WriteByte('\n')
	line := b.String()

	h.mu.Lock()
	_, err := h.writer.Write([]byte(line))
	h.mu.Unlock()
	return err
}

// formatAttrValue formats a single slog.Attr as key=value string.
// It skips Group attributes entirely.
func formatAttrValue(a slog.Attr) string {
	switch a.Value.Kind() {
	case slog.KindString:
		v := a.Value.String()
		if containsSpace(v) {
			return fmt.Sprintf("%s=%q", a.Key, v)
		}
		return a.Key + "=" + v
	case slog.KindInt64:
		return a.Key + "=" + strconv.FormatInt(a.Value.Int64(), 10)
	case slog.KindUint64:
		return a.Key + "=" + strconv.FormatUint(a.Value.Uint64(), 10)
	case slog.KindFloat64:
		return a.Key + "=" + strconv.FormatFloat(a.Value.Float64(), 'f', -1, 64)
	case slog.KindBool:
		return a.Key + "=" + strconv.FormatBool(a.Value.Bool())
	case slog.KindDuration:
		return a.Key + "=" + a.Value.Duration().String()
	case slog.KindTime:
		return a.Key + "=" + a.Value.Time().Format(time.RFC3339)
	case slog.KindGroup:
		return ""
	default:
		return fmt.Sprintf("%s=%v", a.Key, a.Value.Any())
	}
}

// containsSpace reports whether s contains any whitespace character.
func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}

func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &textHandler{
		writer: h.writer,
		level:  h.level,
		attrs:  newAttrs,
	}
}

func (h *textHandler) WithGroup(_ string) slog.Handler {
	return h
}
