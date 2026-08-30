package jailer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Rationale: these fixtures are written directly from ADR-0016's accepted schema rather than produced by the
// transition functions. Exact bytes make field renames, omissions, additions, and lifecycle-shape drift visible.
func TestInstanceSchemaThreeCanonicalJSON(t *testing.T) {
	t.Parallel()

	records := schemaThreeProofRecords()
	for name, want := range schemaThreeCanonicalJSON() {
		name, want := name, want
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw, err := encodeInstanceRecord(records[name])
			require.NoError(t, err)
			assert.Equal(t, want, string(raw))

			decoded, err := decodeInstanceRecord([]byte(want))
			require.NoError(t, err)
			assertInstanceRecordEqual(t, records[name], decoded)
		})
	}
}

// Rationale: complete independently constructed records prove each pure transition, including retained authority and
// generation changes. Field-by-field assertions alone could miss an accidentally added or copied authority claim.
func TestInstanceSchemaThreeTransitionsMatchIndependentRecords(t *testing.T) {
	t.Parallel()

	want := schemaThreeProofRecords()
	launching, err := registerLaunchingInstanceRecord(
		testInstanceCaller(), testLaunchIntent(false), testReleaseIdentity(), nil,
	)
	require.NoError(t, err)
	assertInstanceRecordEqual(t, want["launching"], launching)

	launchPresent := schemaThreeProofLaunch(instanceLaunchRelay{
		kind:    instanceRelayPresent,
		locator: instanceProcessLocator{pid: 4343, startTimeTicks: 876543},
	})
	launchingPresent := want["launching"]
	launchingPresent.launch = &launchPresent
	active, err := activateInstanceRecord(launchingPresent, schemaThreeProofActive())
	require.NoError(t, err)
	assertInstanceRecordEqual(t, want["active"], active)

	cleaningLaunch, err := beginInstanceCleanup(testInstanceCaller(), want["launching"])
	require.NoError(t, err)
	assertInstanceRecordEqual(t, want["cleaning_from_launching"], cleaningLaunch)
	retry, err := beginInstanceCleanup(testInstanceCaller(), cleaningLaunch)
	require.NoError(t, err)
	assertInstanceRecordEqual(t, want["cleaning_from_launching"], retry)

	cleaningActive, err := beginInstanceCleanup(testInstanceCaller(), want["active"])
	require.NoError(t, err)
	assertInstanceRecordEqual(t, want["cleaning_from_active"], cleaningActive)
	cleaned, err := completeInstanceCleanup(cleaningActive)
	require.NoError(t, err)
	assertInstanceRecordEqual(t, want["cleaned"], cleaned)

	relaunched, err := registerLaunchingInstanceRecord(
		testInstanceCaller(), testLaunchIntent(false), testReleaseIdentity(), &cleaned,
	)
	require.NoError(t, err)
	wantRelaunched := want["launching"]
	wantRelaunched.launchGeneration = 2
	wantRelaunched.cleanupGeneration = 1
	assertInstanceRecordEqual(t, wantRelaunched, relaunched)
}

func assertInstanceRecordEqual(t *testing.T, want, got instanceRecord) {
	t.Helper()

	if diff := diffInstanceRecord(want, got); diff != "" {
		t.Errorf("instance record mismatch (-want +got):\n%s", diff)
	}
}

func schemaThreeCanonicalJSON() map[string]string {
	const launchAbsent = `{"owner_gid":1001,"host_boot_id":"123e4567-e89b-42d3-a456-426614174000",` +
		`"pid":4242,"start_time_ticks":987654,"relay":{"kind":"absent"},` +
		`"managed_cache":{"device_major":8,"device_minor":1,"inode":12345,` +
		`"mount_identity":{"kind":"unique","id":67890}},` +
		`"release":{"version":"1.16.0","architecture":"x86_64",` +
		`"firecracker_sha256":"` + testReleaseHash + `","jailer_sha256":"` + testJailerHash + `"}}`
	const launchPresent = `{"owner_gid":1001,"host_boot_id":"123e4567-e89b-42d3-a456-426614174000",` +
		`"pid":4242,"start_time_ticks":987654,` +
		`"relay":{"kind":"present","pid":4343,"start_time_ticks":876543},` +
		`"managed_cache":{"device_major":8,"device_minor":1,"inode":12345,` +
		`"mount_identity":{"kind":"unique","id":67890}},` +
		`"release":{"version":"1.16.0","architecture":"x86_64",` +
		`"firecracker_sha256":"` + testReleaseHash + `","jailer_sha256":"` + testJailerHash + `"}}`
	const credentialsUID = `{"real":1000,"effective":1000,"saved":1000,"filesystem":1000}`
	const credentialsGID = `{"real":1001,"effective":1001,"saved":1001,"filesystem":1001}`
	const active = `{"user_ids":` + credentialsUID + `,"group_ids":` + credentialsGID +
		`,"supplementary_groups":[],"mount_namespace":{"device":41,"inode":42},` +
		`"jail_root":{"device":43,"inode":44},` +
		`"executable":{"device":45,"inode":46,"size_bytes":120,"mode":33261,"uid":0,"gid":0,"links":1,` +
		`"manifest_sha256":"` + testReleaseHash + `"},` +
		`"relay":{"kind":"present","user_ids":` + credentialsUID + `,"group_ids":` + credentialsGID +
		`,"supplementary_groups":[],"mount_namespace":{"device":51,"inode":52},` +
		`"root":{"device":53,"inode":54},` +
		`"executable":{"device":55,"inode":56,"size_bytes":120,"mode":33261,"uid":0,"gid":0,"links":1,` +
		`"sha256":"` + testRelayHash + `"}}}`

	return map[string]string{
		"launching": `{"schema_version":3,"owner_uid":1000,"vm_id":"` + testVMID +
			`","lifecycle":"launching","launch_generation":1,"cleanup_generation":0,"launch":` + launchAbsent + `}`,
		"active": `{"schema_version":3,"owner_uid":1000,"vm_id":"` + testVMID +
			`","lifecycle":"active","launch_generation":1,"cleanup_generation":0,"launch":` + launchPresent +
			`,"active":` + active + `}`,
		"cleaning_from_launching": `{"schema_version":3,"owner_uid":1000,"vm_id":"` + testVMID +
			`","lifecycle":"cleaning","launch_generation":1,"cleanup_generation":1,"launch":` + launchAbsent + `}`,
		"cleaning_from_active": `{"schema_version":3,"owner_uid":1000,"vm_id":"` + testVMID +
			`","lifecycle":"cleaning","launch_generation":1,"cleanup_generation":1,"launch":` + launchPresent +
			`,"active":` + active + `}`,
		"cleaned": `{"schema_version":3,"owner_uid":1000,"vm_id":"` + testVMID +
			`","lifecycle":"cleaned","launch_generation":1,"cleanup_generation":1}`,
	}
}

func schemaThreeProofRecords() map[string]instanceRecord {
	launchAbsent := schemaThreeProofLaunch(instanceLaunchRelay{kind: instanceRelayAbsent})
	launchPresent := schemaThreeProofLaunch(instanceLaunchRelay{
		kind:    instanceRelayPresent,
		locator: instanceProcessLocator{pid: 4343, startTimeTicks: 876543},
	})
	active := schemaThreeProofActive()
	return map[string]instanceRecord{
		"launching": {
			schemaVersion: 3, ownerUID: 1000, vmID: testVMID, lifecycle: instanceLifecycleLaunching,
			launchGeneration: 1, cleanupGeneration: 0, launch: &launchAbsent,
		},
		"active": {
			schemaVersion: 3, ownerUID: 1000, vmID: testVMID, lifecycle: instanceLifecycleActive,
			launchGeneration: 1, cleanupGeneration: 0, launch: &launchPresent, active: &active,
		},
		"cleaning_from_launching": {
			schemaVersion: 3, ownerUID: 1000, vmID: testVMID, lifecycle: instanceLifecycleCleaning,
			launchGeneration: 1, cleanupGeneration: 1, launch: &launchAbsent,
		},
		"cleaning_from_active": {
			schemaVersion: 3, ownerUID: 1000, vmID: testVMID, lifecycle: instanceLifecycleCleaning,
			launchGeneration: 1, cleanupGeneration: 1, launch: &launchPresent, active: &active,
		},
		"cleaned": {
			schemaVersion: 3, ownerUID: 1000, vmID: testVMID, lifecycle: instanceLifecycleCleaned,
			launchGeneration: 1, cleanupGeneration: 1,
		},
	}
}

func schemaThreeProofLaunch(relay instanceLaunchRelay) instanceLaunchRecord {
	return instanceLaunchRecord{
		ownerGID:   1001,
		hostBootID: "123e4567-e89b-42d3-a456-426614174000",
		process:    instanceProcessLocator{pid: 4242, startTimeTicks: 987654},
		relay:      relay,
		cacheIdentity: managedCacheIdentity{
			deviceMajor: 8, deviceMinor: 1, inode: 12345, mountID: 67890,
			mountIDKind: managedCacheMountIdentityUnique,
		},
		release: releaseIdentity{
			version: "1.16.0", architecture: "x86_64",
			firecrackerSHA256: testReleaseHash, jailerSHA256: testJailerHash,
		},
	}
}

func schemaThreeProofActive() instanceActiveIdentity {
	userIDs := processCredentialIdentity{real: 1000, effective: 1000, saved: 1000, filesystem: 1000}
	groupIDs := processCredentialIdentity{real: 1001, effective: 1001, saved: 1001, filesystem: 1001}
	return instanceActiveIdentity{
		userIDs: userIDs, groupIDs: groupIDs, supplementaryGroups: []uint32{},
		mountNamespace: instanceObjectIdentity{device: 41, inode: 42},
		jailRoot:       instanceObjectIdentity{device: 43, inode: 44},
		executable: instanceFirecrackerExecutableIdentity{
			device: 45, inode: 46, sizeBytes: trustedReleaseExecutableMinBytes,
			mode: unix.S_IFREG | trustedReleaseStoreExecutableMode,
			uid:  0, gid: 0, links: 1, manifestSHA256: testReleaseHash,
		},
		relay: instanceActiveRelay{
			kind: instanceRelayPresent, userIDs: userIDs, groupIDs: groupIDs, supplementaryGroups: []uint32{},
			mountNamespace: instanceObjectIdentity{device: 51, inode: 52},
			root:           instanceObjectIdentity{device: 53, inode: 54},
			executable: instanceRelayExecutableIdentity{
				device: 55, inode: 56, sizeBytes: trustedReleaseExecutableMinBytes,
				mode: unix.S_IFREG | trustedReleaseStoreExecutableMode,
				uid:  0, gid: 0, links: 1, sha256: testRelayHash,
			},
		},
	}
}
