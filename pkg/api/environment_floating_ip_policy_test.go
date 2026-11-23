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
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

type environmentFloatingPolicyClient struct {
	trafficPolicyClient
	remove       func(context.Context, networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error)
	applyVM      func(context.Context, networkservice.VMFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error)
	applyNetwork func(context.Context, networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error)
}

func (client environmentFloatingPolicyClient) RemoveNetworkFloatingIPTrafficPolicy(ctx context.Context,
	request networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
	return client.remove(ctx, request)
}

func (client environmentFloatingPolicyClient) ApplyVMFloatingIPTrafficPolicy(ctx context.Context,
	request networkservice.VMFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
	return client.applyVM(ctx, request)
}

func (client environmentFloatingPolicyClient) ApplyNetworkFloatingIPTrafficPolicy(ctx context.Context,
	request networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
	return client.applyNetwork(ctx, request)
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

func TestEnvironmentFloatingIPPolicyPendingClaimCannotAdoptReplacementNames(t *testing.T) {
	// A pending claim retains full IDs even when the originals disappear and their names are reused.
	for _, family := range []string{"vm", "network"} {
		for _, missing := range []string{"source", "destination network", "resource", "noncanonical destination network"} {
			t.Run(family+"/"+missing, func(t *testing.T) {
				ctx := t.Context()
				sourceNetworkID, destinationNetworkID := strings.Repeat("a", 64), strings.Repeat("b", 64)
				vmID, resourceID := strings.Repeat("c", 32), strings.Repeat("d", 32)
				networks, vms := testutil.NewNetworkRepo(), testutil.NewVMRepo()
				source := &model.NetworkItem{ID: sourceNetworkID, Name: "source", IsPresent: true}
				destination := &model.NetworkItem{ID: destinationNetworkID, Name: "destination", IsPresent: true}
				sourceVM := &model.VMItem{ID: vmID, Name: "source", NetworkID: model.VMNetworkID(sourceNetworkID)}
				if missing == "source" {
					if family == "vm" {
						sourceVM.ID, sourceVM.Name = strings.Repeat("e", 32), vmID
					} else {
						source.ID, source.Name = strings.Repeat("e", 64), sourceNetworkID
					}
				}
				if missing == "destination network" {
					destination.ID, destination.Name = strings.Repeat("f", 64), destinationNetworkID
				}
				require.NoError(t, networks.Upsert(ctx, source))
				require.NoError(t, networks.Upsert(ctx, destination))
				require.NoError(t, vms.Upsert(ctx, sourceVM))
				// Root has only a different resource whose name equals the missing saved ID.
				replacement := model.FloatingIPItem{
					ID:        strings.Repeat("e", 32),
					Name:      resourceID,
					NetworkID: destinationNetworkID,
				}
				rootResources := map[string]model.FloatingIPItem{replacement.ID: replacement}
				rootErr := errs.NotFound(errs.CodeNetworkNotFound, "saved floating resource no longer exists")
				applies := 0
				ownership := inputs.EnvironmentPolicyOwnershipInput{ClaimID: "11111111-1111-4111-8111-111111111111"}
				check := func(id, claim, incarnation string) (networkservice.TrafficPolicyMutationResult, error) {
					applies++
					require.Equal(t, resourceID, id, "root receives the saved ID, never a replacement selected by name")
					require.Equal(t, ownership.ClaimID, claim)
					require.Empty(t, incarnation, "pending claim has no acknowledged receipt")
					_, present := rootResources[id]
					require.False(t, present, "exact root selection cannot adopt the ID-shaped replacement name")
					return networkservice.TrafficPolicyMutationResult{}, rootErr
				}
				client := environmentFloatingPolicyClient{
					applyVM: func(_ context.Context, request networkservice.VMFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
						require.Equal(t, vmID, request.SourceVMID)
						return check(request.DestinationFloatingIPID, request.ClaimID, request.Incarnation)
					},
					applyNetwork: func(_ context.Context, request networkservice.NetworkFloatingIPTrafficPolicyRequest) (networkservice.TrafficPolicyMutationResult, error) {
						require.Equal(t, sourceNetworkID, request.SourceNetworkID)
						return check(request.DestinationFloatingIPID, request.ClaimID, request.Incarnation)
					},
				}
				// A projection attempt would panic: no projection repository is configured before root success.
				op := &Operation{Repos: Repos{Network: networks, VM: vms}, trafficPolicyClient: client}
				if missing == "noncanonical destination network" {
					destinationNetworkID = "destination"
				}
				var receipt *results.EnvironmentPolicyReceipt
				var err error
				if family == "vm" {
					receipt, err = op.VMToFloatingIPTrafficPolicyEnsure(
						ctx,
						inputs.VMToFloatingIPTrafficPolicyCreateInput{
							SourceVM: vmID, DestinationFloatingIP: resourceID, DestinationNetwork: destinationNetworkID,
							Protocol: "tcp", DestinationPort: "6443"},
						ownership,
					)
				} else {
					receipt, err = op.NetworkToFloatingIPTrafficPolicyEnsure(
						ctx,
						inputs.NetworkToFloatingIPTrafficPolicyCreateInput{
							SourceNetwork:         sourceNetworkID,
							DestinationFloatingIP: resourceID,
							DestinationNetwork:    destinationNetworkID,
							Protocol:              "tcp",
							DestinationPort:       "6443",
						},
						ownership,
					)
				}
				require.Error(t, err)
				require.Nil(t, receipt, "a refused retry cannot replace the durable ownership receipt")
				if missing == "resource" {
					require.ErrorIs(t, err, rootErr)
					require.Equal(t, 1, applies)
				} else {
					require.Zero(t, applies, "missing exact local endpoints must refuse before root mutation")
				}
			})
		}
	}
}
