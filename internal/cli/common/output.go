// Package common provides CLI display helpers — table rendering, JSON output,
// error display, and the MVMCli singleton matching Python's ``utils/cli.py:MVMCli``.
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"

	"mvmctl/internal/infra/errs"
)

// ─── Spinner ───────────────────────────────────────────────────────────────────

// Spinner provides a simple terminal spinner that prints sequential dots/characters
// on stderr, matching Python's Rich console.status("", spinner="dots").
// Call Start/Stop to control the spinner lifecycle.
type Spinner struct {
	done    chan struct{}
	text    string
	stopped bool // prevents double-close panic on second Stop() call
	mu      sync.Mutex
}

// NewSpinner creates a new spinner with an initial text message.
func NewSpinner(text string) *Spinner {
	return &Spinner{
		done: make(chan struct{}),
		text: text,
	}
}

// Start begins the spinner animation in a separate goroutine.
// The spinner writes dots to stderr and will continue until Stop() is called.
func (s *Spinner) Start() {
	go func() {
		chars := []string{".", "..", "..."}
		i := 0
		for {
			select {
			case <-s.done:
				// Clear the spinner line
				fmt.Fprintf(os.Stderr, "\r%s\r", s.text)
				return
			default:
				msg := s.text
				if msg != "" {
					fmt.Fprintf(os.Stderr, "\r  %s%s", msg, chars[i%len(chars)])
				} else {
					fmt.Fprintf(os.Stderr, "\r%s", chars[i%len(chars)])
				}
				i++
				time.Sleep(200 * time.Millisecond)
			}
		}
	}()
}

// Stop stops the spinner and clears the spinner line.
// Safe to call multiple times (subsequent calls are no-ops).
func (s *Spinner) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	close(s.done)
}

// UpdateText updates the spinner's current text message.
func (s *Spinner) UpdateText(text string) {
	s.text = text
}

// ─── Prettification patterns (matching Python _PRETTIFY_PATTERNS) ─────────────

var prettifyPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\bId\b`), "ID"},
	{regexp.MustCompile(`\bSsh\b`), "SSH"},
	{regexp.MustCompile(`\bIpv`), "IPv"},
	{regexp.MustCompile(`\bMac\b`), "MAC"},
	{regexp.MustCompile(`\bPid\b`), "PID"},
	{regexp.MustCompile(`\bUuid\b`), "UUID"},
	{regexp.MustCompile(`\bNat\b`), "NAT"},
	{regexp.MustCompile(`\bTap\b`), "TAP"},
	{regexp.MustCompile(`\bVms?\b`), "VM"},
	{regexp.MustCompile(`\bCpus?\b`), "CPU"},
	{regexp.MustCompile(`\bKvm\b`), "KVM"},
	{regexp.MustCompile(`\bOs\b`), "OS"},
	{regexp.MustCompile(`\bPci\b`), "PCI"},
	{regexp.MustCompile(`\bTmpfs\b`), "TMPFS"},
	{regexp.MustCompile(`\bFs\b`), "FS"},
}

func prettifyKey(key string) string {
	s := strings.ReplaceAll(key, "_", " ")
	s = toTitle(s)
	for _, p := range prettifyPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

func toTitle(s string) string {
	if s == "" {
		return ""
	}
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// ─── MVMCli singleton ─────────────────────────────────────────────────────────

// ─── TTY detection ───────────────────────────────────────────────────────────

// isStdoutTTY returns true if stdout is a terminal (i.e. not piped).
func isStdoutTTY() bool {
	fi, _ := os.Stdout.Stat()
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// isStderrTTY returns true if stderr is a terminal (i.e. not piped).
func isStderrTTY() bool {
	fi, _ := os.Stderr.Stat()
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ANSI escape codes for Rich-compatible styling.
const (
	ansiRed     = "\033[31m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiDim     = "\033[2m"
	ansiBold    = "\033[1m"
	ansiReset   = "\033[0m"
)

// MVMCli is a centralized display output matching Python's MVMCli.
// Python uses Rich Console which auto-applies markup when stdout is a TTY
// and strips it when piped. Go uses ANSI escape codes with manual TTY checks.
type MVMCli struct{}

// MVMCLI is the module-level singleton matching Python's mvm_cli.
var MVMCLI = &MVMCli{}

// NewCli returns the singleton MVMCli instance.
func NewCli() *MVMCli { return MVMCLI }

// Error prints an error message to stderr.
// Matches Python's mvm_cli.error() — Rich: "[red]✗ Error:[/] {message}"
func (c *MVMCli) Error(message string, isUnexpected ...bool) {
	unexpected := len(isUnexpected) > 0 && isUnexpected[0]
	tty := isStderrTTY()
	if unexpected {
		if tty {
			fmt.Fprintf(os.Stderr, "%s⚠ Unexpected Error:%s %s\n", ansiYellow, ansiReset, message)
		} else {
			fmt.Fprintf(os.Stderr, "⚠ Unexpected Error: %s\n", message)
		}
	} else {
		if tty {
			fmt.Fprintf(os.Stderr, "%s✗ Error:%s %s\n", ansiRed, ansiReset, message)
		} else {
			fmt.Fprintf(os.Stderr, "✗ Error: %s\n", message)
		}
	}
}

// Success prints a success message to stdout.
// Matches Python's mvm_cli.success() — Rich: "[green]✓ {message}[/]"
func (c *MVMCli) Success(message string) {
	if isStdoutTTY() {
		fmt.Printf("%s✓ %s%s\n", ansiGreen, message, ansiReset)
	} else {
		fmt.Printf("✓ %s\n", message)
	}
}

// Warning prints a warning message to stderr.
// Matches Python's mvm_cli.warning() — Rich: "[yellow]! {message}[/]"
func (c *MVMCli) Warning(message string) {
	if isStderrTTY() {
		fmt.Fprintf(os.Stderr, "%s! %s%s\n", ansiYellow, message, ansiReset)
	} else {
		fmt.Fprintf(os.Stderr, "! %s\n", message)
	}
}

// Info prints an info/dim message to stdout.
// Matches Python's mvm_cli.info() — Rich: "[dim]  {message}[/]"
func (c *MVMCli) Info(message string) {
	if isStdoutTTY() {
		fmt.Printf("%s  %s%s\n", ansiDim, message, ansiReset)
	} else {
		fmt.Printf("  %s\n", message)
	}
}

// SectionHeader prints a bold section title.
// Matches Python's mvm_cli.section_header() — Rich: "[bold]{title}[/]"
func (c *MVMCli) SectionHeader(title string) {
	if isStdoutTTY() {
		fmt.Printf("\n%s%s%s\n", ansiBold, title, ansiReset)
	} else {
		fmt.Printf("\n%s\n", title)
	}
}

// InspectHeader prints an inspect-style header with underline.
// Matches Python's mvm_cli.inspect_header() — Rich: "[bold]{full}[/]" + "==="
func (c *MVMCli) InspectHeader(title, subtitle string) {
	tty := isStdoutTTY()
	if subtitle != "" {
		full := fmt.Sprintf("%s (%s)", title, subtitle)
		if tty {
			fmt.Printf("\n%s%s%s\n", ansiBold, full, ansiReset)
		} else {
			fmt.Printf("\n%s\n", full)
		}
		fmt.Println(strings.Repeat("=", len(full)))
	} else {
		if tty {
			fmt.Printf("\n%s%s%s\n", ansiBold, title, ansiReset)
		} else {
			fmt.Printf("\n%s\n", title)
		}
		fmt.Println(strings.Repeat("=", len(title)))
	}
}

// KeyValue prints a key-value pair with consistent padding.
func (c *MVMCli) KeyValue(key, value string, indent int, keyWidth int) {
	if indent == 0 {
		indent = 2
	}
	if keyWidth == 0 {
		keyWidth = 12
	}
	padding := strings.Repeat(" ", indent)
	fmt.Printf("%s%-*s %s\n", padding, keyWidth, key+":", value)
}

// Table prints a table using go-pretty, matching Python's Rich table with SIMPLE box style.
// Uses no borders, no column separators, with a simple header separator made of ─ characters.
func (c *MVMCli) Table(columns []string, rows [][]string, title ...string) {
	tw := table.NewWriter()

	// Configure style: no borders, no vertical separators, header separator only
	tw.Style().Options.DrawBorder = false
	tw.Style().Options.SeparateColumns = false
	tw.Style().Options.SeparateFooter = false
	tw.Style().Options.SeparateHeader = true
	tw.Style().Options.SeparateRows = false

	// Use ─ for horizontal lines to match Rich's box.SIMPLE
	tw.Style().Box.MiddleHorizontal = "─"

	if len(title) > 0 && title[0] != "" {
		tw.SetTitle(title[0])
	}

	// Build header row with no auto-index
	header := table.Row{}
	for _, col := range columns {
		header = append(header, col)
	}
	tw.AppendHeader(header)

	// Build data rows
	for _, row := range rows {
		dataRow := table.Row{}
		for _, cell := range row {
			dataRow = append(dataRow, cell)
		}
		tw.AppendRow(dataRow)
	}

	// Render with go-pretty's auto-column sizing
	rendered := tw.Render()

	// Print with leading space to match Python's default indentation
	for _, line := range strings.Split(rendered, "\n") {
		fmt.Println(" " + line)
	}
}

// PrintDictTree prints a nested map/slice as a tree, matching Python's print_dict_tree.
func (c *MVMCli) PrintDictTree(data interface{}, title string) {
	if title != "" {
		fmt.Println(title)
	}
	c.buildTree(data, "", true)
}

func (c *MVMCli) buildTree(data interface{}, indent string, isRoot bool) {
	switch v := data.(type) {
	case map[string]interface{}:
		keys := sortedKeys(v)
		for i, key := range keys {
			val := v[key]
			pretty := prettifyKey(key)
			connector := "├─ "
			if i == len(keys)-1 {
				connector = "└─ "
			}
			switch child := val.(type) {
			case map[string]interface{}:
				fmt.Printf("%s%s%s\n", indent, connector, pretty)
				childIndent := indent + "   "
				if i < len(keys)-1 {
					childIndent = indent + "│  "
				}
				c.buildTree(child, childIndent, false)
			case []interface{}:
				if len(child) > 0 {
					if _, isMapSlice := child[0].(map[string]interface{}); isMapSlice {
						fmt.Printf("%s%s%s\n", indent, connector, pretty)
						childIndent := indent + "   "
						if i < len(keys)-1 {
							childIndent = indent + "│  "
						}
						for j, item := range child {
							itemConnector := "├─ "
							if j == len(child)-1 {
								itemConnector = "└─ "
							}
							fmt.Printf("%s%s#%d\n", childIndent, itemConnector, j+1)
							itemChildIndent := childIndent + "   "
							if j < len(child)-1 {
								itemChildIndent = childIndent + "│  "
							}
							c.buildTree(item, itemChildIndent, false)
						}
					} else {
						items := make([]string, len(child))
						for j, item := range child {
							items[j] = fmt.Sprintf("%v", item)
						}
						display := strings.Join(items, ", ")
						fmt.Printf("%s%s%s: %s\n", indent, connector, pretty, display)
					}
				} else {
					fmt.Printf("%s%s%s: -\n", indent, connector, pretty)
				}
			case nil:
				fmt.Printf("%s%s%s: -\n", indent, connector, pretty)
			default:
				display := c.formatLeafValue(key, child)
				fmt.Printf("%s%s%s: %s\n", indent, connector, pretty, display)
			}
		}
	case []interface{}:
		for i, item := range v {
			connector := "├─ "
			if i == len(v)-1 {
				connector = "└─ "
			}
			if m, ok := item.(map[string]interface{}); ok {
				fmt.Printf("%s%s#%d\n", indent, connector, i+1)
				childIndent := indent + "   "
				if i < len(v)-1 {
					childIndent = indent + "│  "
				}
				c.buildTree(m, childIndent, false)
			} else {
				fmt.Printf("%s%s%v\n", indent, connector, item)
			}
		}
	default:
		if !isRoot {
			fmt.Printf("%s%s\n", indent, fmt.Sprintf("%v", data))
		}
	}
}

func (c *MVMCli) formatLeafValue(key string, value interface{}) string {
	if value == nil {
		return "-"
	}
	if s, ok := value.(string); ok && strings.HasSuffix(key, "_at") {
		formatted := FormatTimestamp(s, "full")
		if formatted != s {
			return formatted
		}
	}
	return fmt.Sprintf("%v", value)
}

// FormatMarker delegates to the package-level function.
func (c *MVMCli) FormatMarker(isDefault bool) string { return FormatMarker(isDefault) }

// FormatTimestamp delegates to the package-level function.
func (c *MVMCli) FormatTimestamp(isoString string, style string) string { return FormatTimestamp(isoString, style) }

// FormatSize delegates to the package-level function.
func (c *MVMCli) FormatSize(sizeBytes int64) string { return FormatSize(sizeBytes) }

// FormatID delegates to the package-level function.
func (c *MVMCli) FormatID(idString string) string { return FormatID(idString) }

// FormatName delegates to the package-level function.
func (c *MVMCli) FormatName(name string, isMissing bool) string { return FormatName(name, isMissing) }

// ─── Static helper functions matching MVMCli static methods ──────────────────

// Timestamp format aliases for parsing/displaying timestamps.
// All formats use stdlib constants — no hardcoded format strings.
const (
	// SecTZ is ISO8601 with seconds and timezone — identical to time.RFC3339.
	SecTZ = time.RFC3339

	// LegacyDisplayDateTime is the old display format — identical to time.DateTime.
	LegacyDisplayDateTime = time.DateTime
)

func parseTime(isoString string) (time.Time, bool) {
	// Only RFC3339 is valid. Nano-precision variants of RFC3339 are also accepted.
	for _, f := range []string{time.RFC3339, time.RFC3339Nano} {
		t, err := time.Parse(f, isoString)
		if err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// FormatTimestamp formats an ISO timestamp as relative or full date string.
func FormatTimestamp(isoString string, style string) string {
	if isoString == "" {
		return "-"
	}

	t, ok := parseTime(isoString)
	if !ok {
		return isoString
	}

	if style == "full" {
		return t.Format(time.RFC3339)
	}

	// Relative style
	now := time.Now().UTC()
	if t.Location() != time.UTC {
		t = t.UTC()
	}
	delta := now.Sub(t)
	totalSeconds := int(delta.Seconds())

	if totalSeconds < 0 {
		return "just now"
	}
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds ago", totalSeconds)
	}
	minutes := totalSeconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm ago", minutes)
	}
	hours := minutes / 60
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := hours / 24
	if days < 7 {
		return fmt.Sprintf("%dd ago", days)
	}
	weeks := days / 7
	if weeks < 5 {
		return fmt.Sprintf("%dw ago", weeks)
	}
	months := days / 30
	if months < 12 {
		return fmt.Sprintf("%dmo ago", months)
	}
	years := days / 365
	return fmt.Sprintf("%dy ago", years)
}

// FormatSize formats bytes as human-readable size, or "-" if negative.
func FormatSize(sizeBytes int64) string {
	if sizeBytes < 0 {
		return "-"
	}
	if sizeBytes == 0 {
		return "0 B"
	}
	const unit = 1024
	if sizeBytes < unit {
		return fmt.Sprintf("%d B", sizeBytes)
	}
	div, exp := int64(unit), 0
	for n := sizeBytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	return fmt.Sprintf("%.1f %s", float64(sizeBytes)/float64(div), units[exp+1])
}

// FormatID returns the first 6 characters of a hash for display.
// Strips "SHA256:" prefix if present.
func FormatID(idString string) string {
	if strings.HasPrefix(idString, "SHA256:") {
		idString = idString[len("SHA256:"):]
	}
	if len(idString) > 6 {
		return idString[:6]
	}
	return idString
}

// FormatMarker returns "*" if isDefault, else empty string.
func FormatMarker(isDefault bool) string {
	if isDefault {
		return "*"
	}
	return ""
}

// FormatName returns name with a missing indicator suffix if not present.
// Uses ANSI red markup (only when stdout is a TTY) to match Python's
// ``[red]{name}[/]`` Rich formatting, which auto-strips when piped.
func FormatName(name string, isMissing bool) string {
	if isMissing {
		if fileInfo, _ := os.Stdout.Stat(); (fileInfo.Mode() & os.ModeCharDevice) != 0 {
			return "\033[31m" + name + "\033[0m"
		}
		return name
	}
	return name
}

// FormatEntityName returns a display-ready entity name.
// Matches Python's cli.py FormatEntityName function.
func FormatEntityName(name string) string {
	if name == "" {
		return "-"
	}
	return name
}

// FormatSettingValue formats an arbitrary setting value for display.
func FormatSettingValue(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v", v)
}

// FormatJSON marshals v to indented JSON.
func FormatJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// ─── Error display (matching Python's handle_errors + mvm_cli.error) ─────────

// FormatError returns a clean, user-friendly error string suitable for
// display to the user. Matches Python's mvm_cli.error() format:
//
//	"✗ Error: <message>"
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var de *errs.DomainError
	if errors.As(err, &de) {
		if de.Message != "" {
			return "✗ Error: " + de.Message
		}
		if de.Entity != "" {
			if isNotFoundCode(de.Code) {
				return "✗ Error: " + de.Entity + " not found"
			}
			if isAlreadyExistsCode(de.Code) {
				return "✗ Error: " + de.Entity + " already exists"
			}
		}
		return "✗ Error: " + string(de.Code)
	}
	return "✗ Error: " + err.Error()
}

func isNotFoundCode(c errs.Code) bool {
	s := string(c)
	return strings.HasSuffix(s, ".not_found") || s == "not_found"
}

func isAlreadyExistsCode(c errs.Code) bool {
	s := string(c)
	return strings.HasSuffix(s, ".already_exists") || s == "already_exists"
}

// DisplayError returns a verbose error string for debugging.
func DisplayError(err error, verbose bool) string {
	var de *errs.DomainError
	if errors.As(err, &de) {
		var b strings.Builder
		fmt.Fprintf(&b, "ERROR %s\n", de.Code)
		if de.Message != "" {
			fmt.Fprintf(&b, "│\n│   %s\n", de.Message)
		}
		if verbose && de.Err != nil {
			fmt.Fprintf(&b, "│\n│   Caused by: %v\n", de.Err)
		}
		return b.String()
	}
	return fmt.Sprintf("ERROR: %v\n", err)
}

// ─── ListingColumn (matching Python's cli/_common.py) ─────────────────────────

// ListingColumn represents a column in a listing table.
// The order of ListingColumn entries in the list determines both the
// short and long display order. Columns with LongOnly=true are hidden
// in short mode.
type ListingColumn struct {
	Header   string
	Extract  func(interface{}) string
	LongOnly bool
}

// renderListing builds and prints a listing table from column specs.
// Matches Python's render_listing() in cli/_common.py.
func renderListing(items []interface{}, columns []ListingColumn, style string, title ...string) {
	visible := columns
	if style != "long" {
		var short []ListingColumn
		for _, c := range columns {
			if !c.LongOnly {
				short = append(short, c)
			}
		}
		visible = short
	}
	headers := make([]string, len(visible))
	for i, c := range visible {
		headers[i] = c.Header
	}
	rows := make([][]string, len(items))
	for i, item := range items {
		row := make([]string, len(visible))
		for j, c := range visible {
			row[j] = c.Extract(item)
		}
		rows[i] = row
	}
	tableTitle := ""
	if len(title) > 0 {
		tableTitle = title[0]
	}
	MVMCLI.Table(headers, rows, tableTitle)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple stable sort
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
