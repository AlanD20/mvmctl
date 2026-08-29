package network

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// TrafficPolicyService manages the two closed traffic-policy source forms.
type TrafficPolicyService struct {
	repos TrafficPolicyRepositories
}

// TrafficPolicyIntents is the complete backend-neutral traffic policy generation.
// The adapter receives distinct slices so a source form cannot be reinterpreted.
type TrafficPolicyIntents struct {
	FromNetworks []*model.NetworkTrafficPolicy
	FromVMs      []*model.VMTrafficPolicy
}

// NewTrafficPolicyService creates a traffic policy service.
func NewTrafficPolicyService(repos TrafficPolicyRepositories) *TrafficPolicyService {
	return &TrafficPolicyService{repos: repos}
}

// CreateFromNetwork persists trusted network-source traffic intent.
func (s *TrafficPolicyService) CreateFromNetwork(ctx context.Context, policy *model.NetworkTrafficPolicy) error {
	existing, err := s.repos.FromNetwork.GetByIdentity(ctx, policy)
	if err != nil {
		slog.Error("failed to check network traffic policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to check network traffic policy", err)
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodePolicyAlreadyExists, "network traffic policy already exists",
			errs.WithEntity(existing.ID))
	}
	if err := s.repos.FromNetwork.Create(ctx, policy); err != nil {
		slog.Error("failed to persist network traffic policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to persist network traffic policy", err)
	}
	return nil
}

// CreateFromVM persists trusted exact-VM-source traffic intent.
func (s *TrafficPolicyService) CreateFromVM(ctx context.Context, policy *model.VMTrafficPolicy) error {
	existing, err := s.repos.FromVM.GetByIdentity(ctx, policy)
	if err != nil {
		slog.Error("failed to check VM traffic policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to check VM traffic policy", err)
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodePolicyAlreadyExists, "VM traffic policy already exists",
			errs.WithEntity(existing.ID))
	}
	if err := s.repos.FromVM.Create(ctx, policy); err != nil {
		slog.Error("failed to persist VM traffic policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to persist VM traffic policy", err)
	}
	return nil
}

// Intents returns the complete persisted typed intent for a later enforcement adapter.
func (s *TrafficPolicyService) Intents(ctx context.Context) (*TrafficPolicyIntents, error) {
	fromNetworks, err := s.repos.FromNetwork.List(ctx)
	if err != nil {
		slog.Error("failed to list network traffic policy intents", "error", err)
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list network traffic policy intents", err)
	}
	fromVMs, err := s.repos.FromVM.List(ctx)
	if err != nil {
		slog.Error("failed to list VM traffic policy intents", "error", err)
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list VM traffic policy intents", err)
	}
	return &TrafficPolicyIntents{FromNetworks: fromNetworks, FromVMs: fromVMs}, nil
}

// RemoveFromNetworks deletes network-source policy intent atomically.
func (s *TrafficPolicyService) RemoveFromNetworks(ctx context.Context, policyIDs []string) error {
	if err := s.repos.FromNetwork.DeleteMany(ctx, policyIDs); err != nil {
		slog.Error("failed to remove network traffic policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove network traffic policies", err)
	}
	return nil
}

// RemoveFromVMs deletes exact-VM-source policy intent atomically.
func (s *TrafficPolicyService) RemoveFromVMs(ctx context.Context, policyIDs []string) error {
	if err := s.repos.FromVM.DeleteMany(ctx, policyIDs); err != nil {
		slog.Error("failed to remove VM traffic policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove VM traffic policies", err)
	}
	return nil
}

// InvalidateByVM removes policies where a VM is either source or destination.
func (s *TrafficPolicyService) InvalidateByVM(ctx context.Context, vmID string) error {
	if err := s.repos.FromNetwork.DeleteByVM(ctx, vmID); err != nil {
		slog.Error("failed to invalidate network traffic policies", "vm_id", vmID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate network traffic policies", err)
	}
	if err := s.repos.FromVM.DeleteByVM(ctx, vmID); err != nil {
		slog.Error("failed to invalidate VM traffic policies", "vm_id", vmID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate VM traffic policies", err)
	}
	return nil
}

// InvalidateByNetwork removes network-source policies bound to the network.
func (s *TrafficPolicyService) InvalidateByNetwork(ctx context.Context, networkID string) error {
	if err := s.repos.FromNetwork.DeleteByNetwork(ctx, networkID); err != nil {
		slog.Error("failed to invalidate network traffic policies", "network_id", networkID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate network traffic policies", err)
	}
	return nil
}
