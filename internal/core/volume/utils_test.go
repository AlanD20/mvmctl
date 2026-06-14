package volume

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

// ─── VolumesToDrives ────────────────────────────────────────────────────────
// Rationale: Converts VolumeItem slice to Firecracker DriveConfig slice.

func TestVolumesToDrives(t *testing.T) {
	t.Run("nil_volume_skipped", func(t *testing.T) {
		vols := []*model.VolumeItem{nil, {ID: "v1", Path: "/dev/vda"}}
		got := VolumesToDrives(vols)
		assert.Len(t, got, 1)
		assert.Equal(t, "v1", got[0].DriveID)
	})

	t.Run("empty_list_returns_empty", func(t *testing.T) {
		got := VolumesToDrives(nil)
		assert.Empty(t, got)
	})

	t.Run("single_volume_has_correct_defaults", func(t *testing.T) {
		vols := []*model.VolumeItem{{
			ID:   "root",
			Path: "/dev/vda",
		}}
		got := VolumesToDrives(vols)
		require.Len(t, got, 1)
		want := []model.DriveConfig{{
			DriveID:      "root",
			PathOnHost:   "/dev/vda",
			IsRootDevice: false,
			IsReadOnly:   false,
			CacheType:    "Unsafe",
			IOEngine:     "Sync",
		}}
		assert.Empty(t, cmp.Diff(want, got))
	})

	t.Run("is_read_only_propagated", func(t *testing.T) {
		vols := []*model.VolumeItem{{
			ID:         "data",
			Path:       "/dev/vdb",
			IsReadOnly: true,
		}}
		got := VolumesToDrives(vols)
		require.Len(t, got, 1)
		assert.True(t, got[0].IsReadOnly)
	})

	t.Run("multiple_volumes_preserve_order", func(t *testing.T) {
		vols := []*model.VolumeItem{
			{ID: "a", Path: "/dev/vda"},
			{ID: "b", Path: "/dev/vdb"},
			{ID: "c", Path: "/dev/vdc"},
		}
		got := VolumesToDrives(vols)
		require.Len(t, got, 3)
		assert.Equal(t, "a", got[0].DriveID)
		assert.Equal(t, "b", got[1].DriveID)
		assert.Equal(t, "c", got[2].DriveID)
	})
}

// ─── formatProcessError ─────────────────────────────────────────────────────
// Rationale: Formats subprocess errors matching Python's ProcessError format.

func TestFormatProcessError(t *testing.T) {
	t.Run("exit_error_with_stderr", func(t *testing.T) {
		exitErr := runAndGetExitErr(t, 42)
		msg := formatProcessError("mycmd", "something went wrong", exitErr)
		assert.Contains(t, msg, "Command failed (exit 42): mycmd")
		assert.Contains(t, msg, "something went wrong")
	})

	t.Run("exit_error_with_fallback_stderr", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "echo fallback-stderr >&2; exit 1")
		var buf bytes.Buffer
		cmd.Stderr = &buf
		err := cmd.Run()
		exitErr := &exec.ExitError{ProcessState: err.(*exec.ExitError).ProcessState, Stderr: buf.Bytes()}
		msg := formatProcessError("cmd", "", exitErr)
		assert.Contains(t, msg, "Command failed (exit 1): cmd")
		assert.Contains(t, msg, "fallback-stderr")
	})

	t.Run("command_not_found", func(t *testing.T) {
		msg := formatProcessError("nonexistent-binary", "", exec.ErrNotFound)
		assert.Equal(t, "Command not found: nonexistent-binary", msg)
	})

	t.Run("other_error_returns_raw", func(t *testing.T) {
		ctxErr := context.Canceled
		msg := formatProcessError("cmd", "", ctxErr)
		assert.Equal(t, context.Canceled.Error(), msg)
	})

	t.Run("exit_code_255", func(t *testing.T) {
		exitErr := runAndGetExitErr(t, 255)
		msg := formatProcessError("cmd", "err msg", exitErr)
		assert.Contains(t, msg, "Command failed (exit 255): cmd")
	})
}

// runAndGetExitErr runs `sh -c "exit N"` and returns the *exec.ExitError.
func runAndGetExitErr(t *testing.T, exitCode int) *exec.ExitError {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit "+itoa(exitCode))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr
	}
	t.Fatalf("expected ExitError for exit %d, got %T: %v", exitCode, err, err)
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─── sanitizeStderr ─────────────────────────────────────────────────────────
// Rationale: Strips and truncates stderr output to 100 characters.

func TestSanitizeStderr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "short_string", input: "hello", want: "hello"},
		{name: "long_string_truncated", input: string(make([]byte, 150)), want: string(make([]byte, 100)) + "..."},
		{name: "empty_string", input: "", want: ""},
		{name: "whitespace_trimmed", input: "  hello  ", want: "hello"},
		{name: "exactly_100_chars", input: string(make([]byte, 100)), want: string(make([]byte, 100))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeStderr(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}
