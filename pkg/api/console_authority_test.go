package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

type fakeConsoleAuthority struct {
	observation jailer.ConsoleObserveResult
	stopResult  jailer.ConsoleStopResult
	err         error
}

func (fake *fakeConsoleAuthority) Observe(_ context.Context, request jailer.ConsoleObserveRequest) (
	jailer.ConsoleObserveResult, error,
) {
	if fake.err != nil {
		return jailer.ConsoleObserveResult{}, fake.err
	}
	if fake.observation.VMID != request.VMID {
		return jailer.ConsoleObserveResult{}, errs.New(errs.CodeInternal, "wrong VM")
	}
	return fake.observation, nil
}

func (fake *fakeConsoleAuthority) Stop(_ context.Context, request jailer.ConsoleStopRequest) (
	jailer.ConsoleStopResult, error,
) {
	if fake.err != nil {
		return jailer.ConsoleStopResult{}, fake.err
	}
	if fake.stopResult.VMID != request.VMID {
		return jailer.ConsoleStopResult{}, errs.New(errs.CodeInternal, "wrong VM")
	}
	return fake.stopResult, nil
}

func TestConsolePublicOperationsConsumeRootAuthorityInsteadOfCachePID(t *testing.T) {
	cache := t.TempDir()
	t.Setenv(infra.EnvKey("CACHE_DIR"), cache)
	const vmID = "22222222222222222222222222222222"
	vmDir := filepath.Join(cache, "vms", vmID)
	require.NoError(t, os.MkdirAll(vmDir, 0700))
	// An arbitrary mirror must not choose console state, endpoint, or a kill target.
	mirror := filepath.Join(vmDir, infra.VMConsolePIDFilename)
	require.NoError(t, os.WriteFile(mirror, []byte("999999999"), 0600))
	repo := testutil.NewVMRepo()
	require.NoError(t, repo.Upsert(t.Context(), &model.VMItem{ID: vmID, Name: "console-vm"}))
	pid := 4343
	rootPath := "/run/mvmctl/runtime/1000/" + vmID + "/console.sock"
	client := &fakeConsoleAuthority{
		observation: jailer.ConsoleObserveResult{
			VMID:       vmID,
			State:      jailer.ConsoleRunning,
			PID:        &pid,
			SocketPath: rootPath,
		},
		stopResult: jailer.ConsoleStopResult{VMID: vmID, Stopped: true},
	}
	op := &Operation{Repos: Repos{VM: repo}, consoleClient: client}
	state, err := op.ConsoleGetState(t.Context(), "console-vm")
	require.NoError(t, err)
	assert.True(t, state.Running)
	assert.Equal(t, &pid, state.PID)
	assert.Equal(t, rootPath, state.SocketPath)
	info, err := op.ConsoleGetConnectionInfo(t.Context(), "console-vm")
	require.NoError(t, err)
	assert.Equal(t, &model.ConsoleConnectionInfo{VMID: vmID, VMName: "console-vm", SocketPath: rootPath}, info)
	require.NoError(t, op.ConsoleKill(t.Context(), "console-vm"))
	bytes, err := os.ReadFile(mirror)
	require.NoError(t, err)
	assert.Equal(t, "999999999", string(bytes))

	client.err = errs.New(errs.CodeConsoleRelayFailed, "root identity is unavailable")
	_, err = op.ConsoleGetState(t.Context(), "console-vm")
	require.ErrorIs(t, err, client.err)
	require.ErrorIs(t, op.ConsoleKill(t.Context(), "console-vm"), client.err)
	client.err = errs.New(errs.CodeProcessError, "console stop response is not authoritative",
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{"process_started": true, "outcome_unknown": true}))
	stopErr := op.ConsoleKill(t.Context(), "console-vm")
	require.ErrorIs(t, stopErr, client.err)
	domainErr := errs.AsDomainError(stopErr)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["process_started"])
	assert.Equal(t, true, domainErr.Details["outcome_unknown"])
	client.err = nil
	client.stopResult.Stopped = false
	err = op.ConsoleKill(t.Context(), "console-vm")
	require.Error(t, err)
	assert.Equal(t, errs.CodeConsoleNotRunning, errs.AsDomainError(err).Code)
}
