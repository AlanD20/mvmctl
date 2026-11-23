package network_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

func TestPrimaryLeaseSkipsFloatingIPAfterConcurrentPrimaryCollision(t *testing.T) {
	repo := &retryLeaseRepo{LeaseRepo: *testutil.NewLeaseRepo(), collideIP: "10.0.0.2"}
	item := newNetWithLeases("n-1", "qa", "10.0.0.0/29", "10.0.0.1")
	controller, err := network.NewLeaseController(t.Context(), item, repo, nil)
	require.NoError(t, err)
	address, err := controller.LeaseExcluding(t.Context(), "vm-1", []string{"10.0.0.3"})
	require.NoError(t, err)
	if address != "10.0.0.4" {
		t.Fatalf(
			"allocated primary = %q, want first address after gateway, concurrent primary and floating reservation",
			address,
		)
	}
	lease, err := repo.Get(t.Context(), "n-1", "10.0.0.4")
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NotNil(t, lease.VMID)
	if *lease.VMID != "vm-1" {
		t.Fatalf("primary lease owner = %q, want vm-1", *lease.VMID)
	}
}

func TestSpecificPrimaryLeaseCannotAcknowledgeConcurrentWinnersAddress(t *testing.T) {
	repo := &retryLeaseRepo{LeaseRepo: *testutil.NewLeaseRepo(), collideIP: "10.0.0.2"}
	item := newNetWithLeases("n-1", "qa", "10.0.0.0/29", "10.0.0.1")
	controller, err := network.NewLeaseController(t.Context(), item, repo, nil)
	require.NoError(t, err)
	address, err := controller.LeaseSpecific(t.Context(), "10.0.0.2", "loser")
	require.Error(t, err)
	domain := errs.AsDomainError(err)
	require.NotNil(t, domain)
	if domain.Class != errs.ClassConflict || address != "" {
		t.Fatalf("losing admission = (%q, %v), want empty address and conflict", address, err)
	}
	winner, err := repo.Get(t.Context(), "n-1", "10.0.0.2")
	require.NoError(t, err)
	require.NotNil(t, winner)
	require.NotNil(t, winner.VMID)
	if *winner.VMID != "other-process" {
		t.Fatalf("concurrent winner changed to %q", *winner.VMID)
	}
}
