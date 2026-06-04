// Package event provides the unified event types for all long-running
// operations (downloads, builds, VM creation, etc.). Every layer —
// infrastructure, service, API, CLI — emits and consumes event.Progress.
package event

import (
	"fmt"
	"time"
)

// Progress represents a progress update during a long-running operation.
// Fields are additive — use only what you need for each phase:
//
//	Simple status:  Phase + Status + Message
//	Download:       Phase + Status + Message + Current + Total
//	Explicit pct:   Phase + Status + Message + Percent
type Progress struct {
	Phase   string   `json:"phase"`
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Current int64    `json:"current,omitempty"`
	Total   int64    `json:"total,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
}

// OnProgressCallback is a callback that consumes progress events.
type OnProgressCallback func(Progress)

// OnDownloadCallback is a callback for raw byte-level download progress.
type OnDownloadCallback func(currentBytes, totalBytes int64)

// FormatBytesHR formats bytes using binary (IEC) units — e.g. "512 B", "4.2 MiB", "1.5 GiB".
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

// FormatProgress bridges raw HTTP download progress into progress events.
// Takes an onProgress callback (that consumes event.Progress) and returns a
// (current, total) callback suitable for HTTP downloads.
//
// The returned callback is throttled — it only emits a new event when the
// download percentage changes. After the first second the message also
// includes total size, current progress, and download speed.
func FormatProgress(onProgress OnProgressCallback) OnDownloadCallback {
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

		pct64 := float64(pct)
		onProgress(Progress{
			Phase:   "download",
			Status:  "running",
			Message: parts,
			Current: current,
			Total:   total,
			Percent: &pct64,
		})
	}
}
