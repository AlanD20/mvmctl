package workflow

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

// ── File locking ──

// acquireLock opens or creates a .lock file in the given directory and
// acquires a POSIX advisory lock (flock). exclusive=true acquires LOCK_EX,
// false acquires LOCK_SH. Returns a release function that must be called to
// unlock and close the lock file. The dir must already exist.
func acquireLock(dir string, exclusive bool) (func(), error) {
	lockPath := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_RDONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}

	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}

	if err := unix.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock on %s: %w", lockPath, err)
	}

	var released bool
	return func() {
		if released {
			return
		}
		released = true
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}

// ── State file helpers ──

// marshalState serialises the state to YAML bytes using 2-space indent.
// If the state has a ContentHash it is written, otherwise omitted.
func marshalState(state *model.WorkflowState) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(state); err != nil {
		return nil, err
	}
	enc.Close()
	return buf.Bytes(), nil
}

// ── WriteWorkflowState ──

// WriteWorkflowState persists a WorkflowState to a YAML file within dir.
// Creates dir if it does not exist. Writes are atomic (write to .tmp,
// rename to state.yaml), include a content hash for integrity, create a
// .lock file for inter-process coordination, and backup the previous
// state.yaml to state.yaml.backup before overwriting.
func WriteWorkflowState(dir string, state *model.WorkflowState) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create workflow state dir %s: %w", dir, err)
	}

	statePath := filepath.Join(dir, "state.yaml")
	backupPath := filepath.Join(dir, "state.yaml.backup")

	// Acquire exclusive lock.
	release, err := acquireLock(dir, true)
	if err != nil {
		return fmt.Errorf("lock workflow state: %w", err)
	}
	defer release()

	// Backup existing state file before overwriting.
	if _, statErr := os.Stat(statePath); statErr == nil {
		if cpErr := infra.CopyFile(statePath, backupPath); cpErr != nil {
			return fmt.Errorf("backup state file %s: %w", statePath, cpErr)
		}
	}

	// Write to a temporary file first, then rename atomically.
	tmpPath := statePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp state file %s: %w", tmpPath, err)
	}

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(state); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode workflow state: %w", err)
	}
	enc.Close()
	f.Close()

	// Atomically replace the state file.
	if err := os.Rename(tmpPath, statePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename state file %s -> %s: %w", tmpPath, statePath, err)
	}

	return nil
}

// ── ReadWorkflowState ──

// ReadWorkflowState reads a WorkflowState from a state.yaml file in dir.
// Acquires a shared lock if a .lock file exists. Verifies the ContentHash
// if present in the file. Returns an integrity error on hash mismatch.
func ReadWorkflowState(dir string) (*model.WorkflowState, error) {
	statePath := filepath.Join(dir, "state.yaml")

	// Acquire shared lock (best-effort — if .lock doesn't exist, skip).
	lockPath := filepath.Join(dir, ".lock")
	if _, statErr := os.Stat(lockPath); statErr == nil {
		release, err := acquireLock(dir, false)
		if err != nil {
			return nil, fmt.Errorf("acquire shared lock: %w", err)
		}
		defer release()
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("read state file %s: %w", statePath, err)
	}

	var state model.WorkflowState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal workflow state: %w", err)
	}

	// Verify content hash if present.
	if state.ContentHash != "" {
		expected := state.ContentHash
		state.ContentHash = ""
		data, err := marshalState(&state)
		state.ContentHash = expected
		if err != nil {
			return nil, fmt.Errorf("compute content hash: %w", err)
		}
		got := crypto.SHA256(data)
		if expected != got {
			return nil, fmt.Errorf(
				"state file integrity check failed: content hash mismatch (file=%s computed=%s)",
				expected, got,
			)
		}
	}

	return &state, nil
}

// ── RemoveWorkflowState ──

// RemoveWorkflowState removes the entire workflow state directory for a given workflow ID.
func RemoveWorkflowState(wfID string) error {
	dir := infra.GetWorkflowsStateDirByID(wfID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove workflow state dir %s: %w", dir, err)
	}
	return nil
}
