package guestfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
)

func TestGuestControlGuestFSUsesPrivateUploadDuringOrdinaryProvisioning(t *testing.T) {
	_, agent, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	backend := NewGuestfsBackend("/caller/staging/rootfs.img", 0, 0, 1000, 1000)
	require.NoError(t, backend.InjectGuestControl(t.Context(), agent))
	directory := t.TempDir()
	commands, err := buildScript(ProvisioningConfig{GuestControlAgent: backend.guestControl}, "/dev/sda", false, directory)
	require.NoError(t, err)
	path := filepath.Join(directory, "guest-control-agent.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	want, err := agent.Encode()
	require.NoError(t, err)
	require.Equal(t, want, raw)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	// These emitted calls are the GuestFS effect contract, not a source-text assertion.
	require.Contains(t, commands, "upload "+path+" "+infra.GuestControlAgentMaterialPath)
	require.Contains(t, commands, "chmod 0600 "+infra.GuestControlAgentMaterialPath)
	require.Contains(t, commands, "chown 0 0 "+infra.GuestControlAgentMaterialPath)
	require.NotContains(t, strings.Join(commands, "\n"), string(raw))
}
