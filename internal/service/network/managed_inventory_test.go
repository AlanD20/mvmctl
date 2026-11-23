package network

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

// Rationale: networkless VM journals share the owner directory with managed-network records,
// but unknown siblings must still block root network admission.
func TestManagedNetworkPrefixAdmissionDistinguishesNetworklessFromUnknownState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entry     string
		wantError bool
	}{
		{name: "networkless journal directory", entry: "networkless"},
		{name: "unknown directory", entry: "unexpected", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newNetworkStoreFixture(t)
			ownerDir := filepath.Dir(fixture.recordPath)
			require.NoError(t, os.Mkdir(filepath.Join(ownerDir, tc.entry), 0700))
			tx := managedNetworkTransaction{
				storeRoot: fixture.storeRoot,
				key: networkKey{
					ownerUID:  OwnerUID(testOwnerUID),
					networkID: NetworkID(testNetworkID),
				},
			}
			err := tx.rejectPrefixOverlap(t.Context(), contractManagedNetworkL3("10.44.5.0/24", "10.44.5.1"))
			if tc.wantError {
				require.ErrorContains(t, err, "incomplete or unknown state")
				assertNetworkAuthorityErrorClass(t, err, errs.ClassInternal)
				return
			}
			require.NoError(t, err)
		})
	}
}
