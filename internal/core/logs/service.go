package logs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/pkg/errs"
)

const logFollowPollIntervalS = 0.3

// Service provides stateless log file operations.
// Matches Python's LogService exactly.
type Service struct{}

// NewService creates a new LogService.
func NewService() *Service {
	return &Service{}
}

// GetLogPath returns the full path to a VM's log file based on the log type.
// Matches Python's LogService.get_log_path() exactly — validates that:
//  1. VM directory exists (raises LogsError if not)
//  2. Log file exists (raises LogsError if not)
//
// logType: "boot" (serial console) or "os" (firecracker log).
// logFilename: the firecracker log filename (for "os" type).
// serialOutputFilename: the serial console output filename (for "boot" type).
func (s *Service) GetLogPath(vmDir string, logType, logFilename, serialOutputFilename string) (string, error) {
	// Validate VM directory exists (matches Python LogsError("VM directory not found at ..."))
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return "", errs.New(errs.CodeValidationFailed, "VM directory not found at "+vmDir)
	}

	var logFile string
	if logType == "boot" {
		logFile = filepath.Join(vmDir, serialOutputFilename)
	} else {
		logFile = filepath.Join(vmDir, logFilename)
	}

	// Validate log file exists (matches Python LogsError("Log file not found for VM: ..."))
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		return "", errs.New(errs.CodeValidationFailed, "log file not found for VM: "+logFile)
	}

	return logFile, nil
}

// ReadLogLines reads the last N lines from a log file.
// Matches Python's LogService.read_log_lines() — uses O(1) circular buffer
// (Python's deque(f, maxlen=lines) with modulo indexing).
func (s *Service) ReadLogLines(logFile string, lines int) ([]string, error) {
	f, err := os.Open(logFile)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error reading log file: %s", err), err)
	}
	defer f.Close()

	if lines < 0 {
		return nil, errs.New(errs.CodeValidationFailed, "maxlen must be non-negative")
	}
	if lines == 0 {
		return []string{}, nil
	}

	// O(1) circular buffer: pre-allocated slice with modulo indexing,
	// matching Python's deque(f, maxlen=lines).
	//
	// Use ReadString('\n') + TrimRight("\n") to match Python's
	// rstrip("\n") behavior exactly: strips only trailing \n
	// characters, preserving \r (Windows line endings).  Using
	// bufio.Scanner.Text() would strip both \r\n, which differs
	// from Python's rstrip("\n").
	buf := make([]string, lines)
	count := 0
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			// Strip trailing newline(s) matching Python's rstrip("\n").
			// ReadString('\n') includes the delimiter, so we strip \n
			// but leave any \r in place (Windows-style line endings).
			line = strings.TrimRight(line, "\n")
			buf[count%lines] = line
			count++
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error reading log file: %s", readErr), readErr)
		}
	}

	// Extract in order — oldest to newest.
	n := min(count, lines)
	result := make([]string, n)
	for i := range n {
		result[i] = buf[(count-n+i)%lines]
	}
	return result, nil
}

// FollowLogSync follows a log file synchronously (like tail -f), sending each newly
// written line to the returned channel. Returns a channel of log lines and a channel
// for errors (buffered with capacity 1). Spawns a goroutine that reads the file and
// sends lines on the line channel until ctx is cancelled, EOF is reached, or an error
// occurs. Both channels are closed when the goroutine exits.
// Matches Python's LogService.follow_log() exactly — synchronous Generator[str] behavior
// where each yield blocks until a new line is available:
//
//	while True:
//	    line = f.readline()
//	    if not line:
//	        time.sleep(LOG_FOLLOW_POLL_INTERVAL_S)
//	        continue
//	    yield line.rstrip("\n")
func (s *Service) FollowLogSync(ctx context.Context, logFile string) (<-chan string, <-chan error) {
	lineCh := make(chan string, 10)
	errCh := make(chan error, 1)

	go func() {
		defer close(lineCh)
		defer close(errCh)

		f, err := os.Open(logFile)
		if err != nil {
			select {
			case errCh <- errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error following log: %s", err), err):
			case <-ctx.Done():
			}
			return
		}
		defer f.Close()

		// Seek to end of file (like tail -f starting from current end)
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			select {
			case errCh <- errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error following log: %s", err), err):
			case <-ctx.Done():
			}
			return
		}

		reader := bufio.NewReader(f)
		pollInterval := time.Duration(logFollowPollIntervalS * float64(time.Second))

		for {
			line, readErr := reader.ReadString('\n')
			if len(line) > 0 {
				// ReadString includes the delimiter; strip trailing newline(s)
				// matching Python's line.rstrip("\n")
				line = strings.TrimRight(line, "\n")

				select {
				case lineCh <- line:
				case <-ctx.Done():
					return
				}
			}

			if readErr != nil {
				if readErr == io.EOF {
					// No new data — sleep and retry,
					// matching Python's time.sleep(LOG_FOLLOW_POLL_INTERVAL_S)
					select {
					case <-ctx.Done():
						return
					case <-time.After(pollInterval):
					}
					continue
				}
				select {
				case errCh <- errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error following log: %s", readErr), readErr):
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return lineCh, errCh
}

// FollowLog follows a log file in real-time (like tail -f).
// Lines are sent to the provided channel. Closing the context cancels.
// Matches Python's LogService.follow_log() exactly.
func (s *Service) FollowLog(ctx context.Context, logFile string, lines chan<- string) error {
	f, err := os.Open(logFile)
	if err != nil {
		return errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error following log: %s", err), err)
	}
	defer f.Close()

	// Seek to end of file (like tail -f starting from current end)
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error following log: %s", err), err)
	}

	reader := bufio.NewReader(f)
	pollInterval := time.Duration(logFollowPollIntervalS * float64(time.Second))

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			// ReadString includes the delimiter; strip trailing newline(s)
			// matching Python's line.rstrip("\n")
			line = strings.TrimRight(line, "\n")

			select {
			case lines <- line:
			case <-ctx.Done():
				return nil
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				// No new data — sleep and retry,
				// matching Python's time.sleep(LOG_FOLLOW_POLL_INTERVAL_S)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(pollInterval):
				}
				continue
			}
			return errs.WrapMsg(errs.CodeInternal, fmt.Sprintf("error following log: %s", readErr), readErr)
		}
	}
}
