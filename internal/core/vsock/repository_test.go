package vsock_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

var ctx = context.Background()

// ─── CRUD ──────────────────────────────────────────────────────────────────
// Rationale: Repository CRUD is the foundation of vsock data access.
// A bug in Upsert, GetByVMID, or DeleteByVMID would corrupt vsock state.

func TestVsockRepo_CRUD(t *testing.T) {
	repo := testutil.NewVsockRepo()

	t.Run("create_and_get", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID:       "vsock-1",
			VmID:     "vm-1",
			GuestCID: 3,
			UDSPath:  "/tmp/vm-1-vsock.sock",
			Port:     1024,
			Token:    "secret-token",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		got, err := repo.GetByVMID(ctx, "vm-1")
		require.NoError(t, err)
		require.NotNil(t, got)

		if diff := cmp.Diff(item, got); diff != "" {
			t.Errorf("GetByVMID() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("update_and_get", func(t *testing.T) {
		updated := &model.VsockConfigItem{
			ID:       "vsock-1",
			VmID:     "vm-1",
			GuestCID: 5,
			UDSPath:  "/tmp/vm-1-vsock-updated.sock",
			Port:     2048,
			Token:    "new-token",
		}
		require.NoError(t, repo.Upsert(ctx, updated))

		got, err := repo.GetByVMID(ctx, "vm-1")
		require.NoError(t, err)
		require.NotNil(t, got)

		if diff := cmp.Diff(updated, got); diff != "" {
			t.Errorf("GetByVMID() after update mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("delete_removes_record", func(t *testing.T) {
		require.NoError(t, repo.DeleteByVMID(ctx, "vm-1"))

		got, err := repo.GetByVMID(ctx, "vm-1")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

// ─── ListByVMIDs ───────────────────────────────────────────────────────────
// Rationale: ListByVMIDs is used by the enricher for batch vsock resolution.
// An N+1 query bug there would cause slow enrichment with many VMs.

func TestVsockRepo_ListByVMIDs(t *testing.T) {
	repo := testutil.NewVsockRepo()

	// Seed 3 items
	for _, item := range []*model.VsockConfigItem{
		{ID: "v1", VmID: "vm-1", GuestCID: 3, UDSPath: "/tmp/v1.sock", Port: 1024, Token: "t1"},
		{ID: "v2", VmID: "vm-2", GuestCID: 4, UDSPath: "/tmp/v2.sock", Port: 1025, Token: "t2"},
		{ID: "v3", VmID: "vm-3", GuestCID: 5, UDSPath: "/tmp/v3.sock", Port: 1026, Token: "t3"},
	} {
		require.NoError(t, repo.Upsert(ctx, item))
	}

	t.Run("list_two_of_three", func(t *testing.T) {
		got, err := repo.ListByVMIDs(ctx, []string{"vm-1", "vm-3"})
		require.NoError(t, err)
		require.Len(t, got, 2)

		gotByID := make(map[string]*model.VsockConfigItem)
		for _, item := range got {
			gotByID[item.VmID] = item
		}
		assert.Equal(t, "v1", gotByID["vm-1"].ID)
		assert.Equal(t, "v3", gotByID["vm-3"].ID)
	})

	t.Run("list_nonexistent_returns_empty", func(t *testing.T) {
		got, err := repo.ListByVMIDs(ctx, []string{"nonexistent"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("list_empty_ids_returns_empty", func(t *testing.T) {
		got, err := repo.ListByVMIDs(ctx, []string{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// ─── NotFound ──────────────────────────────────────────────────────────────
// Rationale: GetByVMID must return nil,nil for non-existent VM IDs.
// Returning an error would break the resolver's not-found handling.

func TestVsockRepo_NotFound(t *testing.T) {
	repo := testutil.NewVsockRepo()

	t.Run("get_nonexistent_returns_nil", func(t *testing.T) {
		got, err := repo.GetByVMID(ctx, "nonexistent-vm")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("delete_nonexistent_is_noop", func(t *testing.T) {
		err := repo.DeleteByVMID(ctx, "nonexistent-vm")
		assert.NoError(t, err)
	})
}
