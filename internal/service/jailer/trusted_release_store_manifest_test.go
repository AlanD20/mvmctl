package jailer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: The slot descriptor, not its replaceable pathname, selects the root-owned manifest. The decoded manifest
// must exactly match the slot and preserve every field needed to derive executable authority.
func TestTrustedReleaseDirectoryReadsManifestFromPinnedSlot(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	want := testTrustedReleaseManifest(fixture.slot)
	writeTrustedReleaseManifestFile(t, fixture.slotPath, want)
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)

	movedPath := fixture.slotPath + ".moved"
	require.NoError(t, os.Rename(fixture.slotPath, movedPath))
	require.NoError(t, os.Mkdir(fixture.slotPath, 0700))
	replacement := testTrustedReleaseManifest(fixture.slot)
	replacement.firecracker.sizeBytes++
	writeTrustedReleaseManifestFile(t, fixture.slotPath, replacement)

	got, err := directory.readManifest(t.Context())
	require.NoError(t, err)
	if diff := cmp.Diff(
		want,
		got,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("pinned trusted release manifest mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func TestTrustedReleaseDirectoryRejectsUnsafeOrCorruptManifest(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseStoreFixture){
		"missing": func(_ *testing.T, _ *trustedReleaseStoreFixture) {},
		"symlink": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			target := filepath.Join(filepath.Dir(fixture.slotPath), "attacker-manifest.json")
			writeTrustedReleaseManifestFile(t, filepath.Dir(target), testTrustedReleaseManifest(fixture.slot))
			require.NoError(t, os.Rename(filepath.Join(filepath.Dir(target), trustedReleaseManifestLeaf), target))
			require.NoError(t, os.Symlink(target, filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf)))
		},
		"directory": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.Mkdir(filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf), 0600))
		},
		"wrong mode": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			writeTrustedReleaseManifestFile(t, fixture.slotPath, testTrustedReleaseManifest(fixture.slot))
			require.NoError(t, os.Chmod(filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf), 0644))
		},
		"multiple links": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			writeTrustedReleaseManifestFile(t, fixture.slotPath, testTrustedReleaseManifest(fixture.slot))
			manifestPath := filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf)
			require.NoError(t, os.Link(manifestPath, manifestPath+".link"))
		},
		"empty": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf), nil, 0600))
		},
		"oversized": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.WriteFile(
				filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf),
				bytes.Repeat([]byte{' '}, maxTrustedReleaseManifestBytes+1),
				0600,
			))
		},
		"malformed JSON": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.WriteFile(
				filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf),
				[]byte(`{"schema_version":`),
				0600,
			))
		},
		"slot mismatch": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			other := testTrustedReleaseManifest(releaseSlot{version: "1.16.0", architecture: "x86_64"})
			writeTrustedReleaseManifestFile(t, fixture.slotPath, other)
		},
	}

	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			setup(t, fixture)
			store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
			require.NoError(t, err)
			directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
			require.NoError(t, err)
			_, err = directory.readManifest(t.Context())
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			require.NoError(t, directory.Release(t.Context()))
			require.NoError(t, store.Release(t.Context()))
		})
	}
}

func TestTrustedReleaseDirectoryRejectsManifestOwnerIdentityMismatch(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	manifest := testTrustedReleaseManifest(fixture.slot)
	writeTrustedReleaseManifestFile(t, fixture.slotPath, manifest)
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)
	directory.policy.expectedGID++

	_, err = directory.readManifest(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func TestTrustedReleaseDirectoryReadsPartialManifestChunks(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	want := testTrustedReleaseManifest(fixture.slot)
	writeTrustedReleaseManifestFile(t, fixture.slotPath, want)
	realRead := fixture.deps.read
	fixture.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
		if len(value) > 7 {
			value = value[:7]
		}
		return realRead(ctx, fd, value)
	}
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)

	got, err := directory.readManifest(t.Context())
	require.NoError(t, err)
	if diff := cmp.Diff(
		want,
		got,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("partial-read trusted release manifest mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func TestTrustedReleaseDirectoryRejectsManifestMutationDuringRead(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writeTrustedReleaseManifestFile(t, fixture.slotPath, testTrustedReleaseManifest(fixture.slot))
	manifestPath := filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf)
	realRead := fixture.deps.read
	mutated := false
	fixture.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
		count, err := realRead(ctx, fd, value)
		if !mutated {
			mutated = true
			require.NoError(t, os.Chmod(manifestPath, 0644))
		}
		return count, err
	}
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)

	_, err = directory.readManifest(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func TestTrustedReleaseDirectoryManifestReadHonorsCancellation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writeTrustedReleaseManifestFile(t, fixture.slotPath, testTrustedReleaseManifest(fixture.slot))
	ctx, cancel := context.WithCancel(t.Context())
	realRead := fixture.deps.read
	fixture.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
		count, err := realRead(ctx, fd, value)
		cancel()
		return count, err
	}
	store, err := openTrustedReleaseStoreForRead(ctx, fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(ctx, fixture.slot)
	require.NoError(t, err)

	_, err = directory.readManifest(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	require.NoError(t, directory.Release(ctx))
	require.NoError(t, store.Release(ctx))
}

func TestTrustedReleaseDirectoryManifestReadPreservesPrimaryErrorWhenCloseFails(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.slotPath, trustedReleaseManifestLeaf),
		[]byte(`{"invalid":true}`),
		0600,
	))
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)
	realClose := directory.deps.close
	directory.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	_, err = directory.readManifest(t.Context())
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.Contains(t, domainErr.Message, "close trusted release manifest")
	assert.ErrorIs(t, err, unix.EIO)
	directory.deps.close = realClose
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func TestTrustedReleaseDirectoryRejectsInvalidManifestReadCount(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writeTrustedReleaseManifestFile(t, fixture.slotPath, testTrustedReleaseManifest(fixture.slot))
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)
	directory.deps.read = func(_ context.Context, _ int, value []byte) (int, error) {
		return len(value) + 1, nil
	}

	_, err = directory.readManifest(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func writeTrustedReleaseManifestFile(t *testing.T, directory string, manifest trustedReleaseManifest) {
	t.Helper()

	raw, err := encodeTrustedReleaseManifest(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, trustedReleaseManifestLeaf), raw, 0600))
}
