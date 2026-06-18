// Package rotating provides a RotatingFileWriter with continuous log file rotation.
//
// This package is a leaf-level utility — it imports nothing from mvmctl itself.
// It exists in a sub-package of logging so that both infra (audit.go) and
// logging (setup.go) can import it without creating a circular dependency.
package rotating

import (
	"fmt"
	"os"
	"sync"
)

// RotatingFileWriter implements io.Writer with continuous log file rotation
// (10MB max, 3 backups).
//
// Unlike a rotate-at-startup approach, this writer checks file size BEFORE
// every write and rotates automatically.
type RotatingFileWriter struct {
	path        string
	maxBytes    int64
	backupCount int
	file        *os.File
	size        int64
	mu          sync.Mutex // guards file handle and size
}

// NewRotatingFileWriter opens (or creates) the log file and returns a writer
// that rotates automatically. Returns an error if the file cannot be opened
// at construction time (silently skipped by SetupLogging).
func NewRotatingFileWriter(path string) (*RotatingFileWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	fi, _ := f.Stat()
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	return &RotatingFileWriter{
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
func (w *RotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Ensure file is open (re-open if rotate closed it)
	if w.file == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			// File can't be opened — silently drop.
			return len(p), nil
		}
		w.file = f
		w.size = 0
	}

	// Rotate if writing would exceed maxBytes (checked before write)
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
// - Shifts .2 → .3, .1 → .2
// - Renames current → .1
// - Opens new empty log file
func (w *RotatingFileWriter) rotate() {
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
