package jailer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	testAuthorityUID = uint32(1000)
	testReleaseHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testJailerHash   = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func TestInstanceAuthorityRootConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/var/lib/mvmctl/instances", infra.JailerInstanceRoot)
	assert.Equal(t, "/run/mvmctl", infra.JailerRuntimeRoot)
}

func TestInstanceRecordCodecRoundTrip(t *testing.T) {
	t.Parallel()

	want := testRegisteredInstanceRecord()
	raw, err := encodeInstanceRecord(want)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), maxInstanceRecordBytes)

	got, err := decodeInstanceRecord(raw)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NotContains(t, string(raw), "password")
	assert.NotContains(t, string(raw), "token")
	assert.NotContains(t, string(raw), "rootfs")
}

func TestDecodeInstanceRecordRejectsMalformedSchema(t *testing.T) {
	t.Parallel()

	valid, err := encodeInstanceRecord(testRegisteredInstanceRecord())
	require.NoError(t, err)
	expectedCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + testVMID

	tests := map[string]string{
		"empty":                 "",
		"trailing":              string(valid) + `{}`,
		"unknown_top_level":     strings.Replace(string(valid), `"vm_id":`, `"unknown":1,"vm_id":`, 1),
		"case_variant_field":    strings.Replace(string(valid), `"vm_id":`, `"VM_ID":`, 1),
		"duplicate_field":       strings.Replace(string(valid), `"vm_id":`, `"vm_id":"`+testVMID+`","vm_id":`, 1),
		"case_folded_duplicate": strings.Replace(string(valid), `"vm_id":`, `"VM_ID":"`+testVMID+`","vm_id":`, 1),
		"nested_unknown":        strings.Replace(string(valid), `"version":`, `"unknown":1,"version":`, 1),
		"nested_duplicate":      strings.Replace(string(valid), `"version":`, `"version":"1.16.0","version":`, 1),
		"nested_case_duplicate": strings.Replace(string(valid), `"version":`, `"VERSION":"1.16.0","version":`, 1),
		"missing_required":      strings.Replace(string(valid), `"owner_uid":1000,`, "", 1),
		"unsupported_version":   strings.Replace(string(valid), `"schema_version":1`, `"schema_version":2`, 1),
		"invalid_lifecycle":     strings.Replace(string(valid), `"lifecycle":"registered"`, `"lifecycle":"running"`, 1),
		"owner_zero":            strings.Replace(string(valid), `"owner_uid":1000`, `"owner_uid":0`, 1),
		"pid_zero":              strings.Replace(string(valid), `"pid":4242`, `"pid":0`, 1),
		"start_ticks_zero":      strings.Replace(string(valid), `"start_time_ticks":987654`, `"start_time_ticks":0`, 1),
		"wrong_cgroup": strings.Replace(
			string(valid),
			`"cgroup_path":"`+expectedCgroup+`"`,
			`"cgroup_path":"/sys/fs/cgroup/mvmctl/ffffffffffffffffffffffffffffffff"`,
			1,
		),
		"uppercase_release_hash": strings.Replace(string(valid), testReleaseHash, strings.ToUpper(testReleaseHash), 1),
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, decodeErr := decodeInstanceRecord([]byte(raw))
			require.Error(t, decodeErr)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(decodeErr).Code)
		})
	}
}

func TestDecodeInstanceRecordRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	_, err := decodeInstanceRecord([]byte(strings.Repeat(" ", maxInstanceRecordBytes+1)))
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceLifecycleTransitions(t *testing.T) {
	t.Parallel()

	caller := instanceCaller{uid: testAuthorityUID}
	registration := testLaunchRegistration()

	registered, err := registerInstanceRecord(caller, registration, nil)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleRegistered, registered.lifecycle)
	assert.Zero(t, registered.cleanupGeneration)

	_, err = registerInstanceRecord(caller, registration, &registered)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAlreadyExists, errs.AsDomainError(err).Code)

	cleaning, err := beginInstanceCleanup(caller, registered)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaning, cleaning.lifecycle)
	assert.Equal(t, uint64(1), cleaning.cleanupGeneration)

	retry, err := beginInstanceCleanup(caller, cleaning)
	require.NoError(t, err)
	assert.Equal(t, cleaning, retry)

	cleaned, err := completeInstanceCleanup(cleaning)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaned, cleaned.lifecycle)

	relaunched, err := registerInstanceRecord(caller, registration, &cleaned)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleRegistered, relaunched.lifecycle)
	assert.Equal(t, cleaned.cleanupGeneration, relaunched.cleanupGeneration)
}

func TestInstanceLifecycleRejectsForeignCaller(t *testing.T) {
	t.Parallel()

	record := testRegisteredInstanceRecord()
	foreign := instanceCaller{uid: testAuthorityUID + 1}

	_, err := registerInstanceRecord(foreign, testLaunchRegistration(), &record)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
	assert.NotContains(t, err.Error(), "1000")

	_, err = beginInstanceCleanup(foreign, record)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
}

func TestLaunchRegistrationRejectsInvalidReleaseAsValidation(t *testing.T) {
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

			registration := testLaunchRegistration()
			registration.release.version = version
			_, err := registerInstanceRecord(
				instanceCaller{uid: testAuthorityUID},
				registration,
				nil,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestInstanceTransitionRejectsCorruptExistingRecord(t *testing.T) {
	t.Parallel()

	caller := instanceCaller{uid: testAuthorityUID}
	corrupt := testRegisteredInstanceRecord()
	corrupt.lifecycle = instanceLifecycleCleaned
	corrupt.process.cgroupPath = "/sys/fs/cgroup/foreign"

	_, err := registerInstanceRecord(caller, testLaunchRegistration(), &corrupt)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)

	corrupt.lifecycle = instanceLifecycleRegistered
	_, err = beginInstanceCleanup(caller, corrupt)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)

	corrupt.lifecycle = instanceLifecycleCleaning
	_, err = completeInstanceCleanup(corrupt)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func testLaunchRegistration() launchRegistration {
	return launchRegistration{
		vmID: testVMID,
		release: releaseIdentity{
			version:           "1.16.0",
			architecture:      "x86_64",
			firecrackerSHA256: testReleaseHash,
			jailerSHA256:      testJailerHash,
		},
		process: processIdentity{
			pid:            4242,
			startTimeTicks: 987654,
			cgroupPath:     infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + testVMID,
		},
	}
}

func testRegisteredInstanceRecord() instanceRecord {
	registration := testLaunchRegistration()
	return instanceRecord{
		schemaVersion:     instanceRecordSchemaVersion,
		ownerUID:          testAuthorityUID,
		vmID:              registration.vmID,
		lifecycle:         instanceLifecycleRegistered,
		release:           registration.release,
		process:           registration.process,
		cleanupGeneration: 0,
	}
}
