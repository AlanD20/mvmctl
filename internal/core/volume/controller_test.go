package volume_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

// repoWrapper wraps volume.Repository with optional error injection
// and context cancellation checking for testing.
type repoWrapper struct {
	volume.Repository
	upsertErr error // if non-nil, Upsert returns this error
	checkCtx  bool  // if true, checks ctx.Err() before Upsert/Delete
}

func (w *repoWrapper) Upsert(ctx context.Context, v *model.VolumeItem) error {
	if w.checkCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if w.upsertErr != nil {
		return w.upsertErr
	}
	return w.Repository.Upsert(ctx, v)
}

func (w *repoWrapper) Delete(ctx context.Context, id string) error {
	if w.checkCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return w.Repository.Delete(ctx, id)
}

// ─── Controller.Attach ───────────────────────────────────────────────────────
// Rationale: Tests attaching a volume sets VolumeStatusAttached and the vmID
// in the repository, and re-attaching with the same vmID is idempotent.
// Error cases: Upsert failure is propagated; context cancellation is propagated.

func TestController_Attach(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		pre       func(vol *model.VolumeItem) // optional pre-condition setup on the volume
		vmID      string
		want      model.VolumeStatus
		wantErr   bool
		upsertErr error // if non-nil, repo.Upsert returns this error
		cancelCtx bool  // if true, use cancelled context
	}{
		// ── Error paths ──
		"attach_fails_on_upsert_error": {
			pre:       func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAvailable },
			vmID:      "vm-123",
			want:      model.VolumeStatusAvailable, // unchanged
			wantErr:   true,
			upsertErr: errors.New("connection refused"),
		},
		"attach_context_cancellation": {
			pre:       func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAvailable },
			vmID:      "vm-123",
			want:      model.VolumeStatusAvailable, // unchanged
			wantErr:   true,
			cancelCtx: true,
		},
		// ── Success paths ──
		"attaches_volume": {
			pre:  func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAvailable },
			vmID: "vm-123",
			want: model.VolumeStatusAttached,
		},
		"reattach_same_vm_id_succeeds": {
			pre:  func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAttached; vol.VMID = ptr.Ptr("vm-123") },
			vmID: "vm-123",
			want: model.VolumeStatusAttached,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			baseRepo := testutil.NewVolumeRepo()
			var testRepo volume.Repository = baseRepo
			if tc.upsertErr != nil || tc.cancelCtx {
				testRepo = &repoWrapper{
					Repository: baseRepo,
					upsertErr:  tc.upsertErr,
					checkCtx:   tc.cancelCtx,
				}
			}

			vol := &model.VolumeItem{
				ID:        "vol-001",
				Name:      "test-vol",
				SizeBytes: 1_048_576,
				Format:    model.VolumeFormatRaw,
				Path:      "/tmp/test.raw",
				Status:    model.VolumeStatusAvailable,
				CreatedAt: "2025-06-01T00:00:00Z",
				UpdatedAt: "2025-06-01T00:00:00Z",
			}
			if tc.pre != nil {
				tc.pre(vol)
			}
			require.NoError(t, baseRepo.Upsert(ctx, vol))

			// Save expected state before the operation for error assertions
			wantStatus := vol.Status
			var wantVMID *string
			if vol.VMID != nil {
				v := *vol.VMID
				wantVMID = &v
			}

			testCtx := ctx
			if tc.cancelCtx {
				var cancel context.CancelFunc
				testCtx, cancel = context.WithCancel(ctx)
				cancel()
			}

			controller := volume.NewController(vol, testRepo)
			err := controller.Attach(testCtx, tc.vmID)

			if tc.wantErr {
				require.Error(t, err)

				// Verify repo state is unchanged (Upsert never happened)
				fromRepo, repoErr := baseRepo.Get(ctx, vol.ID)
				require.NoError(t, repoErr)
				require.NotNil(t, fromRepo)
				assert.Equal(t, wantStatus, fromRepo.Status)
				if wantVMID != nil {
					require.NotNil(t, fromRepo.VMID)
					assert.Equal(t, *wantVMID, *fromRepo.VMID)
				} else {
					assert.Nil(t, fromRepo.VMID)
				}
				return
			}

			require.NoError(t, err)

			// Primary assertion: state change in repo
			fromRepo, repoErr := baseRepo.Get(ctx, vol.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, fromRepo)

			want := &model.VolumeItem{
				ID:         vol.ID,
				Name:       vol.Name,
				SizeBytes:  vol.SizeBytes,
				Format:     vol.Format,
				Path:       vol.Path,
				Status:     tc.want,
				VMID:       ptr.Ptr(tc.vmID),
				CreatedAt:  vol.CreatedAt,
				UpdatedAt:  vol.UpdatedAt,
				IsReadOnly: vol.IsReadOnly,
			}
			assert.Empty(t, cmp.Diff(want, fromRepo))
		})
	}
}

// ─── Controller.Detach ───────────────────────────────────────────────────────
// Rationale: Tests detaching a volume sets VolumeStatusAvailable and clears
// vmID in the repository. Detaching an already-available volume is idempotent.
// Error cases: Upsert failure is propagated; context cancellation is propagated.

func TestController_Detach(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		pre       func(vol *model.VolumeItem) // optional pre-condition setup
		want      model.VolumeStatus
		wantNil   bool // whether VMID should be nil after detach
		wantErr   bool
		upsertErr error // if non-nil, repo.Upsert returns this error
		cancelCtx bool  // if true, use cancelled context
	}{
		// ── Error paths ──
		"detach_fails_on_upsert_error": {
			pre:       func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAttached; vol.VMID = ptr.Ptr("vm-456") },
			want:      model.VolumeStatusAttached, // unchanged
			wantNil:   false,
			wantErr:   true,
			upsertErr: errors.New("connection refused"),
		},
		"detach_context_cancellation": {
			pre:       func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAttached; vol.VMID = ptr.Ptr("vm-456") },
			want:      model.VolumeStatusAttached, // unchanged
			wantNil:   false,
			wantErr:   true,
			cancelCtx: true,
		},
		// ── Success paths ──
		"detaches_volume": {
			pre:     func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAttached; vol.VMID = ptr.Ptr("vm-456") },
			want:    model.VolumeStatusAvailable,
			wantNil: true,
		},
		"detach_available_volume_succeeds": {
			pre:     func(vol *model.VolumeItem) { vol.Status = model.VolumeStatusAvailable; vol.VMID = nil },
			want:    model.VolumeStatusAvailable,
			wantNil: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			baseRepo := testutil.NewVolumeRepo()
			var testRepo volume.Repository = baseRepo
			if tc.upsertErr != nil || tc.cancelCtx {
				testRepo = &repoWrapper{
					Repository: baseRepo,
					upsertErr:  tc.upsertErr,
					checkCtx:   tc.cancelCtx,
				}
			}

			vol := &model.VolumeItem{
				ID:        "vol-002",
				Name:      "detach-test",
				SizeBytes: 2_097_152,
				Format:    model.VolumeFormatQCOW2,
				Path:      "/tmp/test.qcow2",
				Status:    model.VolumeStatusAttached,
				VMID:      ptr.Ptr("vm-456"),
				CreatedAt: "2025-06-01T00:00:00Z",
				UpdatedAt: "2025-06-01T00:00:00Z",
			}
			if tc.pre != nil {
				tc.pre(vol)
			}
			require.NoError(t, baseRepo.Upsert(ctx, vol))

			// Save expected state before the operation for error assertions
			wantStatus := vol.Status
			var wantVMID *string
			if vol.VMID != nil {
				v := *vol.VMID
				wantVMID = &v
			}

			testCtx := ctx
			if tc.cancelCtx {
				var cancel context.CancelFunc
				testCtx, cancel = context.WithCancel(ctx)
				cancel()
			}

			controller := volume.NewController(vol, testRepo)
			err := controller.Detach(testCtx)

			if tc.wantErr {
				require.Error(t, err)

				// Verify repo state is unchanged (Upsert never happened)
				fromRepo, repoErr := baseRepo.Get(ctx, vol.ID)
				require.NoError(t, repoErr)
				require.NotNil(t, fromRepo)
				assert.Equal(t, wantStatus, fromRepo.Status)
				if wantVMID != nil {
					require.NotNil(t, fromRepo.VMID)
					assert.Equal(t, *wantVMID, *fromRepo.VMID)
				} else {
					assert.Nil(t, fromRepo.VMID)
				}
				return
			}

			require.NoError(t, err)

			// Primary assertion: state change in repo
			fromRepo, repoErr := baseRepo.Get(ctx, vol.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, fromRepo)

			want := &model.VolumeItem{
				ID:         vol.ID,
				Name:       vol.Name,
				SizeBytes:  vol.SizeBytes,
				Format:     vol.Format,
				Path:       vol.Path,
				Status:     tc.want,
				VMID:       nil,
				CreatedAt:  vol.CreatedAt,
				UpdatedAt:  vol.UpdatedAt,
				IsReadOnly: vol.IsReadOnly,
			}
			assert.Empty(t, cmp.Diff(want, fromRepo))
		})
	}
}
