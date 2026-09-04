package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type relaunchVsockRepo struct {
	vsock.Repository
	item     *model.VsockConfigItem
	err      error
	getCalls int
}

// Rationale: successful start and reboot must deliver the same complete typed
// relaunch state to their single spawn seam, including normal-process-only
// agent credentials that are absent from the privileged launch request.
func TestVMLifecycleCarriesTypedRelaunchStateToSpawner(t *testing.T) {
	tests := map[string]struct {
		status model.VMStatus
		run    func(*Operation, context.Context) *errs.BatchResult
	}{
		"start": {
			status: model.VMStatusStopped,
			run: func(op *Operation, ctx context.Context) *errs.BatchResult {
				return op.VMStart(ctx, inputs.VMInput{Identifiers: []string{"vm-success"}})
			},
		},
		"reboot": {
			status: model.VMStatusRunning,
			run: func(op *Operation, ctx context.Context) *errs.BatchResult {
				return op.VMReboot(ctx, inputs.VMInput{Identifiers: []string{"vm-success"}})
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			vmRepo := testutil.NewVMRepo()
			require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
				ID: "vm-success", Name: "vm-success", Status: tc.status,
				CgroupLimits: model.VMCgroupLimits{PolicyVersion: 1},
			}))
			vsockRepo := &relaunchVsockRepo{
				Repository: testutil.NewVsockRepo(),
				item: &model.VsockConfigItem{
					VmID: "vm-success", GuestCID: 42, UDSPath: "/persisted.sock",
					Port: 1024, Token: "secret",
				},
			}
			var captured *vmRelaunchState
			op := &Operation{
				CacheDir: t.TempDir(),
				Repos:    Repos{VM: vmRepo, Vsock: vsockRepo},
				AuditLog: &logging.AuditLog{},
				relaunchSpawn: func(_ context.Context, state *vmRelaunchState, snapshotMode bool, snapshotDir string) error {
					captured = state
					assert.False(t, snapshotMode)
					assert.Empty(t, snapshotDir)
					state.vm.Status = model.VMStatusRunning
					return vmRepo.UpdateStatus(t.Context(), state.vm.ID, model.VMStatusRunning)
				},
			}

			result := tc.run(op, t.Context())
			require.Len(t, result.Items, 1)
			require.NoError(t, result.Items[0].Exception)
			assert.Equal(t, "success", result.Items[0].Status)
			require.NotNil(t, captured)
			require.NotNil(t, captured.vsock)
			assert.Equal(t, 42, captured.vsock.GuestCID)
			assert.Equal(t, 1024, captured.vsock.Port)
			assert.Equal(t, "secret", captured.vsock.Token)
			assert.Equal(t, filepath.Join(op.CacheDir, "vms", "vm-success", infra.VMVsockSocketFilename),
				captured.vsock.UDSPath)
			assert.Same(t, captured.vsock, captured.vm.Vsock)
		})
	}
}

func (r *relaunchVsockRepo) GetByVMID(_ context.Context, _ string) (*model.VsockConfigItem, error) {
	r.getCalls++
	return r.item, r.err
}

func TestLoadVMRelaunchState(t *testing.T) {
	t.Run("row is cloned and normalized", func(t *testing.T) {
		started := time.Unix(123, 0)
		row := &model.VsockConfigItem{
			ID: "vsock-1", VmID: "vm-1", GuestCID: model.MinVsockGuestCID,
			UDSPath: "/database-selected.sock", Port: 1024, Token: "secret",
			AgentVersion: "1.2.3", Upgrading: true, UpgradeStartedAt: &started,
		}
		repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), item: row}
		cacheDir := t.TempDir()
		op := &Operation{CacheDir: cacheDir, Repos: Repos{Vsock: repo}}
		vmItem := &model.VMItem{ID: "vm-1", Name: "example"}

		state, err := op.loadVMRelaunchState(t.Context(), vmItem)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Same(t, vmItem, state.vm)
		want := *row
		want.UDSPath = filepath.Join(cacheDir, "vms", vmItem.ID, infra.VMVsockSocketFilename)
		if diff := cmp.Diff(&want, state.vsock); diff != "" {
			t.Errorf("relaunch vsock state mismatch (-want +got):\n%s", diff)
		}

		state.vsock.UDSPath = "/mutated"
		*state.vsock.UpgradeStartedAt = time.Unix(456, 0)
		assert.Equal(t, "/database-selected.sock", row.UDSPath)
		assert.Equal(t, time.Unix(123, 0), *row.UpgradeStartedAt)
	})

	t.Run("valid absence", func(t *testing.T) {
		repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo()}
		vmItem := &model.VMItem{ID: "vm-absent"}
		state, err := (&Operation{Repos: Repos{Vsock: repo}}).loadVMRelaunchState(t.Context(), vmItem)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Same(t, vmItem, state.vm)
		assert.Nil(t, state.vsock)
	})

	t.Run("valid CID boundaries", func(t *testing.T) {
		for name, cid := range map[string]int{
			"minimum": model.MinVsockGuestCID,
			"maximum": int(model.MaxVsockGuestCID),
		} {
			t.Run(name, func(t *testing.T) {
				repo := &relaunchVsockRepo{
					Repository: testutil.NewVsockRepo(),
					item:       &model.VsockConfigItem{VmID: "vm-boundary", GuestCID: cid},
				}
				state, err := (&Operation{CacheDir: t.TempDir(), Repos: Repos{Vsock: repo}}).
					loadVMRelaunchState(t.Context(), &model.VMItem{ID: "vm-boundary"})
				require.NoError(t, err)
				require.NotNil(t, state.vsock)
				assert.Equal(t, cid, state.vsock.GuestCID)
			})
		}
	})

	t.Run("repository failure preserves cause", func(t *testing.T) {
		cause := errors.New("database unavailable")
		repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), err: cause}
		state, err := (&Operation{Repos: Repos{Vsock: repo}}).loadVMRelaunchState(
			t.Context(), &model.VMItem{ID: "vm-error"},
		)
		require.Error(t, err)
		assert.Nil(t, state)
		assert.ErrorIs(t, err, cause)
		assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(err).Code)
	})

	for name, row := range map[string]*model.VsockConfigItem{
		"VM mismatch":       {VmID: "other-vm", GuestCID: model.MinVsockGuestCID},
		"reserved CID":      {VmID: "vm-invalid", GuestCID: model.MinVsockGuestCID - 1},
		"CID above maximum": {VmID: "vm-invalid", GuestCID: int(model.MaxVsockGuestCID) + 1},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), item: row}
			state, err := (&Operation{Repos: Repos{Vsock: repo}}).loadVMRelaunchState(
				t.Context(), &model.VMItem{ID: "vm-invalid"},
			)
			require.Error(t, err)
			assert.Nil(t, state)
			assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(err).Code)
		})
	}
}

// Rationale: lifecycle commands must cross the typed relaunch-state seam before
// the spawner can accept an incomplete VM relation graph.
func TestVMLifecycleLoadsRelaunchStateBeforeSpawner(t *testing.T) {
	tests := map[string]struct {
		status model.VMStatus
		run    func(*Operation, context.Context) *errs.BatchResult
	}{
		"start": {
			status: model.VMStatusStopped,
			run: func(op *Operation, ctx context.Context) *errs.BatchResult {
				return op.VMStart(ctx, inputs.VMInput{Identifiers: []string{"vm-relaunch"}})
			},
		},
		"reboot": {
			status: model.VMStatusRunning,
			run: func(op *Operation, ctx context.Context) *errs.BatchResult {
				return op.VMReboot(ctx, inputs.VMInput{Identifiers: []string{"vm-relaunch"}})
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			vmRepo := testutil.NewVMRepo()
			require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
				ID: "vm-relaunch", Name: "vm-relaunch", Status: tc.status,
				CgroupLimits: model.VMCgroupLimits{PolicyVersion: 1},
			}))
			cause := errors.New("vsock lookup failed")
			vsockRepo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), err: cause}
			op := &Operation{Repos: Repos{VM: vmRepo, Vsock: vsockRepo}}

			result := tc.run(op, t.Context())
			require.Len(t, result.Items, 1)
			require.Error(t, result.Items[0].Exception)
			assert.ErrorIs(t, result.Items[0].Exception, cause)
			assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(result.Items[0].Exception).Code)
			assert.Equal(t, 1, vsockRepo.getCalls)
			persisted, getErr := vmRepo.Get(t.Context(), "vm-relaunch")
			require.NoError(t, getErr)
			require.NotNil(t, persisted)
			assert.Equal(t, tc.status, persisted.Status)
		})
	}
}

// Rationale: malformed persisted state must be rejected by the lifecycle call
// site itself, before start or reboot can mutate the VM or reach the spawner.
func TestVMLifecycleRejectsCorruptRelaunchCIDBeforeSpawner(t *testing.T) {
	tests := map[string]struct {
		status model.VMStatus
		run    func(*Operation, context.Context) *errs.BatchResult
	}{
		"start": {
			status: model.VMStatusStopped,
			run: func(op *Operation, ctx context.Context) *errs.BatchResult {
				return op.VMStart(ctx, inputs.VMInput{Identifiers: []string{"vm-corrupt"}})
			},
		},
		"reboot": {
			status: model.VMStatusRunning,
			run: func(op *Operation, ctx context.Context) *errs.BatchResult {
				return op.VMReboot(ctx, inputs.VMInput{Identifiers: []string{"vm-corrupt"}})
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			vmRepo := testutil.NewVMRepo()
			require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
				ID: "vm-corrupt", Name: "vm-corrupt", Status: tc.status,
				CgroupLimits: model.VMCgroupLimits{PolicyVersion: 1},
			}))
			vsockRepo := &relaunchVsockRepo{
				Repository: testutil.NewVsockRepo(),
				item: &model.VsockConfigItem{
					VmID: "vm-corrupt", GuestCID: model.MinVsockGuestCID - 1,
				},
			}
			op := &Operation{CacheDir: t.TempDir(), Repos: Repos{VM: vmRepo, Vsock: vsockRepo}}

			result := tc.run(op, t.Context())
			require.Len(t, result.Items, 1)
			require.Error(t, result.Items[0].Exception)
			assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(result.Items[0].Exception).Code)
			assert.Equal(t, 1, vsockRepo.getCalls)
			persisted, getErr := vmRepo.Get(t.Context(), "vm-corrupt")
			require.NoError(t, getErr)
			require.NotNil(t, persisted)
			assert.Equal(t, tc.status, persisted.Status)
		})
	}
}
