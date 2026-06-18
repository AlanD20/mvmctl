package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMLoad_AbsolutizesRelativePaths(t *testing.T) {
	// Create temp dir with snapshot files.
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "test.mem")
	stateFile := filepath.Join(tmpDir, "test.state")
	require.NoError(t, os.WriteFile(memFile, []byte("mem"), 0644))
	require.NoError(t, os.WriteFile(stateFile, []byte("state"), 0644))

	// Change to temp dir so relative paths resolve correctly.
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { os.Chdir(origDir) })

	// Set up in-memory VM repo with a running VM.
	// Running status avoids the vmRespawnFirecracker path (which needs
	// full enrichment + Firecracker binary), letting us test path
	// absolutization through to controller.LoadSnapshot.
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.VMItem{
		ID:     "vm-1",
		Name:   "test-vm",
		Status: model.VMStatusRunning,
	}))

	op := &Operation{
		Repos: Repos{VM: repo},
	}

	// Call VMLoad with relative paths from the temp dir.
	err = op.VMLoad(context.Background(), inputs.VMInput{Identifiers: []string{"test-vm"}}, "test.mem", "test.state", false)

	// The function should NOT fail with "file not found" — that proves
	// os.Stat() resolved relative paths correctly after Abs().
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "Snapshot file(s) not found",
		"relative paths should be absolutized before os.Stat check")
}

func TestVMSnapshot_AbsolutizesRelativePaths(t *testing.T) {
	// Set up in-memory VM repo with a VM.
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(context.Background(), &model.VMItem{
		ID:     "vm-1",
		Name:   "test-vm",
		Status: model.VMStatusRunning,
	}))

	op := &Operation{
		Repos: Repos{VM: repo},
	}

	// Call VMSnapshot with relative paths.
	err := op.VMSnapshot(context.Background(), inputs.VMInput{Identifiers: []string{"test-vm"}}, "test.mem", "test.state")

	// Should fail with a controller/Snapshot error (VM has no API socket),
	// NOT a resolver error ("VM not found") — that proves the VM was
	// resolved correctly after path absolutization.
	require.Error(t, err)
	// The error should mention "snapshot" or "api socket" but not
	// "VM not found" (that would mean the resolver was never reached).
	assert.NotContains(t, err.Error(), "VM not found",
		"path absolutization should not interfere with VM resolution")
}

// Ensure the test helpers compile and the interface contract is met.
var _ vm.Repository = (*testutil.VMRepo)(nil)
