package cloudinit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// Rationale: a custom source path is input data, not a managed artifact name.
// ISO provisioning must copy it into the fixed VM-local leaf.
func TestProvisionISOCopiesCustomSourceToFixedManagedLeaf(t *testing.T) {
	sourceDir := t.TempDir()
	vmDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "custom-name.iso")
	require.NoError(t, os.WriteFile(sourcePath, []byte("custom-iso"), 0600))

	provisioner := &Provisioner{config: &Config{
		VMDir:            vmDir,
		CloudInitISOPath: &sourcePath,
	}}
	result, err := provisioner.provisionISO(t.Context())
	require.NoError(t, err)

	wantPath := filepath.Join(vmDir, infra.VMCloudInitISOFilename)
	require.NotNil(t, result.ISOPath)
	assert.Equal(t, wantPath, *result.ISOPath)
	data, err := os.ReadFile(wantPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("custom-iso"), data)
}
