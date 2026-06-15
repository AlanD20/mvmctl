package vsock_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
)

// ─── NewClient ─────────────────────────────────────────────────────────────
// Rationale: NewClient constructs a Client from a VsockConfigItem. If the
// item reference is not stored, Exec/Shell/Teardown would have no state.

func TestNewClient(t *testing.T) {
	item := &model.VsockConfigItem{
		ID: "vsock-1", VmID: "vm-1",
		GuestCID: 3, UDSPath: "/tmp/test.sock", Port: 1024, Token: "tok",
	}
	client := vsock.NewClient(item, 0)
	require.NotNil(t, client)
}

// ─── Teardown ──────────────────────────────────────────────────────────────
// Rationale: Teardown removes the UDS socket file during VM cleanup. If it
// fails to remove the file or errors on non-existent files, VM cleanup is
// broken — stale sockets accumulate or cleanup fails spuriously.

func TestTeardown_RemovesFile(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	f, err := os.Create(sockPath)
	require.NoError(t, err)
	f.Close()

	client := vsock.NewClient(&model.VsockConfigItem{
		VmID:    "vm-1",
		UDSPath: sockPath,
	}, 0)

	err = client.Teardown(ctx)
	assert.NoError(t, err)

	_, err = os.Stat(sockPath)
	assert.True(t, os.IsNotExist(err), "socket file must be removed after Teardown")
}

func TestTeardown_NonExistentFile(t *testing.T) {
	// Use a path where the parent directory exists but the file does not.
	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	client := vsock.NewClient(&model.VsockConfigItem{
		VmID:    "vm-1",
		UDSPath: sockPath,
	}, 0)

	err := client.Teardown(ctx)
	assert.NoError(t, err, "Teardown must not error on non-existent file")
}

func TestTeardown_EmptyPath(t *testing.T) {
	client := vsock.NewClient(&model.VsockConfigItem{
		VmID: "vm-1",
	}, 0)

	err := client.Teardown(ctx)
	assert.NoError(t, err, "Teardown must not error when UDSPath is empty")
}

func TestTeardown_ContextCancelled(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test-cancel.sock")
	f, err := os.Create(sockPath)
	require.NoError(t, err)
	f.Close()

	client := vsock.NewClient(&model.VsockConfigItem{
		VmID:    "vm-1",
		UDSPath: sockPath,
	}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = client.Teardown(ctx)
	assert.NoError(t, err, "Teardown ignores context cancellation")

	_, err = os.Stat(sockPath)
	assert.True(t, os.IsNotExist(err), "socket file must be removed even with cancelled context")
}
