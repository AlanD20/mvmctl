package vm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
	"mvmctl/internal/lib/model"
	jailersvc "mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type resumeTestBroker struct {
	err error
}

func (broker resumeTestBroker) Resume(
	ctx context.Context,
	request jailersvc.VMAPIRequest,
) (jailersvc.VMAPIResult, error) {
	if err := ctx.Err(); err != nil {
		return jailersvc.VMAPIResult{}, err
	}
	if broker.err != nil {
		return jailersvc.VMAPIResult{}, broker.err
	}
	return jailersvc.VMAPIResult{VMID: request.VMID, Applied: true}, nil
}

// newResumeControllerDB uses the production schema/repository because refusal must leave durable projection intact.
// The internal package cannot import testutil, which itself imports the VM domain.
func newResumeControllerDB(t *testing.T, status model.VMStatus) (*Controller, *db.Handle) {
	t.Helper()
	handle := db.New(filepath.Join(t.TempDir(), "resume.db"))
	_, err := handle.RunMigrationsCtx(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	_, err = handle.DB().ExecContext(t.Context(), "PRAGMA foreign_keys = OFF")
	require.NoError(t, err)
	repo := NewRepository(handle.DB())
	item := &model.VMItem{
		ID: "0123456789abcdef0123456789abcdef", Name: "resume-source", Status: status,
		NetworkPlacement: model.VMNetworkPlacementNetworkless, CloudInitMode: "off",
		CreatedAt: "2026-10-09T00:00:00Z", UpdatedAt: "2026-10-09T00:00:00Z",
	}
	require.NoError(t, repo.Upsert(t.Context(), item))
	return NewController(item, repo), handle
}

func TestControllerResumeRootRefusalPreservesProjection(t *testing.T) {
	// Rationale: A running/starting cache survived interrupted export while root held the source paused.
	// A root refusal must escape Resume and must not claim recovery by updating SQLite.
	for name, status := range map[string]model.VMStatus{
		"running": model.VMStatusRunning, "starting": model.VMStatusStarting, "paused": model.VMStatusPaused,
	} {
		t.Run(name, func(t *testing.T) {
			controller, _ := newResumeControllerDB(t, status)
			before, err := controller.repo.Get(t.Context(), controller.vm.ID)
			require.NoError(t, err)
			rootErr := errs.New(errs.CodeImageImportFailed, "VM rootfs export recovery is pending",
				errs.WithDetails(map[string]any{"source_vm_id": controller.vm.ID, "vm_state": "unknown"}))
			err = controller.resume(t.Context(), resumeTestBroker{err: rootErr})
			assert.ErrorIs(t, err, rootErr)
			after, readErr := controller.repo.Get(t.Context(), controller.vm.ID)
			require.NoError(t, readErr)
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("root refusal changed durable projection (-want +got):\n%s", diff)
			}
			assert.Equal(t, status, controller.vm.Status)
		})
	}
}

func TestControllerResumeProjectsOnlyRootConfirmedRunning(t *testing.T) {
	for name, status := range map[string]model.VMStatus{
		"running_idempotence": model.VMStatusRunning,
		"stale_starting":      model.VMStatusStarting,
		"paused_resume":       model.VMStatusPaused,
	} {
		t.Run(name, func(t *testing.T) {
			controller, _ := newResumeControllerDB(t, status)
			require.NoError(t, controller.resume(t.Context(), resumeTestBroker{}))
			persisted, err := controller.repo.Get(t.Context(), controller.vm.ID)
			require.NoError(t, err)
			require.NotNil(t, persisted)
			assert.Equal(t, model.VMStatusRunning, persisted.Status)
		})
	}
}

func TestControllerResumeCancelledAuthorityDoesNotProjectRunning(t *testing.T) {
	controller, _ := newResumeControllerDB(t, model.VMStatusStarting)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := controller.resume(ctx, resumeTestBroker{})
	assert.ErrorIs(t, err, context.Canceled)
	persisted, err := controller.repo.Get(t.Context(), controller.vm.ID)
	require.NoError(t, err)
	assert.Equal(t, model.VMStatusStarting, persisted.Status)
}

func TestControllerResumeProjectionFailureRemainsError(t *testing.T) {
	// Rationale: Root success cannot hide failure to persist the caller's status projection.
	controller, handle := newResumeControllerDB(t, model.VMStatusPaused)
	_, err := handle.DB().ExecContext(t.Context(), `CREATE TRIGGER refuse_resume_projection
		BEFORE UPDATE OF status ON vm_instances BEGIN SELECT RAISE(ABORT, 'projection refused'); END`)
	require.NoError(t, err)
	err = controller.resume(t.Context(), resumeTestBroker{})
	assert.Error(t, err)
	persisted, err := controller.repo.Get(t.Context(), controller.vm.ID)
	require.NoError(t, err)
	assert.Equal(t, model.VMStatusPaused, persisted.Status)
}
