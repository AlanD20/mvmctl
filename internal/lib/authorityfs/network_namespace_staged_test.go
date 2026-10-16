package authorityfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rationale: a process may die after linkat publishes the journaled placeholder but before the namespace mount.
// Recovery must remove that exact leaf without relying on the original descriptor, while rejecting a replacement.
func TestNetworklessInterruptedTargetPublicationRecovery(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("trusted networkless xattr requires root")
	}
	for _, replace := range []bool{false, true} {
		name := "exact"
		if replace {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			root := prepareTestRoot(t)
			jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
			jailerUID, err := jailer.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, jailerUID.Release(context.Background())) })
			key, err := NewVMKey(testVMID)
			require.NoError(t, err)
			vm, err := jailerUID.AcquireVM(t.Context(), key)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, vm.Release(context.Background())) })
			hold, err := vm.RetainTopology(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, hold.Release(context.Background())) })

			authority := openTestNetwork(t, root)
			t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
			var token [32]byte
			token[0] = 1
			target, err := uid.PrepareNetworklessNamespaceTarget(t.Context(), hold, token)
			require.NoError(t, err)
			receipt, err := target.TargetIdentity(t.Context())
			require.NoError(t, err)
			require.NoError(t, target.Link(t.Context()))
			require.NoError(t, target.Discard(t.Context())) // Simulate loss of all pre-crash descriptors.

			leaf := filepath.Join(root, "run/mvmctl/authority/1000/netns", testVMID)
			if replace {
				require.NoError(t, os.Remove(leaf))
				require.NoError(t, os.WriteFile(leaf, nil, 0o600))
			}
			recovered, err := uid.OpenNetworklessPlaceholderRecovery(t.Context(), hold, receipt, token)
			if replace {
				require.Error(t, err)
				_, statErr := os.Lstat(leaf)
				require.NoError(t, statErr, "foreign replacement must remain untouched")
				return
			}
			require.NoError(t, err)
			require.NoError(t, recovered.Remove(t.Context()))
			require.NoError(t, recovered.Release(t.Context()))
			_, err = os.Lstat(leaf)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
