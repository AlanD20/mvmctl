package logging

import (
	"fmt"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/logging/rotating"
	"mvmctl/internal/lib/system"
)

// AuditLog provides centralized audit logging matching Python's mvmctl.utils.auditlog.AuditLog.
// Writes structured entries to a single {cacheDir}/audit.log file.
//
// Uses rotating.RotatingFileWriter under the hood, which provides continuous
// 10MB rotation with 3 backups — matching Python's RotatingFileHandler behavior.
//
// The file handle is managed entirely by RotatingFileWriter; AuditLog only
// writes formatted entries through the io.Writer interface.
type AuditLog struct {
	path   string
	writer *rotating.RotatingFileWriter
}

// NewAuditLog creates a new AuditLog writing to infra.GetAuditLogPath().
// The underlying RotatingFileWriter is created at construction time (opening
// the file immediately). If the file cannot be opened, the writer is nil and
// Log/LogOperation silently succeed (matching Python's NullHandler fallback).
func NewAuditLog() *AuditLog {
	path := infra.GetAuditLogPath()
	writer, _ := rotating.NewRotatingFileWriter(path)
	return &AuditLog{path: path, writer: writer}
}

// Path returns the audit log file path.
func (l *AuditLog) Path() string {
	return l.path
}

// detectUser returns the current username via system.CurrentUsername.
// Falls back to UID string on error.
func detectUser() string {
	u, err := system.CurrentUsername()
	if err != nil {
		uid, _, _ := system.GetRealUserIDs()
		return fmt.Sprintf("%d", uid)
	}
	return u
}

// Log appends a raw entry string with dual timestamps matching Python's
// AuditLog output. Python's FileHandler formatter produces:
//
//	%(asctime)s UTC %(message)s
//
// Where datefmt="%Y-%m-%dT%H:%M:%S". The message itself contains another
// timestamp: [YYYY-MM-DDTHH:MM:SSZ]. This replicates both so the output
// matches Python exactly:
//
//	2024-01-15T10:30:00 UTC [2024-01-15T10:30:00Z] user=root op=...
func (l *AuditLog) Log(entry string) error {
	if l.writer == nil {
		return nil // silent fallback if writer failed to open
	}
	ts := time.Now().Format(time.RFC3339)
	_, err := fmt.Fprintf(l.writer, "%s UTC [%s] %s\n", ts, ts, entry)
	return err
}

// LogOperation writes a structured audit log entry.
//
// Format: {ts} UTC [{ts}] user={user} op={operation} [changes={k=v,...}] [context={...}]
func (l *AuditLog) LogOperation(operation string, changes map[string]any, context string) error {
	if l.writer == nil {
		return nil // silent fallback if writer failed to open
	}
	ts := time.Now().Format(time.RFC3339)
	msg := fmt.Sprintf("%s UTC [%s] user=%s op=%s", ts, ts, detectUser(), operation)

	if len(changes) > 0 {
		pairs := make([]string, 0, len(changes))
		for k, v := range changes {
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
		}
		msg += " changes=" + strings.Join(pairs, ",")
	}

	if context != "" {
		msg += " context=" + context
	}

	_, err := fmt.Fprintln(l.writer, msg)
	return err
}
