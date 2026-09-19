package loopmount

import (
	"context"

	"mvmctl/pkg/errs"
)

// Config is retained only to diagnose obsolete in-process service callers.
// Deprecated: Use the closed managed LoopMount client operations.
type Config struct {
	InputJSON string
	Umount    string
}

// Run refuses the retired raw-path wire service without reading input or performing effects.
// Deprecated: Use the closed managed LoopMount client operations.
func Run(context.Context, Config) error {
	return errs.New(errs.CodeLoopMountError,
		"the raw LoopMount provision service has been retired; use managed VM or image operations with a clean installation")
}
