package logs

import (
	"context"

	"mvmctl/internal/infra"
)

// Controller is a stateful log controller bound to a single VM.
// Matches Python's LogController exactly.
type Controller struct {
	vmHash string
	vmDir  string
	vmName string
	svc    *Service
}

// NewController creates a LogController bound to the given VM.
// In Python, the constructor takes a VMInstanceItem and extracts the hash
// from vm.id (falling back to vm.name if id is falsy).
// Here we accept the pre-resolved id/hash, VM directory path, and VM name.
// The hash is set to vmID first; if vmID is empty, it falls back to vmName.
func NewController(vmID, vmDir, vmName string) *Controller {
	hash := vmID
	if hash == "" {
		hash = vmName
	}
	return &Controller{
		vmHash: hash,
		vmDir:  vmDir,
		vmName: vmName,
		svc:    NewService(),
	}
}

// Show reads the last N lines from the VM's log file.
// Matches Python's LogController.show().
func (c *Controller) Show(
	ctx context.Context,
	logType string,
	lines int,
	logFilename, serialOutputFilename string,
) ([]string, error) {
	logFile, err := c.getLogPath(ctx, logType, logFilename, serialOutputFilename)
	if err != nil {
		return nil, err
	}
	return c.svc.ReadLogLines(logFile, lines)
}

// FollowSync follows log file lines synchronously, sending each newly written line to the
// returned channel. Returns a channel of log lines and a channel for errors.
// Matching Python's Generator[str] behavior:
//
//	for line in controller.follow(...):
//	    print(line)
func (c *Controller) FollowSync(
	ctx context.Context,
	logType, logFilename, serialOutputFilename string,
) (<-chan string, <-chan error) {
	logFile, err := c.getLogPath(ctx, logType, logFilename, serialOutputFilename)
	if err != nil {
		lineCh := make(chan string, 10)
		errCh := make(chan error, 1)
		errCh <- err
		close(lineCh)
		close(errCh)
		return lineCh, errCh
	}
	return c.svc.FollowLogSync(ctx, logFile)
}

// Follow streams log file lines in real-time.
// Lines are sent to the provided channel until context is cancelled.
// Matches Python's LogController.follow() — which returns a Generator[str].
func (c *Controller) Follow(
	ctx context.Context,
	logType string,
	lines chan<- string,
	logFilename, serialOutputFilename string,
) error {
	logFile, err := c.getLogPath(ctx, logType, logFilename, serialOutputFilename)
	if err != nil {
		return err
	}
	return c.svc.FollowLog(ctx, logFile, lines)
}

func (c *Controller) getLogPath(_ context.Context, logType, logFilename, serialOutputFilename string) (string, error) {
	// Resolve the VM directory from the vmHash (matching Python's
	// CacheUtils.get_vm_dir(vm_hash) in LogService.get_log_path()).
	// The hash is guaranteed non-empty by NewController's fallback logic.
	// Service.GetLogPath() checks:
	//   1. VM directory exists (raises "VM directory not found at {path}")
	//   2. Log file exists (raises "Log file not found for VM: {path}")
	vmDir := c.vmDir
	if vmDir == "" && c.vmHash != "" {
		vmDir = infra.GetVMDirByID(c.vmHash)
	}
	return c.svc.GetLogPath(vmDir, logType, logFilename, serialOutputFilename)
}
