package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
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
	mu     sync.Mutex
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

	// Extract name from attrs
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
