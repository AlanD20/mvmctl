package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

// ─── NewKeyCmd ────────────────────────────────────────────────────────────────
// Rationale: NewKeyCmd is the entry point for all SSH key CLI operations.
// Missing subcommands silently disable key management without error.

func TestNewKeyCmd(t *testing.T) {
	mock := &testutil.MockKeyAPI{}
	cmd := cli.NewKeyCmd(mock, nil)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "ls", hasAlias: true, alias: "list"},
		{use: "create", hasAlias: false},
		{use: "import", hasAlias: false},
		{use: "rm", hasAlias: true, alias: "remove"},
		{use: "inspect", hasAlias: false},
		{use: "export", hasAlias: false},
		{use: "default", hasAlias: false},
	}

	assert.Equal(t, "key", cmd.Use, "root command must be 'key'")
	assert.Equal(t, "SSH key management", cmd.Short)

	for _, sc := range expectedSubcommands {
		t.Run("has_subcommand_"+sc.use, func(t *testing.T) {
			sub, _, err := cmd.Find([]string{sc.use})
			require.NoError(t, err, "subcommand %q not found", sc.use)
			require.NotNil(t, sub, "subcommand %q is nil", sc.use)

			if sc.hasAlias {
				aliasCmd, _, aliasErr := cmd.Find([]string{sc.alias})
				require.NoError(t, aliasErr, "alias %q not found for %q", sc.alias, sc.use)
				require.NotNil(t, aliasCmd, "alias %q is nil for %q", sc.alias, sc.use)
			}
		})
	}

	t.Run("no_extra_subcommands", func(t *testing.T) {
		expected := make(map[string]bool)
		for _, sc := range expectedSubcommands {
			expected[sc.use] = true
			if sc.hasAlias {
				expected[sc.alias] = true
			}
		}
		for _, sub := range cmd.Commands() {
			assert.True(t, expected[sub.Name()], "unexpected subcommand: %s", sub.Name())
		}
	})
}

// ─── key ls (via key ls) ──────────────────────────────────────────────────────
// Rationale: SSH key listing is how users see their registered keys.
// A broken list command prevents users from selecting keys for VM SSH access.

func TestNewKeyListCmd(t *testing.T) {
	t.Run("empty_list_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{}
		cmd := cli.NewKeyCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("single_key_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyListAllFunc: func(ctx context.Context) ([]*model.SSHKeyItem, error) {
				return []*model.SSHKeyItem{
					{ID: "key-1", Name: "my-key", Algorithm: "ed25519", Fingerprint: "SHA256:abc123"},
				}, nil
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("multiple_keys_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyListAllFunc: func(ctx context.Context) ([]*model.SSHKeyItem, error) {
				return []*model.SSHKeyItem{
					{ID: "key-1", Name: "my-key", Algorithm: "ed25519", Fingerprint: "SHA256:abc123"},
					{ID: "key-2", Name: "work-key", Algorithm: "rsa", Fingerprint: "SHA256:def456"},
				}, nil
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyListAllFunc: func(ctx context.Context) ([]*model.SSHKeyItem, error) {
				return []*model.SSHKeyItem{
					{ID: "key-1", Name: "my-key", Algorithm: "ed25519"},
				}, nil
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyListAllFunc: func(ctx context.Context) ([]*model.SSHKeyItem, error) {
				return nil, errors.New("database connection failed")
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database connection failed")
	})

	t.Run("context_is_propagated_to_keyapi", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelled := false
		mock := &testutil.MockKeyAPI{
			KeyListAllFunc: func(ctx context.Context) ([]*model.SSHKeyItem, error) {
				if ctx.Err() != nil {
					cancelled = true
				}
				return nil, nil
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err, "key list should not error on cancelled context — list is read-only")
		assert.True(t, cancelled, "mock KeyListAllFunc should have received the cancelled context")
	})
}

// ─── key create (via key create) ──────────────────────────────────────────────
// Rationale: Key create is the mechanism for generating new SSH keypairs.
// A broken create command prevents users from setting up SSH access to VMs.

func TestNewKeyCreateCmd(t *testing.T) {
	t.Run("success_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyCreateFunc: func(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
				assert.Equal(t, "my-key", input.Name)
				assert.Equal(t, "ed25519", input.Algorithm)
				return &model.SSHKeyItem{
					ID:          "key-1",
					Name:        "my-key",
					Fingerprint: "SHA256:abc",
					Algorithm:   "ed25519",
				}, nil
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("algorithm", "ed25519")
		err := createCmd.RunE(createCmd, []string{"my-key"})
		assert.NoError(t, err)
	})

	t.Run("with_rsa_bits_success", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyCreateFunc: func(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
				assert.Equal(t, "rsa", input.Algorithm)
				assert.Equal(t, 4096, input.Bits)
				return &model.SSHKeyItem{ID: "key-1", Name: "rsa-key", Algorithm: "rsa"}, nil
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("algorithm", "rsa")
		createCmd.Flags().Set("bits", "4096")
		err := createCmd.RunE(createCmd, []string{"rsa-key"})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockKeyAPI{
			KeyCreateFunc: func(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
				return nil, errors.New("key generation failed")
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("algorithm", "ed25519")
		err := createCmd.RunE(createCmd, []string{"my-key"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "key generation failed")
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockKeyAPI{
			KeyCreateFunc: func(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return &model.SSHKeyItem{ID: "key-1"}, nil
				}
			},
		}
		cmd := cli.NewKeyCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.SetContext(ctx)
		createCmd.Flags().Set("algorithm", "ed25519")
		err := createCmd.RunE(createCmd, []string{"my-key"})
		require.Error(t, err)
	})
}
