package firewall

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
)

func persistentFloatingLeaseTestState(t *testing.T) *mutationLeaseState {
	t.Helper()
	state := floatingLeaseTestState(t)
	state.intent.FloatingIPs[0].Lifetime = FloatingIPPersistent
	state.intent.FloatingIPs[0].LeaseDuration = 0
	var err error
	state.compiled, err = state.intent.compile()
	require.NoError(t, err)
	return state
}

func TestPersistentFloatingIPIntentRequiresExplicitLifetime(t *testing.T) {
	for name, change := range map[string]func(*FloatingIPBinding){
		"persistent": func(binding *FloatingIPBinding) {
			binding.Lifetime, binding.LeaseDuration = FloatingIPPersistent, 0
		},
		"finite":                          nil,
		"missing lifetime":                func(binding *FloatingIPBinding) { binding.Lifetime = "" },
		"unknown lifetime":                func(binding *FloatingIPBinding) { binding.Lifetime = "forever" },
		"persistent with finite duration": func(binding *FloatingIPBinding) { binding.Lifetime = FloatingIPPersistent },
		"finite without duration":         func(binding *FloatingIPBinding) { binding.LeaseDuration = 0 },
		"ownerless with lifetime": func(binding *FloatingIPBinding) {
			binding.OwnerVMID, binding.OwnerLaunchGeneration, binding.LeaseDuration = "", 0, 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			intent := secondaryDurableIntent()
			if change != nil {
				change(&intent.FloatingIPs[0])
			}
			raw, err := json.Marshal(intent)
			require.NoError(t, err)
			var restored Intent
			require.NoError(t, json.Unmarshal(raw, &restored))
			compiled, err := restored.compile()
			if name != "persistent" && name != "finite" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, intent.FloatingIPs[0].Lifetime, compiled.floatingIPs[0].lifetime)
			require.Equal(t, intent.FloatingIPs[0].LeaseDuration, compiled.floatingIPs[0].leaseDuration)
			connection := &recordingKernelApplicationConnection{}
			require.NoError(t, queueKernelApplicationTable(connection, compiled.bridge, compiled.bridge, nil))
			for _, event := range connection.events {
				require.NotContains(t, event, "secondary key", "compilation or publication activated ownership")
			}
		})
	}
}

func TestPersistentFloatingIPDigestAndSealBindLifetime(t *testing.T) {
	finite := floatingLeaseTestState(t)
	persistent := persistentFloatingLeaseTestState(t)
	require.NotEqual(t, finite.compiled.id, persistent.compiled.id)
	require.NoError(t, validateKernelApplicationTarget(persistent.compiled))
	persistent.compiled.floatingIPs[0].lifetime = FloatingIPFinite
	require.Error(t, validateKernelApplicationTarget(persistent.compiled), "changing ownership mode escaped the seal")
}

func TestDecodePersistentFloatingIPElementHasNoExpiration(t *testing.T) {
	fields := secondaryElementFields(t)[:1]
	got, err := decodeSecondaryLeaseElements(
		[]netlink.Message{secondaryElementsMessage(t, fields)}, &kernelBaseSequence{},
	)
	require.NoError(t, err)
	want := []secondaryLeaseElement{{
		lifetime: FloatingIPPersistent,
		key:      [20]byte{'m', 'v', 'm', 'v', '-', 't', 'e', 's', 't', 0, 0, 0, 0, 0, 0, 0, 10, 25, 0, 100},
	}}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(secondaryLeaseElement{})); diff != "" {
		t.Errorf("persistent readback (-want +got):\n%s", diff)
	}
	for _, partial := range [][]netlink.Attribute{
		append(secondaryElementFields(t)[:1], secondaryElementFields(t)[1]),
		append(secondaryElementFields(t)[:1], secondaryElementFields(t)[2]),
	} {
		_, err := decodeSecondaryLeaseElements(
			[]netlink.Message{secondaryElementsMessage(t, partial)}, &kernelBaseSequence{},
		)
		require.Error(t, err, "one missing timed attribute must not turn finite authority into persistent authority")
	}
}

func TestPersistentFloatingIPObservationRequiresTheExactKernelLifetime(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		for _, observedPersistent := range []bool{false, true} {
			t.Run(
				fmt.Sprintf("persistent=%v/observedPersistent=%v", persistent, observedPersistent),
				func(t *testing.T) {
					state := floatingLeaseTestState(t)
					if persistent {
						state = persistentFloatingLeaseTestState(t)
					}
					element := secondaryLeaseElement{lifetime: FloatingIPFinite, key: secondaryLiveKey(),
						timeout: 5 * time.Second, remaining: time.Second}
					if observedPersistent {
						element.lifetime, element.timeout, element.remaining = FloatingIPPersistent, 0, 0
					}
					observed := kernelObservationFromCompiled(state.compiled)
					observed.tables[0].secondaryElements = []secondaryLeaseElement{element}
					binding := state.intent.FloatingIPs[0]
					result, err := state.observeFloatingIPLease(binding.ID, binding.Generation, observed)
					if persistent != observedPersistent {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					require.True(t, result.Present)
					require.Equal(t, binding.Lifetime, result.Lifetime)
					require.Equal(t, element.remaining, result.Remaining)
					require.NoError(t, state.verifyFloatingIPLeaseInstalled(binding.ID, binding.Generation, observed))
					observed.tables[0].secondaryElements = nil
					require.Error(t, state.verifyFloatingIPLeaseInstalled(binding.ID, binding.Generation, observed),
						"a persistent journal must not prove a missing kernel key is installed")
				},
			)
		}
	}
}

func TestPersistentFloatingIPExactEffectHasNoTimeoutAndFencingRevokesIt(t *testing.T) {
	state := persistentFloatingLeaseTestState(t)
	connection := &recordingKernelApplicationConnection{}
	floatingLeaseEffectNamespace(state, connection)
	require.NoError(t, state.mutateFloatingIPLeaseElement(t.Context(), state.intent.FloatingIPs[0], true))
	want := []string{
		fmt.Sprintf("add secondary key secondary_ipv4/%x timeout=0s", secondaryLiveKey()),
		"flush",
		"close",
	}
	if diff := cmp.Diff(want, connection.events); diff != "" {
		t.Errorf("persistent insert (-want +got):\n%s", diff)
	}
	before := state.compiled
	observed := kernelObservationFromCompiled(before)
	observed.tables[0].secondaryElements = []secondaryLeaseElement{
		{lifetime: FloatingIPPersistent, key: secondaryLiveKey()},
	}
	fenced := cloneIntent(state.intent)
	fenced.FloatingIPs[0] = ownerlessFloatingLease(fenced.FloatingIPs[0])
	fenced.FloatingIPs[0].Generation++
	target, err := fenced.compile()
	require.NoError(t, err)
	revoked := secondaryLeaseRevocations(before, target, observed)
	require.Len(t, revoked, 1)
	key := secondaryLiveKey()
	require.Equal(t, key[:], revoked[0].Key)
	connection = &recordingKernelApplicationConnection{}
	require.NoError(t, queueKernelApplicationTable(connection, target.bridge, before.bridge, revoked))
	for _, event := range connection.events {
		require.False(t, strings.HasPrefix(event, "add secondary key"), "fencing reactivated permission")
	}
}

func TestIntentJournalRefusesOldImplicitFloatingIPLifetime(t *testing.T) {
	journal := intentJournal{SchemaVersion: intentJournalSchema, Before: secondaryDurableIntent()}
	raw, err := json.Marshal(journal)
	require.NoError(t, err)
	_, err = decodeIntentJournal(raw)
	require.NoError(t, err)
	journal.SchemaVersion = 1
	raw, err = json.Marshal(journal)
	require.NoError(t, err)
	_, err = decodeIntentJournal(raw)
	require.Error(t, err, "old lifetime grammar must not be reinterpreted as persistent authority")
}
