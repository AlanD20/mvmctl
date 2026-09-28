package jailer

import (
	"encoding/json"
	"strings"
	"testing"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

func validVMLaunchRequest() VMLaunchRequest {
	return VMLaunchRequest{
		VMID:             strings.Repeat("a", 32),
		CacheRoot:        "/home/runner/.cache/mvmctl",
		KernelID:         strings.Repeat("b", 64),
		RootfsBytes:      managedVMRootfsMinBytes,
		Release:          ReleaseSlot{Version: "1.16.2", Architecture: "x86_64"},
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
}

func TestVMLaunchRequestRequiresAdmittedNetworkPlacement(t *testing.T) {
	request := validVMLaunchRequest()
	if err := request.Validate(); err != nil {
		t.Fatalf("valid managed launch request rejected: %v", err)
	}

	for _, tc := range []struct {
		name      string
		placement model.VMNetworkPlacement
	}{
		{name: "unknown", placement: "other"},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			candidate.NetworkPlacement = tc.placement
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "network placement") {
				t.Fatalf("invalid launch placement was admitted: %v", err)
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
	if got := string(fields["network_placement"]); got != `"managed"` {
		t.Fatalf("managed launch wire placement = %s, want explicit managed variant", got)
	}
	var roundTrip VMLaunchRequest
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("valid managed launch did not decode: %v", err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("decoded managed launch was rejected: %v", err)
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

// Rationale: exact launch admission must reject previously admitted and future releases
// before Jailer executes, including the 1.16.0 namespace-handle regression.
func TestVMLaunchRequestRequiresQualifiedRelease(t *testing.T) {
	for _, placement := range []model.VMNetworkPlacement{
		model.VMNetworkPlacementManaged,
		model.VMNetworkPlacementNetworkless,
	} {
		t.Run(string(placement), func(t *testing.T) {
			request := validVMLaunchRequest()
			request.NetworkPlacement = placement
			if placement == model.VMNetworkPlacementNetworkless {
				request.NetworkID, request.GuestIPv4, request.GuestMAC = "", "", ""
			}
			for _, version := range []string{"1.16.0", "1.16.1", "1.16.3"} {
				t.Run(version, func(t *testing.T) {
					candidate := request
					candidate.Release.Version = version
					err := candidate.Validate()
					if err == nil || !strings.Contains(err.Error(), "Firecracker/Jailer 1.16.2") {
						t.Errorf("unqualified release %s was admitted: %v", version, err)
					}
				})
			}
			if err := request.Validate(); err != nil {
				t.Errorf("qualified release was rejected: %v", err)
			}
		})
	}
}

func TestVMLaunchRequestNetworklessWireIsClosed(t *testing.T) {
	request := validVMLaunchRequest()
	request.NetworkPlacement = model.VMNetworkPlacementNetworkless
	request.NetworkID = ""
	request.GuestIPv4 = ""
	request.GuestMAC = ""
	if err := request.Validate(); err != nil {
		t.Fatalf("valid networkless launch request rejected: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if got := string(fields["network_placement"]); got != `"networkless"` {
		t.Fatalf("networkless launch wire placement = %s, want explicit networkless variant", got)
	}
	var decoded VMLaunchRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("valid networkless launch did not decode: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded networkless launch was rejected: %v", err)
	}

	for _, tc := range []struct {
		name   string
		change func(*VMLaunchRequest)
		want   string
	}{
		{name: "managed network ID", change: func(value *VMLaunchRequest) { value.NetworkID = strings.Repeat("c", 64) }, want: "managed endpoint"},
		{name: "guest IPv4", change: func(value *VMLaunchRequest) { value.GuestIPv4 = "10.0.0.2" }, want: "managed endpoint"},
		{name: "guest MAC", change: func(value *VMLaunchRequest) { value.GuestMAC = "02:00:00:00:00:02" }, want: "managed endpoint"},
		{name: "kernel IP", change: func(value *VMLaunchRequest) { value.BootArgs += " ip=dhcp" }, want: "boot arguments"},
		{name: "NoCloud NET", change: func(value *VMLaunchRequest) { value.BootArgs += " ds=nocloud-net" }, want: "boot arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			tc.change(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("networkless request with %s was admitted: %v", tc.name, err)
			}
		})
	}
}

func TestVMLaunchRequestNetworklessAdmitsOnlyLocalNoCloudDatasource(t *testing.T) {
	request := validVMLaunchRequest()
	request.NetworkPlacement = model.VMNetworkPlacementNetworkless
	request.NetworkID, request.GuestIPv4, request.GuestMAC = "", "", ""
	for _, tc := range []struct {
		name      string
		bootArgs  string
		cloudInit bool
		wantError bool
	}{
		{name: "ISO seed", bootArgs: "console=ttyS0 ds=" + infra.NoCloudISODatasource, cloudInit: true},
		{name: "injected seed", bootArgs: "console=ttyS0 ds=" + infra.NoCloudInjectDatasource},
		{name: "ISO without ISO", bootArgs: "console=ttyS0 ds=" + infra.NoCloudISODatasource, wantError: true},
		{name: "injected with ISO", bootArgs: "console=ttyS0 ds=" + infra.NoCloudInjectDatasource, cloudInit: true, wantError: true},
		{name: "remote source", bootArgs: "console=ttyS0 ds=nocloud;s=https://example.invalid/", wantError: true},
		{name: "duplicate source", bootArgs: "console=ttyS0 ds=nocloud ds=nocloud", cloudInit: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			candidate.BootArgs, candidate.CloudInit = tc.bootArgs, tc.cloudInit
			err := candidate.Validate()
			if tc.wantError && (err == nil || !strings.Contains(err.Error(), "datasource")) {
				t.Fatalf("unadmitted networkless datasource was accepted: %v", err)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("local networkless datasource was rejected: %v", err)
			}
		})
	}
}
