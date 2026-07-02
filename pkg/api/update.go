// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"

	"mvmctl/internal/core/update"
	"mvmctl/pkg/api/results"
)

// UpdateAPI defines the public interface for self-update operations.
type UpdateAPI interface {
	SelfUpdateCheck(ctx context.Context) (*results.UpdateCheckResult, error)
	SelfUpdateApply(ctx context.Context, force bool) error
}

// SelfUpdateCheck compares the current build version against the latest
// GitHub release and returns whether an update is available.
func (op *Operation) SelfUpdateCheck(ctx context.Context) (*results.UpdateCheckResult, error) {
	svc := update.NewService()
	result, err := svc.Check(ctx)
	if err != nil {
		return nil, err
	}
	return &results.UpdateCheckResult{
		CurrentVersion: result.CurrentVersion,
		LatestVersion:  result.LatestVersion,
		HasUpdate:      result.HasUpdate,
	}, nil
}

// SelfUpdateApply downloads the latest release binary, verifies its SHA256
// checksum, and atomically replaces the current binary via os.Rename.
func (op *Operation) SelfUpdateApply(ctx context.Context, force bool) error {
	svc := update.NewService()
	return svc.Apply(ctx, force)
}
