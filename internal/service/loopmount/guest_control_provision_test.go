package loopmount

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/guestcontrolauth"
)

func TestGuestControlOrdinaryLoopMountWriteInstallsOnlyAgentMaterial(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "etc"), 0755))
	tree := newTestUnprivilegedRootfsGuestTree(t, root)
	host, agent, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	agentRaw, err := agent.Encode()
	require.NoError(t, err)
	hostRaw, err := host.Encode()
	require.NoError(t, err)
	written, err := writeGuestControlAgentMaterial(t.Context(), tree, agent)
	require.NoError(t, err)
	require.Equal(t, uint32(1), written)
	assertRootfsProvisionedFile(t, root, guestControlAgentMaterialPath, agentRaw, 0600)
	directory, err := os.Stat(filepath.Join(root, "etc/mvmctl"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), directory.Mode().Perm())
	raw, err := os.ReadFile(filepath.Join(root, guestControlAgentMaterialPath))
	require.NoError(t, err)
	_, err = guestcontrolauth.DecodeAgentMaterial(raw)
	require.NoError(t, err)
	require.NotEqual(t, hostRaw, raw)
	written, err = writeGuestControlAgentMaterial(t.Context(), tree, agent)
	require.NoError(t, err)
	require.Zero(t, written, "same selected material is a converged ordinary provisioning retry")
}

func TestGuestControlLoopMountMaterialValidationRefusesHostPrivateRecord(t *testing.T) {
	host, agent, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	hostRaw, err := host.Encode()
	require.NoError(t, err)
	agentRaw, err := agent.Encode()
	require.NoError(t, err)
	require.Error(t, (VMProvisionPlan{GuestControl: hostRaw}).Validate(minimumRootfsBytes))
	require.NoError(t, (VMProvisionPlan{GuestControl: agentRaw}).Validate(minimumRootfsBytes))
}
