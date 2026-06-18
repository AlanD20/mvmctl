package network_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// --- Custom LeaseRepository wrappers ---

// retryLeaseRepo wraps testutil.LeaseRepo to simulate a concurrent lease
// acquisition on a specific IP. On first Acquire for collideIP, it creates the
// lease in the underlying repo (simulating another process) but returns nil
// (simulating INSERT OR IGNORE returning 0 rows). On subsequent calls it
// delegates normally. This tests that Lease() retries with the next IP.
type retryLeaseRepo struct {
	testutil.LeaseRepo
	mu         sync.Mutex
	collideIP  string
	hasCollide bool
}

func (r *retryLeaseRepo) Acquire(
	ctx context.Context,
	networkID, ipv4 string,
	vmID *string,
) (*model.NetworkLeaseItem, error) {
	r.mu.Lock()
	colliding := ipv4 == r.collideIP && !r.hasCollide
	if colliding {
		r.hasCollide = true
		r.mu.Unlock()
		// Simulate another process acquiring this IP — create the lease in the repo.
		otherVM := "other-process"
		_, err := r.LeaseRepo.Acquire(ctx, networkID, ipv4, &otherVM)
		if err != nil {
			return nil, err
		}
		return nil, nil // Return nil — our INSERT OR IGNORE got 0 rows.
	}
	r.mu.Unlock()
	return r.LeaseRepo.Acquire(ctx, networkID, ipv4, vmID)
}

// collideAllRepo wraps testutil.LeaseRepo where Acquire always returns nil
// (simulating persistent collision). Used to test max retries exhaustion.
type collideAllRepo struct {
	testutil.LeaseRepo
}

func (r *collideAllRepo) Acquire(
	_ context.Context,
	_ string, _ string, _ *string,
) (*model.NetworkLeaseItem, error) {
	return nil, nil // Always collide
}

// --- Helpers ---

func newNetWithLeases(id, name, subnet, gateway string) *model.NetworkItem {
	return &model.NetworkItem{
		ID:          id,
		Name:        name,
		Subnet:      subnet,
		IPv4Gateway: gateway,
		IsPresent:   true,
		CreatedAt:   "2024-01-01T00:00:00Z",
	}
}

// --- NewLeaseController ---
// Rationale: Must accept *model.NetworkItem, resolve string via repo, error on
// string without repo, and reject invalid types.

func TestNewLeaseController(t *testing.T) {
	ctx := context.Background()

	t.Run("from_network_entity", func(t *testing.T) {
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, testutil.NewLeaseRepo(), nil)
		require.NoError(t, err)
		require.NotNil(t, lc)
		assert.Equal(t, "n-1", lc.NetworkID())
		assert.Equal(t, "test", lc.NetworkName())
	})

	t.Run("from_string_with_repo", func(t *testing.T) {
		netRepo := testutil.NewNetworkRepo()
		net := newNetWithLeases("n-1", "resolve-me", "10.0.0.0/24", "10.0.0.1")
		require.NoError(t, netRepo.Upsert(ctx, net))

		lc, err := network.NewLeaseController(ctx, "resolve-me", testutil.NewLeaseRepo(), netRepo)
		require.NoError(t, err)
		require.NotNil(t, lc)
		assert.Equal(t, "n-1", lc.NetworkID())
		assert.Equal(t, "resolve-me", lc.NetworkName())
	})

	t.Run("from_string_without_repo_errors", func(t *testing.T) {
		lc, err := network.NewLeaseController(ctx, "nonexistent", testutil.NewLeaseRepo(), nil)
		require.Error(t, err)
		assert.Nil(t, lc)
		assertCode(t, err, errs.CodeNetworkNotFound)
		assert.Contains(t, err.Error(), "no network repository provided")
	})

	t.Run("invalid_type_errors", func(t *testing.T) {
		lc, err := network.NewLeaseController(ctx, 42, testutil.NewLeaseRepo(), nil)
		require.Error(t, err)
		assert.Nil(t, lc)
		assert.Contains(t, err.Error(), "expected *model.NetworkItem or string")
	})

	t.Run("string_not_found_errors", func(t *testing.T) {
		netRepo := testutil.NewNetworkRepo()
		lc, err := network.NewLeaseController(ctx, "does-not-exist", testutil.NewLeaseRepo(), netRepo)
		require.Error(t, err)
		assert.Nil(t, lc)
	})
}

// --- LeaseController CRUD wrappers ---
// Rationale: Thin wrappers around leaseRepo — verify delegation works.

func TestLeaseController_CRUDWrappers(t *testing.T) {
	ctx := context.Background()
	leaseRepo := testutil.NewLeaseRepo()
	net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")

	lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
	require.NoError(t, err)

	t.Run("GetLeases_empty", func(t *testing.T) {
		leases, err := lc.GetLeases(ctx)
		require.NoError(t, err)
		assert.Empty(t, leases)
	})

	t.Run("Get_not_found", func(t *testing.T) {
		lease, err := lc.Get(ctx, "10.0.0.99")
		require.NoError(t, err)
		assert.Nil(t, lease)
	})

	// Add a lease for subsequent tests
	_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.2", strPtr("vm-1"))
	require.NoError(t, err)

	t.Run("GetLeases_one", func(t *testing.T) {
		leases, err := lc.GetLeases(ctx)
		require.NoError(t, err)
		assert.Len(t, leases, 1)
		assert.Equal(t, "10.0.0.2", leases[0].IPv4)
	})

	t.Run("Get_found", func(t *testing.T) {
		lease, err := lc.Get(ctx, "10.0.0.2")
		require.NoError(t, err)
		require.NotNil(t, lease)
		assert.Equal(t, "10.0.0.2", lease.IPv4)
		assert.Equal(t, "vm-1", *lease.VMID)
	})

	t.Run("GetByVMID", func(t *testing.T) {
		leases, err := lc.GetByVMID(ctx, "vm-1")
		require.NoError(t, err)
		assert.Len(t, leases, 1)
		assert.Equal(t, "10.0.0.2", leases[0].IPv4)
	})

	t.Run("GetByVMID_not_found", func(t *testing.T) {
		leases, err := lc.GetByVMID(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, leases)
	})
}

// --- IsAvailable ---
// Rationale: Must return true for unleased IPs, false for taken ones.

func TestLeaseController_IsAvailable(t *testing.T) {
	ctx := context.Background()
	leaseRepo := testutil.NewLeaseRepo()
	net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")

	lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
	require.NoError(t, err)

	t.Run("available_when_not_leased", func(t *testing.T) {
		avail, err := lc.IsAvailable(ctx, "10.0.0.99")
		require.NoError(t, err)
		assert.True(t, avail)
	})

	_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.2", strPtr("vm-1"))
	require.NoError(t, err)

	t.Run("not_available_when_leased", func(t *testing.T) {
		avail, err := lc.IsAvailable(ctx, "10.0.0.2")
		require.NoError(t, err)
		assert.False(t, avail)
	})
}

// --- Lease ---
// Rationale: Core IP allocation logic with retry loop, collision handling, and
// exhaustion detection.

func TestLeaseController_Lease(t *testing.T) {
	ctx := context.Background()

	t.Run("first_available_ip", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		ip, err := lc.Lease(ctx, "vm-1")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.2", ip, "first available IP after gateway")
	})

	t.Run("skips_existing_leases", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		// Pre-occupy .2
		_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.2", strPtr("other-vm"))
		require.NoError(t, err)

		ip, err := lc.Lease(ctx, "vm-1")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.3", ip, "should skip already-allocated .2")
	})

	t.Run("subnet_exhaustion", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		leaseRepo.SetNetwork(2, true) // /30 = 2 hosts minus gateway = 1 usable
		// /30: total=4, start=1, end=3 → .1, .2. Gateway=.1, usable=.2
		net := newNetWithLeases("n-1", "test", "10.0.0.0/30", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		// Fill the only available IP
		_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.2", strPtr("vm-1"))
		require.NoError(t, err)

		_, err = lc.Lease(ctx, "vm-2")
		require.Error(t, err)
		assertCode(t, err, errs.CodeNetworkLeaseExhausted)
	})

	t.Run("retry_on_collision_then_succeeds", func(t *testing.T) {
		// /29: total=8, start=1, end=7 → .1..6, gateway=.1, usable=.2,.3,.4,.5,.6
		// First attempt gets .2 but collides (another process grabbed it).
		// Retry with .2 taken → gets .3.
		leaseRepo := &retryLeaseRepo{
			LeaseRepo: *testutil.NewLeaseRepo(),
			collideIP: "10.0.0.2",
		}
		net := newNetWithLeases("n-1", "test", "10.0.0.0/29", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		ip, err := lc.Lease(ctx, "vm-1")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.3", ip, "after collision on .2, should get .3")
	})

	t.Run("max_retries_exhausted", func(t *testing.T) {
		// Acquire always returns nil → retry 10 times with same IP → exhaustion.
		leaseRepo := &collideAllRepo{LeaseRepo: *testutil.NewLeaseRepo()}
		// /30: only .2 available (after gateway .1)
		net := newNetWithLeases("n-1", "test", "10.0.0.0/30", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		_, err = lc.Lease(ctx, "vm-1")
		require.Error(t, err)
		assertCode(t, err, errs.CodeNetworkError)
		assert.Contains(t, err.Error(), "Failed to allocate IP after 10 attempts")
	})
}

// --- LeaseSpecific ---
// Rationale: Allocate a specific IP. Must fail if already leased.

func TestLeaseController_LeaseSpecific(t *testing.T) {
	ctx := context.Background()

	t.Run("available_ip_succeeds", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		ip, err := lc.LeaseSpecific(ctx, "10.0.0.42", "vm-1")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.42", ip)
	})

	t.Run("already_leased_errors", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		// Pre-occupy the IP
		_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.42", strPtr("other-vm"))
		require.NoError(t, err)

		_, err = lc.LeaseSpecific(ctx, "10.0.0.42", "vm-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "IP 10.0.0.42 is already leased")
	})

	t.Run("allows_leasing_different_ip_after_one_taken", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.42", strPtr("other-vm"))
		require.NoError(t, err)

		ip, err := lc.LeaseSpecific(ctx, "10.0.0.99", "vm-1")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.99", ip)
	})
}

// --- Release ---
// Rationale: Releasing a VM's leases must remove them from the repo.

func TestLeaseController_Release(t *testing.T) {
	ctx := context.Background()

	t.Run("releases_vm_leases", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.2", strPtr("vm-1"))
		require.NoError(t, err)
		_, err = leaseRepo.Acquire(ctx, "n-1", "10.0.0.3", strPtr("vm-1"))
		require.NoError(t, err)

		err = lc.Release(ctx, "vm-1")
		require.NoError(t, err)

		leases, err := lc.GetLeases(ctx)
		require.NoError(t, err)
		assert.Empty(t, leases)
	})

	t.Run("release_nonexistent_vm_is_noop", func(t *testing.T) {
		leaseRepo := testutil.NewLeaseRepo()
		net := newNetWithLeases("n-1", "test", "10.0.0.0/24", "10.0.0.1")
		lc, err := network.NewLeaseController(ctx, net, leaseRepo, nil)
		require.NoError(t, err)

		err = lc.Release(ctx, "nonexistent")
		require.NoError(t, err)
	})
}

// --- Ensure retryLeaseRepo and collideAllRepo implemen ---

var (
	_ network.LeaseRepository = (*retryLeaseRepo)(nil)
	_ network.LeaseRepository = (*collideAllRepo)(nil)
)
