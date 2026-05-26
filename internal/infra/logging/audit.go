package logging

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/infra/logging/rotating"
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

// NewAuditLog creates a new AuditLog writing to {cacheDir}/audit.log.
// The underlying RotatingFileWriter is created at construction time (opening
// the file immediately). If the file cannot be opened, the writer is nil and
// Log/LogOperation silently succeed (matching Python's NullHandler fallback).
func NewAuditLog(cacheDir string) *AuditLog {
	path := filepath.Join(cacheDir, "audit.log")
	writer, _ := rotating.NewRotatingFileWriter(path)
	return &AuditLog{path: path, writer: writer}
}

// Path returns the audit log file path.
func (l *AuditLog) Path() string {
	return l.path
}

// detectUser returns the current username, matching Python's getpass.getuser().
// Falls back to UID string on error.
func detectUser() string {
	u, err := user.Current()
	if err != nil {
		return fmt.Sprintf("%d", os.Getuid())
	}
	return u.Username
}

// formatVal renders a value for audit log entries, matching Python's str() representation.
// In particular, bool values render as "True"/"False" (capitalized) matching Python.
func formatVal(v interface{}) string {
	switch val := v.(type) {
	case bool:
		if val {
			return "True"
		}
		return "False"
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
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
	now := time.Now().UTC()
	asctime := now.Format(time.RFC3339) // matches Python datefmt
	msgTS := now.Format(time.RFC3339)   // matches Python message format
	_, err := fmt.Fprintf(l.writer, "%s UTC [%s] %s\n", asctime, msgTS, entry)
	return err
}

// LogOperation writes a structured audit log entry matching Python's AuditLog.log().
//
// Python's FileHandler uses formatter: "%(asctime)s UTC %(message)s"
// with datefmt="%Y-%m-%dT%H:%M:%S". The message itself includes a second
// timestamp: [YYYY-MM-DDTHH:MM:SSZ]. This produces BOTH timestamps so the
// output matches Python exactly:
//
//	2024-01-15T10:30:00 UTC [2024-01-15T10:30:00Z] user=root op=...
//
// Python format uses repr(context) which produces quoted strings with proper escaping.
// Go equivalent: replace single quotes and backslashes in context, then wrap in quotes.
func (l *AuditLog) LogOperation(operation string, changes map[string]interface{}, context string) error {
	if l.writer == nil {
		return nil // silent fallback if writer failed to open
	}
	now := time.Now().UTC()
	asctime := now.Format(time.RFC3339) // matches Python datefmt
	msgTS := now.Format(time.RFC3339)   // matches Python message format
	msg := fmt.Sprintf("%s UTC [%s] user=%s op=%s", asctime, msgTS, detectUser(), operation)

	if len(changes) > 0 {
		pairs := make([]string, 0, len(changes))
		for k, v := range changes {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, formatVal(v)))
		}
		msg += " changes=" + strings.Join(pairs, ",")
	}

	if context != "" {
		// Python uses repr(context) which returns a quoted string with internal
		// special characters properly escaped. repr() wraps strings in single quotes,
		// escaping internal single quotes and backslashes.
		// Example: context="it's done" → context='it\'s done'
		replaced := strings.ReplaceAll(context, "\\", "\\\\")
		replaced = strings.ReplaceAll(replaced, "'", "\\'")
		// Also escape newlines and tabs for safe log embedding
		replaced = strings.ReplaceAll(replaced, "\n", "\\n")
		replaced = strings.ReplaceAll(replaced, "\r", "\\r")
		replaced = strings.ReplaceAll(replaced, "\t", "\\t")
		msg += fmt.Sprintf(" context='%s'", replaced)
	}

	_, err := fmt.Fprintln(l.writer, msg)
	return err
}
