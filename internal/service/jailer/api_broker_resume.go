package jailer

import (
	"context"
	"net"
)

// CALLER MUST HOLD: admitted active instance and authenticated API peer authority.
// Root state decides idempotence only after LockActive has refused pending export recovery.
func resumeVMAPI(ctx context.Context, connection net.Conn, vmID string) (returnErr error) {
	state, err := observeVMAPIState(ctx, connection, vmID)
	if err != nil {
		return err
	}

	switch state {
	case "Running":
		return nil
	case "Paused":
		return sendVMAPIOperation(ctx, connection, vmAPIResume)
	default:
		return instanceStateError(vmID, "VM resume requires authoritative running or paused state")
	}
}
