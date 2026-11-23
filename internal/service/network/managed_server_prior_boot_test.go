package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A prior-boot network may retain historical cleaned topology evidence, but a live
// or differently bound journal must keep the network's removal receipt unpublished.
func TestPriorBootNetworkRemovalRequiresExactCleanedTopology(t *testing.T) {
	fixture := newNetworkStoreFixture(t)
	record := fixture.record
	record.schemaVersion = managedNetworkSchemaVersion
	record.configuration = &managedNetworkConfiguration{
		l3: contractManagedNetworkL3("10.44.5.0/24", "10.44.5.1"),
	}
	raw, err := encodeNetworkAuthorityRecord(record)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	prior := record
	tx := managedNetworkTransaction{
		key:       networkKey{ownerUID: OwnerUID(testOwnerUID), networkID: NetworkID(testNetworkID)},
		storeRoot: fixture.storeRoot,
		record:    record,
		priorBoot: &prior,
	}

	topologyDir := filepath.Join(filepath.Dir(fixture.recordPath), "topologies")
	require.NoError(t, os.Mkdir(topologyDir, 0700))
	path := filepath.Join(topologyDir, testVMID+".json")
	cleaned := topologyJournalRecord{
		Schema: topologyJournalSchema, Lifecycle: topologyCleaned,
		OwnerUID: testOwnerUID, NetworkID: testNetworkID, VMID: testVMID,
		BootID: testBootID, LaunchGeneration: 1, NetworkDigest: hex.EncodeToString(digest[:]),
		Prefix: "10.44.5.0/24", Gateway: "10.44.5.1", GuestIPv4: "10.44.5.2",
		GuestMAC: "02:00:00:00:00:01", EndpointID: testVMID,
		Topology: TopologyRecord{
			OwnerUID: testOwnerUID, TAPExecutionUID: testExecutionUID,
			VMID: testVMID, NetworkID: testNetworkID,
		},
	}

	for _, tc := range []struct {
		name    string
		change  func(*topologyJournalRecord)
		wantErr string
	}{
		{name: "exact cleaned history"},
		{name: "active topology", change: func(r *topologyJournalRecord) { r.Lifecycle = topologyActive },
			wantErr: "network absence conflicts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := cleaned
			if tc.change != nil {
				tc.change(&journal)
			}
			encoded, err := json.Marshal(journal)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, encoded, 0600))
			err = tx.checkAbsentNetworkTopologyClaims(t.Context(), fixture.uid)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
