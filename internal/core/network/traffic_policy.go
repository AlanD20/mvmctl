package network

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// TrafficPolicyService manages the two closed traffic-policy source forms.
type TrafficPolicyService struct {
	repo TrafficPolicyRepository
}

// TrafficPolicyAllowIntents contains persisted desired grants in their two closed source forms.
// These rows are not compiled firewall rules and do not describe applied kernel state.
type TrafficPolicyAllowIntents struct {
	NetworkToVM []*model.NetworkToVMTrafficAllowIntent
	VMToVM      []*model.VMToVMTrafficAllowIntent
}

// NewTrafficPolicyService creates a traffic policy service.
func NewTrafficPolicyService(repo TrafficPolicyRepository) *TrafficPolicyService {
	return &TrafficPolicyService{repo: repo}
}

// CreateNetworkToVM persists trusted network-source traffic intent.
func (s *TrafficPolicyService) CreateNetworkToVM(
	ctx context.Context,
	intent *model.NetworkToVMTrafficAllowIntent,
) error {
	existing, err := s.repo.GetNetworkByIdentity(ctx, intent)
	if err != nil {
		slog.Error("failed to check network traffic allow intent", "policy", intent.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to check network traffic policy", err)
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodePolicyAlreadyExists, "network traffic policy already exists",
			errs.WithEntity(existing.ID))
	}
	if err := s.repo.CreateNetworkToVM(ctx, intent); err != nil {
		slog.Error("failed to persist network traffic allow intent", "policy", intent.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to persist network traffic policy", err)
	}
	return nil
}

// CreateVMToVM persists trusted exact-VM-source traffic intent.
func (s *TrafficPolicyService) CreateVMToVM(ctx context.Context, intent *model.VMToVMTrafficAllowIntent) error {
	existing, err := s.repo.GetVMByIdentity(ctx, intent)
	if err != nil {
		slog.Error("failed to check VM traffic allow intent", "policy", intent.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to check VM traffic policy", err)
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodePolicyAlreadyExists, "VM traffic policy already exists",
			errs.WithEntity(existing.ID))
	}
	if err := s.repo.CreateVMToVM(ctx, intent); err != nil {
		slog.Error("failed to persist VM traffic allow intent", "policy", intent.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to persist VM traffic policy", err)
	}
	return nil
}

// AllowIntents returns all persisted traffic grants without compiling enforcement state.
func (s *TrafficPolicyService) AllowIntents(ctx context.Context) (*TrafficPolicyAllowIntents, error) {
	fromNetworks, err := s.repo.ListNetworkToVM(ctx)
	if err != nil {
		slog.Error("failed to list network traffic policy intents", "error", err)
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list network traffic policy intents", err)
	}
	fromVMs, err := s.repo.ListVMToVM(ctx)
	if err != nil {
		slog.Error("failed to list VM traffic policy intents", "error", err)
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list VM traffic policy intents", err)
	}
	return &TrafficPolicyAllowIntents{NetworkToVM: fromNetworks, VMToVM: fromVMs}, nil
}

// RemoveNetworkToVM deletes network-source policy intent atomically.
func (s *TrafficPolicyService) RemoveNetworkToVM(ctx context.Context, policyIDs []string) error {
	if err := s.repo.DeleteNetworkToVM(ctx, policyIDs); err != nil {
		slog.Error("failed to remove network traffic policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove network traffic policies", err)
	}
	return nil
}

// RemoveVMToVM deletes exact-VM-source policy intent atomically.
func (s *TrafficPolicyService) RemoveVMToVM(ctx context.Context, policyIDs []string) error {
	if err := s.repo.DeleteVMToVM(ctx, policyIDs); err != nil {
		slog.Error("failed to remove VM traffic policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove VM traffic policies", err)
	}
	return nil
}

// InvalidateByVM removes policies where a VM is either source or destination.
func (s *TrafficPolicyService) InvalidateByVM(ctx context.Context, vmID string) error {
	if err := s.repo.InvalidateByVM(ctx, vmID); err != nil {
		slog.Error("failed to invalidate traffic policy allow intents", "vm_id", vmID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate traffic policies", err)
	}
	return nil
}

// InvalidateByNetwork removes network-source policies bound to the network.
func (s *TrafficPolicyService) InvalidateByNetwork(ctx context.Context, networkID string) error {
	if err := s.repo.InvalidateByNetwork(ctx, networkID); err != nil {
		slog.Error("failed to invalidate network traffic policies", "network_id", networkID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate network traffic policies", err)
	}
	return nil
}
