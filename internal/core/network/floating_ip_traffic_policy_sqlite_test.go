package network_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestFloatingIPTrafficPolicyProjectionReceiptsAndInvalidation(t *testing.T) {
	database, repo := seedTrafficPolicyResources(t)
	ctx := t.Context()
	resource := strings.Repeat("c", 32)
	vm := &model.VMToFloatingIPTrafficAllowIntent{
		ID:                      strings.Repeat("a", 64),
		SourceVMID:              "vm-a",
		DestinationFloatingIPID: resource,
		Protocol:                model.TrafficProtocolTCP,
		DestinationPortStart:    443,
		DestinationPortEnd:      443,
		ClaimID:                 "claim",
		Incarnation:             "first",
		CreatedAt:               "2026-01-01T00:00:00Z",
		UpdatedAt:               "2026-01-01T00:00:00Z",
	}
	net := &model.NetworkToFloatingIPTrafficAllowIntent{
		ID:                      strings.Repeat("b", 64),
		SourceNetworkID:         "net-b",
		DestinationFloatingIPID: resource,
		Protocol:                model.TrafficProtocolUDP,
		DestinationPortStart:    53,
		DestinationPortEnd:      53,
		ClaimID:                 "claim",
		Incarnation:             "first",
		CreatedAt:               vm.CreatedAt,
		UpdatedAt:               vm.UpdatedAt,
	}
	require.NoError(t, repo.ProjectVMToFloatingIP(ctx, vm))
	require.NoError(t, repo.ProjectNetworkToFloatingIP(ctx, net))
	// Retrying a receipt is idempotent; a replacement receipt defeats a stale remover.
	require.NoError(t, repo.ProjectVMToFloatingIP(ctx, vm))
	vm.Incarnation = "replacement"
	require.NoError(t, repo.ProjectVMToFloatingIP(ctx, vm))
	require.NoError(t, repo.DeleteOwnedVMToFloatingIP(ctx, vm.ID, "claim", "first"))
	got, err := repo.GetVMToFloatingIP(ctx, vm.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "replacement", got.Incarnation)
	rebound := *vm
	rebound.DestinationFloatingIPID = strings.Repeat("d", 32)
	require.Error(t, repo.ProjectVMToFloatingIP(ctx, &rebound))
	matches, err := repo.FindNetworkToFloatingIPByPrefix(ctx, net.ID[:12])
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.Equal(t, resource, matches[0].DestinationFloatingIPID)
	// Rollback both families if the second deletion fails.
	_, err = database.Exec(`CREATE TRIGGER refuse_fp_delete
 BEFORE DELETE ON network_floating_ip_traffic_policies
 BEGIN SELECT RAISE(ABORT,'injected refusal'); END;`)
	require.NoError(t, err)
	require.Error(t, repo.InvalidateByFloatingIP(ctx, resource))
	got, err = repo.GetVMToFloatingIP(ctx, vm.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	_, err = database.Exec(`DROP TRIGGER refuse_fp_delete`)
	require.NoError(t, err)
	require.NoError(t, repo.InvalidateByFloatingIP(ctx, resource))
	vms, err := repo.ListVMToFloatingIP(ctx)
	require.NoError(t, err)
	require.Empty(t, vms)
	nets, err := repo.ListNetworkToFloatingIP(ctx)
	require.NoError(t, err)
	require.Empty(t, nets)
	require.NoError(t, repo.ProjectVMToFloatingIP(ctx, vm))
	require.NoError(t, repo.ProjectNetworkToFloatingIP(ctx, net))
	require.NoError(t, repo.InvalidateByVM(ctx, "vm-a"))
	vms, err = repo.ListVMToFloatingIP(ctx)
	require.NoError(t, err)
	require.Empty(t, vms)
	nets, err = repo.ListNetworkToFloatingIP(ctx)
	require.NoError(t, err)
	require.Len(t, nets, 1)
	require.NoError(t, repo.InvalidateByNetwork(ctx, "net-b"))
	nets, err = repo.ListNetworkToFloatingIP(ctx)
	require.NoError(t, err)
	require.Empty(t, nets)
}

func TestFloatingIPTrafficPolicyProjectionRejectsNoncanonicalDestinationAndMissingSource(t *testing.T) {
	_, repo := seedTrafficPolicyResources(t)
	intent := &model.NetworkToFloatingIPTrafficAllowIntent{
		ID:                      strings.Repeat("a", 64),
		SourceNetworkID:         "net-a",
		DestinationFloatingIPID: "address-or-name",
		Protocol:                model.TrafficProtocolTCP,
		DestinationPortStart:    80,
		DestinationPortEnd:      80,
		CreatedAt:               "2026-01-01T00:00:00Z",
		UpdatedAt:               "2026-01-01T00:00:00Z",
	}
	require.Error(t, repo.ProjectNetworkToFloatingIP(t.Context(), intent))
	intent.DestinationFloatingIPID = strings.Repeat("b", 32)
	intent.SourceNetworkID = "missing"
	require.Error(t, repo.ProjectNetworkToFloatingIP(t.Context(), intent))
}
