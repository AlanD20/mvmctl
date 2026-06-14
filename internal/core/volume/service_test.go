package volume_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// ─── Service.CreateDisk ──────────────────────────────────────────────────────
// Rationale: Tests disk creation for raw and qcow2 formats, invalid format
// rejection, and propagation of subprocess failures (context cancellation).

func TestService_CreateDisk(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		vol     func(tmpDir string) *model.VolumeItem
		stubErr error
		wantErr bool
		errCode errs.Code
		wantCmd string // first element of args expected in FakeRunner call
	}{
		// ── Error paths ──
		"error/invalid_format_returns_error": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:     "vol-bad",
					Name:   "bad-format",
					Path:   filepath.Join(tmpDir, "test.img"),
					Format: "unsupported",
				}
			},
			wantErr: true,
			errCode: errs.CodeVolumeError,
		},
		"error/context_cancellation_returns_error": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:        "vol-cancel",
					Name:      "cancel-test",
					Path:      filepath.Join(tmpDir, "test.raw"),
					Format:    model.VolumeFormatRaw,
					SizeBytes: 1024,
				}
			},
			stubErr: context.Canceled,
			wantErr: true,
			errCode: errs.CodeVolumeError,
		},
		// ── Success paths ──
		"creates_raw_volume_with_fallocate": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:        "vol-raw-001",
					Name:      "test-raw",
					Path:      filepath.Join(tmpDir, "test.raw"),
					Format:    model.VolumeFormatRaw,
					SizeBytes: 1_048_576,
					CreatedAt: "2025-01-01T00:00:00Z",
				}
			},
			wantCmd: "fallocate",
		},
		"creates_qcow2_volume_with_qemu_img": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:        "vol-qcow2-001",
					Name:      "test-qcow2",
					Path:      filepath.Join(tmpDir, "test.qcow2"),
					Format:    model.VolumeFormatQCOW2,
					SizeBytes: 2_097_152,
					CreatedAt: "2025-01-01T00:00:00Z",
				}
			},
			wantCmd: "qemu-img",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			fake := &testutil.FakeRunner{StubRunErr: tc.stubErr}
			old := system.DefaultRunner
			system.DefaultRunner = fake
			t.Cleanup(func() { system.DefaultRunner = old })

			svc := volume.NewService(repo)
			tmpDir := t.TempDir()
			vol := tc.vol(tmpDir)

			got, err := svc.CreateDisk(ctx, vol)

			if tc.wantErr {
				require.Error(t, err)
				var de *errs.DomainError
				require.True(t, errors.As(err, &de))
				assert.Equal(t, tc.errCode, de.Code)
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)

			// Primary assertion: state change in repo
			fromRepo, repoErr := repo.Get(ctx, vol.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, fromRepo)
			assert.Equal(t, vol.SizeBytes, fromRepo.SizeBytes)

			// Verify the correct subprocess command was invoked
			if tc.wantCmd != "" {
				require.Len(t, fake.Calls, 1)
				assert.Equal(t, tc.wantCmd, fake.Calls[0].Args[0])
			}
		})
	}
}

// ─── Service.Remove ──────────────────────────────────────────────────────────
// Rationale: Tests volume removal from both the repository and filesystem,
// and that nonexistent volumes are silently ignored (matching Python behaviour).

func TestService_Remove(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		vol         func(tmpDir string) *model.VolumeItem
		upsertFirst bool
		createFile  bool
	}{
		// ── Error paths (both succeed silently) ──
		"nonexistent_volume_silently_ignored": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:   "ghost",
					Name: "ghost",
					Path: filepath.Join(tmpDir, "ghost.raw"),
				}
			},
			upsertFirst: false,
			createFile:  false,
		},
		// ── Success paths ──
		"removes_from_repo_and_disk": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:   "vol-001",
					Name: "remove-test",
					Path: filepath.Join(tmpDir, "test.raw"),
				}
			},
			upsertFirst: true,
			createFile:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			svc := volume.NewService(repo)
			tmpDir := t.TempDir()
			vol := tc.vol(tmpDir)

			if tc.upsertFirst {
				require.NoError(t, repo.Upsert(ctx, vol))
			}
			if tc.createFile {
				require.NoError(t, os.WriteFile(vol.Path, []byte("data"), 0o644))
			}

			err := svc.Remove(ctx, vol)

			// Primary assertion: always returns nil
			require.NoError(t, err)

			// State change: removed from repo
			if tc.upsertFirst {
				fromRepo, repoErr := repo.Get(ctx, vol.ID)
				require.NoError(t, repoErr)
				assert.Nil(t, fromRepo)
			}

			// State change: removed from filesystem
			if tc.createFile {
				_, statErr := os.Stat(vol.Path)
				assert.True(t, os.IsNotExist(statErr))
			}
		})
	}
}

// ─── Service.Remove (context cancellation) ────────────────────────────────────
// Rationale: Remove silently discards all errors, including when the context is
// cancelled. This test verifies that Remove returns nil and the repo is unchanged
// when repo.Delete fails due to context cancellation.

func TestService_Remove_ContextCancellation(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vol := &model.VolumeItem{
		ID:   "vol-cancel-rm",
		Name: "cancel-rm",
		Path: filepath.Join(tmpDir, "cancel-rm.raw"),
	}

	baseRepo := testutil.NewVolumeRepo()
	require.NoError(t, baseRepo.Upsert(ctx, vol))

	// Wrap with context-checking repo
	testRepo := &repoWrapper{
		Repository: baseRepo,
		checkCtx:   true,
	}

	// Create the file on disk
	require.NoError(t, os.WriteFile(vol.Path, []byte("data"), 0o644))

	svc := volume.NewService(testRepo)

	// Cancel context before calling Remove
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := svc.Remove(cancelledCtx, vol)

	// Remove silently discards errors — returns nil even when repo.Delete fails
	require.NoError(t, err)

	// Repo state: volume should still exist because Delete was never called
	// (the wrapper returned early due to context cancellation)
	fromRepo, repoErr := baseRepo.Get(ctx, vol.ID)
	require.NoError(t, repoErr)
	require.NotNil(t, fromRepo)
	assert.Equal(t, vol.ID, fromRepo.ID)

	// Filesystem: file should be removed because os.Remove is called
	// regardless of repo.Delete result (context cancellation doesn't affect it)
	_, statErr := os.Stat(vol.Path)
	assert.True(t, os.IsNotExist(statErr), "file should have been removed by os.Remove")
}

// ─── Service.ResizeDisk ──────────────────────────────────────────────────────
// Rationale: Tests raw and qcow2 resize operations, and error when the disk file
// does not exist on the filesystem.

func TestService_ResizeDisk(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		vol        func(tmpDir string) *model.VolumeItem
		newSize    int64
		createFile bool
		stubErr    error
		wantErr    bool
		errCode    errs.Code
		wantCmd    string
	}{
		// ── Error paths ──
		"error/nonexistent_disk_file_returns_error": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:     "vol-missing",
					Name:   "missing-disk",
					Path:   filepath.Join(tmpDir, "missing.raw"),
					Format: model.VolumeFormatRaw,
				}
			},
			newSize:    2_097_152,
			createFile: false,
			wantErr:    true,
			errCode:    errs.CodeVolumeError,
		},
		"error/context_cancellation_returns_error": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:        "vol-cancel-resize",
					Name:      "cancel-resize",
					Path:      filepath.Join(tmpDir, "cancel.raw"),
					Format:    model.VolumeFormatRaw,
					SizeBytes: 1_048_576,
				}
			},
			newSize:    2_097_152,
			createFile: true,
			stubErr:    context.Canceled,
			wantErr:    true,
			errCode:    errs.CodeVolumeError,
		},
		// ── Success paths ──
		"raw_resize_with_fallocate": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:        "vol-resize-raw",
					Name:      "resize-raw",
					Path:      filepath.Join(tmpDir, "resize.raw"),
					Format:    model.VolumeFormatRaw,
					SizeBytes: 1_048_576,
					CreatedAt: "2025-01-01T00:00:00Z",
				}
			},
			newSize:    2_097_152,
			createFile: true,
			wantCmd:    "fallocate",
		},
		"qcow2_resize_with_qemu_img": {
			vol: func(tmpDir string) *model.VolumeItem {
				return &model.VolumeItem{
					ID:        "vol-resize-qcow2",
					Name:      "resize-qcow2",
					Path:      filepath.Join(tmpDir, "resize.qcow2"),
					Format:    model.VolumeFormatQCOW2,
					SizeBytes: 1_048_576,
					CreatedAt: "2025-01-01T00:00:00Z",
				}
			},
			newSize:    4_194_304,
			createFile: true,
			wantCmd:    "qemu-img",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			fake := &testutil.FakeRunner{StubRunErr: tc.stubErr}
			old := system.DefaultRunner
			system.DefaultRunner = fake
			t.Cleanup(func() { system.DefaultRunner = old })

			svc := volume.NewService(repo)
			tmpDir := t.TempDir()
			vol := tc.vol(tmpDir)

			// Upsert original volume so it exists in repo
			require.NoError(t, repo.Upsert(ctx, vol))

			if tc.createFile {
				require.NoError(t, os.WriteFile(vol.Path, []byte("dummy"), 0o644))
			}

			got, err := svc.ResizeDisk(ctx, vol, tc.newSize)

			if tc.wantErr {
				require.Error(t, err)
				var de *errs.DomainError
				require.True(t, errors.As(err, &de))
				assert.Equal(t, tc.errCode, de.Code)
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)

			// Primary assertion: repo has the new size
			fromRepo, repoErr := repo.Get(ctx, vol.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, fromRepo)
			assert.Equal(t, tc.newSize, fromRepo.SizeBytes)

			// Verify the correct subprocess command was invoked
			if tc.wantCmd != "" {
				require.Len(t, fake.Calls, 1)
				assert.Equal(t, tc.wantCmd, fake.Calls[0].Args[0])
			}
		})
	}
}

// ─── Service.SetVolumesState ─────────────────────────────────────────────────
// Rationale: Tests state transitions for volume collections, including
// validation that vm_id is required when attaching, empty lists are a no-op,
// and attached volumes are properly detached when transitioning to available.

func TestService_SetVolumesState(t *testing.T) {
	ctx := context.Background()

	// ── attached_without_vm_id_returns_error ──
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

	// ── empty_volumes_list_returns_nil ──
	// Rationale: An empty volume list is a no-op and must return nil.
	t.Run("empty_volumes_list_returns_nil", func(t *testing.T) {
		repo := testutil.NewVolumeRepo()
		svc := volume.NewService(repo)

		err := svc.SetVolumesState(ctx, []*model.VolumeItem{}, model.VolumeStatusAttached, ptr.Ptr("vm-01"))
		require.NoError(t, err)
	})

	// ── attached_volume_transition_to_available ──
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

	// ── context_cancellation_during_attach ──
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
