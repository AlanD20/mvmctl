package firewall

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testFirewallBootID       = "12345678-1234-4abc-8def-1234567890ab"
	testFirewallGenerationA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testFirewallGenerationB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testFirewallGenerationC  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testFirewallNamespaceDev = uint64(41)
	testFirewallNamespaceIno = uint64(73)
)

func TestGenerationRecordCodecRoundTripsExactLifecycleState(t *testing.T) {
	t.Parallel()

	binding := testGenerationRecordBinding()
	committed := mustCommittedGenerationRecord(t, binding, generationRecordIdentity{
		empty: true,
		id:    testFirewallGenerationA,
	})
	applying, err := beginGenerationRecordApplication(
		committed,
		generationRecordIdentity{empty: true, id: testFirewallGenerationA},
		generationRecordIdentity{id: testFirewallGenerationB},
	)
	require.NoError(t, err)

	tests := map[string]generationRecord{
		"committed empty generation": committed,
		"applying exact transition":  applying,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := encodeGenerationRecord(want)
			require.NoError(t, err)
			got, err := decodeGenerationRecord(raw)
			require.NoError(t, err)
			if diff := cmp.Diff(want, got, generationRecordCmpOptions...); diff != "" {
				t.Errorf("generation record mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGenerationRecordCodecRejectsNonCanonicalAuthority(t *testing.T) {
	t.Parallel()

	valid, err := encodeGenerationRecord(mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{id: testFirewallGenerationA},
	))
	require.NoError(t, err)
	validJSON := string(valid)

	tests := map[string]string{
		"empty input": "",
		"unknown field": strings.Replace(
			validJSON,
			`"schema_version":1`,
			`"schema_version":1,"foreign":true`,
			1,
		),
		"duplicate field": strings.Replace(
			validJSON,
			`"lifecycle":"committed"`,
			`"lifecycle":"committed","lifecycle":"committed"`,
			1,
		),
		"case-folded duplicate": strings.Replace(
			validJSON,
			`"lifecycle":"committed"`,
			`"LIFECYCLE":"committed","lifecycle":"committed"`,
			1,
		),
		"nested duplicate": strings.Replace(
			validJSON,
			`"device":41`,
			`"device":41,"device":41`,
			1,
		),
		"missing required field": strings.Replace(
			validJSON,
			`"before":null,"target":null`,
			`"before":null`,
			1,
		),
		"trailing value": validJSON + `{}`,
		"wrong schema":   strings.Replace(validJSON, `"schema_version":1`, `"schema_version":2`, 1),
		"wrong boot ID": strings.Replace(
			validJSON,
			testFirewallBootID,
			"not-a-boot-id",
			1,
		),
		"zero namespace device": strings.Replace(validJSON, `"device":41`, `"device":0`, 1),
		"zero namespace inode":  strings.Replace(validJSON, `"inode":73`, `"inode":0`, 1),
		"bad generation ID": strings.Replace(
			validJSON,
			testFirewallGenerationA,
			"AAAA",
			1,
		),
		"empty generation without exact ID": strings.Replace(
			validJSON,
			`"empty":false,"generation_id":"`+testFirewallGenerationA+`"`,
			`"empty":true,"generation_id":""`,
			1,
		),
		"committed carries before": strings.Replace(
			validJSON,
			`"before":null`,
			`"before":{"empty":false,"generation_id":"`+testFirewallGenerationA+`"}`,
			1,
		),
		"scalar before": strings.Replace(validJSON, `"before":null`, `"before":false`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGenerationRecord([]byte(raw))
			require.Error(t, err)
		})
	}
}

func TestGenerationRecordTransitionsRequireExactIdentities(t *testing.T) {
	t.Parallel()

	committed := mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{id: testFirewallGenerationA},
	)

	_, err := beginGenerationRecordApplication(
		committed,
		generationRecordIdentity{id: testFirewallGenerationC},
		generationRecordIdentity{id: testFirewallGenerationB},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "does not match committed authority")

	_, err = beginGenerationRecordApplication(
		committed,
		generationRecordIdentity{id: testFirewallGenerationA},
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "must change generation")

	applying, err := beginGenerationRecordApplication(
		committed,
		generationRecordIdentity{id: testFirewallGenerationA},
		generationRecordIdentity{empty: true, id: testFirewallGenerationB},
	)
	require.NoError(t, err)
	_, err = completeGenerationRecordApplication(
		applying,
		generationRecordIdentity{empty: true, id: testFirewallGenerationC},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "does not match applying authority")
}

func TestGenerationRecordAdmissionRejectsForeignHostBinding(t *testing.T) {
	t.Parallel()

	binding := testGenerationRecordBinding()
	record := mustCommittedGenerationRecord(
		t,
		binding,
		generationRecordIdentity{id: testFirewallGenerationA},
	)

	tests := map[string]generationRecordBinding{
		"wrong boot": {
			bootID:    "22345678-1234-4abc-8def-1234567890ab",
			namespace: binding.namespace,
		},
		"wrong namespace device": {
			bootID: binding.bootID,
			namespace: generationNamespaceIdentity{
				device: binding.namespace.device + 1,
				inode:  binding.namespace.inode,
			},
		},
		"wrong namespace inode": {
			bootID: binding.bootID,
			namespace: generationNamespaceIdentity{
				device: binding.namespace.device,
				inode:  binding.namespace.inode + 1,
			},
		},
	}
	for name, current := range tests {
		t.Run(name, func(t *testing.T) {
			err := admitGenerationRecord(record, current)
			require.Error(t, err)
		})
	}
}

var generationRecordCmpOptions = []cmp.Option{cmp.AllowUnexported(
	generationRecord{},
	generationRecordBinding{},
	generationNamespaceIdentity{},
	generationRecordIdentity{},
)}

func testGenerationRecordBinding() generationRecordBinding {
	return generationRecordBinding{
		bootID: testFirewallBootID,
		namespace: generationNamespaceIdentity{
			device: testFirewallNamespaceDev,
			inode:  testFirewallNamespaceIno,
		},
	}
}

func mustCommittedGenerationRecord(
	t *testing.T,
	binding generationRecordBinding,
	identity generationRecordIdentity,
) generationRecord {
	t.Helper()
	record, err := newCommittedGenerationRecord(binding, identity)
	require.NoError(t, err)
	return record
}
