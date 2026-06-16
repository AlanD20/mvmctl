package common

// Internal package: tests both exported and unexported functions.
// Unexported functions (e.g., prettifyKey, toTitle) cannot be accessed
// from an external test package.

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

// ─── prettifyKey ─────────────────────────────────────────────────────────────
// Rationale: prettifyKey is used for every key displayed in tree/error output.
// A regression here would show raw underscored keys ("vm.not_found") instead of
// user-friendly variants ("VM Not Found"), degrading the CLI UX.

func TestPrettifyKey(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		// Boundary / edge cases
		"empty_string":      {input: "", want: ""},
		"already_exists":    {input: "already_exists", want: "Already Exists"},
		"plain_single_word": {input: "error", want: "Error"},

		// Happy paths — dotted keys with no underscores (dots preserved)
		"dotted_key": {input: "network.subnet.overlap", want: "Network.subnet.overlap"},

		// Happy paths — underscore keys trigger acronym prettification
		// Note: dots are NOT replaced by prettifyKey, so "vm.not" → "VM.not"
		"not_found_suffix": {input: "vm.not_found", want: "VM.not Found"},
		"id_acronym":       {input: "vm_id", want: "VM ID"},
		"ssh_acronym":      {input: "ssh_key", want: "SSH Key"},
		"ipv_prefix":       {input: "ipv4_address", want: "IPv4 Address"},
		"ipv6_prefix":      {input: "ipv6_address", want: "IPv6 Address"},
		"mac_acronym":      {input: "mac_address", want: "MAC Address"},
		"pid_acronym":      {input: "process_pid", want: "Process PID"},
		"uuid_acronym":     {input: "disk_uuid", want: "Disk UUID"},
		"nat_acronym":      {input: "enable_nat", want: "Enable NAT"},
		"tap_acronym":      {input: "tap_device", want: "TAP Device"},
		"vm_singular":      {input: "vm_state", want: "VM State"},
		"vms_plural":       {input: "running_vms", want: "Running VM"},
		"cpu_singular":     {input: "cpu_count", want: "CPU Count"},
		"cpus_plural":      {input: "num_cpus", want: "Num CPU"},
		"kvm_acronym":      {input: "use_kvm", want: "Use KVM"},
		"os_acronym":       {input: "host_os", want: "Host OS"},
		"pci_acronym":      {input: "pci_device", want: "PCI Device"},
		"tmpfs_acronym":    {input: "tmpfs_size", want: "TMPFS Size"},
		"fs_acronym":       {input: "fs_type", want: "FS Type"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := prettifyKey(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("prettifyKey() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── toTitle ─────────────────────────────────────────────────────────────────
// Rationale: toTitle is the foundation for prettifyKey and any title-casing
// output. It capitalises the first letter of each word without lowercasing
// the rest, which preserves acronyms but must not strip or corrupt input.

func TestToTitle(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		// Boundary / edge cases
		"empty_string": {input: "", want: ""},
		"single_lower": {input: "a", want: "A"},
		"single_upper": {input: "A", want: "A"},

		// Happy paths
		"two_words":        {input: "hello world", want: "Hello World"},
		"all_upper":        {input: "HELLO", want: "HELLO"},
		"mixed_case":       {input: "hELLO wORLD", want: "HELLO WORLD"},
		"three_words":      {input: "one two three", want: "One Two Three"},
		"collapsed_spaces": {input: "multiple   spaces", want: "Multiple Spaces"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := toTitle(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("toTitle() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── parseTime ───────────────────────────────────────────────────────────────
// Rationale: parseTime is the single entry point for timestamp parsing across
// the CLI. A regression here would cause timestamps to appear as raw strings
// ("-") or produce incorrect relative time display.

func TestParseTime(t *testing.T) {
	tests := map[string]struct {
		input  string
		want   time.Time
		wantOK bool
	}{
		// Invalid / boundary cases FIRST
		"empty_string": {input: "", wantOK: false},
		"not_a_date":   {input: "not-a-date", wantOK: false},
		"no_timezone":  {input: "2023-06-15T10:30:00", wantOK: false},
		"rfc1123":      {input: "Mon, 15 Jun 2023 10:30:00 GMT", wantOK: false},

		// Valid cases
		"rfc3339": {
			input:  "2023-06-15T10:30:00Z",
			want:   time.Date(2023, 6, 15, 10, 30, 0, 0, time.UTC),
			wantOK: true,
		},
		"rfc3339_nano": {
			input:  "2023-06-15T10:30:00.123456789Z",
			want:   time.Date(2023, 6, 15, 10, 30, 0, 123456789, time.UTC),
			wantOK: true,
		},
		"rfc3339_utc_positive": {
			input:  "2023-06-15T12:30:00+02:00",
			want:   time.Date(2023, 6, 15, 10, 30, 0, 0, time.UTC),
			wantOK: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := parseTime(tc.input)

			if !tc.wantOK {
				assert.False(t, ok)
				assert.True(t, got.IsZero(), "expected zero time on parse failure")
				return
			}

			assert.True(t, ok)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseTime() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── isNotFoundCode ──────────────────────────────────────────────────────────
// Rationale: isNotFoundCode controls the "not found" branch in FormatError and
// HandleErrors. A false negative would display raw code strings instead of
// user-friendly "entity not found" messages.

func TestIsNotFoundCode(t *testing.T) {
	tests := map[string]struct {
		input errs.Code
		want  bool
	}{
		// False cases FIRST
		"empty":                   {input: "", want: false},
		"success_code":            {input: "vm.created", want: false},
		"already_exists":          {input: "vm.already_exists", want: false},
		"partial_suffix_no_match": {input: "vm.not_found.extra", want: false},
		"no_dot_prefix":           {input: "ismissing", want: false},

		// True cases
		"vm_not_found":      {input: errs.CodeVMNotFound, want: true},
		"network_not_found": {input: errs.CodeNetworkNotFound, want: true},
		"bare_not_found":    {input: "not_found", want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := isNotFoundCode(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("isNotFoundCode() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── isAlreadyExistsCode ─────────────────────────────────────────────────────
// Rationale: isAlreadyExistsCode controls the "already exists" branch in
// FormatError. A false negative would display raw code strings instead of
// user-friendly "entity already exists" messages.

func TestIsAlreadyExistsCode(t *testing.T) {
	tests := map[string]struct {
		input errs.Code
		want  bool
	}{
		// False cases FIRST
		"empty":                   {input: "", want: false},
		"not_found":               {input: "vm.not_found", want: false},
		"partial_suffix_no_match": {input: "vm.already_exists.extra", want: false},

		// True cases
		"vm_already_exists":      {input: errs.CodeVMAlreadyExists, want: true},
		"network_already_exists": {input: errs.CodeNetworkAlreadyExists, want: true},
		"bare_already_exists":    {input: "already_exists", want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := isAlreadyExistsCode(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("isAlreadyExistsCode() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── FormatTimestamp ─────────────────────────────────────────────────────────
// Rationale: FormatTimestamp renders all timestamps in CLI output (list, inspect,
// tree views). A regression here would display raw ISO strings to users or show
// incorrect relative durations, creating confusion.

func TestFormatTimestamp(t *testing.T) {
	// Deterministic test — fixed timestamp with style="full"
	t.Run("full_style", func(t *testing.T) {
		ts := time.Date(2023, 6, 15, 10, 30, 0, 0, time.UTC)
		got := Cli.FormatTimestamp(ts.Format(time.RFC3339), "full")
		want := "2023-06-15T10:30:00Z"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("FormatTimestamp() full mismatch (-want +got):\n%s", diff)
		}
	})

	// Full style with nano-precision input
	t.Run("full_nano", func(t *testing.T) {
		ts := time.Date(2023, 6, 15, 10, 30, 0, 123456789, time.UTC)
		got := Cli.FormatTimestamp(ts.Format(time.RFC3339Nano), "full")
		want := ts.Format(time.RFC3339) // full style always uses RFC3339 (second precision)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("FormatTimestamp() full nano mismatch (-want +got):\n%s", diff)
		}
	})

	// Empty string
	t.Run("empty_string_returns_dash", func(t *testing.T) {
		got := Cli.FormatTimestamp("", "full")
		if diff := cmp.Diff("-", got); diff != "" {
			t.Errorf("FormatTimestamp() empty mismatch (-want +got):\n%s", diff)
		}
	})

	// Invalid format returns raw string unchanged
	t.Run("invalid_format_returns_raw", func(t *testing.T) {
		got := Cli.FormatTimestamp("not-a-date", "full")
		if diff := cmp.Diff("not-a-date", got); diff != "" {
			t.Errorf("FormatTimestamp() invalid mismatch (-want +got):\n%s", diff)
		}
	})

	// Relative style — current time
	t.Run("now_relative", func(t *testing.T) {
		now := time.Now().UTC()
		got := Cli.FormatTimestamp(now.Format(time.RFC3339), "relative")
		// Should be "0s ago", "just now", or within a few seconds
		assert.Contains(t, got, " ago")
	})

	// Relative style — 1 hour ago
	t.Run("one_hour_ago", func(t *testing.T) {
		oneHourAgo := time.Now().UTC().Add(-1 * time.Hour)
		got := Cli.FormatTimestamp(oneHourAgo.Format(time.RFC3339), "relative")
		// Should be "1h ago" or "60m ago" depending on exact timing
		assert.Contains(t, got, " ago")
	})

	// Relative style — 1 day ago
	t.Run("one_day_ago", func(t *testing.T) {
		oneDayAgo := time.Now().UTC().Add(-24 * time.Hour)
		got := Cli.FormatTimestamp(oneDayAgo.Format(time.RFC3339), "relative")
		// Should be "24h ago", "1d ago", or similar
		assert.Contains(t, got, " ago")
	})

	// Invalid kind defaults to relative
	t.Run("invalid_kind", func(t *testing.T) {
		now := time.Now().UTC()
		got := Cli.FormatTimestamp(now.Format(time.RFC3339), "unknown_kind")
		// Unknown kind should fall back to relative style
		assert.Contains(t, got, " ago")
	})

	// Zero time
	t.Run("zero_time", func(t *testing.T) {
		var zero time.Time
		got := Cli.FormatTimestamp(zero.Format(time.RFC3339), "full")
		want := "0001-01-01T00:00:00Z"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("FormatTimestamp() zero mismatch (-want +got):\n%s", diff)
		}
	})
}

// ─── FormatSize ──────────────────────────────────────────────────────────────
// Rationale: FormatSize is used in every list/detail display for storage sizes
// (images, volumes, kernels). Wrong formatting would mislead users about disk
// usage.

func TestFormatSize(t *testing.T) {
	tests := map[string]struct {
		input int64
		want  string
	}{
		// Negative / boundary cases FIRST
		"negative": {input: -1, want: "-"},
		"zero":     {input: 0, want: "0 B"},

		// Happy paths
		"under_1kib":   {input: 1023, want: "1023 B"},
		"exactly_1kib": {input: 1024, want: "1.0 KiB"},
		"exactly_1mib": {input: 1 << 20, want: "1.0 MiB"},
		"exactly_1gib": {input: 1 << 30, want: "1.0 GiB"},
		"exactly_1tib": {input: int64(1) << 40, want: "1.0 TiB"},
		"one_byte":     {input: 1, want: "1 B"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Cli.FormatSize(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FormatSize() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── FormatID ────────────────────────────────────────────────────────────────
// Rationale: FormatID is used to display truncated hashes in every list output
// (vm ls, image ls, etc.). A regression would show full-length hashes or
// corrupt display strings.

func TestFormatID(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		// Boundary cases FIRST
		"empty":          {input: "", want: ""},
		"shorter_than_6": {input: "ab", want: "ab"},
		"exactly_6":      {input: "abcdef", want: "abcdef"},

		// Happy paths
		"truncated":     {input: "abc123def456", want: "abc123"},
		"sha256_prefix": {input: "SHA256:abc123def456", want: "abc123"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Cli.FormatID(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FormatID() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── MarshalJSONDefaultStr ───────────────────────────────────────────────────
// Rationale: MarshalJSONDefaultStr is used by the API layer to serialise
// responses. A regression could cause empty output "{}" for valid data or
// crash on non-serialisable types.

func TestMarshalJSONDefaultStr(t *testing.T) {
	t.Run("nil_returns_null", func(t *testing.T) {
		got := MarshalJSONDefaultStr(nil)
		want := "null"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("MarshalJSONDefaultStr(nil) mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("struct_returns_json", func(t *testing.T) {
		v := struct {
			Name  string `json:"name"`
			Value int    `json:"value"`
		}{Name: "test", Value: 42}
		got := MarshalJSONDefaultStr(v)
		want := "{\n  \"name\": \"test\",\n  \"value\": 42\n}"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("MarshalJSONDefaultStr(struct) mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("string_returns_quoted", func(t *testing.T) {
		got := MarshalJSONDefaultStr("hello")
		want := "\"hello\""
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("MarshalJSONDefaultStr(string) mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("map_with_unserializable_fallback", func(t *testing.T) {
		// A map containing a channel cannot be marshalled directly;
		// the fallback converts the channel to its string representation.
		v := map[string]any{"key": "value", "ch": make(chan int)}
		got := MarshalJSONDefaultStr(v)
		assert.Contains(t, got, "\"key\": \"value\"")
		assert.Contains(t, got, "\"ch\": ")
	})

	t.Run("integer_returns_number", func(t *testing.T) {
		got := MarshalJSONDefaultStr(42)
		want := "42"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("MarshalJSONDefaultStr(int) mismatch (-want +got):\n%s", diff)
		}
	})
}

// ─── isBrokenPipe ────────────────────────────────────────────────────────────
// Rationale: isBrokenPipe determines whether HandleErrors swallows the error
// and exits with code 0 (matching Python's BrokenPipeError handling).
// A false negative would cause CLI error spew on pipe close.

func TestIsBrokenPipe(t *testing.T) {
	tests := map[string]struct {
		input func() error // thunk to construct error at test time
		want  bool
	}{
		// False cases FIRST
		"nil_panics": {
			input: func() error { return nil }, // would panic at err.Error()
			want:  false,
		},
		"non_pipe_error": {
			input: func() error { return errors.New("connection refused") },
			want:  false,
		},
		"empty_message": {
			input: func() error { return errors.New("") },
			want:  false,
		},

		// True cases
		"syscall_epipe": {
			input: func() error { return syscall.EPIPE },
			want:  true,
		},
		"io_closed_pipe": {
			input: func() error { return io.ErrClosedPipe },
			want:  true,
		},
		"wrapped_epipe": {
			input: func() error { return fmt.Errorf("write: %w", syscall.EPIPE) },
			want:  true,
		},
		"message_contains_broken_pipe": {
			input: func() error { return errors.New("write: broken pipe") },
			want:  true,
		},
		"message_contains_Broken_pipe": {
			input: func() error { return errors.New("Broken pipe occurred") },
			want:  true,
		},
		"message_contains_broken_pipe_suffix": {
			input: func() error { return errors.New("broken pipe") },
			want:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got bool
			if tc.input() == nil {
				// nil error causes panic in isBrokenPipe (err.Error() on nil)
				// Test that we recognise the panic behaviour
				require.Panics(t, func() { isBrokenPipe(nil) },
					"isBrokenPipe(nil) should panic due to nil.Error() call")
				return
			}
			got = isBrokenPipe(tc.input())
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("isBrokenPipe() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── isDatabaseError ─────────────────────────────────────────────────────────
// Rationale: isDatabaseError routes errors to database-specific error messages
// in HandleErrors ("Run 'mvm init' first", etc.). A false negative would show
// a generic unexpected error instead of a helpful init hint.

func TestIsDatabaseError(t *testing.T) {
	tests := map[string]struct {
		input func() error
		want  bool
	}{
		// False cases FIRST
		"nil_panics": {
			input: func() error { return nil },
			want:  false,
		},
		"connection_refused": {
			input: func() error { return errors.New("connection refused") },
			want:  false,
		},
		"random_message": {
			input: func() error { return errors.New("something went wrong") },
			want:  false,
		},

		// True cases
		"database_is_locked": {
			input: func() error { return errors.New("database is locked") },
			want:  true,
		},
		"no_such_table": {
			input: func() error { return errors.New("no such table: vms") },
			want:  true,
		},
		"no_such_column": {
			input: func() error { return errors.New("no such column: name") },
			want:  true,
		},
		"unique_constraint": {
			input: func() error { return errors.New("UNIQUE constraint failed: vm.name") },
			want:  true,
		},
		"foreign_key_constraint": {
			input: func() error { return errors.New("FOREIGN KEY constraint failed") },
			want:  true,
		},
		"table_already_exists": {
			input: func() error { return errors.New("table already exists") },
			want:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.input() == nil {
				require.Panics(t, func() { isDatabaseError(nil) },
					"isDatabaseError(nil) should panic due to nil.Error() call")
				return
			}
			got := isDatabaseError(tc.input())
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("isDatabaseError() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── FormatSettingValue ──────────────────────────────────────────────────────
// Rationale: FormatSettingValue renders setting values in config display
// ("mvm config show"). A regression would show "<nil>" or other internal
// representations instead of human-friendly overrides like "<auto>".

func TestFormatSettingValue(t *testing.T) {
	tests := map[string]struct {
		value any
		key   string
		want  string
	}{
		// Boundary / nil cases FIRST
		"nil_with_known_override": {value: nil, key: "build_jobs", want: "<auto>"},
		"nil_unknown_key":         {value: nil, key: "unknown", want: "(unset)"},
		"nil_empty_key":           {value: nil, key: "", want: "(unset)"},

		// Happy paths
		"string_value": {value: "hello", key: "", want: "hello"},
		"int_value":    {value: 42, key: "", want: "42"},
		"bool_value":   {value: true, key: "", want: "true"},
		"zero_int":     {value: 0, key: "", want: "0"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Cli.FormatSettingValue(tc.value, tc.key)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FormatSettingValue() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
