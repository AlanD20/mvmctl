// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/logs_operations.py exactly.
package api

import (
	"context"
	"fmt"

	"mvmctl/internal/core/logs"
	"mvmctl/internal/infra"
	"mvmctl/pkg/api/inputs"
)

// LogStream streams log lines for a VM synchronously via callback.
// Matches Python's LogOperation.stream() -> Generator[str] exactly.
//
// Python:
//
//	for line in LogOperation.stream(inputs):
//	    print(line)
//
// Go equivalent:
//
//	op.LogStream(ctx, inputs, func(line string) error {
//	    fmt.Println(line)
//	    return nil
//	})
//
// For "show" (non-follow): resolves input, reads lines from controller, invokes
// callback for each line, then returns. Blocks the caller's goroutine — no
// goroutine spawn necessary, matching Python's synchronous generator semantics.
//
// For "follow": resolves input, reads from the channel returned by the controller's
// FollowSync, invoking the callback for each line as it is received. Blocks until
// the channel is closed (ctx cancelled or error occurs).
func (op *Operation) LogStream(ctx context.Context, input *inputs.LogInput, callback func(string) error) error {
	// Python: resolved = LogRequest(inputs=inputs).resolve()
	//         controller = LogController(resolved.vm)
	//         if resolved.follow:
	//             yield from controller.follow(...)
	//         else:
	//             yield from controller.show(...)
	req := inputs.NewLogRequest(*input, op.Connection.DB())
	resolved, err := req.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return err
	}

	// Create LogController bound to the resolved VM.
	// Python: controller = LogController(resolved.vm)
	// Python's LogController.__init__: self._hash = vm.id if vm.id else vm.name
	vmDir := infra.GetVmDir(resolved.VM.ID)
	controller := logs.NewController(resolved.VM.ID, vmDir, resolved.VM.Name)

	if resolved.Follow {
		// Python: yield from controller.follow(...)
		// Synchronous: read from channels until they close.
		lineCh, errCh := controller.FollowSync(ctx, resolved.LogType,
			resolved.LogFilename, resolved.SerialOutputFilename)
		for line := range lineCh {
			if cbErr := callback(line); cbErr != nil {
				return cbErr
			}
		}
		if err := <-errCh; err != nil {
			return err
		}
		return nil
	}

	// Python: yield from controller.show(...)
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
//
// This wraps the controller calls in a goroutine and sends lines to the returned
// channel. Matches the original goroutine+channel pattern for callers that need
// it (e.g., integrating with event loops or select statements).
//
// For "show": goroutine reads lines, sends them to channel, closes channel.
// For "follow": goroutine follows the log file, sending lines as they arrive,
// until ctx is cancelled.
func (op *Operation) LogStreamChannel(ctx context.Context, input *inputs.LogInput) (<-chan string, error) {
	req := inputs.NewLogRequest(*input, op.Connection.DB())
	resolved, err := req.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return nil, err
	}

	// Create LogController bound to the resolved VM.
	vmDir := infra.GetVmDir(resolved.VM.ID)
	controller := logs.NewController(resolved.VM.ID, vmDir, resolved.VM.Name)

	ch := make(chan string, 100)

	go func() {
		defer close(ch)
		if resolved.Follow {
			// Python: yield from controller.follow(...)
			if err := controller.Follow(ctx, resolved.LogType, ch,
				resolved.LogFilename, resolved.SerialOutputFilename); err != nil {
				select {
				case ch <- fmt.Sprintf("Error following log: %v", err):
				case <-ctx.Done():
				}
			}
		} else {
			// Python: yield from controller.show(...)
			lines, err := controller.Show(ctx, resolved.LogType, resolved.Lines,
				resolved.LogFilename, resolved.SerialOutputFilename)
			if err != nil {
				select {
				case ch <- fmt.Sprintf("Error reading log: %v", err):
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

	return ch, nil
}
