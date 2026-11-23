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

// observeVMAPIState issues only the fixed state GET on an already admitted connection.
// CALLER MUST HOLD: active instance and authenticated API peer authority; caller owns Close.
func observeVMAPIState(ctx context.Context, connection net.Conn, vmID string) (_ string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return "", instanceValidationError("VM state observation requires a deadline")
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return "", vmAPIBrokerError("set VM state observation deadline", err)
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
		return "", vmAPIBrokerError("construct fixed VM state request", err)
	}
	// Reuse only this admitted connection; the observer never reconnects or issues a mutation.
	if err := request.Write(connection); err != nil {
		return "", vmAPIBrokerError("observe authoritative VM state", errors.Join(err, ctx.Err()))
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, 64<<10)), request)
	if err != nil {
		return "", vmAPIBrokerError("observe authoritative VM state", errors.Join(err, ctx.Err()))
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return "", vmAPIBrokerError("read authoritative VM state", errors.Join(err, ctx.Err()))
	}
	if response.StatusCode != http.StatusOK || len(payload) > 64<<10 {
		return "", instanceStateError(vmID, "VM state observation has no admitted response")
	}
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return "", instanceStateError(vmID, "VM state observation is malformed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return state.State, nil
}
