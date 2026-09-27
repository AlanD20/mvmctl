package jailer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
)

// Rationale: The root snapshot projection crosses the privileged transport. A
// missing or mixed placement must not become a managed endpoint by default.
func TestValidateSnapshotProjectionPlacement(t *testing.T) {
	snapshotID := strings.Repeat("a", 64)
	base := model.SnapshotCaptureResult{
		SnapshotID: snapshotID, SourceVMID: strings.Repeat("b", 32), Mode: "disks",
		RootfsBytes: 512 << 20, VCPUCount: 1, MemSizeMiB: 256,
		ReleaseVersion: "1.16.1", ReleaseArchitecture: "x86_64",
	}
	cases := []struct {
		name      string
		placement model.VMNetworkPlacement
		networkID string
		ipv4      string
		mac       string
		wantError bool
	}{
		{name: "missing_placement", wantError: true},
		{name: "networkless_with_endpoint", placement: model.VMNetworkPlacementNetworkless,
			networkID: strings.Repeat("c", 64), wantError: true},
		{name: "managed_without_endpoint", placement: model.VMNetworkPlacementManaged, wantError: true},
		{name: "networkless", placement: model.VMNetworkPlacementNetworkless},
		{name: "managed", placement: model.VMNetworkPlacementManaged, networkID: strings.Repeat("c", 64),
			ipv4: "10.20.30.2", mac: "02:00:00:00:00:01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projection := base
			projection.NetworkPlacement = tc.placement
			projection.NetworkID = tc.networkID
			projection.GuestIPv4 = tc.ipv4
			projection.GuestMAC = tc.mac
			err := validateSnapshotProjection(projection, snapshotID)
			if tc.wantError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
