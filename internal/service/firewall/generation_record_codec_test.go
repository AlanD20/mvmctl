package firewall

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
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

// Rationale: The persistent file is security authority, so its field names, ordering, null transition slots, empty
// marker, and exact generation identities must remain reviewable and stable rather than being inferred from a roundtrip.
func TestGenerationRecordCodecEmitsCanonicalLifecycleJSON(t *testing.T) {
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
	tests := map[string]struct {
		record generationRecord
		want   string
	}{
		"committed": {
			record: committed,
			want: `{"schema_version":1,"lifecycle":"committed","boot_id":"` + testFirewallBootID +
				`","host_namespace":{"device":41,"inode":73},"committed":{"empty":true,"generation_id":"` +
				testFirewallGenerationA + `"},"before":null,"target":null}`,
		},
		"applying": {
			record: applying,
			want: `{"schema_version":1,"lifecycle":"applying","boot_id":"` + testFirewallBootID +
				`","host_namespace":{"device":41,"inode":73},"committed":{"empty":true,"generation_id":"` +
				testFirewallGenerationA + `"},"before":{"empty":true,"generation_id":"` +
				testFirewallGenerationA + `"},"target":{"empty":false,"generation_id":"` +
				testFirewallGenerationB + `"}}`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := encodeGenerationRecord(tc.record)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(raw))
		})
	}
}

// Rationale: A permissive authority decoder could reinterpret corrupt or future state and authorize fixed-name
// mutation. Every structural, size, type, and lifecycle boundary must fail with the firewall authority error identity.
func TestGenerationRecordCodecRejectsNonCanonicalAuthority(t *testing.T) {
	t.Parallel()

	valid, err := encodeGenerationRecord(mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{id: testFirewallGenerationA},
	))
	require.NoError(t, err)
	validJSON := string(valid)

	applying, err := beginGenerationRecordApplication(
		mustCommittedGenerationRecord(
			t,
			testGenerationRecordBinding(),
			generationRecordIdentity{empty: true, id: testFirewallGenerationA},
		),
		generationRecordIdentity{empty: true, id: testFirewallGenerationA},
		generationRecordIdentity{id: testFirewallGenerationB},
	)
	require.NoError(t, err)
	applyingRaw, err := encodeGenerationRecord(applying)
	require.NoError(t, err)
	applyingJSON := string(applyingRaw)

	tests := map[string]struct {
		raw         string
		wantMessage string
	}{
		"empty input": {raw: "", wantMessage: "record is empty"},
		"exact size limit malformed": {
			raw:         strings.Repeat(" ", maxGenerationRecordBytes),
			wantMessage: "decode strict",
		},
		"over size limit": {
			raw:         strings.Repeat(" ", maxGenerationRecordBytes+1),
			wantMessage: "exceeds size limit",
		},
		"malformed object": {raw: `{"schema_version":`, wantMessage: "decode strict"},
		"top-level array":  {raw: `[]`, wantMessage: "decode strict"},
		"unknown field": {
			raw: strings.Replace(
				validJSON, `"schema_version":1`, `"schema_version":1,"foreign":true`, 1,
			),
			wantMessage: "decode strict",
		},
		"duplicate field": {raw: strings.Replace(
			validJSON, `"lifecycle":"committed"`, `"lifecycle":"committed","lifecycle":"committed"`, 1,
		), wantMessage: "decode strict"},
		"case-folded duplicate": {raw: strings.Replace(
			validJSON, `"lifecycle":"committed"`, `"LIFECYCLE":"committed","lifecycle":"committed"`, 1,
		), wantMessage: "decode strict"},
		"nested duplicate": {raw: strings.Replace(
			validJSON, `"device":41`, `"device":41,"device":41`, 1,
		), wantMessage: "decode strict"},
		"unexpected nested scalar": {raw: strings.Replace(
			validJSON, `"device":41`, `"device":{"nested":41}`, 1,
		), wantMessage: "decode strict"},
		"missing required field": {raw: strings.Replace(
			validJSON, `"before":null,"target":null`, `"before":null`, 1,
		), wantMessage: "decode strict"},
		"trailing value": {raw: validJSON + `{}`, wantMessage: "trailing input"},
		"wrong schema": {
			raw:         strings.Replace(validJSON, `"schema_version":1`, `"schema_version":2`, 1),
			wantMessage: "unsupported",
		},
		"schema type": {
			raw:         strings.Replace(validJSON, `"schema_version":1`, `"schema_version":"1"`, 1),
			wantMessage: "decode firewall generation record",
		},
		"lifecycle type": {
			raw:         strings.Replace(validJSON, `"lifecycle":"committed"`, `"lifecycle":1`, 1),
			wantMessage: "decode firewall generation record",
		},
		"identity empty type": {
			raw:         strings.Replace(validJSON, `"empty":false`, `"empty":"false"`, 1),
			wantMessage: "decode firewall generation record",
		},
		"namespace device type": {
			raw:         strings.Replace(validJSON, `"device":41`, `"device":"41"`, 1),
			wantMessage: "decode firewall generation record",
		},
		"wrong boot ID": {
			raw:         strings.Replace(validJSON, testFirewallBootID, "not-a-boot-id", 1),
			wantMessage: "boot ID is invalid",
		},
		"zero namespace device": {
			raw:         strings.Replace(validJSON, `"device":41`, `"device":0`, 1),
			wantMessage: "namespace identity is invalid",
		},
		"zero namespace inode": {
			raw:         strings.Replace(validJSON, `"inode":73`, `"inode":0`, 1),
			wantMessage: "namespace identity is invalid",
		},
		"bad generation ID": {
			raw:         strings.Replace(validJSON, testFirewallGenerationA, "AAAA", 1),
			wantMessage: "identity is invalid",
		},
		"empty generation without exact ID": {
			raw: strings.Replace(
				validJSON,
				`"empty":false,"generation_id":"`+testFirewallGenerationA+`"`,
				`"empty":true,"generation_id":""`,
				1,
			),
			wantMessage: "identity is invalid",
		},
		"committed carries before": {
			raw: strings.Replace(
				validJSON,
				`"before":null`,
				`"before":{"empty":false,"generation_id":"`+testFirewallGenerationA+`"}`,
				1,
			),
			wantMessage: "contains an applying transition",
		},
		"scalar before": {
			raw:         strings.Replace(validJSON, `"before":null`, `"before":false`, 1),
			wantMessage: "decode strict",
		},
		"applying missing transition": {
			raw:         strings.Replace(validJSON, `"lifecycle":"committed"`, `"lifecycle":"applying"`, 1),
			wantMessage: "missing transition authority",
		},
		"applying committed mismatch": {raw: strings.Replace(
			applyingJSON,
			`"before":{"empty":true,"generation_id":"`+testFirewallGenerationA+`"}`,
			`"before":{"empty":true,"generation_id":"`+testFirewallGenerationC+`"}`,
			1,
		), wantMessage: "does not retain the committed generation"},
		"applying unchanged target": {raw: strings.Replace(
			applyingJSON,
			`"target":{"empty":false,"generation_id":"`+testFirewallGenerationB+`"}`,
			`"target":{"empty":true,"generation_id":"`+testFirewallGenerationA+`"}`,
			1,
		), wantMessage: "does not change generation"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGenerationRecord([]byte(tc.raw))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantMessage)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeNetworkFirewallFailed, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
		})
	}
}

// Rationale: Exact begin/complete/admit success is the positive authority path. Error-only transition tests cannot
// detect an implementation that rejects all state or silently commits the wrong target.
func TestGenerationRecordTransitionsAndAdmissionSucceedOnlyForExactState(t *testing.T) {
	t.Parallel()

	binding := testGenerationRecordBinding()
	before := generationRecordIdentity{empty: true, id: testFirewallGenerationA}
	target := generationRecordIdentity{id: testFirewallGenerationB}
	committed := mustCommittedGenerationRecord(t, binding, before)

	applying, err := beginGenerationRecordApplication(committed, before, target)
	require.NoError(t, err)
	wantApplying := generationRecord{
		schemaVersion: generationRecordSchemaVersion,
		lifecycle:     generationRecordApplying,
		binding:       binding,
		committed:     before,
		before:        &generationRecordIdentity{empty: before.empty, id: before.id},
		target:        &generationRecordIdentity{empty: target.empty, id: target.id},
	}
	if diff := cmp.Diff(wantApplying, applying, generationRecordCmpOptions...); diff != "" {
		t.Errorf("applying generation record mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, admitGenerationRecord(applying, binding))

	completed, err := completeGenerationRecordApplication(applying, target)
	require.NoError(t, err)
	wantCompleted := mustCommittedGenerationRecord(t, binding, target)
	if diff := cmp.Diff(wantCompleted, completed, generationRecordCmpOptions...); diff != "" {
		t.Errorf("completed generation record mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, admitGenerationRecord(completed, binding))
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
