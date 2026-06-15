// Package cli — "mvm cp" command — copy files between host and microVMs
package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

func NewCpCmd(cpAPI api.CPAPI) *cobra.Command {
	var user string
	var key string
	var force bool

	cmd := &cobra.Command{
		Use:   "cp [sources...] [destination]",
		Short: "Copy files between host and VM",
		Long: `Copy files between host and microVMs using tar-over-SSH.

Uses tar on both sides — no guest dependencies beyond POSIX-mandated tar.

Usage:

  # Copy local files to VM
  mvm cp ./myfile.txt my-vm:/root/

  # Copy multiple local files to VM
  mvm cp file1.txt file2.txt file3.txt my-vm:/dst/
  mvm cp *.txt my-vm:/dst/         # Shell glob expands to multiple files

  # Copy file from VM to local
  mvm cp my-vm:/var/log/syslog ./syslog

  # Copy between VMs
  mvm cp vm1:/data/file.txt vm2:/data/

Path format: use "vm_name:/remote/path" for VM paths,
plain "/local/path" for local paths.

The last positional argument is always the destination. Everything
before it is a source. Multiple sources only work for host -> VM.`,
		// Use ArbitraryArgs so our manual validation below runs and matches
		// Python's explicit len(args_list) < 2 check with click.Abort().
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		// No simple positional completion — cp args are mixed source/dest paths
		RunE: func(cmd *cobra.Command, args []string) error {
			// ── Validation: match Python's explicit len check ──────────────
			// Python:
			//   if len(args_list) < 2:
			//       mvm_cli.error("At least two arguments...")
			//       raise click.Abort()  → SystemExit(2)
			if len(args) < 2 {
				return fmt.Errorf("at least two arguments required: one or more sources and a destination")
			}

			// Convert string to *string for optional fields (matching Python's str | None = None)
			var userPtr, keyPtr *string
			if user != "" {
				userPtr = &user
			}
			if key != "" {
				keyPtr = &key
			}
			input := inputs.CPInput{
				Sources: args[:len(args)-1],
				Dst:     args[len(args)-1],
				User:    userPtr,
				Key:     keyPtr,
				Force:   force,
			}

			// ── Progress tracking ──────────────────────────────────────────
			// Match Python's Rich Progress with transient=True.
			// Python:
			//   progress = Progress(
			//       SpinnerColumn(),
			//       TextColumn("[progress.description]{task.description}"),
			//       BarColumn(),
			//       TransferSpeedColumn(),
			//       transient=True,
			//   )
			//   with progress:
			//       task = progress.add_task("Copying...", total=None)
			//       def on_progress(chunk):
			//           if progress.tasks[0].total is None:
			//               progress.update(task, total=chunk)
			//           progress.update(task, advance=chunk)
			//
			// This sets total=first_chunk on first data, then advances
			// completed past total, so the bar is immediately full and
			// stays full (Rich clamps ratio at 1.0).

			var (
				mu            sync.Mutex
				totalBytes    int64
				transferTotal int64
			)

			startTime := time.Now()
			progressTicker := time.NewTicker(100 * time.Millisecond)
			done := make(chan struct{})

			// Spinner characters matching Rich's default "dots" spinner style.
			spinnerChars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

			// Start progress display goroutine.
			// Matches Python's rich.progress.Progress(transient=True) —
			// the progress display disappears on completion (clear line via \r).
			go func() {
				var spinIdx int
				for {
					select {
					case <-progressTicker.C:
						spinIdx++
						mu.Lock()
						tt := transferTotal
						total := totalBytes
						mu.Unlock()

						spinner := spinnerChars[spinIdx%len(spinnerChars)]

						if tt > 0 && total > 0 {
							// Proportional bar — matches Rich's BarColumn.
							// Python sets total=first_chunk_size, and since
							// advance also adds the first chunk, completed=total
							// = 100% from the very first data received.
							const barWidth = 30
							ratio := float64(tt) / float64(total)
							if ratio > 1.0 {
								ratio = 1.0
							}
							filled := int(ratio * float64(barWidth))
							bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

							// Transfer speed — matches Python's TransferSpeedColumn
							// which uses Rich's filesize() format with "/s" suffix.
							elapsed := time.Since(startTime).Seconds()
							var speedStr string
							if elapsed > 0 {
								speed := float64(tt) / elapsed
								speedStr = formatSpeed(speed)
							} else {
								speedStr = "0.0 B/s"
							}

							fmt.Fprintf(os.Stderr, "\r%s Copying... %s %s", spinner, bar, speedStr)
						} else {
							// Before first data — just show the spinner + description.
							// Rich would show an indeterminate/pulsing bar here, but
							// the first data arrives within the first 100ms tick so
							// this state is barely visible.
							fmt.Fprintf(os.Stderr, "\r%s Copying...", spinner)
						}

					case <-done:
						progressTicker.Stop()
						// transient=True: clear the progress line on completion.
						// Write spaces over the line then \r to position at start.
						fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))
						return
					}
				}
			}()

			// The progress callback receives (current, total) cumulatively.
			// total is the actual total bytes from the copy service.
			result, cpErr := cpAPI.CPCopy(cmd.Context(), input, func(current, total int64) {
				mu.Lock()
				transferTotal = current
				totalBytes = total
				mu.Unlock()
			})

			close(done)

			// ── Result handling: match Python's nuanced success/error ─────
			if cpErr != nil {
				return cpErr
			}

			// Success path
			msg := result.Message
			if msg == "" {
				msg = "Copy completed"
			}
			common.Cli.Success(msg)
			return nil
		},
	}

	cmd.Flags().StringVarP(&user, "user", "u", "", "SSH user for VM connections")
	cmd.Flags().StringVar(&key, "key", "", "SSH private key path or name")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing destination files")

	return cmd
}

// formatSpeed formats bytes-per-second as a human-readable transfer speed,
// matching Python's Rich TransferSpeedColumn which uses Rich's filesize()
// format with a "/s" suffix.
//
//	Rich filesize format:
//	  0 B  → "0.0 B/s"
//	  <1024 B   → "500 B/s"       (no decimal)
//	  <1024 KiB → "500.0 KiB/s"   (1 decimal)
//	  <1024 MiB → "5.2 MiB/s"     (1 decimal)
//	  ≥1024 GiB → "1.0 GiB/s"     (1 decimal)
func formatSpeed(bytesPerSec float64) string {
	if bytesPerSec < 1 {
		return "0.0 B/s"
	}
	if bytesPerSec < 1024 {
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
	if bytesPerSec < 1024*1024 {
		return fmt.Sprintf("%.1f KiB/s", bytesPerSec/1024)
	}
	if bytesPerSec < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MiB/s", bytesPerSec/(1024*1024))
	}
	return fmt.Sprintf("%.1f GiB/s", bytesPerSec/(1024*1024*1024))
}
