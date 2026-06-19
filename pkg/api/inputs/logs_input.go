package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// LogInput specifies log input.
type LogInput struct {
	Identifier string `json:"identifier"`
	OsLog      bool   `json:"os_log"`
	Lines      *int   `json:"lines,omitempty"`
	Follow     *bool  `json:"follow,omitempty"`
}

// ResolvedLogInput specifies resolved log input.
type ResolvedLogInput struct {
	LogType              string
	Lines                int
	Follow               bool
	LogFilename          string
	SerialOutputFilename string

	VM *model.VMItem
}

// Validate checks that the log input is valid.
func (i *LogInput) Validate() error {
	if i.Identifier == "" {
		return fmt.Errorf("VM identifier is required")
	}
	return nil
}

// Resolve resolves all inputs to explicit values.
func (i *LogInput) Resolve(ctx context.Context, cfg *config.Service, vmRepo vm.Repository) (*ResolvedLogInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Resolve VM identifier using the VM resolver.
	resolver := vm.NewResolver(vmRepo)
	vmEntity, err := resolver.Resolve(ctx, i.Identifier)
	if err != nil {
		return nil, err
	}
	// Resolve log type from input.
	logType := "boot"
	if i.OsLog {
		logType = "os"
	}
	if logType != "boot" && logType != "os" {
		return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("unknown log type '%s'. Valid: boot, os", logType))
	}
	// Resolve lines: input or config default.
	lines := 0
	if i.Lines != nil {
		lines = *i.Lines
	} else {
		lines, _ = cfg.GetInt(ctx, "settings.vm", "log_lines")
	}
	// Resolve follow: input or config default.
	follow := false
	if i.Follow != nil {
		follow = *i.Follow
	} else {
		follow, _ = cfg.GetBool(ctx, "settings.vm", "log_follow")
	}
	logFilenameStr, _ := cfg.GetString(ctx, "defaults.firecracker", "log_filename")
	serialOutputFilenameStr, _ := cfg.GetString(ctx, "defaults.firecracker", "serial_output_filename")
	return &ResolvedLogInput{
		VM:                   vmEntity,
		LogType:              logType,
		Lines:                lines,
		Follow:               follow,
		LogFilename:          logFilenameStr,
		SerialOutputFilename: serialOutputFilenameStr,
	}, nil
}
