// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"mvmctl/internal/core/logs"
	"mvmctl/internal/infra"
	"mvmctl/pkg/api/inputs"
)

// LogAPI defines the public interface for log operations.
type LogAPI interface {
	LogStream(ctx context.Context, input inputs.LogInput, callback func(string) error) error
	LogStreamChannel(ctx context.Context, input inputs.LogInput) (lineCh <-chan string, errCh <-chan error, err error)
}

// LogStream streams log lines for a VM synchronously via callback.
// Callers use:
//
//	op.LogStream(ctx, inputs, func(line string) error {
//		fmt.Println(line)
//		return nil
//	})
//
// For "show" (non-follow): resolves input, reads lines from controller, invokes
// callback for each line, then returns. Blocks the caller's goroutine — no
// goroutine spawn necessary
// For "follow": resolves input, reads from the channel returned by the controller's
// FollowSync, invoking the callback for each line as it is received. Blocks until
// the channel is closed (ctx cancelled or error occurs).
func (op *Operation) LogStream(ctx context.Context, input inputs.LogInput, callback func(string) error) error {
	req := inputs.NewLogRequest(input, op.Services.Config, op.Connection.DB())
	resolved, err := req.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return err
	}
	// Create LogController bound to the resolved VM.
	vmDir := infra.GetVMDirByID(resolved.VM.ID)
	controller := logs.NewController(resolved.VM.ID, vmDir, resolved.VM.Name)
	if resolved.Follow {
		// Synchronous: read from channels until they close.
		// Create a cancelCtx so a callback error cancels the controller
		// goroutine — without this, the goroutine leaks when the callback
		// returns early (see Golang CodeReviewComments: Goroutine Lifetimes).
		followCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		lineCh, errCh := controller.FollowSync(followCtx, resolved.LogType,
			resolved.LogFilename, resolved.SerialOutputFilename)
		for line := range lineCh {
			if cbErr := callback(line); cbErr != nil {
				cancel()
				return cbErr
			}
		}
		if err := <-errCh; err != nil {
			return err
		}
		return nil
	}
	lines, err := controller.Show(ctx, resolved.LogType, resolved.Lines,
		resolved.LogFilename, resolved.SerialOutputFilename)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if cbErr := callback(line); cbErr != nil {
			return cbErr
		}
	}
	return nil
}

// LogStreamChannel streams log lines for a VM via a channel.
// Provides a goroutine+channel based version for callers that want asynchronous
// channel-based consumption rather than synchronous callback iteration.
// Returns:
// - lineCh: receives log lines as they arrive
// - errCh: receives a single runtime error (buffered, cap 1), closed on clean exit
// - err: setup error (e.g., VM resolution failure), nil on success
// For "show": goroutine reads lines, sends them to channel, closes channels when done.
// For "follow": goroutine follows the log file, sending lines as they arrive,
// until ctx is cancelled.
// The caller must consume from lineCh until it is closed, then check errCh
// for any runtime error.
func (op *Operation) LogStreamChannel(
	ctx context.Context,
	input inputs.LogInput,
) (lineCh <-chan string, errCh <-chan error, err error) {
	req := inputs.NewLogRequest(input, op.Services.Config, op.Connection.DB())
	resolved, err := req.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return nil, nil, err
	}
	// Create LogController bound to the resolved VM.
	vmDir := infra.GetVMDirByID(resolved.VM.ID)
	controller := logs.NewController(resolved.VM.ID, vmDir, resolved.VM.Name)
	ch := make(chan string, 100)
	ec := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(ec)
		if resolved.Follow {
			if err := controller.Follow(ctx, resolved.LogType, ch,
				resolved.LogFilename, resolved.SerialOutputFilename); err != nil {
				select {
				case ec <- err:
				case <-ctx.Done():
				}
			}
		} else {
			lines, showErr := controller.Show(ctx, resolved.LogType, resolved.Lines,
				resolved.LogFilename, resolved.SerialOutputFilename)
			if showErr != nil {
				select {
				case ec <- showErr:
				case <-ctx.Done():
				}
				return
			}
			for _, line := range lines {
				select {
				case ch <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, ec, nil
}
