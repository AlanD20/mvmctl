package loopmount

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"mvmctl/internal/lib/system"
)

// SpawnResult holds the result of a successful spawn.
type SpawnResult struct {
	Output *WireOutput
	PID    int
}

// Spawn runs the loopmount wire protocol as a subprocess.
// Marshals the WireInput to JSON and pipes it to the subprocess via stdin.
// Captures stdout, parses the WireOutput, and returns the result.
// Requires root privileges (loop mount operations), so SpawnConfig.Privileged
// is set to true — the subprocess runs via sudo.
func Spawn(ctx context.Context, cfg Config, input *WireInput) (*SpawnResult, error) {
	// Marhal input to JSON for stdin.
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal wire input: %w", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd, err := system.SpawnService(ctx, system.SpawnConfig{
		Name:       "provision",
		Privileged: true,
		Stdin:      bytes.NewReader(data),
		Stdout:     &stdoutBuf,
		Stderr:     &stderrBuf,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to spawn provision: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		stderr := stderrBuf.String()
		if stderr != "" {
			return nil, fmt.Errorf("provision subprocess failed: %s: %w", stderr, err)
		}
		return nil, fmt.Errorf("provision subprocess failed: %w", err)
	}

	pid := cmd.Process.Pid

	// Parse the JSON result from stdout.
	var output WireOutput
	if err := json.Unmarshal(stdoutBuf.Bytes(), &output); err != nil {
		return nil, fmt.Errorf("parse wire output: %s: %w", stdoutBuf.String(), err)
	}
	if output.Status == "error" {
		return &SpawnResult{PID: pid, Output: &output},
			fmt.Errorf("provision failed: %s (step: %s)", output.Error, output.Step)
	}

	return &SpawnResult{
		Output: &output,
		PID:    pid,
	}, nil
}
