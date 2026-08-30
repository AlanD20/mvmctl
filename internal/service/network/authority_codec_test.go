package network

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const testBootID = "9f1c2d3e-4a5b-4678-9abc-def012345678"

func TestNetworkAuthorityCodecRoundTripUsesExactCanonicalProjection(t *testing.T) {
	t.Parallel()

	record := testNetworkAuthorityRecord(t, testOwnerUID, testNetworkID, testBootID)
	raw, err := encodeNetworkAuthorityRecord(record)
	require.NoError(t, err)
	wantRaw := `{"schema_version":1,"owner_uid":1000,"network_id":"` + testNetworkID +
		`","boot_id":"` + testBootID +
		`","managed_bridge":{"name":"` + testNames.managedBridge.String() +
		`","ifindex":41,"namespace":{"device":7,"inode":11}}}`
	assert.Equal(t, wantRaw, string(raw))

	decoded, err := decodeNetworkAuthorityRecord(raw)
	require.NoError(t, err)
	if diff := cmp.Diff(record, decoded, cmp.AllowUnexported(
		networkAuthorityRecord{}, linkIdentity{}, namespaceIdentity{},
	)); diff != "" {
		t.Errorf("network authority round trip mismatch (-want +got):\n%s", diff)
	}
}

func TestNetworkAuthorityCodecRejectsAnythingOutsideClosedSchema(t *testing.T) {
	t.Parallel()

	valid := canonicalNetworkAuthorityJSON(testOwnerUID, testNetworkID, testBootID)
	tests := map[string][]byte{
		"empty":                nil,
		"oversized":            []byte(strings.Repeat("x", maxNetworkAuthorityRecordBytes+1)),
		"malformed":            []byte("{"),
		"top level array":      []byte("[]"),
		"unknown root field":   []byte(strings.Replace(valid, `"schema_version":1`, `"extra":0,"schema_version":1`, 1)),
		"unknown nested field": []byte(strings.Replace(valid, `"ifindex":41`, `"extra":0,"ifindex":41`, 1)),
		"duplicate root field": []byte(
			strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		),
		"case conflict": []byte(
			strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"Schema_Version":1`, 1),
		),
		"duplicate nested field": []byte(
			strings.Replace(valid, `"device":7`, `"device":7,"device":7`, 1),
		),
		"missing root field":   []byte(strings.Replace(valid, `,"boot_id":"`+testBootID+`"`, "", 1)),
		"missing nested field": []byte(strings.Replace(valid, `,"inode":11`, "", 1)),
		"trailing object":      []byte(valid + `{}`),
		"trailing scalar":      []byte(valid + ` true`),
		"unsupported schema":   []byte(strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1)),
		"schema wrong type":    []byte(strings.Replace(valid, `"schema_version":1`, `"schema_version":"1"`, 1)),
		"schema null":          []byte(strings.Replace(valid, `"schema_version":1`, `"schema_version":null`, 1)),
		"owner zero":           []byte(strings.Replace(valid, `"owner_uid":1000`, `"owner_uid":0`, 1)),
		"owner negative":       []byte(strings.Replace(valid, `"owner_uid":1000`, `"owner_uid":-1`, 1)),
		"owner fractional":     []byte(strings.Replace(valid, `"owner_uid":1000`, `"owner_uid":1.5`, 1)),
		"owner overflow":       []byte(strings.Replace(valid, `"owner_uid":1000`, `"owner_uid":4294967296`, 1)),
		"network wrong type": []byte(
			strings.Replace(valid, `"network_id":"`+testNetworkID+`"`, `"network_id":[]`, 1),
		),
		"network uppercase": []byte(strings.Replace(valid, testNetworkID, strings.ToUpper(testNetworkID), 1)),
		"boot invalid":      []byte(strings.Replace(valid, testBootID, "not-a-boot-id", 1)),
		"bridge wrong name": []byte(
			strings.Replace(valid, testNames.managedBridge.String(), "mvb000000000000", 1),
		),
		"ifindex zero":       []byte(strings.Replace(valid, `"ifindex":41`, `"ifindex":0`, 1)),
		"ifindex wrong type": []byte(strings.Replace(valid, `"ifindex":41`, `"ifindex":true`, 1)),
		"namespace wrong type": []byte(
			strings.Replace(valid, `"namespace":{"device":7,"inode":11}`, `"namespace":null`, 1),
		),
		"namespace device zero":   []byte(strings.Replace(valid, `"device":7`, `"device":0`, 1)),
		"namespace device object": []byte(strings.Replace(valid, `"device":7`, `"device":{"value":7}`, 1)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeNetworkAuthorityRecord(raw)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeNetworkError, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.NotContains(t, err.Error(), fmt.Sprint(testOwnerUID))
		})
	}
}

func TestNetworkAuthorityRecordBindsOwnerBootAndExactBridge(t *testing.T) {
	t.Parallel()

	validBridge := testManagedBridge(t, testOwnerUID, testNetworkID)
	tests := map[string]struct {
		caller    networkCaller
		networkID NetworkID
		bootID    string
		bridge    linkIdentity
	}{
		"zero caller": {
			caller:    networkCaller{},
			networkID: NetworkID(testNetworkID),
			bootID:    testBootID,
			bridge:    validBridge,
		},
		"invalid network": {
			caller:    networkCaller{uid: testOwnerUID},
			networkID: NetworkID("bad"),
			bootID:    testBootID,
			bridge:    validBridge,
		},
		"invalid boot": {
			caller:    networkCaller{uid: testOwnerUID},
			networkID: NetworkID(testNetworkID),
			bootID:    "bad",
			bridge:    validBridge,
		},
		"wrong bridge name": {
			caller:    networkCaller{uid: testOwnerUID},
			networkID: NetworkID(testNetworkID),
			bootID:    testBootID,
			bridge: linkIdentity{
				name: "mvb000000000000", ifIndex: 41, namespace: namespaceIdentity{device: 7, inode: 11},
			},
		},
		"missing bridge ifindex": {
			caller:    networkCaller{uid: testOwnerUID},
			networkID: NetworkID(testNetworkID),
			bootID:    testBootID,
			bridge: linkIdentity{
				name: validBridge.name, namespace: validBridge.namespace,
			},
		},
		"missing bridge namespace": {
			caller:    networkCaller{uid: testOwnerUID},
			networkID: NetworkID(testNetworkID),
			bootID:    testBootID,
			bridge:    linkIdentity{name: validBridge.name, ifIndex: validBridge.ifIndex},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newNetworkAuthorityRecord(tc.caller, tc.networkID, tc.bootID, tc.bridge)
			require.Error(t, err)
		})
	}

	record := testNetworkAuthorityRecord(t, testOwnerUID, testNetworkID, testBootID)
	require.NoError(t, authorizeNetworkOwner(networkCaller{uid: testOwnerUID}, record))
	err := authorizeNetworkOwner(networkCaller{uid: testOwnerUID + 1}, record)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeUnauthorized, domainErr.Code)
	assert.NotContains(t, err.Error(), fmt.Sprint(testOwnerUID))
}

func TestNetworkStorePathComponentsRejectsNonCanonicalRoots(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"relative":      "var/lib/mvmctl/networks",
		"dot component": "/var/./lib/mvmctl/networks",
		"wrong depth":   "/var/lib/networks",
		"root":          "/",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := networkStorePathComponents(value)
			require.Error(t, err)
		})
	}

	got, err := networkStorePathComponents("/var/lib/mvmctl/networks")
	require.NoError(t, err)
	want := [4]string{"var", "lib", "mvmctl", "networks"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("network authority root components mismatch (-want +got):\n%s", diff)
	}
}

func canonicalNetworkAuthorityJSON(uid uint32, networkID, bootID string) string {
	names := contractTopologyNames(uid, testVMID, networkID)
	return `{"schema_version":1,"owner_uid":` + fmt.Sprint(uid) + `,"network_id":"` + networkID +
		`","boot_id":"` + bootID +
		`","managed_bridge":{"name":"` + names.managedBridge.String() +
		`","ifindex":41,"namespace":{"device":7,"inode":11}}}`
}

func testNetworkAuthorityRecord(t *testing.T, uid uint32, networkID, bootID string) networkAuthorityRecord {
	t.Helper()
	return networkAuthorityRecord{
		schemaVersion: networkAuthorityRecordSchemaVersion,
		ownerUID:      uid,
		networkID:     NetworkID(networkID),
		bootID:        bootID,
		managedBridge: testManagedBridge(t, uid, networkID),
	}
}

func testManagedBridge(t *testing.T, uid uint32, networkID string) linkIdentity {
	t.Helper()
	names := contractTopologyNames(uid, testVMID, networkID)
	return linkIdentity{
		name:      names.managedBridge,
		ifIndex:   41,
		namespace: namespaceIdentity{device: 7, inode: 11},
	}
}
