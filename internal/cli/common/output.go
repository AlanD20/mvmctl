// Package common provides CLI display helpers — table rendering, JSON output,
// error display, and the MVMCli singleton matching Python's “utils/cli.py:MVMCli“.
package common

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
)

// ─── Braille spinner (matching Python's Rich console.status) ──────────────────

var spinnerChars = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Progress is a simple Braille-dots spinner with Start/Stop/UpdateText API.
// Renders on stderr as a single animated character followed by the message.
type Progress struct {
	done    chan struct{}
	text    string
	stopped bool
	mu      sync.Mutex
}

// NewProgress creates a Braille-dots spinner that renders on stderr.
func NewProgress() *Progress {
	return &Progress{done: make(chan struct{}), text: ""}
}

// Start begins the spinner animation. Pass an optional initial message.
// When stderr is not a TTY, just prints the message once (no animation).
func (p *Progress) Start(msg ...string) {
	if len(msg) > 0 {
		p.text = msg[0]
	}
	if !isStderrTTY() {
		fmt.Fprintf(os.Stderr, "%s\n", p.text)
		return
	}
	go func() {
		i := 0
		for {
			select {
			case <-p.done:
				return
			default:
				p.mu.Lock()
				t := p.text
				p.mu.Unlock()
				if t != "" {
					fmt.Fprintf(os.Stderr, "\r%s %s\033[K", spinnerChars[i%len(spinnerChars)], t)
				} else {
					fmt.Fprintf(os.Stderr, "\r%s\033[K", spinnerChars[i%len(spinnerChars)])
				}
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
}

// Stop stops the spinner and clears its line from stderr.
func (p *Progress) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()
	close(p.done)
	if !isStderrTTY() {
		return
	}
	// Wait for goroutine to exit, then clear the line
	time.Sleep(time.Millisecond * 20)
	fmt.Fprintf(os.Stderr, "\r\033[K")
}

// UpdateText changes the message displayed next to the spinner.
func (p *Progress) UpdateText(text string) {
	p.mu.Lock()
	p.text = text
	p.mu.Unlock()
	if !isStderrTTY() {
		fmt.Fprintf(os.Stderr, "%s\n", text)
	}
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
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiReset  = "\033[0m"
)

// MVMCli is a centralized display output matching Python's MVMCli.
// Python uses Rich Console which auto-applies markup when stdout is a TTY
// and strips it when piped. Go uses ANSI escape codes with manual TTY checks.
type MVMCli struct{}

// Cli is the module-level singleton matching Python's mvm_cli.
// All display output goes through this single instance.
var Cli = &MVMCli{}

// NewCli returns the singleton MVMCli instance.
func NewCli() *MVMCli { return Cli }

// Error prints an error message to stderr in red.
// All errors use the same format — no "Unexpected" distinction.
func (c *MVMCli) Error(message string) {
	if isStderrTTY() {
		fmt.Fprintf(os.Stderr, "%s✗ Error:%s %s\n", ansiRed, ansiReset, message)
	} else {
		fmt.Fprintf(os.Stderr, "✗ Error: %s\n", message)
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
// Text prints a plain indented message with no color or decoration.
func (c *MVMCli) Text(message string) {
	fmt.Printf("  %s\n", message)
}

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
func (c *MVMCli) PrintDictTree(data any, title string) {
	if title != "" {
		fmt.Println(title)
	}
	c.buildTree(data, "", true)
}

func (c *MVMCli) buildTree(data any, indent string, isRoot bool) {
	switch v := data.(type) {
	case map[string]any:
		keys := infra.SortedKeys(v)
		for i, key := range keys {
			val := v[key]
			pretty := prettifyKey(key)
			connector := "├─ "
			if i == len(keys)-1 {
				connector = "└─ "
			}
			switch child := val.(type) {
			case map[string]any:
				fmt.Printf("%s%s%s\n", indent, connector, pretty)
				childIndent := indent + "   "
				if i < len(keys)-1 {
					childIndent = indent + "│  "
				}
				c.buildTree(child, childIndent, false)
			case []any:
				if len(child) > 0 {
					if _, isMapSlice := child[0].(map[string]any); isMapSlice {
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
	case []any:
		for i, item := range v {
			connector := "├─ "
			if i == len(v)-1 {
				connector = "└─ "
			}
			if m, ok := item.(map[string]any); ok {
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

func (c *MVMCli) formatLeafValue(key string, value any) string {
	if value == nil {
		return "-"
	}
	if s, ok := value.(string); ok && strings.HasSuffix(key, "_at") {
		formatted := c.FormatTimestamp(s, "full")
		if formatted != s {
			return formatted
		}
	}
	return fmt.Sprintf("%v", value)
}

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
func (c *MVMCli) FormatTimestamp(isoString string, style string) string {
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
func (c *MVMCli) FormatSize(sizeBytes int64) string {
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
func (c *MVMCli) FormatID(idString string) string {
	if strings.HasPrefix(idString, "SHA256:") {
		idString = idString[len("SHA256:"):]
	}
	return crypto.Truncate(idString, 6)
}

// FormatMarker returns "*" if isDefault, else empty string.
func (c *MVMCli) FormatMarker(isDefault bool) string {
	if isDefault {
		return "*"
	}
	return ""
}

// FormatName returns name with a missing indicator suffix if not present.
// Uses ANSI red markup (only when stdout is a TTY) to match Python's
// “[red]{name}[/]“ Rich formatting, which auto-strips when piped.
func (c *MVMCli) FormatName(name string, isMissing bool) string {
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
func (c *MVMCli) FormatEntityName(name string) string {
	if name == "" {
		return "-"
	}
	return name
}

// settingNilOverrides maps setting keys to human-readable labels for nil values.
// e.g., build_jobs = nil means "use all cores" — display as "<auto>".
var settingNilOverrides = map[string]string{
	"build_jobs": "<auto>",
}

// ToMap converts a struct with json tags to map[string]any for PrintDictTree.
func (c *MVMCli) ToMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// FormatSettingValue formats a setting value for display.
// key is the setting name used for nil-value overrides (e.g., build_jobs → "<auto>").
// Pass "" if no key-based override is needed.
func (c *MVMCli) FormatSettingValue(v any, key string) string {
	if v == nil {
		if display, ok := settingNilOverrides[key]; ok {
			return display
		}
		return "(unset)"
	}
	return fmt.Sprintf("%v", v)
}

// FormatJSON marshals v to indented JSON.
func (c *MVMCli) FormatJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// MarshalJSONDefaultStr marshals to JSON with Python's default=str semantics.
// On marshalling error, recursively converts non-serializable values to strings.
func MarshalJSONDefaultStr(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		return string(b)
	}
	v2 := convertToStringsRecursive(v)
	b, _ = json.MarshalIndent(v2, "", "  ")
	return string(b)
}

// convertToStringsRecursive recursively converts non-serializable Go types to strings.
// Handles the equivalent of Python's json.dumps(..., default=str).
func convertToStringsRecursive(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = convertToStringsRecursive(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = convertToStringsRecursive(item)
		}
		return out
	default:
		if _, err := json.Marshal(v); err != nil {
			return fmt.Sprintf("%v", v)
		}
		return v
	}
}

// ─── Error display (matching Python's handle_errors + mvm_cli.error) ─────────

// FormatError returns a clean, user-friendly error string suitable for
// display to the user. Matches Python's mvm_cli.error() format:
//
//	"✗ Error: <message>"
func (c *MVMCli) FormatError(err error) string {
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
func (c *MVMCli) DisplayError(err error, verbose bool) string {
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
	Extract  func(any) string
	LongOnly bool
}

// ResolveListingStyle resolves "short" or "long" from --long flag or user config.
// Matches Python's resolve_listing_style() in cli/_common.py exactly.
func (c *MVMCli) ResolveListingStyle(ctx context.Context, op *api.Operation, longOutput bool) string {
	if longOutput {
		return "long"
	}
	if op != nil {
		value, err := op.ConfigGet(ctx, "settings", "listing_style")
		if err == nil {
			if s, ok := value.(string); ok && s != "" {
				return s
			}
		}
	}
	return "short"
}

// RenderListing builds and prints a listing table from column specs.
// Matches Python's render_listing() in cli/_common.py.
func RenderListing[T any](items []T, columns []ListingColumn, style string, title ...string) {
	visible := columns
	if style != "long" {
		var short []ListingColumn
		for _, col := range columns {
			if !col.LongOnly {
				short = append(short, col)
			}
		}
		visible = short
	}
	headers := make([]string, len(visible))
	for i, col := range visible {
		headers[i] = col.Header
	}
	rows := make([][]string, len(items))
	for i, item := range items {
		row := make([]string, len(visible))
		for j, col := range visible {
			row[j] = col.Extract(item)
		}
		rows[i] = row
	}
	tableTitle := ""
	if len(title) > 0 {
		tableTitle = title[0]
	}
	Cli.Table(headers, rows, tableTitle)
}

// ─── RenderVersionTree ─────────────────────────────────────────────────────────

// RenderVersionTree renders a grouped tree of version items (image ls -r, kernel ls -r).
// Items are grouped by Type, sorted alphabetically, with a header row per type
// and tree-indented version rows underneath.
// The type header display uses Name from the first item, falling back to the type key.
func RenderVersionTree(versions []model.VersionInfo) {
	if len(versions) == 0 {
		return
	}

	// Group by type
	groups := make(map[string][]model.VersionInfo)
	for _, v := range versions {
		groups[v.Type] = append(groups[v.Type], v)
	}

	// Sort types alphabetically
	sortedTypes := make([]string, 0, len(groups))
	for t := range groups {
		sortedTypes = append(sortedTypes, t)
	}
	sort.Strings(sortedTypes)

	rows := make([][]string, 0)
	for _, typeKey := range sortedTypes {
		versionList := groups[typeKey]
		if len(versionList) == 0 {
			continue
		}

		// Type header: use Name from first item, fall back to type key
		typeDisplay := versionList[0].Name
		if typeDisplay == "" {
			typeDisplay = typeKey
		}
		rows = append(rows, []string{typeKey, typeDisplay})

		// Version rows with tree indent
		for j, v := range versionList {
			isLast := j == len(versionList)-1
			prefix := "  └─ "
			if !isLast {
				prefix = "  ├─ "
			}
			versionLabel := prefix + v.Version
			if v.IsPresent {
				versionLabel = prefix + "✓ " + v.Version
			}
			display := v.DisplayName
			rows = append(rows, []string{versionLabel, display})
		}
	}

	Cli.Table([]string{"Type / Version", "Description"}, rows)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// CheckArg guards for empty positional arg.
// Matches Python's MVMCli.check_name_arg() in utils/cli.py.
// Returns the validated value or an error on empty string.
func (c *MVMCli) CheckArg(cmd *cobra.Command, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("value required")
	}
	return value, nil
}

// PromptConfirm asks a yes/no question on stderr. Returns true for yes.
// Matches Python's typer.confirm(text, default=True) behavior.
// Shows [Y/n]: when defaultYes=true, [y/N]: when defaultYes=false.
// Loops on invalid input until y/yes, n/no, or empty (which returns default).
func (c *MVMCli) PromptConfirm(ctx context.Context, prompt string, defaultYes bool) (bool, error) {
	suffix := " [Y/n]: "
	if !defaultYes {
		suffix = " [y/N]: "
	}
	for {
		fmt.Fprint(os.Stderr, prompt+suffix)
		ch := make(chan string, 1)
		go func() {
			var response string
			fmt.Scanln(&response)
			ch <- strings.TrimSpace(strings.ToLower(response))
		}()

		var response string
		select {
		case <-ctx.Done():
			os.Stdin.Close()
			return false, ctx.Err()
		case response = <-ch:
		}

		switch response {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "":
			return defaultYes, nil
		default:
			fmt.Fprint(os.Stderr, "Please enter 'yes' or 'no': ")
		}
	}
}

// PromptSelect shows a numbered list of options on stderr and returns the
// selected value. Defaults to options[defaultIdx] on empty input.
func (c *MVMCli) PromptSelect(ctx context.Context, title string, options []string, defaultIdx int) (string, error) {
	c.Info(title)
	for i, opt := range options {
		c.Info(fmt.Sprintf("  %d. %s", i+1, opt))
	}
	prompt := fmt.Sprintf("Enter number [%d]: ", defaultIdx+1)
	fmt.Fprint(os.Stderr, prompt)

	ch := make(chan string, 1)
	go func() {
		var choice string
		fmt.Scanln(&choice)
		ch <- strings.TrimSpace(choice)
	}()

	select {
	case <-ctx.Done():
		os.Stdin.Close()
		return "", ctx.Err()
	case choice := <-ch:
		if choice == "" {
			return options[defaultIdx], nil
		}
		idx := 0
		if _, err := fmt.Sscan(choice, &idx); err == nil && idx >= 1 && idx <= len(options) {
			return options[idx-1], nil
		}
		return options[defaultIdx], nil
	}
}

// PromptMultiSelect shows numbered options on stderr and returns selected values
// from a comma-separated input. Returns defaultIndices on empty input.
// If defaultIndices is nil, defaults to the first option.
func (c *MVMCli) PromptMultiSelect(
	ctx context.Context,
	title string,
	options []string,
	defaultIndices []int,
) ([]string, error) {
	c.Info(title)
	for i, opt := range options {
		c.Info(fmt.Sprintf("  [%d] %s", i+1, opt))
	}
	def := 1
	if len(defaultIndices) > 0 {
		def = defaultIndices[0] + 1
	}
	fmt.Fprintf(os.Stderr, "Select number(s) [comma-separated] [%d]: ", def)

	ch := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		ch <- input
	}()

	var input string
	select {
	case <-ctx.Done():
		os.Stdin.Close()
		return nil, ctx.Err()
	case input = <-ch:
	}

	input = strings.TrimSpace(input)

	if input == "" {
		if len(defaultIndices) > 0 {
			selected := make([]string, len(defaultIndices))
			for i, idx := range defaultIndices {
				selected[i] = options[idx]
			}
			return selected, nil
		}
		return []string{options[0]}, nil
	}

	var selected []string
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
			return nil, fmt.Errorf("invalid selection: %s", input)
		}
		if idx < 1 || idx > len(options) {
			return nil, fmt.Errorf("invalid index: %d (options are 1-%d)", idx, len(options))
		}
		selected = append(selected, options[idx-1])
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no valid selections")
	}
	return selected, nil
}
