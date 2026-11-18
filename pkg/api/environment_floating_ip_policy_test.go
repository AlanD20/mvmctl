package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type environmentFloatingPolicyClient struct {
	trafficPolicyClient
	remove func(context.Context, networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error)
}

func (client environmentFloatingPolicyClient) RemoveNetworkFloatingIPTrafficPolicy(ctx context.Context,
	request networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
	return client.remove(ctx, request)
}

type environmentFloatingPolicyRepo struct {
	network.TrafficPolicyRepository
	deleteErr error
}

func (repo environmentFloatingPolicyRepo) DeleteOwnedNetworkToFloatingIP(
	ctx context.Context,
	id, claim, incarnation string,
) error {
	if repo.deleteErr != nil {
		return repo.deleteErr
	}
	return repo.TrafficPolicyRepository.DeleteOwnedNetworkToFloatingIP(ctx, id, claim, incarnation)
}

func TestEnvironmentFloatingIPPolicyCleanupNeedsOnlySavedIdentitiesAndPreservesFailedProjection(t *testing.T) {
	// No VM/network resolver or floating resource service is configured: cleanup uses immutable IDs.
	database := testutil.NewInMemoryDB(t)
	sourceID := strings.Repeat("a", 64)
	_, err := database.Exec(`INSERT INTO networks
  (id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present)
  VALUES(?, 'cluster', '172.27.0.0/24', 'mvm0', '172.27.0.1', 1, 1, 1, 1)`, sourceID)
	require.NoError(t, err)
	repo := network.NewTrafficPolicyRepository(database)
	intent := &model.NetworkToFloatingIPTrafficAllowIntent{SourceNetworkID: sourceID,
		DestinationFloatingIPID: strings.Repeat("b", 32), Protocol: model.TrafficProtocolTCP,
		DestinationPortStart: 6443, DestinationPortEnd: 6443,
		ClaimID: "11111111-1111-4111-8111-111111111111", Incarnation: "22222222-2222-4222-8222-222222222222",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	intent.ID = crypto.NetworkToFloatingIPTrafficPolicyDigest(*intent)
	require.NoError(t, repo.ProjectNetworkToFloatingIP(t.Context(), intent))
	rootErr := errors.New("root outcome unknown")
	client := environmentFloatingPolicyClient{remove: func(_ context.Context,
		request networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
		require.Equal(t, intent.ID, request.PolicyID())
		require.Equal(t, intent.ClaimID, request.ClaimID)
		require.Equal(t, intent.Incarnation, request.Incarnation)
		return networkservice.TrafficPolicyMutationResult{PolicyID: intent.ID}, rootErr
	}}
	op := &Operation{Repos: Repos{TrafficPolicy: repo}, trafficPolicyClient: client, AuditLog: &logging.AuditLog{}}
	input := inputs.NetworkToFloatingIPTrafficPolicyCreateInput{SourceNetwork: sourceID,
		DestinationFloatingIP: intent.DestinationFloatingIPID, DestinationNetwork: strings.Repeat("c", 64),
		Protocol: "tcp", DestinationPort: "6443"}
	ownership := inputs.EnvironmentPolicyOwnershipInput{ClaimID: intent.ClaimID, Incarnation: intent.Incarnation}
	require.ErrorIs(t, op.NetworkToFloatingIPTrafficPolicyRemoveOwned(t.Context(), input, ownership), rootErr)
	got, err := repo.GetNetworkToFloatingIP(t.Context(), intent.ID)
	require.NoError(t, err)
	require.Equal(t, intent, got)
	rootErr = nil
	op.Repos.TrafficPolicy = environmentFloatingPolicyRepo{
		TrafficPolicyRepository: repo,
		deleteErr:               errors.New("SQLite failed"),
	}
	err = op.NetworkToFloatingIPTrafficPolicyRemoveOwned(t.Context(), input, ownership)
	require.Error(t, err)
	require.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(err).Code)
	got, err = repo.GetNetworkToFloatingIP(t.Context(), intent.ID)
	require.NoError(t, err)
	require.Equal(t, intent, got)
	op.Repos.TrafficPolicy = repo
	require.NoError(t, op.NetworkToFloatingIPTrafficPolicyRemoveOwned(t.Context(), input, ownership))
	got, err = repo.GetNetworkToFloatingIP(t.Context(), intent.ID)
	require.NoError(t, err)
	require.Nil(t, got)
	require.NoError(
		t,
		op.NetworkToFloatingIPTrafficPolicyRemoveOwned(t.Context(), input, ownership),
		"retry after root removal must converge",
	)
}
