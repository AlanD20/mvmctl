package host

import (
	"context"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

// ── Controller ──
// Matches Python's HostController class.
type Controller struct {
	repo Repository
}

func NewController(repo Repository) *Controller {
	return &Controller{repo: repo}
}

// RecordChanges persists host state changes to the database.
// Uses an atomic bulk insert, then deletes all prior sessions so only
// the latest backup remains.
//
// Matches Python's HostController.record_changes().
func (c *Controller) RecordChanges(
	ctx context.Context,
	changes []*model.HostStateChangeItem,
	sessionID *string,
	changeOrderOffset int,
) (string, error) {
	sid := sessionID
	if sid == nil || *sid == "" {
		s := crypto.UUIDV4()
		sid = &s
	}
	now := time.Now().Format(time.RFC3339)
	for order, change := range changes {
		change.SessionID = *sid
		change.InitTimestamp = now
		change.ChangeOrder = order + changeOrderOffset
		change.CreatedAt = now
	}
	if err := c.repo.AddChanges(ctx, changes); err != nil {
		return "", err
	}
	if err := c.repo.DeleteChangesExceptSession(ctx, *sid); err != nil {
		return "", err
	}
	return *sid, nil
}

// MarkInitialized marks host as fully initialized.
// Matches Python's HostController.mark_initialized().
func (c *Controller) MarkInitialized(ctx context.Context, timestamp string) error {
	if _, err := c.repo.InitializeState(ctx); err != nil {
		return err
	}
	return c.repo.SetInitialized(ctx, timestamp)
}

// ResetState resets all host state flags to False.
// Matches Python's HostController.reset_state().
func (c *Controller) ResetState(ctx context.Context) error {
	return c.repo.ResetState(ctx)
}
