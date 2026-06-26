package system_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/system"
)

// --- ParseProcStatusField ---
// Rationale: Must extract integer values from /proc/[pid]/status format.
// Returns -1 for missing fields or unparseable values.

func TestParseProcStatusField(t *testing.T) {
	procStatus := "Name:\tbash\n" +
		"Umask:\t0022\n" +
		"State:\tS (sleeping)\n" +
		"Tgid:\t1234\n" +
		"Ngid:\t0\n" +
		"Pid:\t1234\n" +
		"PPid:\t1\n" +
		"TracerPid:\t0\n" +
		"Uid:\t1000\t1000\t1000\t1000\n" +
		"Gid:\t1000\t1000\t1000\t1000\n" +
		"FDSize:\t256\n" +
		"Threads:\t1\n" +
		"VmPeak:\t100000 kB\n" +
		"VmSize:\t90000 kB\n" +
		"VmRSS:\t20000 kB\n"

	tests := []struct {
		name  string
		data  string
		field string
		want  int
	}{
		{
			name:  "pid_field",
			data:  procStatus,
			field: "Pid:",
			want:  1234,
		},
		{
			name:  "ppid_field",
			data:  procStatus,
			field: "PPid:",
			want:  1,
		},
		{
			name:  "threads_field",
			data:  procStatus,
			field: "Threads:",
			want:  1,
		},
		{
			name:  "fdsize_field",
			data:  procStatus,
			field: "FDSize:",
			want:  256,
		},
		{
			name:  "tracerpid_zero",
			data:  procStatus,
			field: "TracerPid:",
			want:  0,
		},
		{
			name:  "missing_field_returns_minus_one",
			data:  procStatus,
			field: "NonExistent:",
			want:  -1,
		},
		{
			name:  "empty_data_returns_minus_one",
			data:  "",
			field: "Pid:",
			want:  -1,
		},
		{
			name:  "field_with_units_returns_first_token",
			data:  procStatus,
			field: "VmPeak:",
			want:  100000,
		},
		{
			name:  "field_with_trailing_text_parses_number",
			data:  procStatus,
			field: "State:",
			want:  -1, // "S (sleeping)" is not parseable as int
		},
		{
			name:  "multi_value_field_returns_first_number",
			data:  procStatus,
			field: "Uid:",
			want:  1000,
		},
		{
			name:  "field_without_colon_matches_anyway",
			data:  "SomeField 42\n",
			field: "SomeField",
			want:  42, // HasPrefix doesn't require colon — just checks prefix
		},
		{
			name:  "field_with_colon_in_name",
			data:  "Field:With:Colon 99\n",
			field: "Field:With:Colon",
			want:  99,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := system.ParseProcStatusField(tt.data, tt.field)
			assert.Equal(t, tt.want, got)
		})
	}
}
