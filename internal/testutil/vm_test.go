package testutil_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

var ctx = context.Background()

func seedVM(t *testing.T, repo *testutil.VMRepo, vm *model.VM) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, vm))
}

func newVM(id string, name string, status model.VMStatus) *model.VM {
	return &model.VM{ID: id, Name: name, Status: status}
}

// --- CRUD --------------------------------------------------------------------

func TestVMRepo_Get(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "test", model.VMStatusRunning))

	t.Run("found", func(t *testing.T) {
		got, err := repo.Get(ctx, "vm-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "test", got.Name)
	})

	t.Run("not_found_returns_nil", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestVMRepo_GetByName(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "alpha", model.VMStatusRunning))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "alpha")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "vm-1", got.ID)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestVMRepo_NamesExist(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "alpha", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-2", "beta", model.VMStatusStopped))

	t.Run("some_exist", func(t *testing.T) {
		got, err := repo.NamesExist(ctx, []string{"alpha", "gamma", "beta"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"alpha", "beta"}, got)
	})

	t.Run("none_exist", func(t *testing.T) {
		got, err := repo.NamesExist(ctx, []string{"x", "y"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("empty_input", func(t *testing.T) {
		got, err := repo.NamesExist(ctx, []string{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestVMRepo_Upsert(t *testing.T) {
	repo := testutil.NewVMRepo()

	t.Run("insert_new", func(t *testing.T) {
		err := repo.Upsert(ctx, newVM("vm-1", "new", model.VMStatusRunning))
		assert.NoError(t, err)
	})

	t.Run("update_existing", func(t *testing.T) {
		seedVM(t, repo, newVM("vm-2", "old", model.VMStatusRunning))
		seedVM(t, repo, &model.VM{ID: "vm-2", Name: "updated", Status: model.VMStatusStopped})

		got, _ := repo.Get(ctx, "vm-2")
		require.NotNil(t, got)
		assert.Equal(t, "updated", got.Name)
		assert.Equal(t, model.VMStatusStopped, got.Status)
	})
}

func TestVMRepo_Delete(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "del", model.VMStatusStopped))

	t.Run("delete_existing", func(t *testing.T) {
		err := repo.Delete(ctx, "vm-1")
		assert.NoError(t, err)

		got, _ := repo.Get(ctx, "vm-1")
		assert.Nil(t, got)
	})

	t.Run("delete_nonexistent_noop", func(t *testing.T) {
		err := repo.Delete(ctx, "nonexistent")
		assert.NoError(t, err)
	})
}

func TestVMRepo_DeleteMany(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-2", "b", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-3", "c", model.VMStatusStopped))

	t.Run("deletes_matching", func(t *testing.T) {
		count, err := repo.DeleteMany(ctx, []string{"vm-1", "vm-3"})
		require.NoError(t, err)
		assert.Equal(t, 2, count)

		got1, _ := repo.Get(ctx, "vm-1")
		got3, _ := repo.Get(ctx, "vm-3")
		assert.Nil(t, got1)
		assert.Nil(t, got3)

		got2, _ := repo.Get(ctx, "vm-2")
		require.NotNil(t, got2)
		assert.Equal(t, "b", got2.Name)
	})

	t.Run("nonexistent_ids_skipped", func(t *testing.T) {
		count, err := repo.DeleteMany(ctx, []string{"nonexistent"})
		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

// --- Listing -----------------------------------------------------------------

func TestVMRepo_ListAll(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-2", "b", model.VMStatusStopped))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestVMRepo_ListByStatus(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-2", "b", model.VMStatusStopped))
	seedVM(t, repo, newVM("vm-3", "c", model.VMStatusRunning))

	t.Run("single_status", func(t *testing.T) {
		got, err := repo.ListByStatus(ctx, string(model.VMStatusRunning))
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("multiple_statuses", func(t *testing.T) {
		got, err := repo.ListByStatus(ctx, string(model.VMStatusRunning), string(model.VMStatusStopped))
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})

	t.Run("no_statuses_returns_all", func(t *testing.T) {
		got, err := repo.ListByStatus(ctx)
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})
}

func TestVMRepo_ListExcludingStatuses(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-2", "b", model.VMStatusStopped))
	seedVM(t, repo, newVM("vm-3", "c", model.VMStatusError))

	t.Run("exclude_stopped", func(t *testing.T) {
		got, err := repo.ListExcludingStatuses(ctx, string(model.VMStatusStopped))
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("empty_exclusions_returns_all", func(t *testing.T) {
		got, err := repo.ListExcludingStatuses(ctx)
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})
}

// --- Counting ----------------------------------------------------------------

func TestVMRepo_Count(t *testing.T) {
	repo := testutil.NewVMRepo()
	assert.Equal(t, 0, mustCount(repo))

	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))
	assert.Equal(t, 1, mustCount(repo))

	seedVM(t, repo, newVM("vm-2", "b", model.VMStatusStopped))
	assert.Equal(t, 2, mustCount(repo))
}

func mustCount(repo *testutil.VMRepo) int {
	n, _ := repo.Count(ctx)
	return n
}

func TestVMRepo_CountByStatus(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))
	seedVM(t, repo, newVM("vm-2", "b", model.VMStatusStopped))
	seedVM(t, repo, newVM("vm-3", "c", model.VMStatusRunning))

	t.Run("running_count", func(t *testing.T) {
		n, err := repo.CountByStatus(ctx, string(model.VMStatusRunning))
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})

	t.Run("no_statuses_returns_all", func(t *testing.T) {
		n, err := repo.CountByStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
	})
}

// --- Lookups -----------------------------------------------------------------

func TestVMRepo_FindByPrefix(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "abc-1", Name: "a"})
	seedVM(t, repo, &model.VM{ID: "abc-22", Name: "b"})
	seedVM(t, repo, &model.VM{ID: "xyz-9", Name: "c"})

	t.Run("matching_prefix", func(t *testing.T) {
		got, err := repo.FindByPrefix(ctx, "abc")
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("no_match", func(t *testing.T) {
		got, err := repo.FindByPrefix(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestVMRepo_FindByIP(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", IPv4: "10.0.0.1"})

	t.Run("found", func(t *testing.T) {
		got, err := repo.FindByIP(ctx, "10.0.0.1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "vm-1", got.ID)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.FindByIP(ctx, "10.0.0.99")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestVMRepo_FindByMAC(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", MAC: "02:fc:00:00:00:01"})

	t.Run("found", func(t *testing.T) {
		got, err := repo.FindByMAC(ctx, "02:fc:00:00:00:01")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "vm-1", got.ID)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.FindByMAC(ctx, "00:00:00:00:00:00")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

// --- Foreign key lookups -----------------------------------------------------

func TestVMRepo_FindByNetworkID(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", NetworkID: "net-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", NetworkID: "net-1"})
	seedVM(t, repo, &model.VM{ID: "vm-3", NetworkID: "net-2"})

	got, err := repo.FindByNetworkID(ctx, "net-1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestVMRepo_GetByNetworkIDs(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", NetworkID: "net-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", NetworkID: "net-2"})

	got, err := repo.GetByNetworkIDs(ctx, []string{"net-1", "net-3"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "vm-1", got[0].ID)
}

func TestVMRepo_FindByKernelID(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", KernelID: "k-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", KernelID: "k-1"})
	seedVM(t, repo, &model.VM{ID: "vm-3", KernelID: "k-2"})

	got, err := repo.FindByKernelID(ctx, "k-1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestVMRepo_GetByKernelIDs(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", KernelID: "k-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", KernelID: "k-2"})

	got, err := repo.GetByKernelIDs(ctx, []string{"k-1", "k-3"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestVMRepo_FindByBinaryID(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", BinaryID: "b-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", BinaryID: "b-1"})

	got, err := repo.FindByBinaryID(ctx, "b-1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestVMRepo_GetByBinaryIDs(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", BinaryID: "b-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", BinaryID: "b-2"})

	got, err := repo.GetByBinaryIDs(ctx, []string{"b-1", "b-3"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestVMRepo_GetByImageIDs(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", ImageID: "img-1"})
	seedVM(t, repo, &model.VM{ID: "vm-2", ImageID: "img-2"})

	got, err := repo.GetByImageIDs(ctx, []string{"img-1", "img-3"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "vm-1", got[0].ID)
}

// --- Volume lookups (JSON array fields) --------------------------------------

func TestVMRepo_FindByVolumeID(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", VolumeIDs: []string{"vol-1", "vol-2"}})
	seedVM(t, repo, &model.VM{ID: "vm-2", VolumeIDs: []string{"vol-2"}})
	seedVM(t, repo, &model.VM{ID: "vm-3"})

	t.Run("found", func(t *testing.T) {
		got, err := repo.FindByVolumeID(ctx, "vol-1")
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Equal(t, "vm-1", got[0].ID)
	})

	t.Run("no_match", func(t *testing.T) {
		got, err := repo.FindByVolumeID(ctx, "vol-nonexistent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestVMRepo_FindByVolumeIDsBatch(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", VolumeIDs: []string{"vol-1"}})
	seedVM(t, repo, &model.VM{ID: "vm-2", VolumeIDs: []string{"vol-1", "vol-2"}})

	t.Run("finds_matching", func(t *testing.T) {
		got, err := repo.FindByVolumeIDsBatch(ctx, []string{"vol-1", "vol-3"})
		require.NoError(t, err)
		assert.Len(t, got, 2) // vm-1 and vm-2 both have vol-1
	})

	t.Run("no_matches", func(t *testing.T) {
		got, err := repo.FindByVolumeIDsBatch(ctx, []string{"vol-nonexistent"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// --- SSH key lookups (JSON array fields) ------------------------------------

func TestVMRepo_FindBySSHKeyID(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", SSHKeys: []string{"key-1", "key-2"}})
	seedVM(t, repo, &model.VM{ID: "vm-2", SSHKeys: []string{"key-2"}})

	t.Run("found", func(t *testing.T) {
		got, err := repo.FindBySSHKeyID(ctx, "key-1")
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Equal(t, "vm-1", got[0].ID)
	})

	t.Run("no_match", func(t *testing.T) {
		got, err := repo.FindBySSHKeyID(ctx, "key-nonexistent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// --- Mutations ---------------------------------------------------------------

func TestVMRepo_UpdateStatus(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, newVM("vm-1", "a", model.VMStatusRunning))

	err := repo.UpdateStatus(ctx, "vm-1", model.VMStatusStopped)
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "vm-1")
	require.NotNil(t, got)
	assert.Equal(t, model.VMStatusStopped, got.Status)
}

func TestVMRepo_UpdatePID(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", PID: 0})

	pid := 12345
	err := repo.UpdatePID(ctx, "vm-1", &pid)
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "vm-1")
	require.NotNil(t, got)
	assert.Equal(t, 12345, got.PID)
}

func TestVMRepo_UpdateProcessInfo(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1"})

	pid := 999
	var startTime int64 = 1000
	err := repo.UpdateProcessInfo(ctx, "vm-1", &pid, &startTime)
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "vm-1")
	require.NotNil(t, got)
	assert.Equal(t, 999, got.PID)
	require.NotNil(t, got.ProcessStartTime)
	assert.Equal(t, int64(1000), *got.ProcessStartTime)
}

func TestVMRepo_UpdateExitCode(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1"})

	err := repo.UpdateExitCode(ctx, "vm-1", 137)
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "vm-1")
	require.NotNil(t, got)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 137, *got.ExitCode)
}

// --- Concurrency safety ------------------------------------------------------
// Rationale: The in-memory repos use sync.RWMutex for thread safety.
// These tests verify no data races under concurrent access (-race detects).

func TestVMRepo_ConcurrencySafety(t *testing.T) {
	repo := testutil.NewVMRepo()
	seedVM(t, repo, &model.VM{ID: "vm-1", Name: "concurrent", Status: model.VMStatusRunning})

	t.Run("concurrent_read_write", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			for i := 0; i < 50; i++ {
				repo.Get(ctx, "vm-1")
			}
			close(done)
		}()
		for i := 0; i < 50; i++ {
			repo.Upsert(ctx, &model.VM{ID: "vm-2", Name: "writer"})
		}
		<-done
	})
}

// --- Edge cases --------------------------------------------------------------

func TestVMRepo_EmptyState(t *testing.T) {
	repo := testutil.NewVMRepo()

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}
