package binary_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

// Rationale: Canonical launch must fail before subprocess execution unless the
// selected record resolves to one present exact-version Firecracker/Jailer pair.
func TestService_ResolvePairFailsClosed(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct {
		selected *model.BinaryItem
		seed     []*model.BinaryItem
		wantErr  string
	}{
		"nil_selection": {wantErr: "invalid Firecracker binary selection"},
		"wrong_binary_type": {
			selected: binaryItem("selected", "jailer", "1.16.0", true),
			wantErr:  "invalid Firecracker binary selection",
		},
		"missing_firecracker_record": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed:     []*model.BinaryItem{binaryItem("jailer", "jailer", "1.16.0", true)},
			wantErr:  "missing or mismatched",
		},
		"missing_jailer_record": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed:     []*model.BinaryItem{binaryItem("selected", "firecracker", "1.16.0", true)},
			wantErr:  "missing or mismatched",
		},
		"only_mismatched_jailer_version_exists": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed: []*model.BinaryItem{
				binaryItem("selected", "firecracker", "1.16.0", true),
				binaryItem("jailer", "jailer", "1.15.0", true),
			},
			wantErr: "missing or mismatched",
		},
		"different_firecracker_identity": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed: []*model.BinaryItem{
				binaryItem("other", "firecracker", "1.16.0", true),
				binaryItem("jailer", "jailer", "1.16.0", true),
			},
			wantErr: "missing or mismatched",
		},
		"jailer_marked_missing": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed: []*model.BinaryItem{
				binaryItem("selected", "firecracker", "1.16.0", true),
				binaryItem("jailer", "jailer", "1.16.0", false),
			},
			wantErr: "missing or mismatched",
		},
		"pair_outside_trusted_root": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed: []*model.BinaryItem{
				binaryItem("selected", "firecracker", "1.16.0", true),
				binaryItem("jailer", "jailer", "1.16.0", true),
			},
			wantErr: "not installed in the trusted root",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewBinaryRepo()
			for _, item := range tc.seed {
				require.NoError(t, repo.Upsert(ctx, item))
			}
			svc := binary.NewService(repo, t.TempDir(), t.TempDir())

			pair, err := svc.ResolvePair(ctx, tc.selected)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, pair)
		})
	}
}

// Rationale: A binary referenced by either side of persisted launch state must
// remain on disk and in the repository unless the caller explicitly forces removal.
func TestService_RemovePreservesReferencedBinary(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct {
		attachReference func(*model.BinaryItem)
	}{
		"firecracker_vm_reference": {
			attachReference: func(item *model.BinaryItem) {
				item.VMs = []*model.VMItem{{ID: "vm-id", Name: "vm-name", BinaryID: item.ID}}
			},
		},
		"jailer_vm_reference": {
			attachReference: func(item *model.BinaryItem) {
				item.VMs = []*model.VMItem{{ID: "vm-id", Name: "vm-name", JailerBinaryID: item.ID}}
			},
		},
		"snapshot_reference": {
			attachReference: func(item *model.BinaryItem) {
				item.Snapshots = []*model.SnapshotItem{{ID: "snapshot-id", Name: "snapshot-name", BinaryID: item.ID}}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewBinaryRepo()
			path := filepath.Join(t.TempDir(), "firecracker")
			require.NoError(t, os.WriteFile(path, []byte("release-binary"), 0755))
			item := binaryItem("binary-id", "firecracker", "1.16.0", true)
			item.Path = path
			tc.attachReference(item)
			require.NoError(t, repo.Upsert(ctx, item))
			svc := binary.NewService(repo, t.TempDir(), t.TempDir())

			removed, err := svc.Remove(ctx, item, false)
			require.Error(t, err)
			assert.Nil(t, removed)
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, "release-binary", string(data))
			persisted, getErr := repo.Get(ctx, item.ID)
			require.NoError(t, getErr)
			assert.Same(t, item, persisted)
		})
	}
}

func TestService_RemoveUnreferencedBinary(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewBinaryRepo()
	path := filepath.Join(t.TempDir(), "firecracker")
	require.NoError(t, os.WriteFile(path, []byte("release-binary"), 0755))
	item := binaryItem("binary-id", "firecracker", "1.16.0", true)
	item.Path = path
	require.NoError(t, repo.Upsert(ctx, item))
	svc := binary.NewService(repo, t.TempDir(), t.TempDir())

	removed, err := svc.Remove(ctx, item, false)
	require.NoError(t, err)
	assert.Same(t, item, removed)
	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	persisted, getErr := repo.Get(ctx, item.ID)
	require.NoError(t, getErr)
	assert.Nil(t, persisted)
}

func binaryItem(id, typ, version string, present bool) *model.BinaryItem {
	return &model.BinaryItem{
		ID: id, Type: typ, Version: version, FullVersion: "v" + version,
		Path: "/tmp/untrusted/" + typ, IsPresent: present,
	}
}
