package infra

import (
	"fmt"
	"time"

	"mvmctl/internal/infra/errs"
)

// OnProgressCallback is a callback for ProgressEvent emissions.
// Matches Python's Callable[[ProgressEvent], None].
type OnProgressCallback func(event errs.ProgressEvent)

// FormatBytesHR formats bytes using binary (IEC) units — e.g. "512 B", "4.2 MiB", "1.5 GiB".
// Mirrors Python's CommonUtils.format_bytes_human_readable().
func FormatBytesHR(sizeBytes int64) string {
	if sizeBytes < 1024 {
		return fmt.Sprintf("%d B", sizeBytes)
	}
	f := float64(sizeBytes)
	for _, unit := range []string{"KiB", "MiB", "GiB"} {
		f /= 1024
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, unit)
		}
	}
	return fmt.Sprintf("%.1f TiB", f)
}

// DownloadProgressBridge bridges raw HTTP download progress into ProgressEvent emissions.
// Takes an onProgress callback (that consumes ProgressEvent) and returns a
// (current, total) callback suitable as a ProgressCallback for HTTP downloads.
//
// The returned callback is throttled — it only emits a new event when the download
// percentage actually changes. After the first second of transfer the message
// also includes total size, current progress, and download speed.
//
// Mirrors Python's OperationUtils.download_progress_bridge().
//
// Go's time.Now() includes a monotonic clock reading embedded in the
// returned Time value. When you call time.Since(t) or t.Sub(t2), Go
// transparently uses the monotonic difference, matching Python's
// time.monotonic() semantics and avoiding issues with wall clock jumps
// (NTP, suspend/resume).
func DownloadProgressBridge(onProgress OnProgressCallback) ProgressCallback {
	if onProgress == nil {
		return nil
	}

	var totalStr string
	lastPct := 0
	startedAt := time.Time{}

	return func(current, total int64) {
		if total <= 0 {
			return
		}

		now := time.Now()
		if startedAt.IsZero() {
			startedAt = now
			totalStr = FormatBytesHR(total)
		}

		pct := int(100 * current / total)
		if pct == lastPct {
			return
		}
		lastPct = pct

		elapsed := now.Sub(startedAt)
		parts := fmt.Sprintf("Downloading... %d%%  (%s/%s)", pct, FormatBytesHR(current), totalStr)

		if elapsed >= time.Second {
			speed := int64(float64(current) / elapsed.Seconds())
			parts += fmt.Sprintf("  ·  %s/s", FormatBytesHR(speed))
		}

		onProgress(errs.ProgressEvent{
			Phase:   "download",
			Status:  "running",
			Message: parts,
		})
	}
}
