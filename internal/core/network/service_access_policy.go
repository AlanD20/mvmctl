package network

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// CreateServiceAccessPolicy persists trusted typed policy intent.
func (s *Service) CreateServiceAccessPolicy(ctx context.Context, policy *model.ServiceAccessPolicy) error {
	existing, err := s.policyRepo.GetByIdentity(ctx, policy)
	if err != nil {
		slog.Error("failed to check existing service-access policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to check existing service-access policy", err)
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodePolicyAlreadyExists, "service-access policy already exists",
			errs.WithEntity(existing.ID))
	}
	if err := s.policyRepo.Create(ctx, policy); err != nil {
		slog.Error("failed to persist service-access policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to persist service-access policy", err)
	}
	return nil
}

// ListServiceAccessPolicies returns persisted policy intent without cross-domain resolution.
func (s *Service) ListServiceAccessPolicies(ctx context.Context) ([]*model.ServiceAccessPolicy, error) {
	policies, err := s.policyRepo.List(ctx)
	if err != nil {
		slog.Error("failed to list service-access policies", "error", err)
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list service-access policies", err)
	}
	return policies, nil
}

// RemoveServiceAccessPolicies atomically deletes trusted typed policy intent.
func (s *Service) RemoveServiceAccessPolicies(ctx context.Context, policyIDs []string) error {
	if err := s.policyRepo.DeleteMany(ctx, policyIDs); err != nil {
		slog.Error("failed to delete service-access policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to delete service-access policies", err)
	}
	return nil
}

// InvalidateServiceAccessPoliciesByVM deletes destination-VM-bound policy intent.
func (s *Service) InvalidateServiceAccessPoliciesByVM(ctx context.Context, vmID string) error {
	if err := s.policyRepo.DeleteByVM(ctx, vmID); err != nil {
		slog.Error("failed to invalidate VM service-access policies", "vm_id", vmID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate VM service-access policies", err)
	}
	return nil
}

// InvalidateServiceAccessPoliciesBySourceNetwork deletes source-network-bound policy intent.
func (s *Service) InvalidateServiceAccessPoliciesBySourceNetwork(ctx context.Context, networkID string) error {
	if err := s.policyRepo.DeleteBySourceNetwork(ctx, networkID); err != nil {
		slog.Error("failed to invalidate source-network service-access policies",
			"network_id", networkID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed,
			"failed to invalidate source-network service-access policies", err)
	}
	return nil
}

// ReconcileServiceAccessPolicies compiles trusted API-resolved identities and atomically rebuilds firewall state.
func (s *Service) ReconcileServiceAccessPolicies(
	ctx context.Context,
	policies []*model.ResolvedServiceAccessPolicy,
	networks []*model.NetworkItem,
) error {
	if s.firewallTracker == nil {
		err := errs.New(errs.CodePolicySyncFailed, "firewall tracker is unavailable")
		slog.Error("failed to reconcile service-access policies", "error", err)
		return err
	}
	rules := CompileServiceAccessPolicyRules(policies, networks)
	s.firewallTracker.Initialize(ctx)
	result := s.firewallTracker.ReconcilePolicyRules(ctx, rules)
	if !result.Success {
		message := "firewall policy reconciliation failed"
		if result.ErrorMessage != nil {
			message += ": " + *result.ErrorMessage
		}
		slog.Error("failed to reconcile service-access policies", "error", message)
		return errs.New(errs.CodePolicySyncFailed, message)
	}
	return nil
}
