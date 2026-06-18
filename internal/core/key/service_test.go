package key_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- Helpers ---

// setupPubKeyFile writes a public key file to a temp dir and returns its path.
func setupPubKeyFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pub")
	require.NoError(t, os.WriteFile(path, []byte(content+"\n"), 0644))
	return path
}

// --- NewService ---
// Rationale: Verify the constructor returns a usable, non-nil service.

func TestNewService(t *testing.T) {
	repo := testutil.NewKeyRepo()
	svc := key.NewService(repo, t.TempDir())
	require.NotNil(t, svc)
}

// --- GetPubkey ---
// Rationale: GetPubkey dispatches by entity type. *SSHKeyItem reads the file
// directly. string resolves via repo then reads. Wrong types must return
// a DomainError with CodeKeyError, not panic.

func TestService_GetPubkey(t *testing.T) {
	ctx := context.Background()

	pubKeyContent := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILM6vM6Z9PfT6dQ0PtTzL6z8vC0 test@host"
	pubKeyPath := setupPubKeyFile(t, pubKeyContent)

	missingFilePath := filepath.Join(t.TempDir(), "nonexistent.pub")

	testKey := &model.SSHKeyItem{
		ID:            "SHA256:abc123",
		Name:          "test-key",
		Fingerprint:   "SHA256:abc123",
		PublicKeyPath: pubKeyPath,
		IsPresent:     true,
	}

	tests := map[string]struct {
		entity   any
		seedRepo func(context.Context, *testutil.KeyRepo)
		want     string
		wantErr  string
	}{
		// Error paths FIRST — establish error contract
		"invalid_type": {
			entity:  int(42),
			wantErr: "Invalid key identifier",
		},
		"file_not_found": {
			entity: &model.SSHKeyItem{
				ID:            "SHA256:missing",
				Name:          "missing",
				PublicKeyPath: missingFilePath,
			},
			wantErr: "Public key file not found",
		},
		"not_found_string": {
			entity:  "nonexistent",
			wantErr: "Key not found",
		},

		// Happy paths
		"sshkey_item": {
			entity: testKey,
			want:   pubKeyContent,
		},
		"string_resolved": {
			entity: "test-key",
			seedRepo: func(_ context.Context, repo *testutil.KeyRepo) {
				require.NoError(t, repo.Upsert(ctx, testKey))
			},
			want: pubKeyContent,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewKeyRepo()
			svc := key.NewService(repo, filepath.Dir(pubKeyPath))
			if tc.seedRepo != nil {
				tc.seedRepo(ctx, repo)
			}

			got, err := svc.GetPubkey(ctx, tc.entity)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				var de *errs.DomainError
				if ok := errors.As(err, &de); ok {
					assert.NotEmpty(t, de.Code,
						"error code must not be empty; got message: %s", de.Message)
				}
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GetPubkey() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	// Context cancellation: the *SSHKeyItem path does not use ctx (it reads
	// a file directly). Cancellation should not affect this path. With the
	// in-memory repo even the string path is a no-op — the test documents
	// that the function does not panic or hang on cancelled context.
	t.Run("context_cancelled_sshkey_item", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()

		repo := testutil.NewKeyRepo()
		svc := key.NewService(repo, filepath.Dir(pubKeyPath))

		got, err := svc.GetPubkey(cctx, testKey)
		require.NoError(t, err)
		if diff := cmp.Diff(pubKeyContent, got); diff != "" {
			t.Errorf("GetPubkey() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("context_cancelled_string", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()

		repo := testutil.NewKeyRepo()
		require.NoError(t, repo.Upsert(ctx, testKey))
		svc := key.NewService(repo, filepath.Dir(pubKeyPath))

		got, err := svc.GetPubkey(cctx, "test-key")
		// With in-memory repo the context is not checked, so this
		// succeeds. With a real SQLite repo this would error.
		require.NoError(t, err)
		if diff := cmp.Diff(pubKeyContent, got); diff != "" {
			t.Errorf("GetPubkey() mismatch (-want +got):\n%s", diff)
		}
	})
}

// --- GetPubkeys ---
// Rationale: GetPubkeys dispatches by keys type. []string resolves via
// ResolveMany. []*SSHKeyItem reads files directly. Partial resolution
// errors must return partial content rather than failing entirely.

func TestService_GetPubkeys(t *testing.T) {
	ctx := context.Background()

	key1Content := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILM6vM6Z9PfT6dQ0PtTzL6z8vC0 test@host"
	key1Path := setupPubKeyFile(t, key1Content)

	key2Content := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDA== other@host"
	key2Path := setupPubKeyFile(t, key2Content)

	missingFilePath := filepath.Join(t.TempDir(), "nonexistent.pub")

	key1 := &model.SSHKeyItem{
		ID:            "SHA256:abc",
		Name:          "key-one",
		Fingerprint:   "SHA256:abc",
		PublicKeyPath: key1Path,
		IsPresent:     true,
	}
	key2 := &model.SSHKeyItem{
		ID:            "SHA256:def",
		Name:          "key-two",
		Fingerprint:   "SHA256:def",
		PublicKeyPath: key2Path,
		IsPresent:     true,
	}

	tests := map[string]struct {
		keys     any
		seedRepo func(context.Context, *testutil.KeyRepo)
		want     []string
		wantErr  string
	}{
		// Error paths FIRST — establish error contract
		"invalid_type": {
			keys:    "not a slice",
			wantErr: "invalid keys type",
		},
		"all_resolution_errors": {
			keys: []string{"nonexistent"},
			seedRepo: func(_ context.Context, repo *testutil.KeyRepo) {
				require.NoError(t, repo.Upsert(ctx, key1))
				require.NoError(t, repo.Upsert(ctx, key2))
			},
			wantErr: "Key not found",
		},
		"missing_file": {
			keys: []*model.SSHKeyItem{
				{ID: "missing", PublicKeyPath: missingFilePath},
			},
			wantErr: "Public key file not found",
		},

		// Happy paths
		"string_slice_all_resolved": {
			keys: []string{"key-one", "key-two"},
			seedRepo: func(_ context.Context, repo *testutil.KeyRepo) {
				require.NoError(t, repo.Upsert(ctx, key1))
				require.NoError(t, repo.Upsert(ctx, key2))
			},
			want: []string{key1Content, key2Content},
		},
		"sshkey_items": {
			keys: []*model.SSHKeyItem{key1, key2},
			want: []string{key1Content, key2Content},
		},
		"partial_errors": {
			keys: []string{"key-one", "nonexistent"},
			seedRepo: func(_ context.Context, repo *testutil.KeyRepo) {
				require.NoError(t, repo.Upsert(ctx, key1))
				require.NoError(t, repo.Upsert(ctx, key2))
			},
			want: []string{key1Content},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewKeyRepo()
			svc := key.NewService(repo, filepath.Dir(key1Path))
			if tc.seedRepo != nil {
				tc.seedRepo(ctx, repo)
			}

			got, err := svc.GetPubkeys(ctx, tc.keys)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				var de *errs.DomainError
				if ok := errors.As(err, &de); ok {
					assert.NotEmpty(t, de.Code,
						"error code must not be empty; got message: %s", de.Message)
				}
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GetPubkeys() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	// Context cancellation: the []*SSHKeyItem path does not use ctx (it
	// reads files directly). Cancellation should not affect this path.
	t.Run("context_cancelled_sshkey_items", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()

		repo := testutil.NewKeyRepo()
		svc := key.NewService(repo, filepath.Dir(key1Path))

		got, err := svc.GetPubkeys(cctx, []*model.SSHKeyItem{key1, key2})
		require.NoError(t, err)
		if diff := cmp.Diff([]string{key1Content, key2Content}, got); diff != "" {
			t.Errorf("GetPubkeys() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("context_cancelled_string_slice", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()

		repo := testutil.NewKeyRepo()
		require.NoError(t, repo.Upsert(ctx, key1))
		require.NoError(t, repo.Upsert(ctx, key2))
		svc := key.NewService(repo, filepath.Dir(key1Path))

		got, err := svc.GetPubkeys(cctx, []string{"key-one", "key-two"})
		// With in-memory repo the context is not checked, so this
		// succeeds. With a real SQLite repo this would error.
		require.NoError(t, err)
		if diff := cmp.Diff([]string{key1Content, key2Content}, got); diff != "" {
			t.Errorf("GetPubkeys() mismatch (-want +got):\n%s", diff)
		}
	})
}
