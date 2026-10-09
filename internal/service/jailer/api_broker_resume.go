package jailer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// CALLER MUST HOLD: admitted active instance and authenticated API peer authority.
// Root state decides idempotence only after LockActive has refused pending export recovery.
func resumeVMAPI(ctx context.Context, connection net.Conn, vmID string) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return instanceValidationError("VM resume state observation requires a deadline")
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return vmAPIBrokerError("set VM resume observation deadline", err)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		// The caller owns Close; a deadline interrupts the retained connection without reconnecting.
		_ = connection.SetDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://firecracker/", nil)
	if err != nil {
		return vmAPIBrokerError("construct fixed VM resume state request", err)
	}
	// Keep the admitted connection for the possible PATCH; no reconnect or mutation replay.
	if err := request.Write(connection); err != nil {
		return vmAPIBrokerError("observe authoritative VM state before resume", errors.Join(err, ctx.Err()))
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, 64<<10)), request)
	if err != nil {
		return vmAPIBrokerError("observe authoritative VM state before resume", errors.Join(err, ctx.Err()))
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return vmAPIBrokerError("read authoritative VM state before resume", errors.Join(err, ctx.Err()))
	}
	if response.StatusCode != http.StatusOK || len(payload) > 64<<10 {
		return instanceStateError(vmID, "VM resume has no admitted running or paused state")
	}
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return instanceStateError(vmID, "VM resume state observation is malformed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch state.State {
	case "Running":
		return nil
	case "Paused":
		return sendVMAPIOperation(ctx, connection, vmAPIResume)
	default:
		return instanceStateError(vmID, "VM resume requires authoritative running or paused state")
	}
}
