package jailer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVMLabelOwnershipRejectsForeignOrRetiredBacking(t *testing.T) {
	vmID := strings.Repeat("a", 32)
	cache := protectedBackingCache{Major: 1, Inode: 2}
	ready := protectedBackingRecord{
		Version: 1, Owner: 1000, Kind: protectedBackingRootfs, ID: vmID, Cache: cache, Phase: "ready",
	}
	require.NoError(t, validateVMLabelBacking(1000, vmID, cache, ready))

	for name, change := range map[string]func(*protectedBackingRecord){
		"foreign owner":   func(record *protectedBackingRecord) { record.Owner++ },
		"foreign cache":   func(record *protectedBackingRecord) { record.Cache.Inode++ },
		"different VM":    func(record *protectedBackingRecord) { record.ID = strings.Repeat("b", 32) },
		"retired":         func(record *protectedBackingRecord) { record.Phase = "removed" },
		"unfinished copy": func(record *protectedBackingRecord) { record.Phase = "copying" },
		"volume":          func(record *protectedBackingRecord) { record.Kind = protectedBackingVolume },
		"unknown version": func(record *protectedBackingRecord) { record.Version++ },
	} {
		t.Run(name, func(t *testing.T) {
			backing := ready
			change(&backing)
			require.Error(t, validateVMLabelBacking(1000, vmID, cache, backing))
		})
	}
}
