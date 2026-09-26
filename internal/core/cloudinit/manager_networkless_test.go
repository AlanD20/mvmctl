package cloudinit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: an offline seed must contain user and instance data without
// fabricating a guest endpoint or writing an eth0 network configuration.
func TestManagerGenerateWithoutNetworkConfig(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(&Config{
		VMName: "offline-vm", User: "ubuntu", UserPassword: "offline-test",
		CloudInitDir: dir, SkipNetworkConfig: true,
	})
	require.NoError(t, manager.Generate(t.Context()))
	assert.FileExists(t, filepath.Join(dir, "meta-data"))
	assert.FileExists(t, filepath.Join(dir, "user-data"))
	assert.NoFileExists(t, filepath.Join(dir, "network-config"))

	userData, err := os.ReadFile(filepath.Join(dir, "user-data"))
	require.NoError(t, err)
	assert.Contains(t, string(userData), "hostname: offline-vm")
}
