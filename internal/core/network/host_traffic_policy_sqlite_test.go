package network_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestHostTrafficProjectionOwnershipAndInvalidation(t *testing.T) {
	ctx := t.Context()
	_, repo := seedTrafficPolicyResources(t)
	vm := &model.VMToHostTrafficAllowIntent{
		ID:                   strings.Repeat("c", 64),
		SourceVMID:           "vm-a",
		Protocol:             model.TrafficProtocolTCP,
		DestinationPortStart: 5432,
		DestinationPortEnd:   5432,
		ClaimID:              "claim-a",
		Incarnation:          "generation-a",
		CreatedAt:            "2026-01-01T00:00:00Z",
		UpdatedAt:            "2026-01-01T00:00:00Z",
	}
	network := &model.NetworkToHostTrafficAllowIntent{
		ID:                   strings.Repeat("d", 64),
		SourceNetworkID:      "net-a",
		Protocol:             model.TrafficProtocolUDP,
		DestinationPortStart: 53,
		DestinationPortEnd:   54,
		ClaimID:              "claim-b",
		Incarnation:          "generation-b",
		CreatedAt:            "2026-01-01T00:00:00Z",
		UpdatedAt:            "2026-01-01T00:00:00Z",
	}
	require.NoError(t, repo.ProjectVMToHost(ctx, vm))
	require.NoError(t, repo.ProjectNetworkToHost(ctx, network))
	storedVM, err := repo.GetVMToHost(ctx, vm.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(vm, storedVM); diff != "" {
		t.Errorf("VM host grant (-want +got):\n%s", diff)
	}
	storedNetwork, err := repo.GetNetworkToHost(ctx, network.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(network, storedNetwork); diff != "" {
		t.Errorf("network host grant (-want +got):\n%s", diff)
	}

	// Rationale: Projection repair can update ownership, but cannot rewrite the source or port behind an ID.
	changed := *vm
	changed.DestinationPortEnd = 5433
	assert.Error(t, repo.ProjectVMToHost(ctx, &changed))
	changed = *vm
	changed.Incarnation = "generation-new"
	require.NoError(t, repo.ProjectVMToHost(ctx, &changed))
	require.NoError(t, repo.DeleteOwnedVMToHost(ctx, vm.ID, vm.ClaimID, vm.Incarnation))
	storedVM, err = repo.GetVMToHost(ctx, vm.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(&changed, storedVM); diff != "" {
		t.Errorf("stale VM cleanup preserved (-want +got):\n%s", diff)
	}
	require.NoError(t, repo.DeleteOwnedNetworkToHost(ctx, network.ID, "foreign-claim", network.Incarnation))
	storedNetwork, err = repo.GetNetworkToHost(ctx, network.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(network, storedNetwork); diff != "" {
		t.Errorf("foreign network cleanup preserved (-want +got):\n%s", diff)
	}

	require.NoError(t, repo.InvalidateByVM(ctx, "vm-a"))
	storedVM, err = repo.GetVMToHost(ctx, vm.ID)
	require.NoError(t, err)
	assert.Nil(t, storedVM)
	storedNetwork, err = repo.GetNetworkToHost(ctx, network.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(network, storedNetwork); diff != "" {
		t.Errorf("VM invalidation preserves network grant (-want +got):\n%s", diff)
	}
	require.NoError(t, repo.InvalidateByNetwork(ctx, "net-a"))
	storedNetwork, err = repo.GetNetworkToHost(ctx, network.ID)
	require.NoError(t, err)
	assert.Nil(t, storedNetwork)
}
