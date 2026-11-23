package jailer

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: installed releases and unpublished candidates must cross one identical manifest, executable, and
// identity admission seam so the two trust decisions cannot drift.
func TestTrustedReleaseDirectoryAdmissionBindsManifestIdentityAndExecutables(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseExecutableFixture(t)
	writeTrustedReleaseManifestFile(t, fixture.store.slotPath, fixture.manifest)
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.store.deps, fixture.store.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	directory, err := store.openInstalledSlot(t.Context(), fixture.store.slot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, directory.Release(context.Background())) })

	admission, err := directory.admit(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admission.Release(context.Background())) })
	wantIdentity, err := fixture.manifest.releaseIdentity()
	require.NoError(t, err)
	manifestOptions := cmp.AllowUnexported(
		trustedReleaseManifest{},
		releaseSlot{},
		trustedReleaseExecutable{},
	)
	if diff := cmp.Diff(fixture.manifest, admission.manifest, manifestOptions); diff != "" {
		t.Errorf("trusted release admission manifest mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantIdentity, admission.identity, cmp.AllowUnexported(releaseIdentity{})); diff != "" {
		t.Errorf("trusted release admission identity mismatch (-want +got):\n%s", diff)
	}
	assert.NotNil(t, admission.executables)
	assert.GreaterOrEqual(t, admission.executables.firecrackerFD, 0)
	assert.GreaterOrEqual(t, admission.executables.jailerFD, 0)

	require.NoError(t, admission.Release(t.Context()))
	require.NoError(t, admission.Release(t.Context()))
	assert.Nil(t, admission.executables)
	if diff := cmp.Diff(trustedReleaseManifest{}, admission.manifest, manifestOptions); diff != "" {
		t.Errorf("released admission manifest was not cleared (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(releaseIdentity{}, admission.identity, cmp.AllowUnexported(releaseIdentity{})); diff != "" {
		t.Errorf("released admission identity was not cleared (-want +got):\n%s", diff)
	}
}
