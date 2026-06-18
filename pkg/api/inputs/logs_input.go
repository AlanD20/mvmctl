package inputs
import (
	"context"
	"fmt"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"github.com/jmoiron/sqlx"
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
	VM                   *model.VM
	LogType              string
	Lines                int
	Follow               bool
	LogFilename          string
	SerialOutputFilename string
}
// LogRequest specifies log request.
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
// Resolve resolves all inputs to explicit values.
func (r *LogRequest) Resolve(ctx context.Context, vmRepo vm.Repository) (*ResolvedLogInput, error) {
	vmEntity, err := r.resolveVM(ctx, vmRepo)
	if err != nil {
		return nil, err
	}
	logType := r.resolveLogType()
	// Validate log_type before passing to service
	if logType != "boot" && logType != "os" {
		return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("Unknown log type '%s'. Valid: boot, os", logType))
	}
	lines := r.resolveLines(ctx)
	follow := r.resolveFollow(ctx)
	logFilenameStr, _ := r.cfg.GetString(ctx, "defaults.firecracker", "log_filename")
	serialOutputFilenameStr, _ := r.cfg.GetString(ctx, "defaults.firecracker", "serial_output_filename")
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
	// Use VMRequest pipeline for VM resolution.
	// Let VMNotFoundError propagate directly.
	vmRequest := NewVMRequest(VMInput{Identifiers: []string{r.input.Identifier}}, r.db, vmRepo)
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
	v, _ := r.cfg.GetInt(ctx, "settings.vm", "log_lines")
	return v
}
func (r *LogRequest) resolveFollow(ctx context.Context) bool {
	if r.input.Follow != nil {
		return *r.input.Follow
	}
	b, _ := r.cfg.GetBool(ctx, "settings.vm", "log_follow")
	return b
}
