package volume_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- Service.CreateDisk ---
// Rationale: Root admission requires an exact private backing mode even when
// the operator's process umask permits group writes.

func TestService_CreateDisk(t *testing.T) {
	t.Run("cancelled_before_creation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		repo := testutil.NewVolumeRepo()
		path := filepath.Join(t.TempDir(), "cancelled.raw")
		item, err := volume.NewService(repo).CreateDisk(ctx, &model.VolumeItem{
			ID: "vol-cancel", Path: path, SizeBytes: 1024,
		})
		require.Error(t, err)
		assert.Nil(t, item)
		assert.ErrorIs(t, err, context.Canceled)
		_, statErr := os.Lstat(path)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("existing_path_is_not_overwritten", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		path := filepath.Join(t.TempDir(), "existing.raw")
		content := []byte("operator data")
		require.NoError(t, os.WriteFile(path, content, 0600))
		item, err := volume.NewService(repo).CreateDisk(t.Context(), &model.VolumeItem{
			ID: "vol-existing", Path: path, SizeBytes: 1024,
		})
		require.Error(t, err)
		assert.Nil(t, item)
		got, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if diff := cmp.Diff(content, got); diff != "" {
			t.Errorf("existing raw bytes changed (-want +got):\n%s", diff)
		}
	})

	t.Run("failed_allocation_removes_new_backing", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		path := filepath.Join(t.TempDir(), "failed.raw")
		// Zero is rejected by the public input; here it deterministically makes
		// fallocate fail after CreateDisk has created the private raw file.
		item, err := volume.NewService(repo).CreateDisk(t.Context(), &model.VolumeItem{
			ID: "vol-failed-allocation", Path: path, SizeBytes: 0,
		})
		require.Error(t, err)
		assert.Nil(t, item)
		_, statErr := os.Lstat(path)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
		fromRepo, repoErr := repo.Get(t.Context(), "vol-failed-allocation")
		require.NoError(t, repoErr)
		assert.Nil(t, fromRepo)
	})

	t.Run("private_allocated_file_under_permissive_umask", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		path := filepath.Join(t.TempDir(), "private.raw")
		vol := &model.VolumeItem{ID: "vol-private", Path: path, SizeBytes: 1 << 20}
		// Umask is process-global, so this subtest must not run in parallel.
		oldUmask := unix.Umask(0002)
		defer unix.Umask(oldUmask)
		item, err := volume.NewService(repo).CreateDisk(t.Context(), vol)
		require.NoError(t, err)
		require.NotNil(t, item)
		stat, err := os.Lstat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), stat.Mode().Perm())
		assert.Equal(t, vol.SizeBytes, stat.Size())
		fromRepo, err := repo.Get(t.Context(), vol.ID)
		require.NoError(t, err)
		require.NotNil(t, fromRepo)
		assert.Equal(t, vol.SizeBytes, fromRepo.SizeBytes)
	})
}

// --- Service.Remove ---
// Rationale: A rejected authoritative removal must retain the local projection
// and must not touch a caller-supplied path.

func TestService_Remove(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MVM_CACHE_DIR", t.TempDir())
	repo := testutil.NewVolumeRepo()
	svc := volume.NewService(repo)
	vol := &model.VolumeItem{
		ID:   "invalid-volume-id",
		Name: "remove-test",
		Path: filepath.Join(t.TempDir(), "caller-owned.raw"),
	}
	content := []byte("caller data")
	require.NoError(t, repo.Upsert(ctx, vol))
	require.NoError(t, os.WriteFile(vol.Path, content, 0o600))

	err := svc.Remove(ctx, vol)
	require.Error(t, err)
	var de *errs.DomainError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, errs.CodeValidationFailed, de.Code)

	fromRepo, repoErr := repo.Get(ctx, vol.ID)
	require.NoError(t, repoErr)
	require.NotNil(t, fromRepo)
	gotContent, readErr := os.ReadFile(vol.Path)
	require.NoError(t, readErr)
	if diff := cmp.Diff(content, gotContent); diff != "" {
		t.Errorf("caller-owned file changed (-want +got):\n%s", diff)
	}
}

// --- Service.Remove (context cancellation) ---
// Rationale: A cancelled removal must fail before deleting the local projection.

func TestService_Remove_ContextCancellation(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MVM_CACHE_DIR", t.TempDir())
	vol := &model.VolumeItem{
		ID:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name: "cancel-rm",
	}
	repo := testutil.NewVolumeRepo()
	require.NoError(t, repo.Upsert(ctx, vol))
	svc := volume.NewService(repo)
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := svc.Remove(cancelledCtx, vol)
	require.ErrorIs(t, err, context.Canceled)
	fromRepo, repoErr := repo.Get(ctx, vol.ID)
	require.NoError(t, repoErr)
	require.NotNil(t, fromRepo)
}

// --- Service.ResizeDisk ---
// Rationale: Invalid growth and rejected authoritative requests must leave the
// local projection unchanged.

func TestService_ResizeDisk(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MVM_CACHE_DIR", t.TempDir())
	tests := map[string]struct {
		volumeID string
		newSize  int64
		errCode  errs.Code
	}{
		"zero_target": {
			volumeID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			newSize:  0,
			errCode:  errs.CodeVolumeResizeFailed,
		},
		"negative_target": {
			volumeID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			newSize:  -1,
			errCode:  errs.CodeVolumeResizeFailed,
		},
		"authoritative_rejection": {
			volumeID: "invalid-volume-id",
			newSize:  2_097_152,
			errCode:  errs.CodeValidationFailed,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			svc := volume.NewService(repo)
			vol := &model.VolumeItem{
				ID:        tc.volumeID,
				Name:      "resize-test",
				Path:      filepath.Join(t.TempDir(), "caller-owned.raw"),
				Format:    model.VolumeFormatRaw,
				SizeBytes: 1_048_576,
			}
			require.NoError(t, repo.Upsert(ctx, vol))
			got, err := svc.ResizeDisk(ctx, vol, tc.newSize)
			require.Error(t, err)
			var de *errs.DomainError
			require.ErrorAs(t, err, &de)
			assert.Equal(t, tc.errCode, de.Code)
			assert.Nil(t, got)
			fromRepo, repoErr := repo.Get(ctx, vol.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, fromRepo)
			assert.Equal(t, int64(1_048_576), fromRepo.SizeBytes)
		})
	}
}

// Rationale: Cancellation must stop growth before the local size projection changes.
func TestService_ResizeDisk_ContextCancellation(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MVM_CACHE_DIR", t.TempDir())
	repo := testutil.NewVolumeRepo()
	vol := &model.VolumeItem{
		ID:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Name:      "cancel-resize",
		SizeBytes: 1_048_576,
	}
	require.NoError(t, repo.Upsert(ctx, vol))
	svc := volume.NewService(repo)
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	got, err := svc.ResizeDisk(cancelledCtx, vol, 2_097_152)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, got)
	fromRepo, repoErr := repo.Get(ctx, vol.ID)
	require.NoError(t, repoErr)
	require.NotNil(t, fromRepo)
	assert.Equal(t, int64(1_048_576), fromRepo.SizeBytes)
}

// --- Service.SetVolumesState ---
// Rationale: Tests state transitions for volume collections, including
// validation that vm_id is required when attaching, empty lists are a no-op,
// and attached volumes are properly detached when transitioning to available.

func TestService_SetVolumesState(t *testing.T) {
	ctx := context.Background()

	// --- attached_without_vm_id_returns_error ---
	// Rationale: The service must reject VolumeStatusAttached when vmID is nil.
	t.Run("attached_without_vm_id_returns_error", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		svc := volume.NewService(repo)

		vol := &model.VolumeItem{
			ID:     "vol-001",
			Name:   "test-vol",
			Status: model.VolumeStatusAvailable,
		}
		require.NoError(t, repo.Upsert(ctx, vol))

		err := svc.SetVolumesState(ctx, []*model.VolumeItem{vol}, model.VolumeStatusAttached, nil)

		require.Error(t, err)
		var de *errs.DomainError
		require.True(t, errors.As(err, &de))
		assert.Equal(t, errs.CodeValidationFailed, de.Code)
		return
	})

	// --- empty_volumes_list_returns_nil ---
	// Rationale: An empty volume list is a no-op and must return nil.
	t.Run("empty_volumes_list_returns_nil", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		svc := volume.NewService(repo)

		err := svc.SetVolumesState(ctx, []*model.VolumeItem{}, model.VolumeStatusAttached, ptr.Ptr("vm-01"))
		require.NoError(t, err)
	})

	// --- attached_volume_transition_to_available ---
	// Rationale: Attached volumes should be detached (status→available, vmID→nil)
	// when SetVolumesState is called with VolumeStatusAvailable. Already-available
	// volumes should be skipped.
	t.Run("attached_volume_transition_to_available", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		svc := volume.NewService(repo)

		attachedVol := &model.VolumeItem{
			ID:        "vol-attached",
			Name:      "attached-vol",
			Status:    model.VolumeStatusAttached,
			VMID:      ptr.Ptr("vm-42"),
			SizeBytes: 1024,
			CreatedAt: "2025-01-01T00:00:00Z",
		}
		availableVol := &model.VolumeItem{
			ID:        "vol-available",
			Name:      "available-vol",
			Status:    model.VolumeStatusAvailable,
			SizeBytes: 2048,
			CreatedAt: "2025-01-01T00:00:00Z",
		}
		require.NoError(t, repo.Upsert(ctx, attachedVol))
		require.NoError(t, repo.Upsert(ctx, availableVol))

		err := svc.SetVolumesState(
			ctx,
			[]*model.VolumeItem{attachedVol, availableVol},
			model.VolumeStatusAvailable,
			nil,
		)
		require.NoError(t, err)

		// Primary assertion: attached volume is now available
		fromRepo1, repoErr := repo.Get(ctx, attachedVol.ID)
		require.NoError(t, repoErr)
		require.NotNil(t, fromRepo1)
		assert.Equal(t, model.VolumeStatusAvailable, fromRepo1.Status)
		assert.Nil(t, fromRepo1.VMID)

		// Primary assertion: available volume remains unchanged
		fromRepo2, repoErr := repo.Get(ctx, availableVol.ID)
		require.NoError(t, repoErr)
		require.NotNil(t, fromRepo2)
		assert.Equal(t, model.VolumeStatusAvailable, fromRepo2.Status)
		assert.Nil(t, fromRepo2.VMID)
		assert.Equal(t, int64(2048), fromRepo2.SizeBytes)
	})

	// --- context_cancellation_during_attach ---
	// Rationale: When the context is cancelled, Controller.Attach/Detach should
	// fail on repo.Upsert. SetVolumesState fire-and-forgets individual errors
	// (logs a warning) and returns nil. The volumes should remain unchanged.
	t.Run("context_cancellation_during_attach", func(t *testing.T) {
		baseRepo := testutil.NewVolumeRepo()
		testRepo := &repoWrapper{
			Repository: baseRepo,
			checkCtx:   true,
		}

		svc := volume.NewService(testRepo)

		vol := &model.VolumeItem{
			ID:        "vol-cancel-attach",
			Name:      "cancel-attach",
			Status:    model.VolumeStatusAvailable,
			SizeBytes: 1024,
			CreatedAt: "2025-01-01T00:00:00Z",
		}
		require.NoError(t, baseRepo.Upsert(ctx, vol))

		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()

		err := svc.SetVolumesState(cancelledCtx, []*model.VolumeItem{vol}, model.VolumeStatusAttached, ptr.Ptr("vm-99"))

		// SetVolumesState fire-and-forgets individual controller errors — returns nil
		require.NoError(t, err)

		// Volume state should be unchanged (Upsert was blocked by context cancellation)
		fromRepo, repoErr := baseRepo.Get(ctx, vol.ID)
		require.NoError(t, repoErr)
		require.NotNil(t, fromRepo)
		assert.Equal(t, model.VolumeStatusAvailable, fromRepo.Status)
		assert.Nil(t, fromRepo.VMID)
	})
}
