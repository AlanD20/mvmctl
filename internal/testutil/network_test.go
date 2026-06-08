package testutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func newNetwork(id, name, subnet string) *model.Network {
	return &model.Network{
		ID:        id,
		Name:      name,
		Subnet:    subnet,
		IsPresent: true,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func seedNet(t *testing.T, repo *testutil.NetworkRepo, n *model.Network) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, n))
}

// ─── CRUD ────────────────────────────────────────────────────────────────────

func TestNetworkRepo_Get(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "test-net", "10.0.0.0/24"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.Get(ctx, "n-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "test-net", got.Name)
	})

	t.Run("not_found_returns_nil", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("soft_deleted_returns_nil", func(t *testing.T) {
		repo2 := testutil.NewNetworkRepo()
		n := newNetwork("n-2", "del", "10.0.1.0/24")
		seedNet(t, repo2, n)
		require.NoError(t, repo2.SoftDelete(ctx, "n-2"))

		got, err := repo2.Get(ctx, "n-2")
		assert.NoError(t, err)
		assert.Nil(t, got, "soft-deleted network should not be returned by Get")
	})
}

func TestNetworkRepo_GetByName(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "alpha", "10.0.0.0/24"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "alpha")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "n-1", got.ID)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestNetworkRepo_SoftDelete(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "del-me", "10.0.0.0/24"))

	err := repo.SoftDelete(ctx, "n-1")
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "n-1")
	assert.Nil(t, got, "Get should return nil for soft-deleted")

	// But the record should still exist (hard delete would remove it)
	del, _ := repo.GetByName(ctx, "del-me")
	assert.Nil(t, del, "GetByName should also exclude soft-deleted")
}

func TestNetworkRepo_HardDelete(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "gone", "10.0.0.0/24"))

	err := repo.Delete(ctx, "n-1")
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "n-1")
	assert.Nil(t, got)
}

// ─── Default tracking ────────────────────────────────────────────────────────

func TestNetworkRepo_Default(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "primary", "10.0.0.0/24"))
	seedNet(t, repo, newNetwork("n-2", "secondary", "10.0.1.0/24"))

	t.Run("no_default_initially", func(t *testing.T) {
		got, err := repo.GetDefault(ctx)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("set_default", func(t *testing.T) {
		err := repo.SetDefault(ctx, "n-1")
		require.NoError(t, err)

		got, err := repo.GetDefault(ctx)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "n-1", got.ID)
	})

	t.Run("set_default_clears_previous", func(t *testing.T) {
		err := repo.SetDefault(ctx, "n-2")
		require.NoError(t, err)

		got, err := repo.GetDefault(ctx)
		require.NoError(t, err)
		assert.Equal(t, "n-2", got.ID)

		// First network should no longer be default
		n1, _ := repo.Get(ctx, "n-1")
		assert.False(t, n1.IsDefault)
	})
}

// ─── Listing ─────────────────────────────────────────────────────────────────

func TestNetworkRepo_ListAll(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "a", "10.0.0.0/24"))
	seedNet(t, repo, newNetwork("n-2", "b", "10.0.1.0/24"))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestNetworkRepo_ListAll_excludesSoftDeleted(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "alive", "10.0.0.0/24"))
	seedNet(t, repo, newNetwork("n-2", "dead", "10.0.1.0/24"))
	require.NoError(t, repo.SoftDelete(ctx, "n-2"))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "alive", got[0].Name)
}

func TestNetworkRepo_Count(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	assert.Equal(t, 0, mustNetCount(repo))

	seedNet(t, repo, newNetwork("n-1", "a", "10.0.0.0/24"))
	assert.Equal(t, 1, mustNetCount(repo))

	// Soft-deleted should not be counted
	require.NoError(t, repo.SoftDelete(ctx, "n-1"))
	assert.Equal(t, 0, mustNetCount(repo))
}

func mustNetCount(repo *testutil.NetworkRepo) int {
	n, _ := repo.Count(ctx)
	return n
}

// ─── Mutations ───────────────────────────────────────────────────────────────

func TestNetworkRepo_UpdateBridgeActive(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "br", "10.0.0.0/24"))

	err := repo.UpdateBridgeActive(ctx, "n-1", true)
	require.NoError(t, err)

	got, _ := repo.Get(ctx, "n-1")
	require.NotNil(t, got)
	assert.True(t, got.BridgeActive)
}

func TestNetworkRepo_UpdateManyIsPresent(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("n-1", "a", "10.0.0.0/24"))
	seedNet(t, repo, newNetwork("n-2", "b", "10.0.1.0/24"))

	err := repo.UpdateManyIsPresent(ctx, []string{"n-1", "n-2"}, false)
	require.NoError(t, err)

	n1, _ := repo.Get(ctx, "n-1")
	assert.Nil(t, n1, "isPresent=false should make Get return nil")
}

// ─── Edge cases ──────────────────────────────────────────────────────────────

func TestNetworkRepo_FindByPrefix(t *testing.T) {
	repo := testutil.NewNetworkRepo()
	seedNet(t, repo, newNetwork("abc-1", "a", "10.0.0.0/24"))
	seedNet(t, repo, newNetwork("abc-2", "b", "10.0.1.0/24"))
	seedNet(t, repo, newNetwork("xyz-9", "c", "10.0.2.0/24"))

	got, err := repo.FindByPrefix(ctx, "abc")
	require.NoError(t, err)
	assert.Len(t, got, 2)

	// Soft-deleted networks should not appear in prefix search
	require.NoError(t, repo.SoftDelete(ctx, "abc-1"))
	got2, _ := repo.FindByPrefix(ctx, "abc")
	assert.Len(t, got2, 1)
}

func TestNetworkRepo_EmptyState(t *testing.T) {
	repo := testutil.NewNetworkRepo()

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	def, err := repo.GetDefault(ctx)
	require.NoError(t, err)
	assert.Nil(t, def)
}
