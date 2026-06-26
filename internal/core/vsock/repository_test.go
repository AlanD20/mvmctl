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

// --- CRUD ---
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

// --- ListByVMIDs ---
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

// --- NotFound ---
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

// --- UpgradeLock ---
// Rationale: Agent upgrade locking prevents concurrent upgrades of the vsock
// agent. A bug here could cause race conditions during agent upgrades.

func TestVsockRepo_UpgradeLock(t *testing.T) {
	repo := testutil.NewVsockRepo()

	// --- Error paths first ---

	t.Run("set_lock_already_held", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID: "vsock-upgrade-lock-2", VmID: "vm-upgrade-lock-2",
			GuestCID: 3, UDSPath: "/tmp/vm-upgrade-lock-2.sock",
			Port: 1024, Token: "token-2",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		require.NoError(t, repo.SetUpgradeLock(ctx, item.VmID))

		// CONTRACT: SetUpgradeLock returns error when lock is already held.
		err := repo.SetUpgradeLock(ctx, item.VmID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "upgrade already in progress")
	})

	t.Run("set_lock_nonexistent_vm", func(t *testing.T) {
		// No Upsert — VM does not exist in the repo.

		// CONTRACT: The SQLite implementation returns an error when 0 rows are affected.
		// The mock repo matches this behavior. The error message "upgrade already in
		// progress" is the same for both already-held locks and non-existent VMs because
		// both cases result in 0 rows updated in SQLite.
		err := repo.SetUpgradeLock(ctx, "vm-nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "upgrade already in progress")
	})

	// --- Happy paths ---

	t.Run("set_lock_ok", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID: "vsock-upgrade-lock-1", VmID: "vm-upgrade-lock-1",
			GuestCID: 3, UDSPath: "/tmp/vm-upgrade-lock-1.sock",
			Port: 1024, Token: "token-1",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		require.NoError(t, repo.SetUpgradeLock(ctx, item.VmID))

		// Mirror Test: read back and verify every field (R11, R12).
		got, err := repo.GetByVMID(ctx, item.VmID)
		require.NoError(t, err)
		require.NotNil(t, got)

		// CONTRACT: SetUpgradeLock sets Upgrading=true.
		assert.True(t, got.Upgrading, "after SetUpgradeLock, Upgrading must be true")
		// UpgradeStartedAt: non-deterministic timestamp, assert not nil only.
		assert.NotNil(t, got.UpgradeStartedAt, "after SetUpgradeLock, UpgradeStartedAt must be set")

		// CONTRACT: Fields not related to the lock are preserved unchanged.
		preserved := *got
		preserved.Upgrading = item.Upgrading
		preserved.UpgradeStartedAt = item.UpgradeStartedAt
		preserved.AgentVersion = item.AgentVersion
		if diff := cmp.Diff(item, &preserved); diff != "" {
			t.Errorf("preserved fields mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("clear_lock", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID: "vsock-upgrade-lock-3", VmID: "vm-upgrade-lock-3",
			GuestCID: 3, UDSPath: "/tmp/vm-upgrade-lock-3.sock",
			Port: 1024, Token: "token-3",
		}
		require.NoError(t, repo.Upsert(ctx, item))
		require.NoError(t, repo.SetUpgradeLock(ctx, item.VmID))

		require.NoError(t, repo.ClearUpgradeLock(ctx, item.VmID))

		// Mirror Test: read back and verify every field (R11, R12).
		got, err := repo.GetByVMID(ctx, item.VmID)
		require.NoError(t, err)
		require.NotNil(t, got)

		// CONTRACT: ClearUpgradeLock resets Upgrading to false and UpgradeStartedAt to nil.
		assert.False(t, got.Upgrading, "after ClearUpgradeLock, Upgrading must be false")
		assert.Nil(t, got.UpgradeStartedAt, "after ClearUpgradeLock, UpgradeStartedAt must be nil")

		// CONTRACT: Fields not related to the lock are preserved unchanged.
		preserved := *got
		preserved.Upgrading = item.Upgrading
		preserved.UpgradeStartedAt = item.UpgradeStartedAt
		preserved.AgentVersion = item.AgentVersion
		if diff := cmp.Diff(item, &preserved); diff != "" {
			t.Errorf("preserved fields mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("clear_lock_noop_when_not_set", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID: "vsock-upgrade-lock-4", VmID: "vm-upgrade-lock-4",
			GuestCID: 3, UDSPath: "/tmp/vm-upgrade-lock-4.sock",
			Port: 1024, Token: "token-4",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		// SAFETY INVARIANT: This subtest documents that ClearUpgradeLock is idempotent.
		// It does NOT need to fail on gutted function — the value is proving a negative
		// (no error/corruption from redundant calls).
		// CONTRACT: ClearUpgradeLock on an unlocked VM is safe — no error, no corruption.
		err := repo.ClearUpgradeLock(ctx, item.VmID)
		assert.NoError(t, err)

		got, err := repo.GetByVMID(ctx, item.VmID)
		require.NoError(t, err)
		require.NotNil(t, got)

		// CONTRACT: State is unchanged (no residual side effects).
		assert.False(t, got.Upgrading)
		assert.Nil(t, got.UpgradeStartedAt)

		// CONTRACT: Fields not related to the lock are preserved unchanged.
		preserved := *got
		preserved.Upgrading = item.Upgrading
		preserved.UpgradeStartedAt = item.UpgradeStartedAt
		preserved.AgentVersion = item.AgentVersion
		if diff := cmp.Diff(item, &preserved); diff != "" {
			t.Errorf("preserved fields mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("set_lock_context_cancelled", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID: "vsock-upgrade-lock-cancel-1", VmID: "vm-upgrade-lock-cancel-1",
			GuestCID: 3, UDSPath: "/tmp/vm-upgrade-lock-cancel-1.sock",
			Port: 1024, Token: "token-cancel-1",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		ctxCancelled, cancel := context.WithCancel(ctx)
		cancel()

		// CONTRACT: SetUpgradeLock returns promptly even with cancelled context.
		// The mock repo ignores context, so the operation will succeed since the VM
		// exists and is not locked. The value is proving it doesn't block/hang.
		err := repo.SetUpgradeLock(ctxCancelled, item.VmID)
		assert.NoError(t, err)
	})

	t.Run("update_agent_version", func(t *testing.T) {
		item := &model.VsockConfigItem{
			ID: "vsock-upgrade-lock-5", VmID: "vm-upgrade-lock-5",
			GuestCID: 3, UDSPath: "/tmp/vm-upgrade-lock-5.sock",
			Port: 1024, Token: "token-5",
		}
		require.NoError(t, repo.Upsert(ctx, item))

		version := "0.1.0"
		require.NoError(t, repo.UpdateAgentVersion(ctx, item.VmID, version))

		// Mirror Test: read back and verify every field (R11, R12).
		got, err := repo.GetByVMID(ctx, item.VmID)
		require.NoError(t, err)
		require.NotNil(t, got)

		// CONTRACT: UpdateAgentVersion persists the agent version string.
		assert.Equal(t, version, got.AgentVersion, "AgentVersion must match the persisted value")

		// CONTRACT: Upgrading and UpgradeStartedAt are unaffected by UpdateAgentVersion.
		assert.False(t, got.Upgrading)
		assert.Nil(t, got.UpgradeStartedAt)

		// CONTRACT: Fields not related to the agent version are preserved unchanged.
		preserved := *got
		preserved.Upgrading = item.Upgrading
		preserved.UpgradeStartedAt = item.UpgradeStartedAt
		preserved.AgentVersion = item.AgentVersion
		if diff := cmp.Diff(item, &preserved); diff != "" {
			t.Errorf("preserved fields mismatch (-want +got):\n%s", diff)
		}
	})
}
