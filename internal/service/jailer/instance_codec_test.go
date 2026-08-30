package jailer

import (
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	testAuthorityUID = uint32(1000)
	testAuthorityGID = uint32(1001)
	testReleaseHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testJailerHash   = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	testRelayHash    = "1111111111111111111111111111111111111111111111111111111111111111"
)

func TestInstanceAuthorityDurableRootConstant(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/var/lib/mvmctl/instances", infra.JailerInstanceRoot)
}

func TestInstanceRecordCodecRoundTripsEverySchemaThreeShape(t *testing.T) {
	t.Parallel()

	launching := testLaunchingInstanceRecord()
	activeAbsent := testActiveInstanceRecord(false)
	activePresent := testActiveInstanceRecord(true)
	cleaningLaunch := launching
	cleaningLaunch.lifecycle = instanceLifecycleCleaning
	cleaningLaunch.cleanupGeneration = cleaningLaunch.launchGeneration
	cleaningActive := activePresent
	cleaningActive.lifecycle = instanceLifecycleCleaning
	cleaningActive.cleanupGeneration = cleaningActive.launchGeneration
	cleaned, err := completeInstanceCleanup(cleaningActive)
	require.NoError(t, err)

	tests := map[string]instanceRecord{
		"launching":            launching,
		"active_without_relay": activeAbsent,
		"active_with_relay":    activePresent,
		"cleaning_from_launch": cleaningLaunch,
		"cleaning_from_active": cleaningActive,
		"same_owner_tombstone": cleaned,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw, encodeErr := encodeInstanceRecord(want)
			require.NoError(t, encodeErr)
			assert.LessOrEqual(t, len(raw), maxInstanceRecordBytes)

			got, decodeErr := decodeInstanceRecord(raw)
			require.NoError(t, decodeErr)
			if diff := diffInstanceRecord(want, got); diff != "" {
				t.Errorf("instance record round trip mismatch (-want +got):\n%s", diff)
			}
			assert.NotContains(t, string(raw), "password")
			assert.NotContains(t, string(raw), "token")
			assert.NotContains(t, string(raw), "cgroup")
		})
	}
}

func TestInstanceRecordCodecOmitsWrongLifecycleObjects(t *testing.T) {
	t.Parallel()

	launchingRaw, err := encodeInstanceRecord(testLaunchingInstanceRecord())
	require.NoError(t, err)
	assert.Contains(t, string(launchingRaw), `"launch":`)
	assert.NotContains(t, string(launchingRaw), `"active":`)
	assert.NotContains(t, string(launchingRaw), `"user_ids":`)
	assert.NotContains(t, string(launchingRaw), `"mount_namespace":`)
	assert.NotContains(t, string(launchingRaw), `"executable":`)

	cleaning := testActiveInstanceRecord(true)
	cleaning.lifecycle = instanceLifecycleCleaning
	cleaning.cleanupGeneration = cleaning.launchGeneration
	cleaned, err := completeInstanceCleanup(cleaning)
	require.NoError(t, err)
	cleanedRaw, err := encodeInstanceRecord(cleaned)
	require.NoError(t, err)
	assert.NotContains(t, string(cleanedRaw), `"launch":`)
	assert.NotContains(t, string(cleanedRaw), `"active":`)
	assert.NotContains(t, string(cleanedRaw), `"owner_gid":`)
	assert.NotContains(t, string(cleanedRaw), `"managed_cache":`)
	assert.NotContains(t, string(cleanedRaw), `"release":`)
	assert.NotContains(t, string(cleanedRaw), `"pid":`)
}

// Rationale: the durable root is attacker-controlled input after host storage tampering. Each malformed case pins one
// strict-schema boundary and must fail closed as an internal atomic-authority error rather than being defaulted.
func TestDecodeInstanceRecordRejectsMalformedSchemaThree(t *testing.T) {
	t.Parallel()

	valid, err := encodeInstanceRecord(testActiveInstanceRecord(true))
	require.NoError(t, err)
	validText := string(valid)
	validLaunching := schemaThreeCanonicalJSON()["launching"]
	validCleaned := schemaThreeCanonicalJSON()["cleaned"]
	launchOffset := strings.LastIndex(validLaunching, `,"launch":`)
	require.Positive(t, launchOffset)
	activeOffset := strings.LastIndex(validText, `,"active":`)
	require.Positive(t, activeOffset)
	launchArray := validLaunching[:launchOffset] + `,"launch":[]}`
	activeArray := validText[:activeOffset] + `,"active":[]}`
	nullActive := validText[:activeOffset] + `,"active":null}`
	launchObjectOnCleaned := strings.Replace(validLaunching, `"lifecycle":"launching"`, `"lifecycle":"cleaned"`, 1)
	launchObjectOnCleaned = strings.Replace(
		launchObjectOnCleaned,
		`"cleanup_generation":0`,
		`"cleanup_generation":1`,
		1,
	)
	cleanedWithActive := validCleaned[:len(validCleaned)-1] + validText[activeOffset:]

	tests := map[string]string{
		"empty":                 "",
		"trailing":              validText + `{}`,
		"unknown_top_level":     strings.Replace(validText, `"vm_id":`, `"unknown":1,"vm_id":`, 1),
		"case_variant_field":    strings.Replace(validText, `"vm_id":`, `"VM_ID":`, 1),
		"duplicate_field":       strings.Replace(validText, `"vm_id":`, `"vm_id":"`+testVMID+`","vm_id":`, 1),
		"case_folded_duplicate": strings.Replace(validText, `"vm_id":`, `"VM_ID":"`+testVMID+`","vm_id":`, 1),
		"nested_unknown": strings.Replace(
			validText,
			`"managed_cache":{"device_major":`,
			`"managed_cache":{"unknown":1,"device_major":`,
			1,
		),
		"nested_duplicate": strings.Replace(
			validText,
			`"host_boot_id":`,
			`"host_boot_id":"123e4567-e89b-42d3-a456-426614174000","host_boot_id":`,
			1,
		),
		"missing_common_field": strings.Replace(validText, `"cleanup_generation":0,`, "", 1),
		"missing_launch_field": strings.Replace(validText, `"owner_gid":1001,`, "", 1),
		"missing_active_field": strings.Replace(validText, `"supplementary_groups":[],`, "", 1),
		"missing_present_relay_locator": strings.Replace(
			validText,
			`"relay":{"kind":"present","pid":4343,"start_time_ticks":876543}`,
			`"relay":{"kind":"present","pid":4343}`,
			1,
		),
		"old_schema_one":         strings.Replace(validText, `"schema_version":3`, `"schema_version":1`, 1),
		"old_schema_two":         strings.Replace(validText, `"schema_version":3`, `"schema_version":2`, 1),
		"unknown_schema":         strings.Replace(validText, `"schema_version":3`, `"schema_version":4`, 1),
		"owner_uid_string":       strings.Replace(validText, `"owner_uid":1000`, `"owner_uid":"1000"`, 1),
		"owner_uid_negative":     strings.Replace(validText, `"owner_uid":1000`, `"owner_uid":-1`, 1),
		"owner_uid_fractional":   strings.Replace(validText, `"owner_uid":1000`, `"owner_uid":1000.5`, 1),
		"owner_uid_overflow":     strings.Replace(validText, `"owner_uid":1000`, `"owner_uid":4294967296`, 1),
		"lifecycle_number":       strings.Replace(validText, `"lifecycle":"active"`, `"lifecycle":1`, 1),
		"launch_array":           launchArray,
		"active_array":           activeArray,
		"launch_generation_zero": strings.Replace(validText, `"launch_generation":1`, `"launch_generation":0`, 1),
		"cleanup_generation_mismatch": strings.Replace(
			validText,
			`"cleanup_generation":0`,
			`"cleanup_generation":1`,
			1,
		),
		"generation_numeric_overflow": strings.Replace(
			validText,
			`"launch_generation":1`,
			`"launch_generation":18446744073709551616`,
			1,
		),
		"old_registered_lifecycle": strings.Replace(
			validText,
			`"lifecycle":"active"`,
			`"lifecycle":"registered"`,
			1,
		),
		"active_object_during_launching": strings.Replace(
			validText,
			`"lifecycle":"active"`,
			`"lifecycle":"launching"`,
			1,
		),
		"launch_object_on_cleaned": launchObjectOnCleaned,
		"active_object_on_cleaned": cleanedWithActive,
		"null_active_object":       nullActive,
		"final_identity_in_launch": strings.Replace(
			validText,
			`"owner_gid":1001,`,
			`"owner_gid":1001,"user_ids":{"real":1000,"effective":1000,"saved":1000,"filesystem":1000},`,
			1,
		),
		"absent_relay_with_locator": strings.Replace(
			validText,
			`"relay":{"kind":"present","pid":4343,"start_time_ticks":876543}`,
			`"relay":{"kind":"absent","pid":4343,"start_time_ticks":876543}`,
			1,
		),
		"relay_pid_matches_firecracker": strings.Replace(
			validText,
			`"relay":{"kind":"present","pid":4343`,
			`"relay":{"kind":"present","pid":4242`,
			1,
		),
		"firecracker_pid_zero": strings.Replace(validText, `"pid":4242`, `"pid":0`, 1),
		"firecracker_pid_too_large": strings.Replace(
			validText,
			`"pid":4242`,
			`"pid":2147483648`,
			1,
		),
		"firecracker_start_zero": strings.Replace(
			validText,
			`"start_time_ticks":987654`,
			`"start_time_ticks":0`,
			1,
		),
		"present_relay_pid_zero": strings.Replace(validText, `"pid":4343`, `"pid":0`, 1),
		"present_relay_start_zero": strings.Replace(
			validText,
			`"start_time_ticks":876543`,
			`"start_time_ticks":0`,
			1,
		),
		"release_version_invalid": strings.Replace(validText, `"version":"1.16.0"`, `"version":"../1.16.0"`, 1),
		"release_architecture_invalid": strings.Replace(
			validText,
			`"architecture":"x86_64"`,
			`"architecture":"amd64"`,
			1,
		),
		"release_firecracker_digest_invalid": strings.Replace(
			validText,
			`"firecracker_sha256":"`+testReleaseHash+`"`,
			`"firecracker_sha256":"invalid"`,
			1,
		),
		"release_jailer_digest_invalid": strings.Replace(
			validText,
			`"jailer_sha256":"`+testJailerHash+`"`,
			`"jailer_sha256":"invalid"`,
			1,
		),
		"active_relay_absent_with_identity": strings.Replace(
			validText,
			`"relay":{"kind":"present","user_ids":`,
			`"relay":{"kind":"absent","user_ids":`,
			1,
		),
		"present_active_relay_missing_digest": strings.Replace(
			validText,
			`,"sha256":"`+testRelayHash+`"`,
			``,
			1,
		),
		"supplementary_groups_object": strings.Replace(
			validText,
			`"supplementary_groups":[]`,
			`"supplementary_groups":{}`,
			1,
		),
		"supplementary_groups_negative": strings.Replace(
			validText,
			`"supplementary_groups":[]`,
			`"supplementary_groups":[-1]`,
			1,
		),
		"supplementary_groups_fractional": strings.Replace(
			validText,
			`"supplementary_groups":[]`,
			`"supplementary_groups":[1.5]`,
			1,
		),
		"supplementary_groups_overflow": strings.Replace(
			validText,
			`"supplementary_groups":[]`,
			`"supplementary_groups":[4294967296]`,
			1,
		),
		"null_scalar": strings.Replace(validText, `"owner_uid":1000`, `"owner_uid":null`, 1),
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, decodeErr := decodeInstanceRecord([]byte(raw))
			require.Error(t, decodeErr)
			domainErr := errs.AsDomainError(decodeErr)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
		})
	}
}

func TestDecodeInstanceRecordRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	_, err := decodeInstanceRecord([]byte(strings.Repeat(" ", maxInstanceRecordBytes+1)))
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
}

func TestInstanceLifecycleTransitionsPreserveOnlyStateAuthority(t *testing.T) {
	t.Parallel()

	caller := testInstanceCaller()
	intent := testLaunchIntent(false)

	launching, err := registerLaunchingInstanceRecord(caller, intent, testReleaseIdentity(), nil)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleLaunching, launching.lifecycle)
	assert.Equal(t, uint64(1), launching.launchGeneration)
	assert.Zero(t, launching.cleanupGeneration)
	require.NotNil(t, launching.launch)
	assert.Nil(t, launching.active)

	_, err = registerLaunchingInstanceRecord(caller, intent, testReleaseIdentity(), &launching)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAlreadyExists, errs.AsDomainError(err).Code)

	active, err := activateInstanceRecord(launching, testActiveIdentity(false))
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleActive, active.lifecycle)
	require.NotNil(t, active.active)

	cleaning, err := beginInstanceCleanup(caller, active)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaning, cleaning.lifecycle)
	assert.Equal(t, uint64(1), cleaning.cleanupGeneration)
	require.NotNil(t, cleaning.launch)
	require.NotNil(t, cleaning.active)

	retry, err := beginInstanceCleanup(caller, cleaning)
	require.NoError(t, err)
	if diff := diffInstanceRecord(cleaning, retry); diff != "" {
		t.Errorf("idempotent cleanup changed the record (-want +got):\n%s", diff)
	}

	cleaned, err := completeInstanceCleanup(cleaning)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaned, cleaned.lifecycle)
	assert.Equal(t, uint64(1), cleaned.launchGeneration)
	assert.Equal(t, uint64(1), cleaned.cleanupGeneration)
	assert.Nil(t, cleaned.launch)
	assert.Nil(t, cleaned.active)

	relaunched, err := registerLaunchingInstanceRecord(caller, testLaunchIntent(true), testReleaseIdentity(), &cleaned)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleLaunching, relaunched.lifecycle)
	assert.Equal(t, uint64(2), relaunched.launchGeneration)
	assert.Equal(t, uint64(1), relaunched.cleanupGeneration)
	require.NotNil(t, relaunched.launch)
	assert.Equal(t, testAuthorityGID, relaunched.launch.ownerGID)
}

func TestInstanceLifecycleCanAbortBeforeActivation(t *testing.T) {
	t.Parallel()

	caller := testInstanceCaller()
	launching, err := registerLaunchingInstanceRecord(caller, testLaunchIntent(true), testReleaseIdentity(), nil)
	require.NoError(t, err)

	cleaning, err := beginInstanceCleanup(caller, launching)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaning, cleaning.lifecycle)
	require.NotNil(t, cleaning.launch)
	assert.Nil(t, cleaning.active)
}

func TestInstanceLifecycleRejectsForeignCaller(t *testing.T) {
	t.Parallel()

	record := testLaunchingInstanceRecord()
	foreign := instanceCaller{uid: testAuthorityUID + 1, gid: testAuthorityGID}

	_, err := registerLaunchingInstanceRecord(foreign, testLaunchIntent(false), testReleaseIdentity(), &record)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
	assert.NotContains(t, err.Error(), "1000")

	_, err = beginInstanceCleanup(foreign, record)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
}

func TestInstanceActivationRejectsIncompleteOrMismatchedFinalIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*instanceActiveIdentity){
		"nonempty_supplementary_groups": func(identity *instanceActiveIdentity) {
			identity.supplementaryGroups = []uint32{testAuthorityGID}
		},
		"wrong_firecracker_uid": func(identity *instanceActiveIdentity) {
			identity.userIDs.effective++
		},
		"wrong_firecracker_digest": func(identity *instanceActiveIdentity) {
			identity.executable.manifestSHA256 = testJailerHash
		},
		"relay_presence_mismatch": func(identity *instanceActiveIdentity) {
			identity.relay = instanceActiveRelay{kind: instanceRelayAbsent}
		},
		"relay_wrong_owner": func(identity *instanceActiveIdentity) {
			identity.relay.userIDs.saved++
		},
		"relay_nonempty_supplementary_groups": func(identity *instanceActiveIdentity) {
			identity.relay.supplementaryGroups = []uint32{testAuthorityGID}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			launching := testLaunchingInstanceRecordWithRelay(true)
			identity := testActiveIdentity(true)
			mutate(&identity)

			_, err := activateInstanceRecord(launching, identity)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(err).Code)
		})
	}
}

func TestLaunchIntentRejectsRelaySharingFirecrackerPID(t *testing.T) {
	t.Parallel()

	intent := testLaunchIntent(true)
	intent.relay.locator.pid = intent.process.pid
	_, err := registerLaunchingInstanceRecord(testInstanceCaller(), intent, testReleaseIdentity(), nil)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassValidation, domainErr.Class)
}

func TestLaunchingTransitionRejectsInvalidPreparedReleaseAsAtomicFailure(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"whitespace":     "1.16.0 beta",
		"control":        "1.16.0\nroot",
		"dot_component":  "1.16..0",
		"path_component": "..",
		"punctuation":    "1.16.0!",
	}

	for name, version := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			release := testReleaseIdentity()
			release.version = version
			_, err := registerLaunchingInstanceRecord(testInstanceCaller(), testLaunchIntent(false), release, nil)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestInstanceTransitionRejectsCorruptExistingRecord(t *testing.T) {
	t.Parallel()

	caller := testInstanceCaller()
	corrupt := testLaunchingInstanceRecord()
	corrupt.launchGeneration = 0

	_, err := registerLaunchingInstanceRecord(caller, testLaunchIntent(false), testReleaseIdentity(), &corrupt)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)

	_, err = activateInstanceRecord(corrupt, testActiveIdentity(false))
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)

	_, err = beginInstanceCleanup(caller, corrupt)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)

	corrupt.lifecycle = instanceLifecycleCleaning
	_, err = completeInstanceCleanup(corrupt)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceRelaunchRejectsExhaustedGeneration(t *testing.T) {
	t.Parallel()

	cleaned := instanceRecord{
		schemaVersion:     instanceRecordSchemaVersion,
		ownerUID:          testAuthorityUID,
		vmID:              testVMID,
		lifecycle:         instanceLifecycleCleaned,
		launchGeneration:  math.MaxUint64,
		cleanupGeneration: math.MaxUint64,
	}

	_, err := registerLaunchingInstanceRecord(
		testInstanceCaller(), testLaunchIntent(false), testReleaseIdentity(), &cleaned,
	)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(err).Code)
}

func testInstanceCaller() instanceCaller {
	return instanceCaller{uid: testAuthorityUID, gid: testAuthorityGID}
}

func testLaunchIntent(relayPresent bool) launchIntent {
	relay := instanceLaunchRelay{kind: instanceRelayAbsent}
	if relayPresent {
		relay = instanceLaunchRelay{
			kind:    instanceRelayPresent,
			locator: instanceProcessLocator{pid: 4343, startTimeTicks: 876543},
		}
	}
	return launchIntent{
		vmID:          testVMID,
		hostBootID:    "123e4567-e89b-42d3-a456-426614174000",
		cacheIdentity: testManagedCacheIdentity(),
		process:       instanceProcessLocator{pid: 4242, startTimeTicks: 987654},
		relay:         relay,
	}
}

func testReleaseIdentity() releaseIdentity {
	return releaseIdentity{
		version:           "1.16.0",
		architecture:      "x86_64",
		firecrackerSHA256: testReleaseHash,
		jailerSHA256:      testJailerHash,
	}
}

func testActiveIdentity(relayPresent bool) instanceActiveIdentity {
	userIDs := processCredentialIdentity{
		real: testAuthorityUID, effective: testAuthorityUID,
		saved: testAuthorityUID, filesystem: testAuthorityUID,
	}
	groupIDs := processCredentialIdentity{
		real: testAuthorityGID, effective: testAuthorityGID,
		saved: testAuthorityGID, filesystem: testAuthorityGID,
	}
	identity := instanceActiveIdentity{
		userIDs:             userIDs,
		groupIDs:            groupIDs,
		supplementaryGroups: []uint32{},
		mountNamespace:      instanceObjectIdentity{device: 41, inode: 42},
		jailRoot:            instanceObjectIdentity{device: 43, inode: 44},
		executable: instanceFirecrackerExecutableIdentity{
			device: 45, inode: 46, sizeBytes: trustedReleaseExecutableMinBytes,
			mode: unix.S_IFREG | trustedReleaseStoreExecutableMode,
			uid:  0, gid: 0, links: 1, manifestSHA256: testReleaseHash,
		},
		relay: instanceActiveRelay{kind: instanceRelayAbsent},
	}
	if relayPresent {
		identity.relay = instanceActiveRelay{
			kind:                instanceRelayPresent,
			userIDs:             userIDs,
			groupIDs:            groupIDs,
			supplementaryGroups: []uint32{},
			mountNamespace:      instanceObjectIdentity{device: 51, inode: 52},
			root:                instanceObjectIdentity{device: 53, inode: 54},
			executable: instanceRelayExecutableIdentity{
				device: 55, inode: 56, sizeBytes: trustedReleaseExecutableMinBytes,
				mode: unix.S_IFREG | trustedReleaseStoreExecutableMode,
				uid:  0, gid: 0, links: 1, sha256: testRelayHash,
			},
		}
	}
	return identity
}

func testLaunchingInstanceRecord() instanceRecord {
	return testLaunchingInstanceRecordWithRelay(false)
}

func testLaunchingInstanceRecordWithRelay(relayPresent bool) instanceRecord {
	record, err := registerLaunchingInstanceRecord(
		testInstanceCaller(), testLaunchIntent(relayPresent), testReleaseIdentity(), nil,
	)
	if err != nil {
		panic(err)
	}
	return record
}

func testActiveInstanceRecord(relayPresent bool) instanceRecord {
	record, err := activateInstanceRecord(
		testLaunchingInstanceRecordWithRelay(relayPresent),
		testActiveIdentity(relayPresent),
	)
	if err != nil {
		panic(err)
	}
	return record
}

func testManagedCacheIdentity() managedCacheIdentity {
	return managedCacheIdentity{
		deviceMajor: 8,
		deviceMinor: 1,
		inode:       12345,
		mountID:     67890,
		mountIDKind: managedCacheMountIdentityUnique,
	}
}

func diffInstanceRecord(want, got instanceRecord) string {
	return cmp.Diff(
		want,
		got,
		cmp.AllowUnexported(
			instanceRecord{},
			instanceLaunchRecord{},
			instanceActiveIdentity{},
			instanceLaunchRelay{},
			instanceActiveRelay{},
			instanceProcessLocator{},
			processCredentialIdentity{},
			instanceObjectIdentity{},
			instanceFirecrackerExecutableIdentity{},
			instanceRelayExecutableIdentity{},
			managedCacheIdentity{},
			releaseIdentity{},
		),
	)
}
