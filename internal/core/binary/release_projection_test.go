package binary_test

import (
	"context"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

const projectionTimestamp = "2026-08-31T12:00:00Z"

func TestRepositoryReplaceReleaseProjectionIsAtomicAndArchitectureScoped(t *testing.T) {
	ctx := context.Background()
	database := testutil.NewInMemoryDB(t)
	repo := binary.NewRepository(database)

	oldX8664 := releaseProjectionPair("x86_64", "1.16.1", "old-x86", true)
	aarch64 := releaseProjectionPair("aarch64", "1.16.1", "arm", true)
	for _, item := range []*model.BinaryItem{
		oldX8664.Firecracker,
		oldX8664.Jailer,
		aarch64.Firecracker,
		aarch64.Jailer,
	} {
		require.NoError(t, repo.Upsert(ctx, item))
	}

	replacement := releaseProjectionPair("x86_64", "1.16.1", "new-x86", false)
	require.NoError(t, repo.ReplaceReleaseProjection(ctx, "x86_64", "1.16.1", replacement, false))

	got, err := repo.ListAll(ctx, "x86_64")
	require.NoError(t, err)
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want := []*model.BinaryItem{replacement.Firecracker, replacement.Jailer}
	for _, item := range want {
		// CONTRACT: replacement of the current default release transfers each member's default slot.
		item.IsDefault = true
	}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("x86_64 projection mismatch (-want +got):\n%s", diff)
	}

	gotArm, err := repo.ListAll(ctx, "aarch64")
	require.NoError(t, err)
	sort.Slice(gotArm, func(i, j int) bool { return gotArm[i].ID < gotArm[j].ID })
	wantArm := []*model.BinaryItem{aarch64.Firecracker, aarch64.Jailer}
	sort.Slice(wantArm, func(i, j int) bool { return wantArm[i].ID < wantArm[j].ID })
	if diff := cmp.Diff(wantArm, gotArm); diff != "" {
		t.Errorf("aarch64 projection changed (-want +got):\n%s", diff)
	}
}

func TestRepositoryReplaceReleaseProjectionRollsBackCompletePair(t *testing.T) {
	ctx := context.Background()
	database := testutil.NewInMemoryDB(t)
	repo := binary.NewRepository(database)
	original := releaseProjectionPair("x86_64", "1.16.1", "original", true)
	for _, item := range []*model.BinaryItem{original.Firecracker, original.Jailer} {
		require.NoError(t, repo.Upsert(ctx, item))
	}
	_, err := database.Exec(`
		CREATE TRIGGER reject_replacement_jailer
		BEFORE INSERT ON binaries
		WHEN NEW.id = 'replacement-jailer'
		BEGIN
			SELECT RAISE(ABORT, 'injected projection failure');
		END;
	`)
	require.NoError(t, err)

	replacement := releaseProjectionPair("x86_64", "1.16.1", "replacement", false)
	err = repo.ReplaceReleaseProjection(ctx, "x86_64", "1.16.1", replacement, false)
	require.Error(t, err)

	got, listErr := repo.ListAll(ctx, "x86_64")
	require.NoError(t, listErr)
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want := []*model.BinaryItem{original.Firecracker, original.Jailer}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("failed projection changed persisted pair (-want +got):\n%s", diff)
	}
}

func TestRepositoryRemoveAndRepairReleaseProjection(t *testing.T) {
	ctx := context.Background()
	repo := binary.NewRepository(testutil.NewInMemoryDB(t))
	x8664 := releaseProjectionPair("x86_64", "1.16.1", "x86", true)
	aarch64 := releaseProjectionPair("aarch64", "1.16.1", "arm", true)
	for _, item := range []*model.BinaryItem{
		x8664.Firecracker,
		x8664.Jailer,
		aarch64.Firecracker,
		aarch64.Jailer,
	} {
		require.NoError(t, repo.Upsert(ctx, item))
	}

	require.NoError(t, repo.RemoveReleaseProjection(ctx, "x86_64", "1.16.1"))
	gotX8664, err := repo.ListAll(ctx, "x86_64")
	require.NoError(t, err)
	assert.Empty(t, gotX8664)
	gotArm, err := repo.ListAll(ctx, "aarch64")
	require.NoError(t, err)
	assert.Len(t, gotArm, 2)

	require.NoError(t, repo.ReplaceReleaseProjection(ctx, "x86_64", "1.16.1", x8664, true))
	repaired, err := repo.ListAll(ctx, "x86_64")
	require.NoError(t, err)
	sort.Slice(repaired, func(i, j int) bool { return repaired[i].ID < repaired[j].ID })
	want := []*model.BinaryItem{x8664.Firecracker, x8664.Jailer}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	if diff := cmp.Diff(want, repaired); diff != "" {
		t.Errorf("repaired projection mismatch (-want +got):\n%s", diff)
	}
}

func TestRepositoryEnforcesOneActiveMemberAndDefaultPerArchitecture(t *testing.T) {
	ctx := context.Background()
	repo := binary.NewRepository(testutil.NewInMemoryDB(t))

	first := releaseProjectionItem("first", "firecracker", "x86_64", "1.16.1", true)
	require.NoError(t, repo.Upsert(ctx, first))

	duplicateSlot := releaseProjectionItem("duplicate", "firecracker", "x86_64", "1.16.1", false)
	assert.Error(t, repo.Upsert(ctx, duplicateSlot))
	secondDefault := releaseProjectionItem("second-default", "firecracker", "x86_64", "1.16.0", true)
	assert.Error(t, repo.Upsert(ctx, secondDefault))
	otherArchitecture := releaseProjectionItem("arm", "firecracker", "aarch64", "1.16.1", true)
	assert.NoError(t, repo.Upsert(ctx, otherArchitecture))
}

func releaseProjectionPair(architecture, version, idPrefix string, isDefault bool) *model.BinaryPair {
	return &model.BinaryPair{
		Firecracker: releaseProjectionItem(idPrefix+"-firecracker", "firecracker", architecture, version, isDefault),
		Jailer:      releaseProjectionItem(idPrefix+"-jailer", "jailer", architecture, version, isDefault),
	}
}

func releaseProjectionItem(id, binaryType, architecture, version string, isDefault bool) *model.BinaryItem {
	return &model.BinaryItem{
		ID:           id,
		Type:         binaryType,
		Architecture: architecture,
		Version:      version,
		FullVersion:  "v" + version,
		Path:         "/var/lib/mvmctl/binaries/" + architecture + "/" + version + "/" + binaryType,
		IsDefault:    isDefault,
		IsPresent:    true,
		CreatedAt:    projectionTimestamp,
		UpdatedAt:    projectionTimestamp,
	}
}
