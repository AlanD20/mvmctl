package jailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestReleaseAuthorityRemoveInstalledRemovesCompleteUnreferencedRelease(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.NoError(t, err)
	assert.True(t, removed)

	entries, err := os.ReadDir(fixture.store.architecturePath)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{}, trustedReleaseEntryNamesForRemoveTest(entries)); diff != "" {
		t.Errorf("release architecture entries mismatch (-want +got):\n%s", diff)
	}
}

func TestReleaseAuthorityRemoveInstalledTreatsSafeAbsenceAsUnchangedWithoutReferenceScan(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseStoreFixture) string{
		"managed store":  func(store *trustedReleaseStoreFixture) string { return store.mvmctlPath },
		"binaries store": func(store *trustedReleaseStoreFixture) string { return store.binariesPath },
		"architecture":   func(store *trustedReleaseStoreFixture) string { return store.architecturePath },
		"canonical slot": func(store *trustedReleaseStoreFixture) string {
			return store.slotPath
		},
	}
	for name, absentPath := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseRemovalFixture(t)
			records := filepath.Join(fixture.instanceRoot, "var/lib/mvmctl/instances/1000")
			require.NoError(t, os.MkdirAll(records, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(records, testVMID+".json"), []byte(`{"broken":true}`), 0600))
			require.NoError(t, os.RemoveAll(absentPath(fixture.store)))

			removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
			require.NoError(t, err)
			assert.False(t, removed)
		})
	}
}

func TestReleaseAuthorityRemoveInstalledRecoversAbsentCanonicalBeforeReturningUnchanged(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	require.NoError(t, os.RemoveAll(fixture.store.slotPath))
	prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
	require.NoError(t, err)
	candidate := writeTrustedReleaseRecoveryCandidate(
		t,
		fixture.store.architecturePath,
		prefix+"11111111111111111111111111111111.tmp",
		[]string{trustedReleaseManifestLeaf, trustedReleaseJailerLeaf},
	)
	records := filepath.Join(fixture.instanceRoot, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(records, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(records, testVMID+".json"), []byte(`{"broken":true}`), 0600))

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.NoError(t, err)
	assert.False(t, removed)
	assert.NoDirExists(t, candidate)
}

func TestReleaseAuthorityRemoveInstalledUsesExactManifestIdentityForReferences(t *testing.T) {
	t.Parallel()

	t.Run("different identity in the same slot does not block removal", func(t *testing.T) {
		t.Parallel()

		fixture := newTrustedReleaseRemovalFixture(t)
		record := testRegisteredInstanceRecord()
		record.release.version = fixture.store.slot.version
		record.release.architecture = fixture.store.slot.architecture
		writeTestAuthorityRecord(t, fixture.instanceRoot, record)

		removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
		require.NoError(t, err)
		assert.True(t, removed)
		assert.NoDirExists(t, fixture.store.slotPath)
	})

	t.Run("exact identity blocks removal", func(t *testing.T) {
		t.Parallel()

		fixture := newTrustedReleaseRemovalFixture(t)
		identity, err := fixture.manifest.releaseIdentity()
		require.NoError(t, err)
		record := testRegisteredInstanceRecord()
		record.release = identity
		writeTestAuthorityRecord(t, fixture.instanceRoot, record)

		removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
		require.Error(t, err)
		assert.False(t, removed)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeVMStateInvalid, domainErr.Code)
		assert.Equal(t, errs.ClassConflict, domainErr.Class)
		assert.DirExists(t, fixture.store.slotPath)
	})
}

func TestReleaseAuthorityRemoveInstalledRejectsCorruptReferenceAuthority(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	records := filepath.Join(fixture.instanceRoot, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(records, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(records, testVMID+".json"), []byte(`{"broken":true}`), 0600))

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.Error(t, err)
	assert.False(t, removed)
	assert.DirExists(t, fixture.store.slotPath)
}

func TestReleaseAuthorityRemoveInstalledRejectsCorruptCanonicalBeforeRecovery(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
	require.NoError(t, err)
	candidate := writeTrustedReleaseRecoveryCandidate(
		t,
		fixture.store.architecturePath,
		prefix+"22222222222222222222222222222222.tmp",
		[]string{trustedReleaseManifestLeaf},
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.store.slotPath, trustedReleaseManifestLeaf),
		[]byte(`{"broken":true}`),
		0600,
	))

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.Error(t, err)
	assert.False(t, removed)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.DirExists(t, candidate)
	assert.FileExists(t, filepath.Join(candidate, trustedReleaseManifestLeaf))
}

func TestReleaseAuthorityRemoveInstalledRejectsInvalidOrCancelledInputBeforeEffects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*trustedReleaseRemovalFixture) (context.Context, releaseSlot)
		code   errs.Code
	}{
		"invalid slot": {
			mutate: func(fixture *trustedReleaseRemovalFixture) (context.Context, releaseSlot) {
				slot := fixture.store.slot
				slot.version = "../1.16.1"
				return context.Background(), slot
			},
			code: errs.CodeValidationFailed,
		},
		"cancelled context": {
			mutate: func(fixture *trustedReleaseRemovalFixture) (context.Context, releaseSlot) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, fixture.store.slot
			},
			code: errs.CodeBinaryRemoveFailed,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseRemovalFixture(t)
			ctx, slot := test.mutate(fixture)

			removed, err := fixture.authority.removeInstalled(ctx, slot)
			require.Error(t, err)
			assert.False(t, removed)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, test.code, domainErr.Code)
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

type trustedReleaseRemovalFixture struct {
	store        *trustedReleaseStoreFixture
	authority    *releaseAuthority
	instanceRoot string
	manifest     trustedReleaseManifest
}

func newTrustedReleaseRemovalFixture(t *testing.T) *trustedReleaseRemovalFixture {
	t.Helper()

	instances, instanceRoot := newTestInstanceAuthority(t)
	store := newTrustedReleaseStoreFixture(t)
	manifest := testTrustedReleaseManifest(store.slot)
	writeTrustedReleaseTestExecutable(
		t,
		filepath.Join(store.slotPath, trustedReleaseFirecrackerLeaf),
		trustedReleaseTestELF("removal Firecracker"),
		&manifest.firecracker,
	)
	writeTrustedReleaseTestExecutable(
		t,
		filepath.Join(store.slotPath, trustedReleaseJailerLeaf),
		trustedReleaseTestELF("removal Jailer"),
		&manifest.jailer,
	)
	writeTrustedReleaseManifestFile(t, store.slotPath, manifest)
	return &trustedReleaseRemovalFixture{
		store:        store,
		authority:    newReleaseAuthorityWithPolicy(instances, store.deps, store.policy),
		instanceRoot: instanceRoot,
		manifest:     manifest,
	}
}

func trustedReleaseEntryNamesForRemoveTest(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
