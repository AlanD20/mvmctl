package snapshot_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

// ─── Repository CRUD ───────────────────────────────────────────────────
// Rationale: Full interface contract tests for snapshot.Repository using
// the in-memory mock. Every write must be read back and compared field-for-field.
// Covers Upsert, Get, Delete, ListAll, FindByPrefix, CountBy*, FindBy*.

func TestRepo_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	t.Run("get_existing", func(t *testing.T) {
		item := &model.SnapshotItem{
			ID:           "snap-1",
			Name:         "test-snapshot",
			SourceVMID:   "vm-1",
			SourceVMName: "test-vm",
			SnapshotDir:  "/var/lib/mvm/snapshots/snap-1",
			MemoryFile:   "mem.bin",
			StateFile:    "state.bin",
			RootfsFile:   "rootfs.bin",
			KernelID:     "kernel-1",
			NetworkID:    "net-1",
			BinaryID:     "bin-1",
			VCPUCount:    2,
			MemSizeMiB:   1024,
			DiskSizeMiB:  8192,
			SSHKeys:      db.StringSlice{"ssh-ed25519 AAA...", "ssh-rsa BBB..."},
			SSHUser:      strPtr("ubuntu"),
			ExtraConfig:  &model.SnapshotExtraConfig{PCIEnabled: true},
			CreatedAt:    "2024-01-01T00:00:00Z",
			UpdatedAt:    "2024-01-01T00:00:00Z",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		got, err := repo.Get(ctx, "snap-1")
		require.NoError(t, err)
		require.NotNil(t, got)

		// Mirror Test: compare every field
		if diff := cmp.Diff(item, got); diff != "" {
			t.Errorf("Get() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("upsert_updates_existing", func(t *testing.T) {
		original := &model.SnapshotItem{
			ID:        "snap-2",
			Name:      "original",
			VCPUCount: 1,
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: "2024-01-01T00:00:00Z",
		}
		require.NoError(t, repo.Upsert(ctx, original))

		updated := &model.SnapshotItem{
			ID:        "snap-2",
			Name:      "updated",
			VCPUCount: 4,
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: "2024-01-02T00:00:00Z",
		}
		require.NoError(t, repo.Upsert(ctx, updated))

		got, err := repo.Get(ctx, "snap-2")
		require.NoError(t, err)
		require.NotNil(t, got)

		// Mirror Test: must reflect updated values
		if diff := cmp.Diff(updated, got); diff != "" {
			t.Errorf("Upsert update mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("get_not_found_returns_nil", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestRepo_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(t, repo.Upsert(ctx, &model.SnapshotItem{
		ID:        "snap-delete",
		Name:      "to-be-deleted",
		CreatedAt: "2024-01-01T00:00:00Z",
		UpdatedAt: "2024-01-01T00:00:00Z",
	}))

	// Mirror Test: verify exists before delete
	got, err := repo.Get(ctx, "snap-delete")
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NoError(t, repo.Delete(ctx, "snap-delete"))

	// Mirror Test: must be nil after delete
	got, err = repo.Get(ctx, "snap-delete")
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestRepo_Delete_nonexistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	// Deleting a nonexistent ID must not error (idempotent)
	err := repo.Delete(ctx, "does-not-exist")
	assert.NoError(t, err)
}

func TestRepo_ListAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	t.Run("empty_when_no_snapshots", func(t *testing.T) {
		items, err := repo.ListAll(ctx)
		require.NoError(t, err)
		assert.Empty(t, items)
	})

	t.Run("returns_all_snapshots", func(t *testing.T) {
		item1 := &model.SnapshotItem{
			ID:        "s-a",
			Name:      "snap-a",
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: "2024-01-01T00:00:00Z",
		}
		item2 := &model.SnapshotItem{
			ID:        "s-b",
			Name:      "snap-b",
			CreatedAt: "2024-01-02T00:00:00Z",
			UpdatedAt: "2024-01-02T00:00:00Z",
		}
		require.NoError(t, repo.Upsert(ctx, item1))
		require.NoError(t, repo.Upsert(ctx, item2))

		got, err := repo.ListAll(ctx)
		require.NoError(t, err)

		// CONTRACT: ListAll returns all items (no filtering).
		assert.Len(t, got, 2)

		// Build a lookup map by ID for content verification
		gotByID := make(map[string]*model.SnapshotItem)
		for _, g := range got {
			gotByID[g.ID] = g
		}
		for _, want := range []*model.SnapshotItem{item1, item2} {
			g, ok := gotByID[want.ID]
			if !ok {
				t.Errorf("ListAll() missing snapshot %s", want.ID)
				continue
			}
			if diff := cmp.Diff(want, g); diff != "" {
				t.Errorf("ListAll() mismatch for %s (-want +got):\n%s", want.ID, diff)
			}
		}
	})
}

// ─── Repository FindByPrefix ──────────────────────────────────────────
// Rationale: Prefix-based lookup is critical for the Resolver and CLI
// ID-prefix input. Bugs cause misleading "not found" or wrong match errors.

func TestRepo_FindByPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	// Seed data with intentional prefix overlaps
	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "abc123", Name: "exact-match", CreatedAt: "t1", UpdatedAt: "t1"}),
	)
	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "abc456", Name: "prefix-match", CreatedAt: "t2", UpdatedAt: "t2"}),
	)
	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "def789", Name: "no-match", CreatedAt: "t3", UpdatedAt: "t3"}),
	)

	t.Run("exact_match", func(t *testing.T) {
		got, err := repo.FindByPrefix(ctx, "abc123")
		require.NoError(t, err)
		require.Len(t, got, 1)
		want := &model.SnapshotItem{ID: "abc123", Name: "exact-match", CreatedAt: "t1", UpdatedAt: "t1"}
		if diff := cmp.Diff(want, got[0]); diff != "" {
			t.Errorf("FindByPrefix() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("prefix_matches_multiple", func(t *testing.T) {
		got, err := repo.FindByPrefix(ctx, "abc")
		require.NoError(t, err)
		require.Len(t, got, 2)
		gotByID := make(map[string]*model.SnapshotItem, 2)
		for _, g := range got {
			gotByID[g.ID] = g
		}
		for _, want := range []*model.SnapshotItem{
			{ID: "abc123", Name: "exact-match", CreatedAt: "t1", UpdatedAt: "t1"},
			{ID: "abc456", Name: "prefix-match", CreatedAt: "t2", UpdatedAt: "t2"},
		} {
			g, ok := gotByID[want.ID]
			if !ok {
				t.Errorf("FindByPrefix() missing %s", want.ID)
				continue
			}
			if diff := cmp.Diff(want, g); diff != "" {
				t.Errorf("FindByPrefix() mismatch for %s (-want +got):\n%s", want.ID, diff)
			}
		}
	})

	t.Run("no_match", func(t *testing.T) {
		got, err := repo.FindByPrefix(ctx, "zzz")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("empty_prefix_returns_all", func(t *testing.T) {
		got, err := repo.FindByPrefix(ctx, "")
		require.NoError(t, err)
		require.Len(t, got, 3)
		gotByID := make(map[string]*model.SnapshotItem, 3)
		for _, g := range got {
			gotByID[g.ID] = g
		}
		for _, want := range []*model.SnapshotItem{
			{ID: "abc123", Name: "exact-match", CreatedAt: "t1", UpdatedAt: "t1"},
			{ID: "abc456", Name: "prefix-match", CreatedAt: "t2", UpdatedAt: "t2"},
			{ID: "def789", Name: "no-match", CreatedAt: "t3", UpdatedAt: "t3"},
		} {
			g, ok := gotByID[want.ID]
			if !ok {
				t.Errorf("FindByPrefix() missing %s", want.ID)
				continue
			}
			if diff := cmp.Diff(want, g); diff != "" {
				t.Errorf("FindByPrefix() mismatch for %s (-want +got):\n%s", want.ID, diff)
			}
		}
	})
}

// ─── Repository CountBy ───────────────────────────────────────────────
// Rationale: Reference counting prevents deletion of kernels/networks/binaries
// that are still referenced by snapshots. Under-counting could lead to
// dangling references; over-counting could block valid deletions.

func TestRepo_CountByKernelID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s1", KernelID: "k-1", CreatedAt: "t1", UpdatedAt: "t1"}),
	)
	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s2", KernelID: "k-1", CreatedAt: "t2", UpdatedAt: "t2"}),
	)
	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s3", KernelID: "k-2", CreatedAt: "t3", UpdatedAt: "t3"}),
	)

	t.Run("count_by_kernel_id", func(t *testing.T) {
		count, err := repo.CountByKernelID(ctx, "k-1")
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("no_snapshots_for_kernel", func(t *testing.T) {
		count, err := repo.CountByKernelID(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func TestRepo_CountByNetworkID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s1", NetworkID: "n-1", CreatedAt: "t1", UpdatedAt: "t1"}),
	)
	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s2", NetworkID: "n-1", CreatedAt: "t2", UpdatedAt: "t2"}),
	)

	t.Run("count_by_network_id", func(t *testing.T) {
		count, err := repo.CountByNetworkID(ctx, "n-1")
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

func TestRepo_CountByBinaryID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s1", BinaryID: "b-1", CreatedAt: "t1", UpdatedAt: "t1"}),
	)

	t.Run("count_by_binary_id", func(t *testing.T) {
		count, err := repo.CountByBinaryID(ctx, "b-1")
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}

// ─── Repository FindBy ────────────────────────────────────────────────
// Rationale: Reverse-lookup queries (find snapshots referencing a kernel/
// network/binary) are used by the enricher to populate reverse relations.

func TestRepo_FindByKernelID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(
		t,
		repo.Upsert(
			ctx,
			&model.SnapshotItem{ID: "s1", Name: "snap-a", KernelID: "k-1", CreatedAt: "t1", UpdatedAt: "t1"},
		),
	)
	require.NoError(
		t,
		repo.Upsert(
			ctx,
			&model.SnapshotItem{ID: "s2", Name: "snap-b", KernelID: "k-1", CreatedAt: "t2", UpdatedAt: "t2"},
		),
	)
	require.NoError(
		t,
		repo.Upsert(
			ctx,
			&model.SnapshotItem{ID: "s3", Name: "snap-c", KernelID: "k-2", CreatedAt: "t3", UpdatedAt: "t3"},
		),
	)

	t.Run("find_by_kernel_id", func(t *testing.T) {
		got, err := repo.FindByKernelID(ctx, "k-1")
		require.NoError(t, err)
		require.Len(t, got, 2)
		gotByID := make(map[string]*model.SnapshotItem, 2)
		for _, g := range got {
			gotByID[g.ID] = g
		}
		for _, want := range []*model.SnapshotItem{
			{ID: "s1", Name: "snap-a", KernelID: "k-1", CreatedAt: "t1", UpdatedAt: "t1"},
			{ID: "s2", Name: "snap-b", KernelID: "k-1", CreatedAt: "t2", UpdatedAt: "t2"},
		} {
			g, ok := gotByID[want.ID]
			if !ok {
				t.Errorf("FindByKernelID() missing %s", want.ID)
				continue
			}
			if diff := cmp.Diff(want, g); diff != "" {
				t.Errorf("FindByKernelID() mismatch for %s (-want +got):\n%s", want.ID, diff)
			}
		}
	})

	t.Run("no_snapshots_for_kernel", func(t *testing.T) {
		got, err := repo.FindByKernelID(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestRepo_FindByNetworkID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s1", NetworkID: "n-1", CreatedAt: "t1", UpdatedAt: "t1"}),
	)

	t.Run("find_by_network_id", func(t *testing.T) {
		got, err := repo.FindByNetworkID(ctx, "n-1")
		require.NoError(t, err)
		require.Len(t, got, 1)
		want := &model.SnapshotItem{ID: "s1", NetworkID: "n-1", CreatedAt: "t1", UpdatedAt: "t1"}
		if diff := cmp.Diff(want, got[0]); diff != "" {
			t.Errorf("FindByNetworkID() mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestRepo_FindByBinaryID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := testutil.NewSnapshotRepo()

	require.NoError(
		t,
		repo.Upsert(ctx, &model.SnapshotItem{ID: "s1", BinaryID: "b-1", CreatedAt: "t1", UpdatedAt: "t1"}),
	)

	t.Run("find_by_binary_id", func(t *testing.T) {
		got, err := repo.FindByBinaryID(ctx, "b-1")
		require.NoError(t, err)
		require.Len(t, got, 1)
		want := &model.SnapshotItem{ID: "s1", BinaryID: "b-1", CreatedAt: "t1", UpdatedAt: "t1"}
		if diff := cmp.Diff(want, got[0]); diff != "" {
			t.Errorf("FindByBinaryID() mismatch (-want +got):\n%s", diff)
		}
	})
}

// ─── Test Helpers ─────────────────────────────────────────────────────

// strPtr returns a pointer to s for optional string fields in model types.
func strPtr(s string) *string {
	return &s
}
