// Package cli — "mvm cp" command — copy files between host and microVMs
package cli

import (
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

func NewCpCmd(cpAPI api.CPAPI) *cobra.Command {
	var (
		force  bool
		noSync bool
	)

	cmd := &cobra.Command{
		Use:   "cp [sources...] [destination]",
		Short: "Copy files between host and VM",
		Long: `Copy files between host and microVMs using vsock binary frame protocol.

Fast, secure file transfer over Firecracker's built-in vsock device —
no SSH, no guest dependencies, no network setup required.

Usage:

  # Copy local files to VM (directory mode — preserve source name)
  mvm cp ./myfile.txt my-vm:/root/

  # Copy local file to VM with exact destination name (file mode)
  mvm cp ./myfile.txt my-vm:/root/renamed.txt

  # Copy multiple local files to VM (requires directory destination)
  mvm cp file1.txt file2.txt file3.txt my-vm:/dst/
  mvm cp *.txt my-vm:/dst/         # Shell glob expands to multiple files

  # Copy file from VM to local (directory or file mode)
  mvm cp my-vm:/var/log/syslog ./syslog
  mvm cp my-vm:/var/log/syslog ./logs/        # Preserve remote filename

  # Copy between VMs
  mvm cp vm1:/data/file.txt vm2:/data/

Path format: use "vm_name:/remote/path" for VM paths,
plain "/local/path" for local paths.

The last positional argument is always the destination. Everything
before it is a source. For host-to-VM copies, end the destination
with a "/" to preserve the source filename (directory mode), or
omit the trailing "/" to use the exact destination path (file mode).
Multiple sources require a directory destination (trailing "/").`,
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --- Validation ---
			if len(args) < 2 {
				return fmt.Errorf("at least two arguments required: one or more sources and a destination")
			}

			input := inputs.CPInput{
				Sources: args[:len(args)-1],
				Dest:    args[len(args)-1],
				Force:   force,
				NoSync:  noSync,
			}

			// --- Progress ---
			prog := common.NewProgress()
			prog.Start("Copying...")

			result, cpErr := cpAPI.CPCopy(cmd.Context(), input, func(current, total int64) {
				if total > 0 {
					pct := int(100 * current / total)
					prog.UpdateText(fmt.Sprintf("Copying... %d%%", pct))
				}
			})

			prog.Stop()

			// --- Result handling ---
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

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing destination files")
	cmd.Flags().BoolVarP(&noSync, "no-sync", "", false, "Skip final sync() after transfer (faster but risks data loss on VM stop)")

	return cmd
}
