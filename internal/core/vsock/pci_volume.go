package vsock

import (
	"context"
	"errors"
	"io"
	"time"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/agent"
	"mvmctl/pkg/errs"
)

// RemovePCIVolume asks the authenticated guest to release exactly one serial/capacity pair.
// CRITICAL: Success requires a matching terminal result and physical transport EOF.
func (c *Client) RemovePCIVolume(ctx context.Context, identity model.PCIVolumeIdentity) (returnErr error) {
	if err := identity.Validate(); err != nil {
		return err
	}
	conn, err := c.ensureAgent(ctx)
	if err != nil {
		return err
	}
	// Version probing selects the ordinary JSON protocol for that connection.
	// The closed PCI request must start on a fresh authenticated broker connection.
	if err := conn.Close(); err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	conn, err = c.dialRaw(ctx)
	if err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	defer func() { returnErr = errors.Join(returnErr, conn.Close()) }()
	deadline := time.Now().Add(20 * time.Second)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, agent.PCIVolumeMagic); err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	if err := agent.WritePCIVolumeRemoveRequest(
		ctx,
		conn,
		agent.PCIVolumeRemoveRequest{Token: c.item.Token, Volume: identity},
	); err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	result, err := agent.ReadPCIVolumeRemoveResult(ctx, conn)
	if err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	var trailing [1]byte
	if n, err := conn.Read(trailing[:]); n != 0 || err != io.EOF {
		return errs.New(errs.CodeVMStateInvalid, "guest volume release response is incomplete; retry the same detach")
	}
	if err := ctx.Err(); err != nil {
		return errs.Wrap(errs.CodeVsockConnectionFailed, err)
	}
	if result.Volume != identity || !result.Removed || result.Error != "" {
		return errs.New(
			errs.CodeVMStateInvalid,
			"guest refused selected volume release; unmount it or stop the VM before detaching",
		)
	}
	return nil
}
