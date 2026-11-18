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

type floatingPolicyClient struct {
	trafficPolicyClient
	remove func(
		context.Context,
		networkservice.NetworkFloatingIPTrafficPolicyRequest,
	) (networkservice.TrafficPolicyMutationResult, error)
}

func (c floatingPolicyClient) RemoveNetworkFloatingIPTrafficPolicy(
	ctx context.Context,
	r networkservice.NetworkFloatingIPTrafficPolicyRequest,
) (networkservice.TrafficPolicyMutationResult, error) {
	return c.remove(ctx, r)
}

type floatingPolicyRepo struct {
	network.TrafficPolicyRepository
	deleteErr error
}

func (r floatingPolicyRepo) DeleteOwnedNetworkToFloatingIP(ctx context.Context, id, claim, incarnation string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return r.TrafficPolicyRepository.DeleteOwnedNetworkToFloatingIP(ctx, id, claim, incarnation)
}

func TestFloatingIPTrafficPolicyAPIRemovalRetainsProjectionOnRootFailureAndRetriesLocalFailure(t *testing.T) {
	database := testutil.NewInMemoryDB(t)
	_, err := database.Exec(`INSERT INTO networks
 (id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present)
 VALUES('network','blue','10.0.0.0/24','mvm0','10.0.0.1',1,1,1,1)`)
	require.NoError(t, err)
	repo := network.NewTrafficPolicyRepository(database)
	intent := &model.NetworkToFloatingIPTrafficAllowIntent{
		SourceNetworkID:         strings.Repeat("a", 64),
		DestinationFloatingIPID: strings.Repeat("b", 32),
		Protocol:                model.TrafficProtocolTCP,
		DestinationPortStart:    443,
		DestinationPortEnd:      443,
		ClaimID:                 "11111111-1111-4111-8111-111111111111",
		Incarnation:             "22222222-2222-4222-8222-222222222222",
		CreatedAt:               "2026-01-01T00:00:00Z",
		UpdatedAt:               "2026-01-01T00:00:00Z",
	}
	_, err = database.Exec(`UPDATE networks SET id=? WHERE id='network'`, intent.SourceNetworkID)
	require.NoError(t, err)
	intent.ID = crypto.NetworkToFloatingIPTrafficPolicyDigest(*intent)
	require.NoError(t, repo.ProjectNetworkToFloatingIP(t.Context(), intent))
	rootErr := errors.New("root refused")
	calls := 0
	client := floatingPolicyClient{remove: func(
		_ context.Context,
		r networkservice.NetworkFloatingIPTrafficPolicyRequest,
	) (networkservice.TrafficPolicyMutationResult, error) {
		calls++
		require.Equal(t, intent.ClaimID, r.ClaimID)
		require.Equal(t, intent.Incarnation, r.Incarnation)
		return networkservice.TrafficPolicyMutationResult{PolicyID: r.PolicyID()}, rootErr
	}}
	op := &Operation{Repos: Repos{TrafficPolicy: repo}, trafficPolicyClient: client, AuditLog: &logging.AuditLog{}}
	input := inputs.NetworkToFloatingIPTrafficPolicyInput{Identifiers: []string{intent.ID[:12]}}
	require.ErrorIs(t, op.NetworkToFloatingIPTrafficPolicyRemove(t.Context(), input), rootErr)
	got, err := repo.GetNetworkToFloatingIP(t.Context(), intent.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	rootErr = nil
	op.Repos.TrafficPolicy = floatingPolicyRepo{
		TrafficPolicyRepository: repo,
		deleteErr:               errors.New("database refused"),
	}
	err = op.NetworkToFloatingIPTrafficPolicyRemove(t.Context(), input)
	require.Error(t, err)
	require.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(err).Code)
	got, err = repo.GetNetworkToFloatingIP(t.Context(), intent.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	op.Repos.TrafficPolicy = repo
	require.NoError(t, op.NetworkToFloatingIPTrafficPolicyRemove(t.Context(), input))
	require.Equal(t, 3, calls)
	got, err = repo.GetNetworkToFloatingIP(t.Context(), intent.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
