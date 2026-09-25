package network_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- Helpers ---

// assertCode checks that err is a DomainError with the given code.
func assertCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var de *errs.DomainError
	if errors.As(err, &de) {
		if diff := cmp.Diff(code, de.Code); diff != "" {
			t.Errorf("DomainError.Code mismatch (-want +got):\n%s", diff)
		}
	} else {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}

// newNet helper creates a minimal Network for tests.
// IsPresent=true and DeletedAt=nil to pass the in-memory repo's isNotDeleted filter.
func newNet(id, name, subnet, gateway string) *model.NetworkItem {
	return &model.NetworkItem{
		ID:          id,
		Name:        name,
		Subnet:      subnet,
		IPv4Gateway: gateway,
		IsPresent:   true,
		CreatedAt:   "2024-01-01T00:00:00Z",
	}
}

func strPtr(s string) *string { return &s }

// --- ComputeBridgeAddress ---
// Rationale: CIDR parsing and gateway formatting. Invalid subnets must error.

func TestComputeBridgeAddress(t *testing.T) {
	tests := []struct {
		name    string
		gateway string
		subnet  string
		want    string
		wantErr bool
	}{
		{
			name:    "valid_24",
			gateway: "10.0.0.1",
			subnet:  "10.0.0.0/24",
			want:    "10.0.0.1/24",
		},
		{
			name:    "valid_16",
			gateway: "192.168.1.1",
			subnet:  "192.168.0.0/16",
			want:    "192.168.1.1/16",
		},
		{
			name:    "valid_32",
			gateway: "10.0.0.5",
			subnet:  "10.0.0.5/32",
			want:    "10.0.0.5/32",
		},
		{
			name:    "invalid_subnet",
			gateway: "10.0.0.1",
			subnet:  "not-a-cidr",
			wantErr: true,
		},
		{
			name:    "empty_subnet",
			gateway: "10.0.0.1",
			subnet:  "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := network.ComputeBridgeAddress(tt.gateway, tt.subnet)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- ComputeBridgeName ---
// Rationale: Must produce valid 15-char Linux bridge names. Short names stay
// raw; long names use truncation + hash to fit within 15 chars.

func TestComputeBridgeName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short_name",
			input: "default",
			want:  "mvm-default",
		},
		{
			name:  "single_char",
			input: "a",
			want:  "mvm-a",
		},
		{
			name:  "empty_name",
			input: "",
			want:  "mvm-",
		},
		{
			name:  "fits_exactly",
			input: "abcdefghij", // mvm-abcdefghij = 14 chars, under 15 limit
			want:  "mvm-abcdefghij",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := network.ComputeBridgeName(tt.input)
			assert.LessOrEqual(t, len(got), 15, "bridge name must be ≤ 15 chars")
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("long_name_truncates", func(t *testing.T) {
		got := network.ComputeBridgeName("this-is-a-very-long-network-name")
		assert.LessOrEqual(t, len(got), 15, "bridge name must be ≤ 15 chars for long inputs")
		assert.True(t, strings.HasPrefix(got, "mvm-"),
			"expected prefix mvm-, got %s", got)
		// Should contain a hash portion (8 hex chars after the name prefix)
		assert.Regexp(t, `^mvm-[a-z0-9]+-[a-f0-9]{8}$`, got,
			"long bridge name must follow pattern mvm-<truncated>-<8hex>")
	})

	t.Run("deterministic", func(t *testing.T) {
		a := network.ComputeBridgeName("some-network-name-that-is-long")
		b := network.ComputeBridgeName("some-network-name-that-is-long")
		assert.Equal(t, a, b)
	})

	t.Run("different_inputs_different_outputs", func(t *testing.T) {
		a := network.ComputeBridgeName("network-with-a-long-name-1")
		b := network.ComputeBridgeName("network-with-a-long-name-2")
		assert.NotEqual(t, a, b, "different network names must produce different bridge names")
	})
}

// --- GenerateTAPName ---
// Rationale: Deterministic hash-based TAP naming. Same inputs = same output.

func TestGenerateTAPName(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := network.GenerateTAPName("default", "vm-1")
		b := network.GenerateTAPName("default", "vm-1")
		assert.Equal(t, a, b)
	})

	t.Run("different_vm_different_tap", func(t *testing.T) {
		a := network.GenerateTAPName("default", "vm-1")
		b := network.GenerateTAPName("default", "vm-2")
		assert.NotEqual(t, a, b)
	})

	t.Run("different_network_different_tap", func(t *testing.T) {
		a := network.GenerateTAPName("net-a", "vm-1")
		b := network.GenerateTAPName("net-b", "vm-1")
		assert.NotEqual(t, a, b)
	})

	t.Run("starts_with_mvm_prefix", func(t *testing.T) {
		got := network.GenerateTAPName("default", "vm-1")
		assert.True(t, strings.HasPrefix(got, "mvm-"),
			"expected prefix mvm-, got %s", got)
	})

	t.Run("hash_is_hex", func(t *testing.T) {
		got := network.GenerateTAPName("default", "vm-1")
		// Pattern: mvm-<11 hex chars>
		assert.Regexp(t, `^mvm-[a-f0-9]{11}$`, got)
	})
}

// --- Controller.SetDefault ---
// Rationale: Nil network must error; valid network delegates to repo.

func TestController_SetDefault(t *testing.T) {
	t.Run("nil_network_errors", func(t *testing.T) {
		repo := testutil.NewNetworkRepo()
		ctrl := network.NewController(nil, repo)
		err := ctrl.SetDefault(context.Background())
		require.Error(t, err)
		assertCode(t, err, errs.CodeNetworkNotFound)
	})

	t.Run("valid_network_sets_default", func(t *testing.T) {
		repo := testutil.NewNetworkRepo()
		net := newNet("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		require.NoError(t, repo.Upsert(context.Background(), net))

		ctrl := network.NewController(net, repo)
		err := ctrl.SetDefault(context.Background())
		require.NoError(t, err)

		got, err := repo.GetDefault(context.Background())
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "n-1", got.ID)
		assert.True(t, got.IsDefault)
	})

	t.Run("default_clears_previous", func(t *testing.T) {
		repo := testutil.NewNetworkRepo()
		net1 := newNet("n-1", "primary", "10.0.0.0/24", "10.0.0.1")
		net2 := newNet("n-2", "secondary", "10.0.1.0/24", "10.0.1.1")
		require.NoError(t, repo.Upsert(context.Background(), net1))
		require.NoError(t, repo.Upsert(context.Background(), net2))

		ctrl1 := network.NewController(net1, repo)
		require.NoError(t, ctrl1.SetDefault(context.Background()))

		ctrl2 := network.NewController(net2, repo)
		require.NoError(t, ctrl2.SetDefault(context.Background()))

		got, err := repo.GetDefault(context.Background())
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "n-2", got.ID)

		n1, _ := repo.Get(context.Background(), "n-1")
		require.NotNil(t, n1)
		assert.False(t, n1.IsDefault)
	})
}

// --- Service.EnrichWithLeases ---
// Rationale: Batch-load leases from leaseRepo and attach to networks. Networks
// without leases must get an empty slice (not nil) to prevent nil dereference
// in callers.

func TestService_EnrichWithLeases(t *testing.T) {
	ctx := context.Background()

	t.Run("attaches_leases_to_networks", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		svc := network.NewService(testutil.NewNetworkRepo(), nil)

		net1 := newNet("n-1", "alpha", "10.0.0.0/24", "10.0.0.1")
		net2 := newNet("n-2", "beta", "10.0.1.0/24", "10.0.1.1")

		l1, err := leaseRepo.Acquire(ctx, "n-1", "10.0.0.2", strPtr("vm-1"))
		require.NoError(t, err)
		l2, err := leaseRepo.Acquire(ctx, "n-1", "10.0.0.3", strPtr("vm-2"))
		require.NoError(t, err)

		networks := []*model.NetworkItem{net1, net2}
		err = svc.EnrichWithLeases(ctx, networks, leaseRepo)
		require.NoError(t, err)

		require.Len(t, net1.Leases, 2)
		assert.Equal(t, l1.IPv4, net1.Leases[0].IPv4)
		assert.Equal(t, l2.IPv4, net1.Leases[1].IPv4)

		require.NotNil(t, net2.Leases, "networks without leases must get empty slice, not nil")
		assert.Empty(t, net2.Leases)
	})

	t.Run("empty_networks_no_crash", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		svc := network.NewService(testutil.NewNetworkRepo(), nil)

		err := svc.EnrichWithLeases(ctx, []*model.NetworkItem{}, leaseRepo)
		require.NoError(t, err)
	})

	t.Run("network_with_no_leases_gets_empty_slice", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		svc := network.NewService(testutil.NewNetworkRepo(), nil)

		net := newNet("n-1", "alpha", "10.0.0.0/24", "10.0.0.1")
		err := svc.EnrichWithLeases(ctx, []*model.NetworkItem{net}, leaseRepo)
		require.NoError(t, err)
		require.NotNil(t, net.Leases, "must be empty slice, not nil")
		assert.Empty(t, net.Leases)
	})
}
