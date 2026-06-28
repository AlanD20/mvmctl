package system

import (
	"fmt"
	"os"
	"os/exec"

	"mvmctl/internal/infra"
)

// FakerootSession manages a fakeroot state file for preserving ownership
// across multiple commands (tar extraction, mkfs.ext4).
//
// First command uses "fakeroot -s <state>" to save the pseudo-root state.
// Subsequent commands use "fakeroot -i <state>" to restore the saved state.
type FakerootSession struct {
	stateFile string
	firstRun  bool
}

// newSession creates a FakerootSession with a state file in the given directory.
func newSession(stateDir string) (*FakerootSession, error) {
	if _, err := exec.LookPath("fakeroot"); err != nil {
		return nil, fmt.Errorf("fakeroot binary not found in PATH: %w", err)
	}
	f, err := os.CreateTemp(stateDir, "fakeroot-state-*")
	if err != nil {
		return nil, fmt.Errorf("create fakeroot state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close fakeroot state file: %w", err)
	}
	return &FakerootSession{
		stateFile: f.Name(),
		firstRun:  true,
	}, nil
}

// NewFakerootSession creates a FakerootSession with the state file in the
// default fakeroot state directory managed by infra.GetFakerootStateDir().
func NewFakerootSession() (*FakerootSession, error) {
	return newSession(infra.GetFakerootStateDir())
}

// NewFakerootSessionInDir creates a FakerootSession with the state file in
// the specified directory.
func NewFakerootSessionInDir(dir string) (*FakerootSession, error) {
	return newSession(dir)
}

// Command returns the args slice prefixed with the appropriate fakeroot
// invocation. The first call uses "-s" (save state), subsequent calls use
// "-i" (import state).
func (s *FakerootSession) Command(args ...string) []string {
	if s.firstRun {
		s.firstRun = false
		return append([]string{"fakeroot", "-s", s.stateFile}, args...)
	}
	return append([]string{"fakeroot", "-i", s.stateFile}, args...)
}

// Cleanup removes the fakeroot state file.
func (s *FakerootSession) Cleanup() error {
	if err := os.Remove(s.stateFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove fakeroot state file: %w", err)
	}
	return nil
}
