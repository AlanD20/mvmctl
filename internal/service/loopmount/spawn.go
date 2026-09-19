package loopmount

import "context"

// SpawnResult describes the retired service's result shape.
// Deprecated: Use VMProvisionResult, ImageInspectResult, or ImageOptimizeResult.
type SpawnResult struct {
	Output *WireOutput
	PID    int
}

// Spawn refuses the retired raw-path service without starting a subprocess.
// Deprecated: Use the closed managed LoopMount client operations.
func Spawn(ctx context.Context, cfg Config, _ *WireInput) (*SpawnResult, error) {
	return nil, Run(ctx, cfg)
}
