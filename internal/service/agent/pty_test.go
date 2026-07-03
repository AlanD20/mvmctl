// Package agent tests internal (unexported) functions directly.
// extractResizeFrames is unexported, so it must be tested in this package.
package agent

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// --- extractResizeFrames ---
// Rationale: extractResizeFrames must scan the entire pending buffer for
// resize frames (newline-terminated JSON {"type":"resize","rows":X,"cols":Y}),
// remove them all, and return the remaining bytes with the most recent
// dimensions. The original front-only scan missed frames that appeared after
// non-resize bytes in the same read. Every boundary case is tested.

func TestExtractResizeFrames(t *testing.T) {
	tests := map[string]struct {
		pending       []byte
		wantRemaining []byte
		wantRows      int
		wantCols      int
		wantResized   bool
	}{
		"resize_frame_at_start": {
			pending:       []byte(`{"type":"resize","rows":40,"cols":80}` + "\n"),
			wantRemaining: []byte{},
			wantRows:      40,
			wantCols:      80,
			wantResized:   true,
		},
		"resize_frame_after_regular_bytes": {
			pending:       []byte("hello\n" + `{"type":"resize","rows":40,"cols":80}` + "\n"),
			wantRemaining: []byte("hello\n"),
			wantRows:      40,
			wantCols:      80,
			wantResized:   true,
		},
		"multiple_resize_frames_last_wins": {
			pending: []byte(
				`{"type":"resize","rows":10,"cols":20}` + "\n" +
					`{"type":"resize","rows":40,"cols":80}` + "\n",
			),
			wantRemaining: []byte{},
			wantRows:      40,
			wantCols:      80,
			wantResized:   true,
		},
		"no_resize_frame_unchanged": {
			pending:       []byte("hello\nworld\n"),
			wantRemaining: []byte("hello\nworld\n"),
			wantRows:      0,
			wantCols:      0,
			wantResized:   false,
		},
		"incomplete_resize_no_newline": {
			pending:       []byte(`{"type":"resize","rows":40,"cols":80}`),
			wantRemaining: []byte(`{"type":"resize","rows":40,"cols":80}`),
			wantRows:      0,
			wantCols:      0,
			wantResized:   false,
		},
		"json_looking_not_resize": {
			pending:       []byte(`{"type":"something","key":"val"}` + "\n"),
			wantRemaining: []byte(`{"type":"something","key":"val"}` + "\n"),
			wantRows:      0,
			wantCols:      0,
			wantResized:   false,
		},
		"resize_mixed_with_multiple_regular_lines": {
			pending:       []byte("line1\n" + `{"type":"resize","rows":30,"cols":60}` + "\n" + "line2\n"),
			wantRemaining: []byte("line1\nline2\n"),
			wantRows:      30,
			wantCols:      60,
			wantResized:   true,
		},
		"multiple_resizes_among_regular_lines": {
			pending: []byte(
				"a\n" + `{"type":"resize","rows":10,"cols":20}` + "\nb\n" +
					`{"type":"resize","rows":50,"cols":100}` + "\nc\n",
			),
			wantRemaining: []byte("a\nb\nc\n"),
			wantRows:      50,
			wantCols:      100,
			wantResized:   true,
		},
		"empty_buffer": {
			pending:       []byte{},
			wantRemaining: []byte{},
			wantRows:      0,
			wantCols:      0,
			wantResized:   false,
		},
		"resize_frame_after_multiple_lines": {
			pending:       []byte("first\nsecond\n" + `{"type":"resize","rows":24,"cols":80}` + "\n"),
			wantRemaining: []byte("first\nsecond\n"),
			wantRows:      24,
			wantCols:      80,
			wantResized:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotRemaining, gotRows, gotCols, gotResized := extractResizeFrames(tc.pending)

			if diff := cmp.Diff(tc.wantRemaining, gotRemaining); diff != "" {
				t.Errorf("extractResizeFrames() remaining bytes mismatch (-want +got):\n%s", diff)
			}
			if tc.wantRows != gotRows {
				t.Errorf("extractResizeFrames() rows = %d, want %d", gotRows, tc.wantRows)
			}
			if tc.wantCols != gotCols {
				t.Errorf("extractResizeFrames() cols = %d, want %d", gotCols, tc.wantCols)
			}
			if tc.wantResized != gotResized {
				t.Errorf("extractResizeFrames() resized = %v, want %v", gotResized, tc.wantResized)
			}
		})
	}
}

// --- PTY initial window size propagation ---
// Rationale: handleTTY sets the PTY window size (TIOCSWINSZ) on the master
// before calling cmd.Start(). On Linux, this propagates to the slave so the
// child process sees the correct terminal dimensions. If the size is set
// after Start() or not at all, the child may see 0x0 — the "tiny terminal"
// bug. This test validates that setting the size before Start() causes
// processes inside the PTY to report the expected dimensions.

func TestHandleTTY_SetsInitialWindowSize(t *testing.T) {
	master, slave, err := openPTY()
	require.NoError(t, err, "openPTY must succeed")
	defer master.Close()
	defer slave.Close()

	// Match the order in handleTTY: configurePTY before setting window size.
	configurePTY(slave)

	// Set a non-default window size on the master. This should propagate
	// to the slave before the child starts.
	ws := &unix.Winsize{Row: 42, Col: 100}
	err = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, ws)
	require.NoError(t, err, "TIOCSWINSZ on master must succeed")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start a child that reads the terminal size via stty(1).
	// stty size outputs "rows cols\n" to stdout.
	cmd := exec.CommandContext(ctx, "sh", "-c", "stty size")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}

	require.NoError(t, cmd.Start(), "child process must start")

	// Read output from the PTY master — this is what the slave writes.
	buf := make([]byte, 1024)
	n, err := master.Read(buf)
	require.NoError(t, err, "must read PTY master output")

	output := strings.TrimSpace(string(buf[:n]))
	assert.Equal(t, "42 100", output,
		"stty size must report the window size set before Start()")

	// Wait for the child to fully exit.
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()
	select {
	case err := <-waitErr:
		require.NoError(t, err, "child process must exit cleanly")
	case <-time.After(3 * time.Second):
		t.Fatal("child process did not exit within 3s")
	}
}
