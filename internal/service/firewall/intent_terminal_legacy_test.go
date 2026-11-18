package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
)

// The observed old public-reset shape retains one dormant network in Before and no desired resources in Target.
func legacyTerminalIntentFixture(t *testing.T) (generationRecord, intentJournal) {
	t.Helper()
	empty, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	binding := testGenerationRecordBinding()
	binding.bootID = "22345678-1234-4abc-8def-1234567890ab"
	record := mustCommittedGenerationRecord(t, binding, generationRecordIdentityFromCompiled(empty))
	journal := intentJournal{
		SchemaVersion: 1, BootID: binding.bootID,
		HostNamespace: NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode},
		Before:        Intent{Networks: secondaryDurableIntent().Networks},
	}
	return record, journal
}

func TestLegacyTerminalIntentPreservesDormantNetworkHistoryAcrossBoot(t *testing.T) {
	record, journal := legacyTerminalIntentFixture(t)
	raw, err := json.Marshal(journal)
	require.NoError(t, err)
	decoded, err := decodeIntentJournal(raw)
	require.NoError(t, err)
	require.Len(t, decoded.Before.Networks, 1)
	require.Equal(t, strings.Repeat("b", 64), decoded.Before.Networks[0].NetworkID)
	require.True(t, hostResetIntentEmpty(decoded.Target))
	require.NoError(t, admitLegacyTerminalIntent(record, decoded))
	require.NoError(t, admitHostResetGenerationBinding(record, decoded, testGenerationRecordBinding()))
	roundtrip, err := json.Marshal(decoded)
	require.NoError(t, err)
	require.Equal(t, raw, roundtrip, "history admission must preserve exact canonical bytes")
	_, err = decodeIntentJournal(bytes.Replace(raw, []byte(`"schema_version":1`),
		[]byte(`"schema_version":1,"schema_version":1`), 1))
	require.Error(t, err, "legacy handling must not weaken canonical JSON refusal")
}

func TestLegacyTerminalIntentAcceptsMultipleDormantNetworkMetadataRecords(t *testing.T) {
	record, journal := legacyTerminalIntentFixture(t)
	second := journal.Before.Networks[0]
	second.NetworkID, second.HostBridge = strings.Repeat("c", 64), "mvmbr1"
	second.Subnet, second.HostIPv4 = netip.MustParsePrefix("10.1.0.0/24"), netip.MustParseAddr("10.1.0.1")
	journal.Before.Networks = append(journal.Before.Networks, second)
	raw, err := json.Marshal(journal)
	require.NoError(t, err)
	decoded, err := decodeIntentJournal(raw)
	require.NoError(t, err)
	require.NoError(t, admitLegacyTerminalIntent(record, decoded))
	require.Len(t, decoded.Before.Networks, 2)
	require.Equal(t, strings.Repeat("c", 64), decoded.Before.Networks[1].NetworkID)
}

func TestLegacyTerminalIntentRefusesStoppedReservationsAndDormantPolicies(t *testing.T) {
	for name, change := range map[string]func(*Intent){
		"stopped reservation": func(intent *Intent) { intent.Reservations = secondaryDurableIntent().Reservations },
		"dormant host policy": func(intent *Intent) {
			intent.HostServices = []HostServiceIntent{{ID: strings.Repeat("e", 32),
				SourceNetworkID: intent.Networks[0].NetworkID, Protocol: TCP, Ports: PortInterval{First: 80, Last: 80}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, journal := legacyTerminalIntentFixture(t)
			change(&journal.Before)
			projection, err := journal.Before.compile()
			require.NoError(t, err, "regression fixture must remain valid dormant intent")
			require.True(t, projection.empty, "empty projection alone must not admit retained authority")
			require.Error(t, admitLegacyTerminalIntent(record, journal))
			raw, err := json.Marshal(journal)
			require.NoError(t, err)
			_, err = decodeIntentJournal(raw)
			require.Error(t, err)
		})
	}
}

func TestLegacyTerminalIntentRefusesEveryNonNetworkHistoryField(t *testing.T) {
	for name, change := range map[string]func(*Intent){
		"active networks":         func(intent *Intent) { intent.ActiveNetworks = []string{intent.Networks[0].NetworkID} },
		"reservations":            func(intent *Intent) { intent.Reservations = []EndpointReservation{{}} },
		"endpoints":               func(intent *Intent) { intent.Endpoints = []EndpointBinding{{}} },
		"VM grants":               func(intent *Intent) { intent.VMGrants = []VMTrafficGrant{{}} },
		"network grants":          func(intent *Intent) { intent.NetworkGrants = []NetworkTrafficGrant{{}} },
		"host services":           func(intent *Intent) { intent.HostServices = []HostServiceIntent{{}} },
		"VM host grants":          func(intent *Intent) { intent.VMHostGrants = []VMHostTrafficGrant{{}} },
		"network host grants":     func(intent *Intent) { intent.NetworkHostGrants = []NetworkHostTrafficGrant{{}} },
		"floating IPs":            func(intent *Intent) { intent.FloatingIPs = []FloatingIPBinding{{}} },
		"VM floating grants":      func(intent *Intent) { intent.VMFloatingIPGrants = []VMFloatingIPTrafficGrant{{}} },
		"network floating grants": func(intent *Intent) { intent.NetworkFloatingIPGrants = []NetworkFloatingIPTrafficGrant{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			_, journal := legacyTerminalIntentFixture(t)
			change(&journal.Before)
			require.Error(t, validateLegacyTerminalIntent(journal))
		})
	}
}

func TestLegacyTerminalIntentRefusesActiveUnfinishedAndReboundAuthority(t *testing.T) {
	for name, change := range map[string]func(*generationRecord, *intentJournal){
		"applying": func(record *generationRecord, _ *intentJournal) {
			before := record.committed
			target := generationRecordIdentity{id: testFirewallGenerationB}
			record.lifecycle, record.before, record.target = generationRecordApplying, &before, &target
		},
		"nonempty committed": func(record *generationRecord, _ *intentJournal) {
			record.committed = generationRecordIdentity{id: testFirewallGenerationA}
		},
		"wrong journal boot": func(_ *generationRecord, journal *intentJournal) {
			journal.BootID = testFirewallBootID
		},
		"wrong journal namespace": func(_ *generationRecord, journal *intentJournal) { journal.HostNamespace.Inode++ },
		"dormant nonempty target": func(_ *generationRecord, journal *intentJournal) {
			journal.Target = journal.Before
		},
		"active old history": func(_ *generationRecord, journal *intentJournal) {
			journal.Before.ActiveNetworks = []string{journal.Before.Networks[0].NetworkID}
		},
		"old floating history": func(_ *generationRecord, journal *intentJournal) {
			journal.Before.FloatingIPs = secondaryDurableIntent().FloatingIPs
		},
		"prior boot transition": func(_ *generationRecord, journal *intentJournal) { journal.PriorBoot = &priorBootIntent{} },
		"unsupported schema":    func(_ *generationRecord, journal *intentJournal) { journal.SchemaVersion = 99 },
	} {
		t.Run(name, func(t *testing.T) {
			record, journal := legacyTerminalIntentFixture(t)
			change(&record, &journal)
			require.Error(t, admitLegacyTerminalIntent(record, journal))
		})
	}
}

func TestHostResetEmptyIntentIncludesFloatingResourcesAndBothGrantFamilies(t *testing.T) {
	for name, intent := range map[string]Intent{
		"floating resource": {FloatingIPs: secondaryDurableIntent().FloatingIPs},
		"VM floating grant": {VMFloatingIPGrants: []VMFloatingIPTrafficGrant{{
			ID: "retained-vm-floating", SourceVMID: strings.Repeat("1", 32), DestinationFloatingIPID: strings.Repeat("4", 32),
			Protocol: TCP, Ports: PortInterval{First: 80, Last: 80},
		}}},
		"network floating grant": {NetworkFloatingIPGrants: []NetworkFloatingIPTrafficGrant{{
			ID: "retained-network-floating", SourceNetworkID: strings.Repeat("b", 64), DestinationFloatingIPID: strings.Repeat("4", 32),
			Protocol: TCP, Ports: PortInterval{First: 80, Last: 80},
		}}},
	} {
		t.Run(name, func(t *testing.T) { require.False(t, hostResetIntentEmpty(intent)) })
	}
}

func TestPublishLegacyTerminalIntentRequiresEmptyKernelAndStableAuthority(t *testing.T) {
	for _, name := range []string{"terminal history", "kernel nonempty", "record changed", "intent changed", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			root := prepareGenerationRecordTestRoot(t)
			store := openTestGenerationRecordStore(t, root)
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
			record, journal := legacyTerminalIntentFixture(t)
			recordEvidence, err := store.create(t.Context(), record)
			require.NoError(t, err)
			raw, err := json.Marshal(journal)
			require.NoError(t, err)
			intentPath := filepath.Join(root, "var/lib/mvmctl/firewall/intent.json")
			require.NoError(t, os.WriteFile(intentPath, raw, 0600))
			_, beforeRaw, evidence, found, err := store.readIntent(t.Context())
			require.NoError(t, err)
			require.True(t, found)
			kernel := &fakeKernelGenerationApplication{observations: []kernelObservation{{empty: true}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "kernel nonempty":
				_, target := testGenerationApplicationPair(t)
				require.False(t, target.empty, "negative fixture must contain managed kernel tables")
				kernel.observations = []kernelObservation{kernelObservationFromCompiled(target)}
			case "record changed":
				kernel.recordEvent = func(string) {
					changed, err := beginGenerationRecordApplication(
						record,
						record.committed,
						generationRecordIdentity{id: testFirewallGenerationB},
					)
					require.NoError(t, err)
					_, err = store.replace(t.Context(), record, recordEvidence, changed)
					require.NoError(t, err)
				}
			case "intent changed":
				kernel.recordEvent = func(string) { require.NoError(t, os.WriteFile(intentPath, append(raw, '\n'), 0600)) }
			case "cancelled":
				cancel()
			}
			_, _, _, err = publishLegacyTerminalIntent(ctx, store, kernel, record, journal, beforeRaw, evidence)
			if name != "terminal history" {
				require.Error(t, err)
				retained, readErr := os.ReadFile(intentPath)
				require.NoError(t, readErr)
				require.NotContains(t, string(retained), `"schema_version":2`, "refusal republished terminal intent")
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff([]string{"observe"}, kernel.events); diff != "" {
				t.Errorf("kernel operations (-want +got):\n%s", diff)
			}
			readback, newRaw, newEvidence, found, err := store.readIntent(t.Context())
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, uint32(2), readback.SchemaVersion)
			require.Len(t, readback.Before.Networks, 1)
			require.Equal(t, strings.Repeat("b", 64), readback.Before.Networks[0].NetworkID)
			require.True(t, hostResetIntentEmpty(readback.Target))
			require.NotEqual(t, beforeRaw, newRaw)
			require.NotEqual(t, evidence, newEvidence)
			current, currentEvidence, found, err := store.read(t.Context())
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, generationRecordsEqual(record, current))
			require.Equal(t, recordEvidence, currentEvidence, "schema publication must not rebind kernel authority")
		})
	}
}
