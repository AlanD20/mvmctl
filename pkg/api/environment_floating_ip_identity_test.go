package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type floatingIPIdentityAuthority struct {
	resources      []*model.FloatingIPItem
	inventoryCalls int
	creates        []networkservice.FloatingIPCreateRequest
	grants         []networkservice.FloatingIPDelegationGrantRequest
}

func (authority *floatingIPIdentityAuthority) ListFloatingIPs(
	_ context.Context,
	networkID string,
) ([]*model.FloatingIPItem, error) {
	authority.inventoryCalls++
	var resources []*model.FloatingIPItem
	for _, item := range authority.resources {
		if item.NetworkID == networkID {
			resources = append(resources, item)
		}
	}
	return resources, nil
}

func (authority *floatingIPIdentityAuthority) CreateFloatingIP(
	_ context.Context,
	id, networkID, name, address string,
) (*model.FloatingIPItem, error) {
	authority.creates = append(
		authority.creates,
		networkservice.FloatingIPCreateRequest{ID: id, NetworkID: networkID, Name: name, IPv4: address},
	)
	return &model.FloatingIPItem{ID: id, NetworkID: networkID, Name: name, IPv4: address}, nil
}

func (authority *floatingIPIdentityAuthority) GrantFloatingIPDelegation(_ context.Context,
	id, networkID, resourceID, vmID, method string, duration time.Duration) (*model.FloatingIPDelegationItem, error) {
	authority.grants = append(authority.grants, networkservice.FloatingIPDelegationGrantRequest{
		ID: id, NetworkID: networkID, ResourceID: resourceID, VMID: vmID, Method: method, LeaseDuration: duration})
	for _, item := range authority.resources {
		if item.ID == resourceID && item.NetworkID == networkID {
			return &model.FloatingIPDelegationItem{ID: id, NetworkID: networkID, ResourceID: resourceID, VMID: vmID,
				Method: method, LeaseDuration: duration}, nil
		}
	}
	return nil, errs.NotFound(errs.CodeNetworkNotFound, "exact root resource is absent")
}

func TestFloatingIPDelegationEnsurePendingIdentityCannotAdoptReplacementNames(t *testing.T) {
	for _, missing := range []string{"VM", "network", "resource", "network selector", "VM selector", "resource selector"} {
		t.Run(missing, func(t *testing.T) {
			ctx := t.Context()
			networkID, resourceID, vmID, grantID := strings.Repeat(
				"a",
				64,
			), strings.Repeat(
				"b",
				32,
			), strings.Repeat(
				"c",
				32,
			), strings.Repeat(
				"d",
				32,
			)
			networks, vms := testutil.NewNetworkRepo(), testutil.NewVMRepo()
			network := &model.NetworkItem{ID: networkID, Name: "cluster", IsPresent: true}
			target := &model.VMItem{ID: vmID, Name: "controller", NetworkID: model.VMNetworkID(networkID)}
			resource := &model.FloatingIPItem{ID: resourceID, Name: "api", NetworkID: networkID}
			switch missing {
			case "VM":
				target.ID, target.Name = strings.Repeat("e", 32), vmID
			case "network":
				network.ID, network.Name = strings.Repeat("e", 64), networkID
			case "resource":
				resource.ID, resource.Name = strings.Repeat("e", 32), resourceID
			}
			require.NoError(t, networks.Upsert(ctx, network))
			require.NoError(t, vms.Upsert(ctx, target))
			authority := &floatingIPIdentityAuthority{resources: []*model.FloatingIPItem{resource}}
			op := &Operation{
				Repos:               Repos{Network: networks, VM: vms},
				floatingIPAuthority: authority,
				AuditLog:            &logging.AuditLog{},
			}
			input := inputs.FloatingIPDelegationEnsureInput{
				ID: grantID,
				FloatingIPDelegationGrantInput: inputs.FloatingIPDelegationGrantInput{
					Network: networkID, Resource: resourceID, VM: vmID, Method: "associate", Lease: "10s"},
			}
			switch missing {
			case "network selector":
				input.Network = "cluster"
			case "VM selector":
				input.VM = "controller"
			case "resource selector":
				input.Resource = "api"
			}
			receipt, err := op.FloatingIPDelegationEnsure(ctx, input)
			require.Error(t, err)
			require.Nil(t, receipt)
			require.Zero(t, authority.inventoryCalls, "pending retries cannot resolve names from inventory")
			if missing == "resource" {
				require.Equal(t, []networkservice.FloatingIPDelegationGrantRequest{
					{
						ID:            grantID,
						NetworkID:     networkID,
						ResourceID:    resourceID,
						VMID:          vmID,
						Method:        "associate",
						LeaseDuration: 10 * time.Second,
					},
				}, authority.grants)
			} else {
				require.Empty(t, authority.grants, "missing exact local endpoints must refuse before root effects")
			}
		})
	}
}

func TestFloatingIPDelegationFreshGrantResolvesNamesBeforeExactEnsure(t *testing.T) {
	ctx := t.Context()
	networkID, resourceID, vmID := strings.Repeat("a", 64), strings.Repeat("b", 32), strings.Repeat("c", 32)
	networks, vms := testutil.NewNetworkRepo(), testutil.NewVMRepo()
	require.NoError(t, networks.Upsert(ctx, &model.NetworkItem{ID: networkID, Name: "cluster", IsPresent: true}))
	require.NoError(
		t,
		vms.Upsert(ctx, &model.VMItem{ID: vmID, Name: "controller", NetworkID: model.VMNetworkID(networkID)}),
	)
	authority := &floatingIPIdentityAuthority{
		resources: []*model.FloatingIPItem{{ID: resourceID, Name: "api", NetworkID: networkID}},
	}
	op := &Operation{
		Repos:               Repos{Network: networks, VM: vms},
		floatingIPAuthority: authority,
		AuditLog:            &logging.AuditLog{},
	}
	receipt, err := op.FloatingIPDelegationGrant(ctx, inputs.FloatingIPDelegationGrantInput{
		Network: "cluster", Resource: "api", VM: "controller", Method: "associate", Lease: "10s"})
	require.NoError(t, err)
	require.NotNil(t, receipt)
	require.Len(t, receipt.ID, 32) // Non-deterministic new grant identity.
	require.Equal(t, 1, authority.inventoryCalls, "fresh resource selectors resolve once before strict Ensure")
	require.Equal(t, []networkservice.FloatingIPDelegationGrantRequest{{ID: receipt.ID, NetworkID: networkID,
		ResourceID: resourceID, VMID: vmID, Method: "associate", LeaseDuration: 10 * time.Second}}, authority.grants)
}

func TestFloatingIPReservationEnsureRejectsMissingSavedNetworkBeforeEffect(t *testing.T) {
	ctx := t.Context()
	networkID, resourceID := strings.Repeat("a", 64), strings.Repeat("b", 32)
	networks := testutil.NewNetworkRepo()
	require.NoError(t, networks.Upsert(ctx, &model.NetworkItem{ID: strings.Repeat("c", 64), Name: networkID,
		Subnet: "172.27.0.0/24", IPv4Gateway: "172.27.0.1", IsPresent: true}))
	authority := &floatingIPIdentityAuthority{}
	op := &Operation{Repos: Repos{Network: networks}, floatingIPAuthority: authority}
	receipt, err := op.FloatingIPEnsure(
		ctx,
		inputs.FloatingIPCreateInput{Network: networkID, Name: "api", IPv4: "172.27.0.100"},
		inputs.EnvironmentFloatingIPOwnershipInput{ResourceID: resourceID},
	)
	require.Error(t, err)
	require.Nil(t, receipt)
	require.Empty(t, authority.creates)
	require.Zero(t, authority.inventoryCalls)
}

func TestFloatingIPReservationEnsureKeepsExactNetworkAndAddressValidation(t *testing.T) {
	for _, selector := range []string{"exact", "network name", "gateway"} {
		t.Run(selector, func(t *testing.T) {
			ctx := t.Context()
			networkID, resourceID := strings.Repeat("a", 64), strings.Repeat("b", 32)
			networks := testutil.NewNetworkRepo()
			require.NoError(t, networks.Upsert(ctx, &model.NetworkItem{ID: networkID, Name: "cluster",
				Subnet: "172.27.0.0/24", IPv4Gateway: "172.27.0.1", IsPresent: true}))
			authority := &floatingIPIdentityAuthority{}
			op := &Operation{Repos: Repos{Network: networks}, floatingIPAuthority: authority}
			input := inputs.FloatingIPCreateInput{Network: networkID, Name: "api", IPv4: "172.27.0.100"}
			if selector == "network name" {
				input.Network = "cluster"
			}
			if selector == "gateway" {
				input.IPv4 = "172.27.0.1"
			}
			receipt, err := op.FloatingIPEnsure(
				ctx,
				input,
				inputs.EnvironmentFloatingIPOwnershipInput{ResourceID: resourceID},
			)
			if selector != "exact" {
				require.Error(t, err)
				require.Nil(t, receipt)
				require.Empty(t, authority.creates)
				return
			}
			require.NoError(t, err)
			require.Equal(
				t,
				&model.FloatingIPItem{ID: resourceID, NetworkID: networkID, Name: "api", IPv4: input.IPv4},
				receipt,
			)
			require.Equal(
				t,
				[]networkservice.FloatingIPCreateRequest{
					{ID: resourceID, NetworkID: networkID, Name: "api", IPv4: input.IPv4},
				},
				authority.creates,
			)
		})
	}
}
