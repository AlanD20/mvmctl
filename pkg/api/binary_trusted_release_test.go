package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type fakeTrustedReleaseClient struct {
	install func(
		context.Context,
		jailer.TrustedReleaseInstallRequest,
		io.ReadCloser,
		uint64,
	) (jailer.TrustedReleaseInstallResult, error)
	remove func(
		context.Context,
		jailer.TrustedReleaseRemoveRequest,
	) (jailer.TrustedReleaseRemoveResult, error)
}

func (client *fakeTrustedReleaseClient) Install(
	ctx context.Context,
	request jailer.TrustedReleaseInstallRequest,
	payload io.ReadCloser,
	payloadLength uint64,
) (jailer.TrustedReleaseInstallResult, error) {
	return client.install(ctx, request, payload, payloadLength)
}

func (client *fakeTrustedReleaseClient) Remove(
	ctx context.Context,
	request jailer.TrustedReleaseRemoveRequest,
) (jailer.TrustedReleaseRemoveResult, error) {
	return client.remove(ctx, request)
}

type orderedBinaryProjectionRepo struct {
	binary.Repository
	rootCalled     bool
	reads          int
	writes         int
	replaced       *model.BinaryPair
	removedVersion string
	replaceErr     error
	removeErr      error
}

func (repo *orderedBinaryProjectionRepo) GetDefault(
	ctx context.Context,
	typ string,
	architecture string,
) (*model.BinaryItem, error) {
	if !repo.rootCalled {
		panic("SQLite read occurred before root install")
	}
	repo.reads++
	return repo.Repository.GetDefault(ctx, typ, architecture)
}

func (repo *orderedBinaryProjectionRepo) ReplaceReleaseProjection(
	ctx context.Context,
	architecture string,
	version string,
	pair *model.BinaryPair,
	setDefault bool,
) error {
	if !repo.rootCalled {
		panic("SQLite write occurred before root install")
	}
	repo.writes++
	repo.replaced = pair
	if repo.replaceErr != nil {
		return repo.replaceErr
	}
	return repo.Repository.ReplaceReleaseProjection(ctx, architecture, version, pair, setDefault)
}

func (repo *orderedBinaryProjectionRepo) RemoveReleaseProjection(
	ctx context.Context,
	architecture string,
	version string,
) error {
	repo.writes++
	repo.removedVersion = version
	if repo.removeErr != nil {
		return repo.removeErr
	}
	return repo.Repository.RemoveReleaseProjection(ctx, architecture, version)
}

func TestBinaryPullInstallsThroughRootBeforeReadingSQLiteAndProjectsResult(t *testing.T) {
	t.Setenv(assetMirrorEnvironment, "")
	architecture := system.RuntimeArch()
	baseRepo := testutil.NewBinaryRepo()
	repo := &orderedBinaryProjectionRepo{Repository: baseRepo}
	root := &fakeTrustedReleaseClient{}
	root.install = func(
		_ context.Context,
		request jailer.TrustedReleaseInstallRequest,
		payload io.ReadCloser,
		payloadLength uint64,
	) (jailer.TrustedReleaseInstallResult, error) {
		repo.rootCalled = true
		assert.Equal(t, jailer.ReleaseSlot{Version: "1.16.1", Architecture: architecture}, request.Slot)
		assert.False(t, request.AllowReplacement)
		assert.Nil(t, payload)
		assert.Zero(t, payloadLength)
		return trustedReleaseResult(request, jailer.TrustedReleaseInstalled), nil
	}
	op := &Operation{
		Repos:                Repos{Binary: repo},
		Services:             Services{Binary: binary.NewService(repo, t.TempDir(), t.TempDir(), architecture)},
		trustedReleaseClient: root,
	}

	got, err := op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "v1.16.1"}, nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, repo.replaced)
	assert.Equal(t, architecture, got[0].Architecture)
	assert.Equal(t, "1.16.1", got[0].Version)
	assert.Equal(t, filepath.Join(infra.TrustedBinaryRoot, architecture, "1.16.1", "firecracker"), got[0].Path)
	assert.True(t, got[0].IsDefault)
}

func TestBinaryPullRepairsProjectionForEveryRootOutcome(t *testing.T) {
	for _, outcome := range []jailer.TrustedReleaseInstallOutcome{
		jailer.TrustedReleaseInstalled,
		jailer.TrustedReleaseReplaced,
		jailer.TrustedReleaseUnchanged,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Setenv(assetMirrorEnvironment, "")
			architecture := system.RuntimeArch()
			repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo()}
			root := &fakeTrustedReleaseClient{}
			root.install = func(
				_ context.Context,
				request jailer.TrustedReleaseInstallRequest,
				_ io.ReadCloser,
				_ uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				repo.rootCalled = true
				return trustedReleaseResult(request, outcome), nil
			}
			op := &Operation{
				Repos:                Repos{Binary: repo},
				Services:             Services{Binary: binary.NewService(repo, t.TempDir(), t.TempDir(), architecture)},
				trustedReleaseClient: root,
			}

			_, err := op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "1.16.1"}, nil)
			require.NoError(t, err)
			assert.Equal(t, 1, repo.writes)
			require.NotNil(t, repo.replaced)
		})
	}
}

func TestBinaryPullRootErrorIsReturnedUnchangedWithoutSQLiteAccess(t *testing.T) {
	t.Setenv(assetMirrorEnvironment, "")
	architecture := system.RuntimeArch()
	repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo()}
	wantErr := errs.New(errs.CodeBinaryTrustedInstallFailed, "root denied release")
	root := &fakeTrustedReleaseClient{}
	root.install = func(
		_ context.Context,
		_ jailer.TrustedReleaseInstallRequest,
		_ io.ReadCloser,
		_ uint64,
	) (jailer.TrustedReleaseInstallResult, error) {
		repo.rootCalled = true
		return jailer.TrustedReleaseInstallResult{}, wantErr
	}
	op := &Operation{
		Repos:                Repos{Binary: repo},
		Services:             Services{Binary: binary.NewService(repo, t.TempDir(), t.TempDir(), architecture)},
		trustedReleaseClient: root,
	}

	got, err := op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "1.16.1"}, nil)
	assert.Nil(t, got)
	assert.Same(t, wantErr, err)
	assert.Zero(t, repo.reads)
	assert.Zero(t, repo.writes)
}

func TestBinaryPullProjectionFailurePreservesRootOutcomeAndRetryRepairs(t *testing.T) {
	t.Setenv(assetMirrorEnvironment, "")
	architecture := system.RuntimeArch()
	projectionErr := errors.New("projection unavailable")
	repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo(), replaceErr: projectionErr}
	outcome := jailer.TrustedReleaseInstalled
	root := &fakeTrustedReleaseClient{}
	root.install = func(
		_ context.Context,
		request jailer.TrustedReleaseInstallRequest,
		_ io.ReadCloser,
		_ uint64,
	) (jailer.TrustedReleaseInstallResult, error) {
		repo.rootCalled = true
		result := trustedReleaseResult(request, outcome)
		outcome = jailer.TrustedReleaseUnchanged
		return result, nil
	}
	op := &Operation{
		Repos:                Repos{Binary: repo},
		Services:             Services{Binary: binary.NewService(repo, t.TempDir(), t.TempDir(), architecture)},
		trustedReleaseClient: root,
	}

	pair, err := op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "1.16.1"}, nil)
	require.Len(t, pair, 2)
	var domainErr *errs.DomainError
	require.True(t, errors.As(err, &domainErr))
	assert.Equal(t, jailer.TrustedReleaseInstalled, domainErr.Details["root_outcome"])
	assert.Equal(t, true, domainErr.Details["state_repair_required"])
	assert.NotContains(t, domainErr.Message, "root failed")

	repo.replaceErr = nil
	pair, err = op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "1.16.1"}, nil)
	require.NoError(t, err)
	require.Len(t, pair, 2)
	assert.Equal(t, 2, repo.writes)
	stored, err := repo.Repository.GetByTypeAndVersion(t.Context(), "firecracker", architecture, "1.16.1")
	require.NoError(t, err)
	require.NotNil(t, stored)
}

func TestBinaryPullUsesOnlyExactOptionalMirrorArchive(t *testing.T) {
	architecture := system.RuntimeArch()
	tests := map[string]struct {
		writeArchive bool
		wantPayload  string
	}{
		"exact mirror hit":  {writeArchive: true, wantPayload: "trusted archive"},
		"exact mirror miss": {writeArchive: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mirror := t.TempDir()
			t.Setenv(assetMirrorEnvironment, mirror)
			if tc.writeArchive {
				require.NoError(t, os.WriteFile(
					filepath.Join(mirror, "firecracker-v1.16.1-"+architecture+".tgz"),
					[]byte(tc.wantPayload),
					0600,
				))
			}
			repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo()}
			root := &fakeTrustedReleaseClient{}
			root.install = func(
				_ context.Context,
				request jailer.TrustedReleaseInstallRequest,
				payload io.ReadCloser,
				payloadLength uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				repo.rootCalled = true
				if tc.wantPayload == "" {
					assert.Nil(t, payload)
					assert.Zero(t, payloadLength)
				} else {
					require.NotNil(t, payload)
					got, err := io.ReadAll(payload)
					require.NoError(t, err)
					require.NoError(t, payload.Close())
					assert.Equal(t, tc.wantPayload, string(got))
					assert.Equal(t, uint64(len(tc.wantPayload)), payloadLength)
				}
				return trustedReleaseResult(request, jailer.TrustedReleaseInstalled), nil
			}
			op := &Operation{
				Repos:                Repos{Binary: repo},
				Services:             Services{Binary: binary.NewService(repo, t.TempDir(), t.TempDir(), architecture)},
				trustedReleaseClient: root,
			}

			_, err := op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "1.16.1"}, nil)
			require.NoError(t, err)
		})
	}
}

func TestBinaryPullClosesMirrorReaderAfterRootReturns(t *testing.T) {
	architecture := system.RuntimeArch()
	mirror := t.TempDir()
	t.Setenv(assetMirrorEnvironment, mirror)
	require.NoError(t, os.WriteFile(
		filepath.Join(mirror, "firecracker-v1.16.1-"+architecture+".tgz"),
		[]byte("trusted archive"),
		0600,
	))
	repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo()}
	wantErr := errs.New(errs.CodeBinaryTrustedInstallFailed, "root rejected archive")
	var captured *os.File
	root := &fakeTrustedReleaseClient{}
	root.install = func(
		_ context.Context,
		_ jailer.TrustedReleaseInstallRequest,
		payload io.ReadCloser,
		_ uint64,
	) (jailer.TrustedReleaseInstallResult, error) {
		repo.rootCalled = true
		var ok bool
		captured, ok = payload.(*os.File)
		require.True(t, ok)
		return jailer.TrustedReleaseInstallResult{}, wantErr
	}
	op := &Operation{
		Repos:                Repos{Binary: repo},
		Services:             Services{Binary: binary.NewService(repo, t.TempDir(), t.TempDir(), architecture)},
		trustedReleaseClient: root,
	}

	_, err := op.BinaryPull(t.Context(), inputs.BinaryPullInput{Version: "1.16.1"}, nil)
	assert.Same(t, wantErr, err)
	require.NotNil(t, captured)
	_, readErr := captured.Read(make([]byte, 1))
	assert.ErrorIs(t, readErr, os.ErrClosed)
}

func TestBinaryRemoveUsesExactRootSlotAndReconcilesEveryAuthoritativeOutcome(t *testing.T) {
	tests := map[string]struct {
		removed    bool
		wantStatus string
	}{
		"removed":        {removed: true, wantStatus: "success"},
		"already absent": {removed: false, wantStatus: "skipped"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			architecture := system.RuntimeArch()
			repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo()}
			root := &fakeTrustedReleaseClient{}
			root.remove = func(
				_ context.Context,
				request jailer.TrustedReleaseRemoveRequest,
			) (jailer.TrustedReleaseRemoveResult, error) {
				repo.rootCalled = true
				assert.Equal(t, jailer.ReleaseSlot{Version: "1.16.1", Architecture: architecture}, request.Slot)
				return jailer.TrustedReleaseRemoveResult{Removed: tc.removed}, nil
			}
			op := &Operation{Repos: Repos{Binary: repo}, trustedReleaseClient: root}

			result := op.BinaryRemove(
				t.Context(),
				inputs.BinaryRemoveInput{Versions: []string{"v1.16.1", "1.16.1"}},
				false,
			)

			require.Len(t, result.Items, 1)
			assert.Equal(t, tc.wantStatus, result.Items[0].Status)
			assert.Equal(t, "1.16.1", repo.removedVersion)
			assert.Equal(t, 1, repo.writes)
		})
	}
}

func TestBinaryRemoveForceCannotChangeRootRequestOrDenial(t *testing.T) {
	architecture := system.RuntimeArch()
	wantErr := errs.New(errs.CodeBinaryRemoveFailed, "root denied referenced release")
	requests := make([]jailer.TrustedReleaseRemoveRequest, 0, 2)

	for _, force := range []bool{false, true} {
		baseRepo := testutil.NewBinaryRepo()
		pair := binaryPairFromTrustedRelease(trustedReleaseResult(jailer.TrustedReleaseInstallRequest{
			Slot: jailer.ReleaseSlot{Version: "1.16.1", Architecture: architecture},
		}, jailer.TrustedReleaseInstalled))
		pair.Firecracker.VMs = []*model.VMItem{{Name: "referencing-vm"}}
		require.NoError(t, baseRepo.ReplaceReleaseProjection(t.Context(), architecture, "1.16.1", pair, false))
		repo := &orderedBinaryProjectionRepo{Repository: baseRepo}
		root := &fakeTrustedReleaseClient{}
		root.remove = func(
			_ context.Context,
			request jailer.TrustedReleaseRemoveRequest,
		) (jailer.TrustedReleaseRemoveResult, error) {
			requests = append(requests, request)
			return jailer.TrustedReleaseRemoveResult{}, wantErr
		}
		op := &Operation{Repos: Repos{Binary: repo}, trustedReleaseClient: root}

		result := op.BinaryRemove(
			t.Context(),
			inputs.BinaryRemoveInput{Versions: []string{"1.16.1"}},
			force,
		)

		require.Len(t, result.Items, 1)
		assert.Same(t, wantErr, result.Items[0].Exception)
		assert.Zero(t, repo.writes)
		if force {
			assert.Empty(t, result.Items[0].Warnings)
		} else {
			assert.Len(t, result.Items[0].Warnings, 1)
		}
	}
	require.Len(t, requests, 2)
	assert.Equal(t, requests[0], requests[1])
	assert.Equal(t, jailer.ReleaseSlot{Version: "1.16.1", Architecture: architecture}, requests[0].Slot)
}

func TestBinaryRemoveProjectionFailurePreservesKnownRootOutcome(t *testing.T) {
	projectionErr := errors.New("projection unavailable")
	repo := &orderedBinaryProjectionRepo{Repository: testutil.NewBinaryRepo(), removeErr: projectionErr}
	root := &fakeTrustedReleaseClient{}
	root.remove = func(
		_ context.Context,
		_ jailer.TrustedReleaseRemoveRequest,
	) (jailer.TrustedReleaseRemoveResult, error) {
		return jailer.TrustedReleaseRemoveResult{Removed: true}, nil
	}
	op := &Operation{Repos: Repos{Binary: repo}, trustedReleaseClient: root}

	result := op.BinaryRemove(
		t.Context(),
		inputs.BinaryRemoveInput{Versions: []string{"1.16.1"}},
		false,
	)

	require.Len(t, result.Items, 1)
	var domainErr *errs.DomainError
	require.True(t, errors.As(result.Items[0].Exception, &domainErr))
	assert.Equal(t, true, domainErr.Details["root_outcome"])
	assert.Equal(t, true, domainErr.Details["state_repair_required"])
}

func TestBinaryPruneRemovesEachReleaseSlotOnce(t *testing.T) {
	architecture := system.RuntimeArch()
	baseRepo := testutil.NewBinaryRepo()
	pair := binaryPairFromTrustedRelease(trustedReleaseResult(jailer.TrustedReleaseInstallRequest{
		Slot: jailer.ReleaseSlot{Version: "1.16.1", Architecture: architecture},
	}, jailer.TrustedReleaseInstalled))
	require.NoError(t, baseRepo.ReplaceReleaseProjection(t.Context(), architecture, "1.16.1", pair, false))
	repo := &orderedBinaryProjectionRepo{Repository: baseRepo, rootCalled: true}
	removeCalls := 0
	root := &fakeTrustedReleaseClient{}
	root.remove = func(
		_ context.Context,
		_ jailer.TrustedReleaseRemoveRequest,
	) (jailer.TrustedReleaseRemoveResult, error) {
		removeCalls++
		return jailer.TrustedReleaseRemoveResult{Removed: true}, nil
	}
	op := &Operation{Repos: Repos{Binary: repo}, trustedReleaseClient: root}

	removed, err := op.BinaryPrune(t.Context(), false, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"1.16.1"}, removed)
	assert.Equal(t, 1, removeCalls)
}

func trustedReleaseResult(
	request jailer.TrustedReleaseInstallRequest,
	outcome jailer.TrustedReleaseInstallOutcome,
) jailer.TrustedReleaseInstallResult {
	return jailer.TrustedReleaseInstallResult{
		Outcome:       outcome,
		Slot:          request.Slot,
		ArchiveSHA256: strings.Repeat("a", 64),
		Firecracker: jailer.TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("b", 64), SizeBytes: 1 << 20,
		},
		Jailer: jailer.TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("c", 64), SizeBytes: 1 << 20,
		},
	}
}
