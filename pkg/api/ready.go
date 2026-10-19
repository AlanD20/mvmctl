package api

import (
	"context"
	"time"

	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// ReadyAPI exposes bounded, noninteractive guest readiness checks.
type ReadyAPI interface {
	ExecReady(context.Context, inputs.ReadyInput) (*results.ExecResult, error)
}

// ExecReady executes one readiness attempt within the caller's remaining total budget.
func (op *Operation) ExecReady(ctx context.Context, input inputs.ReadyInput) (*results.ExecResult, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, input.Budget())
	defer cancel()
	execInput := inputs.ExecInput{Identifier: input.Identifier, Command: input.Command, User: input.User}
	resolved, err := execInput.Resolve(ctx, op.Repos.VM, op.Repos.Vsock)
	if err != nil {
		return nil, err
	}
	user := resolved.User
	if user == "" {
		user, err = op.Services.Config.GetString(ctx, "defaults.vm", "vsock_user")
		if err != nil {
			return nil, err
		}
	}
	client, err := op.newVsockClient(ctx, resolved.VsockItem, input.Budget(), resolved.VM.Name)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline() // WithTimeout above always establishes a deadline.
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	seconds := int(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	result, err := client.ExecReady(ctx, input.Command, user, seconds)
	if err != nil {
		return nil, err
	}
	return &results.ExecResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode}, nil
}
