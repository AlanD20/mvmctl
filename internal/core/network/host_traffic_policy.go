package network

import (
	"context"
	"log/slog"

	"mvmctl/pkg/errs"
)

// RemoveVMToHost deletes local host-grant projections only after the API has obtained root revocation.
func (s *TrafficPolicyService) RemoveVMToHost(ctx context.Context, policyIDs []string) error {
	if err := s.repo.DeleteVMToHost(ctx, policyIDs); err != nil {
		slog.Error("failed to remove host traffic policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove host traffic policies", err)
	}
	return nil
}

// RemoveNetworkToHost deletes local host-grant projections only after the API has obtained root revocation.
func (s *TrafficPolicyService) RemoveNetworkToHost(ctx context.Context, policyIDs []string) error {
	if err := s.repo.DeleteNetworkToHost(ctx, policyIDs); err != nil {
		slog.Error("failed to remove host traffic policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove host traffic policies", err)
	}
	return nil
}
