package jailer

import (
	"encoding/json"
	"strings"
	"testing"

	"mvmctl/internal/lib/model"
)

func TestVMLaunchRequestRequiresAdmittedNetworkPlacement(t *testing.T) {
	request := VMLaunchRequest{
		VMID:             strings.Repeat("a", 32),
		CacheRoot:        "/home/runner/.cache/mvmctl",
		KernelID:         strings.Repeat("b", 64),
		RootfsBytes:      managedVMRootfsMinBytes,
		Release:          ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
		NetworkPlacement: model.VMNetworkPlacementManaged,
		NetworkID:        strings.Repeat("c", 64),
		GuestIPv4:        "10.0.0.2",
		GuestMAC:         "02:00:00:00:00:02",
		BootArgs:         "console=ttyS0",
		VCPUs:            1,
		MemoryMiB:        256,
		CgroupLimits: model.NewVMCgroupLimits(1, 256, model.VMCgroupPolicy{
			VMMHeadroomMiB: 128,
			CPUWeight:      100,
			PIDsMax:        64,
		}),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid managed launch request rejected: %v", err)
	}

	for _, tc := range []struct {
		name      string
		placement model.VMNetworkPlacement
	}{
		{name: "networkless", placement: model.VMNetworkPlacementNetworkless},
		{name: "unknown", placement: "other"},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			candidate.NetworkPlacement = tc.placement
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "network placement") {
				t.Fatalf("non-managed launch reached the managed path: %v", err)
			}
		})
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "network_placement")
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var decoded VMLaunchRequest
	if err := json.Unmarshal(encoded, &decoded); err == nil || !strings.Contains(err.Error(), "network_placement") {
		t.Fatalf("untagged launch was accepted by the root wire: %v", err)
	}
}
