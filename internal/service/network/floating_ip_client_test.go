package network

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFloatingIPCreateAcknowledgementBindsExactIntent(t *testing.T) {
	request := FloatingIPCreateRequest{
		ID:        strings.Repeat("b", 32),
		NetworkID: testNetworkID,
		Name:      "api",
		IPv4:      "10.10.0.100",
	}
	valid := FloatingIPResult{ID: request.ID, NetworkID: testNetworkID, Name: "api", IPv4: "10.10.0.100", Generation: 1}
	require.NoError(t, valid.validateCreate(request))
	cases := map[string]func(*FloatingIPResult){
		"different resource":          func(r *FloatingIPResult) { r.ID = strings.Repeat("c", 32) },
		"different network":           func(r *FloatingIPResult) { r.NetworkID = strings.Repeat("c", 64) },
		"different name":              func(r *FloatingIPResult) { r.Name = "other" },
		"different requested address": func(r *FloatingIPResult) { r.IPv4 = "10.10.0.101" },
		"retired result":              func(r *FloatingIPResult) { r.Retired = true },
		"zero generation":             func(r *FloatingIPResult) { r.Generation = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			result := valid
			mutate(&result)
			require.Error(t, result.validateCreate(request))
		})
	}
	request.IPv4 = ""
	require.NoError(
		t,
		valid.validateCreate(request),
		"automatic allocation binds the resource without prescribing an address",
	)
	valid.IPv4 = "198.51.100.10"
	require.Error(t, valid.validateCreate(request), "automatic allocation cannot acknowledge a non-private address")
}

func TestFloatingIPRemovalAcknowledgementRejectsWrongGenerationAndOverflow(t *testing.T) {
	request := FloatingIPRemoveRequest{ID: strings.Repeat("b", 32), NetworkID: testNetworkID, ExpectedGeneration: 7}
	result := FloatingIPResult{
		ID:         request.ID,
		NetworkID:  testNetworkID,
		Name:       "api",
		IPv4:       "10.10.0.100",
		Generation: 8,
		Retired:    true,
	}
	require.NoError(t, result.validateRemove(request))
	request.ExpectedGeneration = 8
	require.NoError(
		t,
		result.validateRemove(request),
		"inspection of an already retired exact generation is idempotent",
	)
	request.ExpectedGeneration = 6
	require.Error(t, result.validateRemove(request))
	request.ExpectedGeneration = ^uint64(0)
	result.Generation = 1
	require.Error(t, result.validateRemove(request), "generation arithmetic must not wrap")
	result.Generation = ^uint64(0)
	require.NoError(t, result.validateRemove(request))
	result.Retired = false
	require.Error(t, result.validateRemove(request), "a live resource is never a removal acknowledgement")
}

func TestFloatingIPInventoryAcknowledgementRefusesStalledOrReboundPage(t *testing.T) {
	request := FloatingIPListRequest{NetworkID: testNetworkID}
	item := FloatingIPResult{
		ID:         strings.Repeat("b", 32),
		NetworkID:  testNetworkID,
		Name:       "api",
		IPv4:       "10.10.0.100",
		Generation: 1,
	}
	require.NoError(t, (FloatingIPPage{Items: []FloatingIPResult{}}).validate(request))
	cases := map[string]FloatingIPPage{
		"null items":         {},
		"stalled cursor":     {Items: []FloatingIPResult{item}, NextID: item.ID},
		"duplicate resource": {Items: []FloatingIPResult{item, item}},
	}
	wrongNetwork := item
	wrongNetwork.NetworkID = strings.Repeat("c", 64)
	cases["wrong network"] = FloatingIPPage{Items: []FloatingIPResult{wrongNetwork}}
	retired := item
	retired.Retired = true
	cases["retired resource"] = FloatingIPPage{Items: []FloatingIPResult{retired}}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) { require.Error(t, page.validate(request)) })
	}
	request.AfterID = item.ID
	require.Error(
		t,
		(FloatingIPPage{Items: []FloatingIPResult{item}}).validate(request),
		"a continuation must not replay the cursor resource",
	)
}

func TestFloatingIPInventoryAcknowledgementAcceptsCompleteContinuation(t *testing.T) {
	request := FloatingIPListRequest{NetworkID: testNetworkID}
	page := FloatingIPPage{Items: make([]FloatingIPResult, 0, 64)}
	for index := 1; index <= 64; index++ {
		page.Items = append(page.Items, FloatingIPResult{
			ID: fmt.Sprintf("%032x", index), NetworkID: testNetworkID, Name: fmt.Sprintf("vip-%d", index),
			IPv4: fmt.Sprintf("10.10.0.%d", index+1), Generation: 1,
		})
	}
	page.NextID = "00000000000000000000000000000040"
	require.NoError(t, page.validate(request), "a valid continuation cannot be rejected as a terminal-only inventory")
	page.NextID = "00000000000000000000000000000041"
	require.Error(t, page.validate(request), "the cursor must bind the last returned resource")
}
