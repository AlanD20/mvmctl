package infra

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ProgressOutput is the writer used for progress bar and spinner output.
// Defaults to os.Stdout. The CLI layer may override this to redirect progress
// output (e.g., to a structured log sink or a different file descriptor).
// Per Verdict #26: infrastructure progress bars are user-facing; this hook
// lets cli/common/ control where the output goes.
var ProgressOutput io.Writer = os.Stdout

// --- ASCIIProgressBar ---

// ASCIIProgressBar renders an ASCII progress bar for TTY and non-TTY environments.
// Displays: [####      ] 45% (4.2MB/10MB)
type ASCIIProgressBar struct {
	total       int64
	width       int
	title       string
	current     int64
	lastPercent int
	lastLineLen int
	isTTY       bool
	mu          sync.Mutex // guards bar state (current, lastPercent, lastLineLen)
}

// NewASCIIProgressBar creates a new ASCII progress bar.
func NewASCIIProgressBar(total int64, width int, title string) *ASCIIProgressBar {
	if width <= 0 {
		width = 40
	}
	if title == "" {
		title = "Downloading"
	}
	isTTY := false
	if fd := int(os.Stdout.Fd()); fd >= 0 {
		isTTY = isTerminal(fd)
	}
	return &ASCIIProgressBar{
		total:       total,
		width:       width,
		title:       title,
		lastPercent: -1,
		isTTY:       isTTY,
	}
}

// isTerminal checks if the given file descriptor is a terminal.
// Uses ioctl TCGETS which is equivalent to tcgetattr(3) on Linux.
func isTerminal(fd int) bool {
	var termios syscall.Termios
	_, _, err := syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		syscall.TCGETS,
		uintptr(unsafe.Pointer(&termios)),
		0,
		0,
		0,
	)
	return err == 0
}

// Update advances the progress bar by n bytes and redraws.
func (p *ASCIIProgressBar) Update(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current += n
	p.display()
}

// Finish prints final completion message.
func (p *ASCIIProgressBar) Finish() {
	if p.isTTY {
		ProgressOutput.Write([]byte("\r\033[K"))
	}
	slog.Info("progress complete", "title", p.title)
}

func (p *ASCIIProgressBar) display() {
	var percent int
	if p.total == 0 {
		percent = 0
	} else {
		percent = int(min(100, int64(100*p.current/p.total)))
	}

	if percent == p.lastPercent {
		return
	}

	termWidth := getTermWidth()
	filled := int(p.width * percent / 100)
	bar := strings.Repeat("#", filled) + strings.Repeat(" ", p.width-filled)
	line := fmt.Sprintf("%s [%s] %d%%", p.title, bar, percent)
	if p.total > 0 {
		line += fmt.Sprintf(" (%s/%s)", formatBytes(p.current), formatBytes(p.total))
	}

	if len(line) > termWidth-1 {
		line = line[:termWidth-1]
	}

	var terminator string
	if p.isTTY {
		terminator = "\r\033[K"
	} else {
		terminator = "\n"
	}

	output := terminator + line

	// os.Stdout.Write writes directly to fd 1 (unbuffered), so both TTY and
	// non-TTY paths use the same write. No Sync() call needed.
	ProgressOutput.Write([]byte(output))

	p.lastLineLen = len(line)
	p.lastPercent = percent
}

// getTermWidth returns terminal width, defaulting to 80.
func getTermWidth() int {
	var ws struct {
		Row    uint16
		Col    uint16
		XPixel uint16
		YPixel uint16
	}
	_, _, _ = syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(os.Stdout.Fd()),
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&ws)),
	)
	if ws.Col > 0 {
		return int(ws.Col)
	}
	return 80
}

// formatBytes formats bytes to human-readable (B, KB, MB, GB).
func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	f := float64(size)
	if f < 1024*1024 {
		return fmt.Sprintf("%.1fKB", f/1024)
	}
	if f < 1024*1024*1024 {
		return fmt.Sprintf("%.1fMB", f/(1024*1024))
	}
	return fmt.Sprintf("%.1fGB", f/(1024*1024*1024))
}

// --- Spinner ---

// Spinner is a threaded ASCII spinner for indeterminate progress.
// Displays a rotating Braille character with a message on a single line.
type Spinner struct {
	message string
	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex // guards started/stopped flags
	started bool
	stopped bool // prevents double-close panic on second Stop() call
}

// NewSpinner creates a new Spinner with the given message.
func NewSpinner(message string) *Spinner {
	if message == "" {
		message = "Processing"
	}
	return &Spinner{
		message: message,
	}
}

// frames are the spinner animation frames (Braille characters).
var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Start begins the spinner in a background goroutine.
// Creates channels once on first call; subsequent calls are no-ops.
func (s *Spinner) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	s.stopped = false
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})

	go func() {
		idx := 0
		for {
			select {
			case <-s.stopCh:
				close(s.doneCh)
				return
			default:
				frame := frames[idx%len(frames)]
				line := fmt.Sprintf("%s %s...", frame, s.message)
				ProgressOutput.Write([]byte(fmt.Sprintf("\r\033[K%s", line)))
				idx++
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
}

// Stop halts the spinner and optionally prints a completion message.
// Safe to call multiple times (subsequent calls are no-ops).
// Uses stopped guard to prevent double-close panic.
func (s *Spinner) Stop(doneMessage string) {
	s.mu.Lock()
	if s.stopped || !s.started {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.stopCh)
	s.mu.Unlock()

	select {
	case <-s.doneCh:
	case <-time.After(200 * time.Millisecond):
	}
	ProgressOutput.Write([]byte("\r\033[K"))
	if doneMessage != "" {
		slog.Info("progress done", "message", doneMessage)
	}
}

// WithSpinner starts a spinner, runs fn, and stops the spinner when fn returns.
func WithSpinner(message string, fn func()) {
	s := NewSpinner(message)
	s.Start()
	defer s.Stop("")
	fn()
}
