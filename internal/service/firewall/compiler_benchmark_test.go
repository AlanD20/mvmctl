package firewall

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileGeneration1000VMAllocationGrowthIsBounded(t *testing.T) {
	small := largeCompatibilityIntent(250)
	large := largeCompatibilityIntent(1000)

	var compileErr error
	smallAllocations := testing.AllocsPerRun(1, func() {
		_, compileErr = compileGeneration(small)
	})
	require.NoError(t, compileErr)
	var compiled compiledGeneration
	largeAllocations := testing.AllocsPerRun(1, func() {
		compiled, compileErr = compileGeneration(large)
	})
	require.NoError(t, compileErr)
	assert.Len(t, compiled.bridge.rules, 9*1000)

	// The input grows by 4x. A 6x ceiling leaves room for map/sort capacity
	// steps while rejecting quadratic allocation growth.
	assert.Less(t, largeAllocations, smallAllocations*6)
}

func BenchmarkCompileGeneration1000VMs(b *testing.B) {
	intent := largeCompatibilityIntent(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := compileGeneration(intent); err != nil {
			b.Fatal(err)
		}
	}
}

func largeCompatibilityIntent(vmCount int) generationIntent {
	const (
		networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	intent := generationIntent{
		networks: []networkIntent{
			{
				id:              networkID,
				hostBridge:      "mvmbr0",
				subnet:          netip.MustParsePrefix("10.20.0.0/16"),
				hostIPv4:        netip.MustParseAddr("10.20.0.1"),
				uplinkInterface: "eth0",
				masquerade:      true,
			},
		},
		endpoints: make([]endpointIntent, 0, vmCount),
		vmGrants:  make([]vmTrafficGrant, 0, vmCount),
	}

	for index := range vmCount {
		vmID := fmt.Sprintf("%032x", index+1)
		addressValue := index + 2
		address := netip.AddrFrom4([4]byte{10, 20, byte(addressValue >> 8), byte(addressValue)})
		intent.endpoints = append(intent.endpoints, endpointIntent{
			id:        vmID,
			networkID: networkID,
			hostVeth:  fmt.Sprintf("mvm%08x", index),
			ipv4:      address,
			mac: macAddress{
				0x02, 0, 0, byte(index >> 16), byte(index >> 8), byte(index),
			},
		})
	}

	for index := range vmCount {
		intent.vmGrants = append(intent.vmGrants, vmTrafficGrant{
			id:              fmt.Sprintf("policy-%08d", index),
			sourceVMID:      intent.endpoints[index].id,
			destinationVMID: intent.endpoints[(index+1)%vmCount].id,
			protocol:        protocolTCP,
			ports:           portInterval{first: 443, last: 443},
		})
	}
	return intent
}
