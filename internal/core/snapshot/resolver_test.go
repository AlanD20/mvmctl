package snapshot_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/snapshot"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"mvmctl/internal/testutil"
)

// ─── Resolver ByID ─────────────────────────────────────────────────────
// Rationale: Resolver resolves snapshot identifier prefixes to a single
// SnapshotItem. Ambiguity errors prevent the wrong snapshot from being used;
// not-found errors give clear feedback for invalid input.

func TestResolver_ByID(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()
	resolver := snapshot.NewResolver(repo)

	// Seed data with intentional prefix overlap
	require.NoError(t, repo.Upsert(ctx, &model.SnapshotItem{
		ID:        "abc123def456",
		Name:      "snapshot-alpha",
		CreatedAt: "2024-01-01T00:00:00Z",
		UpdatedAt: "2024-01-01T00:00:00Z",
	}))
	require.NoError(t, repo.Upsert(ctx, &model.SnapshotItem{
		ID:        "abc789ghi012",
		Name:      "snapshot-beta",
		CreatedAt: "2024-01-02T00:00:00Z",
		UpdatedAt: "2024-01-02T00:00:00Z",
	}))
	require.NoError(t, repo.Upsert(ctx, &model.SnapshotItem{
		ID:        "def000111222",
		Name:      "snapshot-gamma",
		CreatedAt: "2024-01-03T00:00:00Z",
		UpdatedAt: "2024-01-03T00:00:00Z",
	}))

	tests := map[string]struct {
		prefix  string
		wantErr string // substring of error message; empty = no error
		want    *model.SnapshotItem
	}{
		// Error paths FIRST
		"not_found": {
			prefix:  "zzz",
			wantErr: "snapshot not found",
		},
		"ambiguous_prefix": {
			prefix:  "abc",
			wantErr: "matches multiple snapshots",
		},

		// Happy paths
		"exact_full_id": {
			prefix: "abc123def456",
			want: &model.SnapshotItem{
				ID:        "abc123def456",
				Name:      "snapshot-alpha",
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-01T00:00:00Z",
			},
		},
		"unique_prefix": {
			prefix: "abc123",
			want: &model.SnapshotItem{
				ID:        "abc123def456",
				Name:      "snapshot-alpha",
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-01T00:00:00Z",
			},
		},
		"single_char_unique": {
			prefix: "d",
			want: &model.SnapshotItem{
				ID:        "def000111222",
				Name:      "snapshot-gamma",
				CreatedAt: "2024-01-03T00:00:00Z",
				UpdatedAt: "2024-01-03T00:00:00Z",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := resolver.ByID(ctx, tc.prefix)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				// CONTRACT: Resolver always returns errs.DomainError
				var de *errs.DomainError
				assert.ErrorAs(t, err, &de)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ByID() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
