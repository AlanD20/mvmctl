package network

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removed networkless VMs retain history, but an incomplete or foreign namespace
// must never be admitted as quiescent merely because it has no managed network.
func TestHostResetNetworklessHistoryRequiresTerminalOwnerBoundRecords(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		change func(*networklessJournalRecord)
		leaf   string
		valid  bool
	}{
		"cleaned": {valid: true},
		"preparing": {change: func(record *networklessJournalRecord) {
			record.Lifecycle, record.Pending = networklessPreparing, "prepare_target"
		}},
		"cleaning": {change: func(record *networklessJournalRecord) {
			record.Lifecycle, record.Pending = networklessCleaning, "delete_namespace"
		}},
		"foreign owner": {change: func(record *networklessJournalRecord) { record.OwnerUID++ }},
		"foreign VM": {
			change: func(record *networklessJournalRecord) { record.VMID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
		},
		"unknown entry": {leaf: "pending.json"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newNetworkStoreFixture(t)
			dir := filepath.Join(filepath.Dir(fixture.recordPath), "networkless")
			require.NoError(t, os.Mkdir(dir, 0700))
			record := networklessJournalRecord{
				Schema: networklessJournalSchema, Lifecycle: networklessCleaned,
				OwnerUID: testOwnerUID, VMID: testVMID, BootID: testBootID, LaunchGeneration: 1,
				Token: "0100000000000000000000000000000000000000000000000000000000000000",
			}
			if test.change != nil {
				test.change(&record)
			}
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			leaf := testVMID + ".json"
			if test.leaf != "" {
				leaf = test.leaf
			}
			path := filepath.Join(dir, leaf)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			inventory := hostResetNetworkInventory{}
			err = inventory.readOwner(t.Context(), fixture.uid)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			retained, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, string(raw), string(retained), "reset observation must retain history")
		})
	}
}
