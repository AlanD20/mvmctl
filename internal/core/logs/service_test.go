package logs_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	logs "mvmctl/internal/core/logs"
	"mvmctl/pkg/errs"
)

// --- Helpers ---

// assertCode checks that err is a DomainError with the given code.
func assertCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var de *errs.DomainError
	if errors.As(err, &de) {
		if diff := cmp.Diff(code, de.Code); diff != "" {
			t.Errorf("DomainError.Code mismatch (-want +got):\n%s", diff)
		}
	} else {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}

// --- TestReadLogLines ---
// Rationale: Covers circular buffer O(1) behavior, edge cases for zero/negative
// lines, Windows line endings, nonexistent files, and the high-water-mark case.

func TestReadLogLines(t *testing.T) {
	svc := logs.NewService()

	// Build 100-line content for the high_water_mark test case.
	var hundredLines strings.Builder
	for i := 1; i <= 100; i++ {
		hundredLines.WriteString(fmt.Sprintf("line%d\n", i))
	}

	tests := map[string]struct {
		lines   int
		content string // file content to write
		want    []string
		wantErr bool
		errCode errs.Code
		errMsg  string
	}{
		"zero_lines": {
			lines:   0,
			content: "line1\nline2\nline3\n",
			want:    []string{},
		},
		"negative_lines": {
			lines:   -1,
			content: "line1\n",
			wantErr: true,
			errCode: errs.CodeValidationFailed,
			errMsg:  "maxlen must be non-negative",
		},
		"one_line_from_one_line_file": {
			lines:   1,
			content: "only line\n",
			want:    []string{"only line"},
		},
		"read_more_than_file_has": {
			lines:   5,
			content: "line1\nline2\nline3\n",
			want:    []string{"line1", "line2", "line3"},
		},
		"circular_buffer_keeps_newest": {
			lines:   3,
			content: "line1\nline2\nline3\nline4\nline5\n",
			want:    []string{"line3", "line4", "line5"},
		},
		"high_water_mark": {
			lines:   1,
			content: hundredLines.String(),
			want:    []string{"line100"},
		},
		"empty_file": {
			lines:   5,
			content: "",
			want:    []string{},
		},
		"windows_line_endings": {
			lines:   5,
			content: "line1\r\nline2\r\n",
			want:    []string{"line1\r", "line2\r"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			filePath := filepath.Join(t.TempDir(), "test.log")
			err := os.WriteFile(filePath, []byte(tt.content), 0644)
			require.NoError(t, err, "setup: write temp file")

			got, err := svc.ReadLogLines(filePath, tt.lines)

			if tt.wantErr {
				assert.Error(t, err)
				assertCode(t, err, tt.errCode)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ReadLogLines mismatch (-want +got):\n%s", diff)
			}
		})
	}

	// --- nonexistent_file ---
	// Rationale: os.Open failure must return a wrapped CodeInternal error.
	t.Run("nonexistent_file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "does-not-exist.log")
		got, err := svc.ReadLogLines(filePath, 5)
		assert.Error(t, err)
		assertCode(t, err, errs.CodeInternal)
		assert.Contains(t, err.Error(), "error reading log file")
		assert.Nil(t, got)
		return
	})
}

// --- TestGetLogPath ---
// Rationale: Validates VM log path resolution for both boot (serial console)
// and os (firecracker log) types, including error paths for missing directories
// and missing files.

func TestGetLogPath(t *testing.T) {
	svc := logs.NewService()

	tests := map[string]struct {
		logType     string
		logFilename string
		// these fields used in setup, not want computation
		serialOutputFilename string
		setup                func(t *testing.T) (vmDir string, want string)
		wantErr              bool
		errCode              errs.Code
		errMsg               string
	}{
		"valid_boot_log": {
			logType:              "boot",
			logFilename:          "fc.log",
			serialOutputFilename: "serial.out",
			setup: func(t *testing.T) (string, string) {
				baseDir := t.TempDir()
				vmDir := filepath.Join(baseDir, "vm")
				require.NoError(t, os.MkdirAll(vmDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(vmDir, "serial.out"), nil, 0644))
				return vmDir, filepath.Join(vmDir, "serial.out")
			},
		},
		"valid_os_log": {
			logType:              "os",
			logFilename:          "fc.log",
			serialOutputFilename: "serial.out",
			setup: func(t *testing.T) (string, string) {
				baseDir := t.TempDir()
				vmDir := filepath.Join(baseDir, "vm")
				require.NoError(t, os.MkdirAll(vmDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(vmDir, "fc.log"), nil, 0644))
				return vmDir, filepath.Join(vmDir, "fc.log")
			},
		},
		"nonexistent_vm_dir": {
			logType:              "boot",
			logFilename:          "fc.log",
			serialOutputFilename: "serial.out",
			setup: func(t *testing.T) (string, string) {
				dir := filepath.Join(t.TempDir(), "nonexistent-vm")
				return dir, ""
			},
			wantErr: true,
			errCode: errs.CodeValidationFailed,
			errMsg:  "VM directory not found",
		},
		"nonexistent_log_file": {
			logType:              "boot",
			logFilename:          "fc.log",
			serialOutputFilename: "serial.out",
			setup: func(t *testing.T) (string, string) {
				baseDir := t.TempDir()
				vmDir := filepath.Join(baseDir, "vm")
				require.NoError(t, os.MkdirAll(vmDir, 0755))
				return vmDir, ""
			},
			wantErr: true,
			errCode: errs.CodeValidationFailed,
			errMsg:  "log file not found",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			vmDir, want := tt.setup(t)

			got, err := svc.GetLogPath(vmDir, tt.logType, tt.logFilename, tt.serialOutputFilename)

			if tt.wantErr {
				assert.Error(t, err)
				assertCode(t, err, tt.errCode)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}
			require.NoError(t, err)

			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("GetLogPath mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
