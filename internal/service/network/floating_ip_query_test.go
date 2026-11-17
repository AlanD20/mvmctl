package network

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
)

func TestFloatingIPInventoryPaginationFitsPrivilegedFrameWithoutTruncation(t *testing.T) {
	var records []floatingIPRecord
	wantFirst := FloatingIPPage{NextID: "00000000000000000000000000000040"}
	for index := 1; index <= 65; index++ {
		// The fixture defines distinct payloads; pagination must preserve every selected payload.
		item := FloatingIPResult{
			NetworkID: testNetworkID, ID: fmt.Sprintf("%032x", index),
			Name: strings.Repeat("n", 60) + fmt.Sprintf("%03d", index),
			IPv4: fmt.Sprintf("10.10.0.%d", index+1), Generation: ^uint64(0),
		}
		records = append(records, floatingIPRecord{
			Schema: 1, OwnerUID: testOwnerUID, NetworkID: testNetworkID,
			ID: item.ID, Name: item.Name, IPv4: item.IPv4, Generation: item.Generation,
		})
		if index <= 64 {
			wantFirst.Items = append(wantFirst.Items, item)
		}
	}
	first := floatingIPRecordPage(records, "")
	if diff := cmp.Diff(wantFirst, first); diff != "" {
		t.Fatalf("first-page resources or payload changed (-want +got):\n%s", diff)
	}
	var frame bytes.Buffer
	require.NoError(t, privilegedwire.EncodeSuccess(t.Context(), &frame, "floating-ip-list", first),
		"a full page at maximum name and generation sizes must fit the real response envelope")
	second := floatingIPRecordPage(records, first.NextID)
	want := FloatingIPPage{Items: []FloatingIPResult{{
		ID: "00000000000000000000000000000041", NetworkID: testNetworkID,
		Name: strings.Repeat("n", 60) + "065", IPv4: "10.10.0.66", Generation: 18446744073709551615,
	}}}
	if diff := cmp.Diff(want, second); diff != "" {
		t.Fatalf("continuation lost or repeated a resource (-want +got):\n%s", diff)
	}
}

func TestFloatingIPInventoryCursorSkipsRetiredResource(t *testing.T) {
	records := []floatingIPRecord{
		{
			Schema: 1, OwnerUID: testOwnerUID, NetworkID: testNetworkID, Generation: 1,
			ID: "00000000000000000000000000000001", Name: "previous", IPv4: "10.10.0.90",
		},
		{
			Schema: 1, OwnerUID: testOwnerUID, NetworkID: testNetworkID, Generation: 2,
			ID: "00000000000000000000000000000002", Name: "removed", IPv4: "10.10.0.99", Retired: true,
		},
		{
			Schema: 1, OwnerUID: testOwnerUID,
			ID:         "00000000000000000000000000000003",
			Name:       "api",
			IPv4:       "10.10.0.100",
			Generation: 1,
			NetworkID:  testNetworkID,
		},
	}
	got := floatingIPRecordPage(records, "00000000000000000000000000000001")
	want := FloatingIPPage{Items: []FloatingIPResult{
		{
			ID:         "00000000000000000000000000000003",
			Name:       "api",
			IPv4:       "10.10.0.100",
			Generation: 1,
			NetworkID:  testNetworkID,
		},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("retired or previous resource leaked into continuation (-want +got):\n%s", diff)
	}
}
