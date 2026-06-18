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
	libnet "mvmctl/internal/lib/network"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- Helpers -----------------------------------------------------------------

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
func newNet(id, name, subnet, gateway string) *model.Network {
	return &model.Network{
		ID:          id,
		Name:        name,
		Subnet:      subnet,
		IPv4Gateway: gateway,
		IsPresent:   true,
		CreatedAt:   "2024-01-01T00:00:00Z",
	}
}

func strPtr(s string) *string { return &s }

// --- ComputeBridgeAddress -----------------------------------------------------
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

// --- ComputeBridgeName --------------------------------------------------------
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

// --- GenerateTAPName ----------------------------------------------------------
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

// --- NatGatewaysList ----------------------------------------------------------
// Rationale: Falsy values ("0", "false", "None") must be treated as
// empty. Whitespace trimming and empty-skipping must work for multi-value.

func TestNatGatewaysList(t *testing.T) {
	tests := []struct {
		name     string
		gateways *string
		want     []string
	}{
		{
			name:     "nil_returns_empty",
			gateways: nil,
			want:     []string{},
		},
		{
			name:     "empty_string_returns_empty",
			gateways: strPtr(""),
			want:     []string{},
		},
		{
			name:     "zero_string_returns_empty",
			gateways: strPtr("0"),
			want:     []string{},
		},
		{
			name:     "false_lowercase_returns_empty",
			gateways: strPtr("false"),
			want:     []string{},
		},
		{
			name:     "False_capitalized_returns_empty",
			gateways: strPtr("False"),
			want:     []string{},
		},
		{
			name:     "none_lowercase_returns_empty",
			gateways: strPtr("none"),
			want:     []string{},
		},
		{
			name:     "None_capitalized_returns_empty",
			gateways: strPtr("None"),
			want:     []string{},
		},
		{
			name:     "single_gateway",
			gateways: strPtr("eth0"),
			want:     []string{"eth0"},
		},
		{
			name:     "multiple_gateways",
			gateways: strPtr("eth0,wlan0,enp0s3"),
			want:     []string{"eth0", "wlan0", "enp0s3"},
		},
		{
			name:     "whitespace_trimmed",
			gateways: strPtr(" eth0 , wlan0 "),
			want:     []string{"eth0", "wlan0"},
		},
		{
			name:     "empty_inner_parts_skipped",
			gateways: strPtr("eth0,,wlan0"),
			want:     []string{"eth0", "wlan0"},
		},
		{
			name:     "trailing_comma",
			gateways: strPtr("eth0,"),
			want:     []string{"eth0"},
		},
		{
			name:     "leading_comma",
			gateways: strPtr(",eth0"),
			want:     []string{"eth0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &model.Network{NATGateways: tt.gateways}
			got := network.NatGatewaysList(n)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Controller.SetDefault ----------------------------------------------------
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

// --- Service.EnrichWithLeases -------------------------------------------------
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

		networks := []*model.Network{net1, net2}
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

		err := svc.EnrichWithLeases(ctx, []*model.Network{}, leaseRepo)
		require.NoError(t, err)
	})

	t.Run("network_with_no_leases_gets_empty_slice", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		svc := network.NewService(testutil.NewNetworkRepo(), nil)

		net := newNet("n-1", "alpha", "10.0.0.0/24", "10.0.0.1")
		err := svc.EnrichWithLeases(ctx, []*model.Network{net}, leaseRepo)
		require.NoError(t, err)
		require.NotNil(t, net.Leases, "must be empty slice, not nil")
		assert.Empty(t, net.Leases)
	})
}

// --- Service.ListAll (no verify) ----------------------------------------------
// Rationale: Without verify=true, ListAll is a passthrough to repo.ListAll.
// With verify=true it calls libnet.BridgeExists (system ops) — skipped here.

func TestService_ListAll_noVerify(t *testing.T) {
	ctx := context.Background()
	netRepo := testutil.NewNetworkRepo()
	svc := network.NewService(netRepo, nil)

	n1 := newNet("n-1", "alpha", "10.0.0.0/24", "10.0.0.1")
	n2 := newNet("n-2", "beta", "10.0.1.0/24", "10.0.1.1")
	require.NoError(t, netRepo.Upsert(ctx, n1))
	require.NoError(t, netRepo.Upsert(ctx, n2))

	got, err := svc.ListAll(ctx, false)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// --- Service.WithBatch and Initialize (nil tracker) ---------------------------
// Rationale: When firewall tracker is nil, these must be no-ops (not panic).

func TestService_TrackerNilIsNoop(t *testing.T) {
	svc := network.NewService(testutil.NewNetworkRepo(), nil)

	assert.Nil(t, svc.FirewallTracker())

	assert.NoError(t, svc.Initialize(context.Background()))
	assert.NoError(t, svc.Teardown(context.Background()))
	assert.NoError(t, svc.EnsureMVMChains(context.Background()))

	// WithBatch should still execute the function
	called := false
	svc.WithBatch(context.Background(), func() {
		called = true
	})
	assert.True(t, called)
}

// --- Service.EnsureBridge -----------------------------------------------------
// Rationale: Must create bridge when absent, or reconcile when present.

func TestService_EnsureBridge(t *testing.T) {
	ctx := context.Background()

	t.Run("creates_new_bridge", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var ran []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			BridgeExistsFn: func(_ context.Context, _ string) bool { return false },
			RunBatchFn: func(_ context.Context, cmds []string) error {
				ran = append(ran, cmds...)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureBridge(ctx, "mvm-br0", "10.0.0.1/24")
		require.NoError(t, err)
		require.Len(t, ran, 3)
		assert.Contains(t, ran[0], "link add name")
		assert.Contains(t, ran[1], "addr add")
		assert.Contains(t, ran[2], "link set")
	})

	t.Run("reconciles_existing_bridge", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var ran []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			BridgeExistsFn:    func(_ context.Context, _ string) bool { return true },
			BridgeHasSubnetFn: func(_ context.Context, _, _ string) bool { return false },
			RunBatchFn: func(_ context.Context, cmds []string) error {
				ran = append(ran, cmds...)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureBridge(ctx, "mvm-br0", "10.0.0.1/24")
		require.NoError(t, err)
		// Reconcile adds subnet then sets link up
		require.Len(t, ran, 2)
		assert.Contains(t, ran[0], "addr add")
		assert.Contains(t, ran[1], "link set")
	})

	t.Run("reconcile_skips_subnet_when_present", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var ran []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			BridgeExistsFn:    func(_ context.Context, _ string) bool { return true },
			BridgeHasSubnetFn: func(_ context.Context, _, _ string) bool { return true },
			RunBatchFn: func(_ context.Context, cmds []string) error {
				ran = append(ran, cmds...)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureBridge(ctx, "mvm-br0", "10.0.0.1/24")
		require.NoError(t, err)
		// Only one command (link set up) since subnet already present
		require.Len(t, ran, 1)
		assert.Contains(t, ran[0], "link set")
	})

	t.Run("batch_failure_errors", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		libnet.DefaultNetOps = &testutil.FakeNetOps{
			BridgeExistsFn: func(_ context.Context, _ string) bool { return false },
			RunBatchFn: func(_ context.Context, _ []string) error {
				return errors.New("ip command failed")
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureBridge(ctx, "mvm-br0", "10.0.0.1/24")
		require.Error(t, err)
	})
}

// --- Service.RemoveBridge -----------------------------------------------------
// Rationale: Removes attached TAPs then bridge.

func TestService_RemoveBridge(t *testing.T) {
	ctx := context.Background()

	t.Run("removes_taps_then_bridge", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var removedTaps []string
		var removedBridge string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			GetBridgeTapsFn: func(_ context.Context, _ string) []string {
				return []string{"tap-1", "tap-2"}
			},
			TapExistsFn:    func(_ context.Context, _ string) bool { return true },
			GetTapBridgeFn: func(_ context.Context, _ string) string { return "mvm-br0" },
			RemoveRawTapFn: func(_ context.Context, tap string) error {
				removedTaps = append(removedTaps, tap)
				return nil
			},
			RemoveRawBridgeFn: func(_ context.Context, bridge string) error {
				removedBridge = bridge
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.RemoveBridge(ctx, "mvm-br0", "net-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"tap-1", "tap-2"}, removedTaps)
		assert.Equal(t, "mvm-br0", removedBridge)
	})
}

// --- Service.EnsureTap --------------------------------------------------------
// Rationale: Creates new TAP, reattaches to same bridge, or reattaches to
// different bridge.

func TestService_EnsureTap(t *testing.T) {
	ctx := context.Background()

	t.Run("creates_new_tap", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var ran []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			TapExistsFn:    func(_ context.Context, _ string) bool { return false },
			GetTapBridgeFn: func(_ context.Context, _ string) string { return "" },
			RunBatchFn: func(_ context.Context, cmds []string) error {
				ran = append(ran, cmds...)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureTapDevice(ctx, "tap-test", "mvm-br0")
		require.NoError(t, err)
		require.Len(t, ran, 3)
		assert.Contains(t, ran[0], "tuntap add")
		assert.Contains(t, ran[1], "master")
		assert.Contains(t, ran[2], "up")
	})

	t.Run("tap_exists_on_same_bridge_is_noop", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var ran []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			TapExistsFn:    func(_ context.Context, _ string) bool { return true },
			GetTapBridgeFn: func(_ context.Context, _ string) string { return "mvm-br0" },
			RunBatchFn: func(_ context.Context, cmds []string) error {
				ran = append(ran, cmds...)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureTapDevice(ctx, "tap-test", "mvm-br0")
		require.NoError(t, err)
		assert.Empty(t, ran, "no ip commands when TAP already on correct bridge")
	})

	t.Run("reattaches_to_different_bridge", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var ran []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			TapExistsFn:    func(_ context.Context, _ string) bool { return true },
			GetTapBridgeFn: func(_ context.Context, _ string) string { return "other-bridge" },
			RunBatchFn: func(_ context.Context, cmds []string) error {
				ran = append(ran, cmds...)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.EnsureTapDevice(ctx, "tap-test", "mvm-br0")
		require.NoError(t, err)
		require.Len(t, ran, 3)
		assert.Contains(t, ran[0], "down")
		assert.Contains(t, ran[1], "master mvm-br0")
		assert.Contains(t, ran[2], "up")
	})
}

// --- Service.RemoveTap --------------------------------------------------------
// Rationale: Removes TAP device. Skips if TAP doesn't exist.

func TestService_RemoveTap(t *testing.T) {
	ctx := context.Background()

	t.Run("removes_existing_tap", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var removedTap string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			TapExistsFn:    func(_ context.Context, _ string) bool { return true },
			GetTapBridgeFn: func(_ context.Context, _ string) string { return "mvm-br0" },
			RemoveRawTapFn: func(_ context.Context, tap string) error {
				removedTap = tap
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.RemoveTap(ctx, "tap-test", "mvm-br0", "net-1")
		require.NoError(t, err)
		assert.Equal(t, "tap-test", removedTap)
	})

	t.Run("nonexistent_tap_skips", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		libnet.DefaultNetOps = &testutil.FakeNetOps{
			TapExistsFn: func(_ context.Context, _ string) bool { return false },
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		err := svc.RemoveTap(ctx, "nonexistent", "", "net-1")
		require.NoError(t, err)
	})
}

// --- Service.CleanupOrphanedBridges -------------------------------------------
// Rationale: Removes host bridges not tracked in DB, skipping non-mvm bridges.

func TestService_CleanupOrphanedBridges(t *testing.T) {
	ctx := context.Background()

	t.Run("removes_orphan_mvm_bridges", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var removedBridges []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			GetSystemBridgesFn: func(_ context.Context) []string {
				return []string{"mvm-alpha", "mvm-bravo", "docker0", "virbr0"}
			},
			GetBridgeSlavesFn: func(_ context.Context, _ string) []string { return nil },
			RemoveRawBridgeFn: func(_ context.Context, bridge string) error {
				removedBridges = append(removedBridges, bridge)
				return nil
			},
		}
		netRepo := testutil.NewNetworkRepo()
		// DB only tracks "mvm-alpha" (not "mvm-bravo" — it's orphan)
		n := newNet("n-1", "alpha", "10.0.0.0/24", "10.0.0.1")
		n.Bridge = "mvm-alpha"
		require.NoError(t, netRepo.Upsert(ctx, n))

		svc := network.NewService(netRepo, nil)
		count := svc.CleanupOrphanedBridges(ctx, []*model.Network{n})
		assert.Equal(t, 1, count)
		assert.Equal(t, []string{"mvm-bravo"}, removedBridges)
	})
}

// --- Service.RemoveStaleInterfaces --------------------------------------------
// Rationale: Removes TAPs for bridges matching a prefix.

func TestService_RemoveStaleInterfaces(t *testing.T) {
	ctx := context.Background()

	t.Run("removes_taps_from_matching_bridges", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		var removedTaps []string
		libnet.DefaultNetOps = &testutil.FakeNetOps{
			GetSystemBridgesFn: func(_ context.Context) []string {
				return []string{"mvm-stale", "mvm-keep"}
			},
			GetBridgeSlavesFn: func(_ context.Context, _ string) []string {
				return []string{"tap-stale-1", "tap-stale-2"}
			},
			RemoveRawTapFn: func(_ context.Context, tap string) error {
				removedTaps = append(removedTaps, tap)
				return nil
			},
		}

		svc := network.NewService(testutil.NewNetworkRepo(), nil)
		summary := svc.RemoveStaleInterfaces(ctx, "mvm-stale")
		assert.Equal(t, 2, len(removedTaps))
		assert.Contains(t, summary[0], "Removed")
	})
}

// --- Service.ListAll with verify=true -----------------------------------------
// Rationale: verify=true checks bridge existence on host.

func TestService_ListAll_verify(t *testing.T) {
	ctx := context.Background()

	t.Run("marks_missing_bridges_as_not_present", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		libnet.DefaultNetOps = &testutil.FakeNetOps{
			BridgeExistsFn: func(_ context.Context, bridge string) bool {
				return bridge != "mvm-gone" // all bridges exist except "mvm-gone"
			},
		}
		netRepo := testutil.NewNetworkRepo()
		n1 := newNet("n-1", "alive", "10.0.0.0/24", "10.0.0.1")
		n1.Bridge = "mvm-alive"
		n2 := newNet("n-2", "gone", "10.0.1.0/24", "10.0.1.1")
		n2.Bridge = "mvm-gone"
		require.NoError(t, netRepo.Upsert(ctx, n1))
		require.NoError(t, netRepo.Upsert(ctx, n2))

		svc := network.NewService(netRepo, nil)
		got, err := svc.ListAll(ctx, true)
		require.NoError(t, err)
		// The alive network should still be listed, gone should not
		assert.Len(t, got, 1)
		assert.Equal(t, "n-1", got[0].ID)
	})

	t.Run("no_missing_bridges_passthrough", func(t *testing.T) {
		orig := libnet.DefaultNetOps
		defer func() { libnet.DefaultNetOps = orig }()

		libnet.DefaultNetOps = &testutil.FakeNetOps{
			BridgeExistsFn: func(_ context.Context, _ string) bool { return true },
		}
		netRepo := testutil.NewNetworkRepo()
		n := newNet("n-1", "alpha", "10.0.0.0/24", "10.0.0.1")
		n.Bridge = "mvm-alpha"
		require.NoError(t, netRepo.Upsert(ctx, n))

		svc := network.NewService(netRepo, nil)
		got, err := svc.ListAll(ctx, true)
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})
}
