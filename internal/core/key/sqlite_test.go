package key_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestKeyL1_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := key.NewRepository(db)

	now := time.Now().Format(time.RFC3339)

	// Seed two SSH keys with distinct names — ORDER BY name means "admin-key" comes first.
	k1 := &model.SSHKeyItem{
		ID:            "key-1111111111111111111111111111111111111111111111111111111111111111",
		Name:          "admin-key",
		Fingerprint:   "SHA256:abc123def456",
		Algorithm:     "ed25519",
		Comment:       "admin@example.com",
		PublicKeyPath: "/home/user/.ssh/id_ed25519.pub",
		IsPresent:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	k2 := &model.SSHKeyItem{
		ID:            "key-2222222222222222222222222222222222222222222222222222222222222222",
		Name:          "deploy-key",
		Fingerprint:   "SHA256:xyz789uvw012",
		Algorithm:     "rsa",
		Comment:       "deploy@ci.example.com",
		PublicKeyPath: "/home/user/.ssh/id_rsa.pub",
		IsPresent:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	require.NoError(t, repo.Upsert(ctx, k1))
	require.NoError(t, repo.Upsert(ctx, k2))

	// List all and verify count and field values.
	items, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)

	assert.Equal(t, "admin-key", items[0].Name)
	assert.Equal(t, "SHA256:abc123def456", items[0].Fingerprint)
	assert.Equal(t, "ed25519", items[0].Algorithm)

	assert.Equal(t, "deploy-key", items[1].Name)
	assert.Equal(t, "SHA256:xyz789uvw012", items[1].Fingerprint)
	assert.Equal(t, "rsa", items[1].Algorithm)
}

func TestKeyL1_Defaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := key.NewRepository(db)

	now := time.Now().Format(time.RFC3339)

	// Seed two SSH keys, neither is default.
	k1 := &model.SSHKeyItem{
		ID:            "key-3333333333333333333333333333333333333333333333333333333333333333",
		Name:          "primary-key",
		Fingerprint:   "SHA256:aaa111bbb222",
		Algorithm:     "ed25519",
		Comment:       "user@primary",
		PublicKeyPath: "/home/user/.ssh/id_ed25519.pub",
		IsDefault:     false,
		IsPresent:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	k2 := &model.SSHKeyItem{
		ID:            "key-4444444444444444444444444444444444444444444444444444444444444444",
		Name:          "secondary-key",
		Fingerprint:   "SHA256:ccc333ddd444",
		Algorithm:     "rsa",
		Comment:       "user@secondary",
		PublicKeyPath: "/home/user/.ssh/id_rsa.pub",
		IsDefault:     false,
		IsPresent:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	require.NoError(t, repo.Upsert(ctx, k1))
	require.NoError(t, repo.Upsert(ctx, k2))

	// No defaults yet.
	defaults, err := repo.GetDefaults(ctx)
	require.NoError(t, err)
	assert.Empty(t, defaults)

	// Set k1 as default.
	require.NoError(t, repo.SetDefault(ctx, k1.ID))

	// GetDefaults should return only k1.
	defaults, err = repo.GetDefaults(ctx)
	require.NoError(t, err)
	require.Len(t, defaults, 1)
	assert.Equal(t, "primary-key", defaults[0].Name)
	assert.Equal(t, "ed25519", defaults[0].Algorithm)
}
