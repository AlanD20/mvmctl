package inputs

import (
	"context"
	"fmt"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"

	"github.com/jmoiron/sqlx"
)

// LogInput matches Python's LogInput dataclass.
//
//	@dataclass
//	class LogInput:
//	    identifier: str
//	    os_log: bool = False
//	    lines: int | None = None
//	    follow: bool | None = None
type LogInput struct {
	Identifier string `json:"identifier"`
	OsLog      bool   `json:"os_log"`
	Lines      *int   `json:"lines,omitempty"`
	Follow     *bool  `json:"follow,omitempty"`
}

// ResolvedLogInput matches Python's ResolvedLogInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedLogInput:
//	    vm: VMInstanceItem
//	    log_type: str
//	    lines: int
//	    follow: bool
//	    log_filename: str
//	    serial_output_filename: str
type ResolvedLogInput struct {
	VM                   *model.VM
	LogType              string
	Lines                int
	Follow               bool
	LogFilename          string
	SerialOutputFilename string
}

// LogRequest matches Python's LogRequest.
//
// Resolve LogInput against the database and constants.
type LogRequest struct {
	cfg    *config.Service
	db     *sqlx.DB
	input  LogInput
	result *ResolvedLogInput
}

// NewLogRequest creates a new LogRequest.
func NewLogRequest(inputs LogInput, cfg *config.Service, db *sqlx.DB) *LogRequest {
	return &LogRequest{
		cfg:   cfg,
		db:    db,
		input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves all inputs to explicit values.
// Matches Python's LogRequest.resolve().
func (r *LogRequest) Resolve(ctx context.Context, vmRepo vm.Repository) (*ResolvedLogInput, error) {
	vmEntity, err := r.resolveVM(ctx, vmRepo)
	if err != nil {
		return nil, err
	}

	logType := r.resolveLogType()

	// Validate log_type before passing to service
	if logType != "boot" && logType != "os" {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "logs",
			Message: fmt.Sprintf("Unknown log type '%s'. Valid: boot, os", logType),
			Class:   errs.ClassValidation,
		}
	}

	lines := r.resolveLines(ctx)
	follow := r.resolveFollow(ctx)

	logFilenameStr := r.cfg.GetString(ctx, "defaults.firecracker", "log_filename", "")
	serialOutputFilenameStr := r.cfg.GetString(ctx, "defaults.firecracker", "serial_output_filename", "")

	r.result = &ResolvedLogInput{
		VM:                   vmEntity,
		LogType:              logType,
		Lines:                lines,
		Follow:               follow,
		LogFilename:          logFilenameStr,
		SerialOutputFilename: serialOutputFilenameStr,
	}

	return r.result, nil
}

func (r *LogRequest) resolveVM(ctx context.Context, vmRepo vm.Repository) (*model.VM, error) {
	// Use VMRequest pipeline like Python's LogRequest._resolve_vm()
	// Python lets VMNotFoundError propagate directly, so we don't wrap
	vmRequest := NewVMRequest(VMInput{Identifiers: []string{r.input.Identifier}}, r.db, vmRepo, nil)
	resolved, err := vmRequest.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return resolved.VMs[0], nil
}

func (r *LogRequest) resolveLogType() string {
	if r.input.OsLog {
		return "os"
	}
	return "boot"
}

func (r *LogRequest) resolveLines(ctx context.Context) int {
	if r.input.Lines != nil {
		return *r.input.Lines
	}
	return r.cfg.GetInt(ctx, "settings.vm", "log_lines", 0)
}

func (r *LogRequest) resolveFollow(ctx context.Context) bool {
	if r.input.Follow != nil {
		return *r.input.Follow
	}
	return r.cfg.GetBool(ctx, "settings.vm", "log_follow", false)
}
