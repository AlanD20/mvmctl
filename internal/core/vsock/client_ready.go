package vsock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"mvmctl/pkg/errs"
)

// ExecReady runs one guest check with bounded diagnostics and cancellation-aware transport.
// It uses the existing authenticated exec protocol without starting an agent upgrade.
func (c *Client) ExecReady(ctx context.Context, command, user string, timeout int) (result *ExecResult, err error) {
	defer func() {
		if err != nil {
			slog.Error("guest readiness check failed", "vm_id", c.item.VmID, "error", err)
		}
	}()
	conn, err := c.dialRaw(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Closing interrupts blocked writes and reads even when cancellation precedes the deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, errs.Wrap(errs.CodeVsockExecFailed, err)
		}
	}
	if err := SendFrame(conn, execRequest{
		ID: "1", Type: "ready", Command: command, Token: c.item.Token, User: user, Timeout: timeout, NoSync: true,
	}); err != nil {
		return nil, errs.Wrap(errs.CodeVsockExecFailed, err)
	}
	// Bound individual JSON frames as well as retained output; an endless output stream cannot grow memory.
	reader := bufio.NewReaderSize(conn, 1<<20)
	result = &ExecResult{}
	for {
		frame, err := reader.ReadSlice('\n')
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeVsockExecFailed, "read readiness response (maximum frame 1 MiB)", err)
		}
		var response execResponse
		if err := json.Unmarshal(frame, &response); err != nil {
			return nil, errs.Wrap(errs.CodeVsockExecFailed, err)
		}
		if response.ID != "1" {
			return nil, errs.New(errs.CodeVsockExecFailed, "readiness response identity mismatch")
		}
		switch response.Type {
		case "error":
			return nil, errs.New(errs.CodeVsockExecFailed, "readiness rejected by agent: "+response.Error)
		case "stdout":
			result.Stdout = readinessTail(result.Stdout, response.Data)
		case "stderr":
			result.Stderr = readinessTail(result.Stderr, response.Data)
		case "result":
			if response.Error != "" {
				return nil, errs.New(errs.CodeVsockExecFailed, "readiness agent error: "+response.Error)
			}
			result.ExitCode = response.Status
			return result, nil
		default:
			return nil, errs.New(errs.CodeVsockExecFailed, fmt.Sprintf("unexpected readiness frame %q", response.Type))
		}
	}
}

func readinessTail(previous, next string) string {
	const limit = 8192
	if len(next) >= limit {
		return next[len(next)-limit:]
	}
	if len(previous)+len(next) > limit {
		previous = previous[len(previous)+len(next)-limit:]
	}
	return previous + next
}
