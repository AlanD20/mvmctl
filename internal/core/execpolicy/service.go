package execpolicy

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Service manages directional execution-policy intent.
type Service struct{ repo Repository }

// NewService creates an execution-policy service.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Create persists trusted execution-policy intent.
func (s *Service) Create(ctx context.Context, policy *model.ExecPolicy) error {
	existing, err := s.repo.GetByIdentity(ctx, policy)
	if err != nil {
		slog.Error("failed to check execution policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to check execution policy", err)
	}
	if existing != nil {
		return errs.AlreadyExists(errs.CodePolicyAlreadyExists, "execution policy already exists",
			errs.WithEntity(existing.ID))
	}
	if err := s.repo.Create(ctx, policy); err != nil {
		slog.Error("failed to persist execution policy", "policy", policy.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyCreateFailed, "failed to persist execution policy", err)
	}
	return nil
}

// Intents returns all execution-policy intent for the host relay adapter.
func (s *Service) Intents(ctx context.Context) ([]*model.ExecPolicy, error) {
	policies, err := s.repo.List(ctx)
	if err != nil {
		slog.Error("failed to list execution policy intents", "error", err)
		return nil, errs.WrapMsg(errs.CodePolicyListFailed, "failed to list execution policy intents", err)
	}
	return policies, nil
}

// Allows reports whether the exact directional identity and target user are granted.
func (s *Service) Allows(
	ctx context.Context,
	sourceVMID string,
	destinationVMID string,
	targetUser string,
) (bool, error) {
	allowed, err := s.repo.Allows(ctx, sourceVMID, destinationVMID, targetUser)
	if err != nil {
		slog.Error("failed to authorize execution policy", "source_vm", sourceVMID,
			"destination_vm", destinationVMID, "target_user", targetUser, "error", err)
		return false, errs.WrapMsg(errs.CodePolicyListFailed, "failed to authorize execution policy", err)
	}
	return allowed, nil
}

// Remove deletes execution-policy intent atomically.
func (s *Service) Remove(ctx context.Context, policyIDs []string) error {
	if err := s.repo.DeleteMany(ctx, policyIDs); err != nil {
		slog.Error("failed to remove execution policies", "policy_ids", policyIDs, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to remove execution policies", err)
	}
	return nil
}

// InvalidateByVM removes policies where a VM is either source or destination.
func (s *Service) InvalidateByVM(ctx context.Context, vmID string) error {
	if err := s.repo.DeleteByVM(ctx, vmID); err != nil {
		slog.Error("failed to invalidate execution policies", "vm_id", vmID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "failed to invalidate execution policies", err)
	}
	return nil
}
