package api

import (
	"context"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
)

// FloatingIPAPI exposes root-backed resource management separately from VM lifecycle commands.
type FloatingIPAPI interface {
	FloatingIPDelegationAPI
	FloatingIPCreate(context.Context, inputs.FloatingIPCreateInput) (*model.FloatingIPItem, error)
	FloatingIPList(context.Context, inputs.FloatingIPNetworkInput) ([]*model.FloatingIPItem, error)
	FloatingIPGet(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error)
	FloatingIPRemove(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error)
}

func (op *Operation) FloatingIPCreate(
	ctx context.Context,
	input inputs.FloatingIPCreateInput,
) (*model.FloatingIPItem, error) {
	item, err := input.Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	id := strings.ReplaceAll(crypto.UUIDV4(), "-", "")
	created, err := op.Services.Network.CreateFloatingIP(ctx, id, item.ID, input.Name, input.IPv4)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation(
		"floating_ip.create",
		map[string]any{"id": created.ID, "network_id": created.NetworkID, "ipv4": created.IPv4},
		"",
	)
	return created, nil
}

func (op *Operation) FloatingIPList(
	ctx context.Context,
	input inputs.FloatingIPNetworkInput,
) ([]*model.FloatingIPItem, error) {
	item, err := input.Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	return op.Services.Network.ListFloatingIPs(ctx, item.ID)
}

func (op *Operation) FloatingIPGet(ctx context.Context, input inputs.FloatingIPInput) (*model.FloatingIPItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	item, err := (inputs.FloatingIPNetworkInput{Network: input.Network}).Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	return network.NewFloatingIPResolver(op.Services.Network).Resolve(ctx, item.ID, input.Identifier)
}

func (op *Operation) FloatingIPRemove(
	ctx context.Context,
	input inputs.FloatingIPInput,
) (*model.FloatingIPItem, error) {
	item, err := op.FloatingIPGet(ctx, input)
	if err != nil {
		return nil, err
	}
	removed, err := op.Services.Network.RemoveFloatingIP(ctx, item)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation(
		"floating_ip.remove",
		map[string]any{"id": removed.ID, "network_id": removed.NetworkID, "generation": removed.Generation},
		"",
	)
	return removed, nil
}
