package firewall

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func secondaryCompilerIntent() generationIntent {
	intent := compatibilityIntent()
	intent.floatingIPs = []floatingIPIntent{
		{id: strings.Repeat("4", 32), networkID: intent.networks[0].id,
			ipv4: netip.MustParseAddr("10.0.0.100"), generation: 7,
			ownerVMID: intent.endpoints[0].id, ownerLaunchGeneration: 9, leaseDuration: 5 * time.Second},
		{id: strings.Repeat("5", 32), networkID: intent.networks[0].id,
			ipv4: netip.MustParseAddr("10.0.0.101"), generation: 1},
	}
	intent.vmHostGrants = []vmHostTrafficGrant{{id: strings.Repeat("6", 32), sourceVMID: intent.endpoints[0].id,
		protocol: protocolTCP, ports: portInterval{first: 80, last: 80}}}
	return intent
}

func secondaryLiveKey() [20]byte {
	return [20]byte{'m', 'v', 'm', 'a', '0', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 0, 0, 100}
}

func TestSecondaryCompilerBindsFencesAndPoliciesToTheOwner(t *testing.T) {
	intent := secondaryCompilerIntent()
	compiled, err := compileGeneration(intent)
	require.NoError(t, err)
	require.NoError(t, validateKernelApplicationTarget(compiled))
	require.Len(t, compiled.bridge.chains, 3)
	assert.Equal(t, *nftables.ChainHookPostrouting, *compiled.bridge.chains[2].Hooknum)
	assert.Equal(t, secondaryLiveKey(), compiled.bridge.secondaryOwners[intent.floatingIPs[0].id])
	assert.Len(t, compiled.bridge.secondaryOwners, 1)
	for _, resource := range intent.floatingIPs {
		fence := requireCompiledRule(t, compiled, "bridge.floating.destination."+resource.id)
		assert.Same(t, compiled.bridge.chains[2], fence.Chain)
		assert.Equal(t, expr.VerdictDrop, finalVerdict(t, fence.Exprs).Kind)
	}
	grant := requireCompiledRule(t, compiled,
		"bridge.allow.vm."+intent.vmGrants[0].id+".from.floating."+intent.floatingIPs[0].id+".to")
	assert.Contains(t, expressionData(grant.Exprs), []byte{10, 0, 0, 100})
	assert.Contains(t, expressionData(grant.Exprs), expectedInterfaceName("mvma0"))
	assert.Contains(t, expressionData(grant.Exprs), expectedInterfaceName("mvmb0"))
	host := requireCompiledRule(t, compiled,
		"inet.input.allow.vm-host."+intent.vmHostGrants[0].id+".floating."+intent.floatingIPs[0].id)
	assert.Contains(t, expressionData(host.Exprs), []byte{10, 0, 0, 100})
	for _, rule := range compiled.rules() {
		if verdict, ok := rule.Exprs[len(rule.Exprs)-1].(*expr.Verdict); ok && verdict.Kind == expr.VerdictAccept {
			assert.NotContains(
				t,
				expressionData(rule.Exprs),
				[]byte{10, 0, 0, 101},
				"unassociated resource gained a policy grant",
			)
		}
	}
}

func TestSecondaryObservationAcceptsExpiryButRejectsForeignOrExtendedLeases(t *testing.T) {
	compiled, err := compileGeneration(secondaryCompilerIntent())
	require.NoError(t, err)
	for name, elements := range map[string][]secondaryLeaseElement{
		"missing after expiry": nil,
		"live":                 {{key: secondaryLiveKey(), timeout: 5 * time.Second, remaining: 3 * time.Second}},
		"expiry boundary":      {{key: secondaryLiveKey(), timeout: 5 * time.Second, remaining: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			observed := kernelObservationFromCompiled(compiled)
			observed.tables[0].secondaryElements = elements
			require.NoError(t, compareKernelObservation(observed, compiled))
		})
	}
	other := secondaryLiveKey()
	copy(other[:16], []byte("mvmb0\x00"))
	unassociated := secondaryLiveKey()
	unassociated[19] = 101
	for name, elements := range map[string][]secondaryLeaseElement{
		"wrong interface":          {{key: other, timeout: 5 * time.Second, remaining: time.Second}},
		"unassociated resource":    {{key: unassociated, timeout: 5 * time.Second, remaining: time.Second}},
		"extended timeout":         {{key: secondaryLiveKey(), timeout: 6 * time.Second, remaining: time.Second}},
		"remaining beyond ceiling": {{key: secondaryLiveKey(), timeout: 5 * time.Second, remaining: 6 * time.Second}},
		"duplicate key":            {{key: secondaryLiveKey(), timeout: 5 * time.Second}, {key: secondaryLiveKey(), timeout: 5 * time.Second}},
	} {
		t.Run(name, func(t *testing.T) {
			observed := kernelObservationFromCompiled(compiled)
			observed.tables[0].secondaryElements = elements
			requireKernelObservationRejected(t, compareKernelObservation(observed, compiled))
		})
	}
}

func TestSecondaryGenerationReadbackIncludesSetAndDestinationChain(t *testing.T) {
	compiled, err := compileGeneration(secondaryCompilerIntent())
	require.NoError(t, err)
	raw := kernelMessagesFromObservation(t, kernelDumpObservationFromCompiled(compiled))
	key := secondaryLiveKey()
	raw.secondaryElementReplies = []netlink.Message{secondaryElementsMessage(t, []netlink.Attribute{
		{Type: 1, Data: mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 1, Data: key[:]}})},
		{Type: 4, Data: testBigEndianUint64(5000)}, {Type: 5, Data: testBigEndianUint64(3000)},
	})}
	observed, err := decodeKernelObservation(raw)
	require.NoError(t, err)
	require.NoError(t, compareKernelObservation(observed, compiled))
	require.Len(t, observed.tables[0].secondaryElements, 1)
	assert.Equal(t, 3*time.Second, observed.tables[0].secondaryElements[0].remaining)
	raw.setReplies = nil
	_, err = decodeKernelObservation(raw)
	requireKernelObservationRejected(t, err)
}

func TestSecondaryApplicationPreservesUnchangedLeasesAndRevokesChangedAuthority(t *testing.T) {
	for _, change := range []string{"policy only", "new generation", "new owner", "already expired", "remove resources"} {
		t.Run(change, func(t *testing.T) {
			before, err := compileGeneration(secondaryCompilerIntent())
			require.NoError(t, err)
			targetIntent := secondaryCompilerIntent()
			targetIntent.hostServices = nil
			switch change {
			case "new generation", "already expired":
				targetIntent.floatingIPs[0].generation++
			case "new owner":
				targetIntent.floatingIPs[0].generation++
				targetIntent.floatingIPs[0].ownerVMID = targetIntent.endpoints[1].id
			case "remove resources":
				targetIntent.floatingIPs = nil
			}
			target, err := compileGeneration(targetIntent)
			require.NoError(t, err)
			observed := kernelObservationFromCompiled(before)
			if change != "already expired" {
				observed.tables[0].secondaryElements = []secondaryLeaseElement{
					{key: secondaryLiveKey(), timeout: 5 * time.Second, remaining: time.Second},
				}
			}
			connection := &recordingKernelApplicationConnection{}
			result, err := kernelApplicationLease(
				t,
				before,
				target,
			).applyKernelGeneration(t.Context(), connection, observed, before, target)
			require.NoError(t, err)
			assert.True(t, result.commitAttempted)
			assert.Equal(t, 1, connection.flushCalls)
			deletion := fmt.Sprintf("delete secondary key secondary_ipv4/%x", secondaryLiveKey())
			if change == "new generation" || change == "new owner" {
				assert.Contains(t, connection.events, deletion)
			} else {
				assert.NotContains(t, connection.events, deletion)
			}
			assert.NotContains(t, connection.events, "add secondary set secondary_ipv4")
			if change == "remove resources" {
				assert.Contains(t, connection.events, "delete secondary set secondary_ipv4")
				assert.Contains(t, connection.events, "delete chain bridge/mvmctl_bridge/postrouting")
			} else {
				assert.NotContains(t, connection.events, "delete secondary set secondary_ipv4")
			}
			for _, event := range connection.events {
				assert.NotContains(t, event, "delete table")
			}
		})
	}
}

func TestSecondaryApplicationAddsSetAndChainWithoutReplacingTables(t *testing.T) {
	before, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	target, err := compileGeneration(secondaryCompilerIntent())
	require.NoError(t, err)
	connection := &recordingKernelApplicationConnection{}
	_, err = kernelApplicationLease(t, before, target).applyKernelGeneration(t.Context(), connection,
		kernelObservationFromCompiled(before), before, target)
	require.NoError(t, err)
	assert.Contains(t, connection.events, "add secondary set secondary_ipv4")
	assert.Contains(t, connection.events, "add chain bridge/mvmctl_bridge/postrouting")
	for _, event := range connection.events {
		assert.NotContains(t, event, "delete table")
		assert.NotContains(t, event, "add table")
	}
}

func TestSecondaryLookupFieldsAreSealedAndObserved(t *testing.T) {
	for name, mutate := range map[string]func(*expr.Lookup){
		"source register":      func(value *expr.Lookup) { value.SourceRegister = 2 },
		"destination register": func(value *expr.Lookup) { value.DestRegister = 2 },
		"destination present":  func(value *expr.Lookup) { value.IsDestRegSet = true },
		"set ID":               func(value *expr.Lookup) { value.SetID = 2 },
		"set name":             func(value *expr.Lookup) { value.SetName = "foreign" },
		"inversion":            func(value *expr.Lookup) { value.Invert = false },
	} {
		t.Run(name, func(t *testing.T) {
			compiled, err := compileGeneration(secondaryCompilerIntent())
			require.NoError(t, err)
			observed := kernelObservationFromCompiled(compiled)
			changed := false
			for _, expression := range compiled.bridge.rules[1].rule.Exprs {
				if lookup, ok := expression.(*expr.Lookup); ok {
					mutate(lookup)
					changed = true
					break
				}
			}
			require.True(t, changed)
			require.Error(t, validateKernelApplicationTarget(compiled), "changed field escaped the application seal")
			require.Error(t, compareKernelObservation(observed, compiled), "changed field escaped exact readback")
		})
	}
}

func TestSecondaryCollectorUsesTheRealTransportForElementDump(t *testing.T) {
	compiled, err := compileGeneration(secondaryCompilerIntent())
	require.NoError(t, err)
	raw := kernelMessagesFromObservation(t, kernelDumpObservationFromCompiled(compiled))
	contracts := exactKernelSnapshotExchanges()
	chainAttributes := append(testExactKernelTableAttribute("mvmctl_bridge", unix.NFTA_CHAIN_TABLE),
		testExactKernelTableAttribute("postrouting", unix.NFTA_CHAIN_NAME)...)
	elementAttributes := append(testExactKernelTableAttribute("mvmctl_bridge", unix.NFTA_SET_ELEM_LIST_TABLE),
		testExactKernelTableAttribute("secondary_ipv4", unix.NFTA_SET_ELEM_LIST_SET)...)
	contracts = append(contracts[:len(contracts)-1],
		kernelObservationSnapshotExchange{
			request: testExactKernelSnapshotRequest(
				unix.NFT_MSG_GETCHAIN,
				netlink.Request,
				nftables.TableFamilyBridge,
				chainAttributes,
			),
			spec: kernelObservationReplySpec{mode: kernelObservationReplySingular},
		},
		kernelObservationSnapshotExchange{
			request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETSETELEM, netlink.Request|netlink.Dump,
				nftables.TableFamilyBridge, elementAttributes),
			spec: kernelObservationReplySpec{mode: kernelObservationReplyMultipart, flags: netlink.Multi},
		},
		kernelObservationSnapshotExchange{
			request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETGEN, netlink.Request, 0, nil),
			spec:    kernelObservationReplySpec{mode: kernelObservationReplySingular},
		},
	)
	factory := &realKernelSnapshotTransportFactory{t: t, steps: successfulKernelSnapshotSteps(t, raw), specs: contracts}
	observed, err := collectKernelObservationSnapshot(t.Context(), factory)
	require.NoError(t, err)
	require.NoError(t, compareKernelObservation(observed, compiled))
	require.Len(t, factory.connections, len(contracts))
	for _, connection := range factory.connections {
		assert.Equal(t, 1, connection.closeCalls)
	}
}

func TestSecondaryAuthoritySurvivesAnIndependentDesiredStateClone(t *testing.T) {
	original := Intent{FloatingIPs: []FloatingIPBinding{{OwnerUID: 1000, ID: strings.Repeat("4", 32),
		NetworkID: strings.Repeat("b", 64), IPv4: netip.MustParseAddr("10.0.0.100"), Generation: 7,
		OwnerVMID: strings.Repeat("1", 32), OwnerLaunchGeneration: 9, LeaseDuration: 5 * time.Second}}}
	cloned := cloneIntent(original)
	require.Equal(
		t,
		original.FloatingIPs,
		cloned.FloatingIPs,
		"an unrelated mutation dropped the secondary fence intent",
	)
	cloned.FloatingIPs[0].Generation++
	cloned.FloatingIPs[0].OwnerVMID = strings.Repeat("2", 32)
	assert.Equal(t, uint64(7), original.FloatingIPs[0].Generation)
	assert.Equal(t, strings.Repeat("1", 32), original.FloatingIPs[0].OwnerVMID)
}
