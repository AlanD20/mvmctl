package cloudinit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/cloudinit"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// Rationale: an offline seed must contain user and instance data without
// fabricating a guest endpoint or writing an eth0 network configuration.
func TestManagerGenerateWithoutNetworkConfig(t *testing.T) {
	dir := t.TempDir()
	manager := cloudinit.NewManager(&cloudinit.Config{
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

// Rationale: successful cloud-localds execution must not publish a group-writable seed
// or hide a failure to restrict its permissions before protected import.
func TestManagerCreateSeedISOPermissions(t *testing.T) {
	tests := []struct {
		name         string
		createOutput bool
	}{
		{name: "missing_generated_output_returns_chmod_error"},
		{name: "permissive_generated_output_becomes_private", createOutput: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"meta-data", "user-data"} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0600))
			}
			outputISO := filepath.Join(dir, infra.VMCloudInitISOFilename)
			if tc.createOutput {
				// Model the output of cloud-localds under a permissive caller umask.
				require.NoError(t, os.WriteFile(outputISO, []byte("seed-iso"), 0600))
				require.NoError(t, os.Chmod(outputISO, 0664))
			}
			original := system.DefaultRunner
			system.DefaultRunner = &testutil.FakeRunner{StubRunResult: &system.RunResult{ExitCode: 0}}
			t.Cleanup(func() { system.DefaultRunner = original })

			err := cloudinit.NewManager(&cloudinit.Config{}).CreateSeedISO(t.Context(), dir, outputISO)
			if !tc.createOutput {
				require.Error(t, err)
				var domainErr *errs.DomainError
				require.ErrorAs(t, err, &domainErr)
				assert.Equal(t, errs.CodeCloudInitProvisionFailed, domainErr.Code)
				assert.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			info, err := os.Stat(outputISO)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			data, err := os.ReadFile(outputISO)
			require.NoError(t, err)
			assert.Equal(t, "seed-iso", string(data))
		})
	}
}
