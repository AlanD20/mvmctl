package binary_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra"
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
			selected: binaryItem("selected", "firecracker", "1.17.0", true),
			seed: []*model.BinaryItem{
				binaryItem("selected", "firecracker", "1.17.0", true),
				binaryItem("jailer", "jailer", "1.16.2", true),
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
		"pair_in_wrong_trusted_slot": {
			selected: binaryItem("selected", "firecracker", "1.16.0", true),
			seed: func() []*model.BinaryItem {
				firecracker := binaryItem("selected", "firecracker", "1.16.0", true)
				jailer := binaryItem("jailer", "jailer", "1.16.0", true)
				firecracker.Path = filepath.Join(infra.TrustedBinaryRoot, "x86_64", "1.16.1", "firecracker")
				jailer.Path = filepath.Join(infra.TrustedBinaryRoot, "x86_64", "1.16.0", "jailer")
				return []*model.BinaryItem{firecracker, jailer}
			}(),
			wantErr: "canonical slot",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewBinaryRepo()
			for _, item := range tc.seed {
				require.NoError(t, repo.Upsert(ctx, item))
			}
			svc := binary.NewService(repo, "x86_64")

			pair, err := svc.ResolvePair(ctx, tc.selected)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, pair)
		})
	}
}

// Rationale: the unprivileged operator cannot traverse or execute a root-only
// release pair; resolving its exact repository projection must still succeed.
func TestService_ResolvePairUsesProtectedReleaseProjection(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	firecracker := binaryItem("selected", "firecracker", "1.17.0", true)
	jailer := binaryItem("jailer", "jailer", "1.17.0", true)
	for _, item := range []*model.BinaryItem{firecracker, jailer} {
		item.Path = filepath.Join(infra.TrustedBinaryRoot, item.Architecture, item.Version, item.Type)
		require.NoError(t, repo.Upsert(t.Context(), item))
	}
	pair, err := binary.NewService(repo, "x86_64").ResolvePair(t.Context(), firecracker)
	require.NoError(t, err)
	require.Equal(t, firecracker.ID, pair.Firecracker.ID)
	assert.Equal(t, jailer.ID, pair.Jailer.ID)
}

// Rationale: launch admission refuses older, future, and prerelease pairs even when their trusted projections match.
func TestService_ResolvePairRejectsUnqualifiedRelease(t *testing.T) {
	for _, version := range []string{"1.16.0", "1.16.1", "1.16.2", "1.16.3", "1.17.1", "1.18.0", "1.17.0-dev"} {
		t.Run(version, func(t *testing.T) {
			repo := testutil.NewBinaryRepo()
			firecracker := binaryItem("selected", "firecracker", version, true)
			jailer := binaryItem("jailer", "jailer", version, true)
			for _, item := range []*model.BinaryItem{firecracker, jailer} {
				item.Path = filepath.Join(infra.TrustedBinaryRoot, item.Architecture, item.Version, item.Type)
				require.NoError(t, repo.Upsert(t.Context(), item))
			}
			pair, err := binary.NewService(repo, "x86_64").ResolvePair(t.Context(), firecracker)
			require.Error(t, err)
			assert.ErrorContains(t, err, "Firecracker/Jailer 1.17.0")
			assert.Nil(t, pair)
		})
	}
}

func binaryItem(id, typ, version string, present bool) *model.BinaryItem {
	return &model.BinaryItem{
		ID: id, Type: typ, Architecture: "x86_64", Version: version, FullVersion: "v" + version,
		Path: "/tmp/untrusted/" + typ, IsPresent: present,
	}
}
