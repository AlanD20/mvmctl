// Package cli — internal service subprocess entry points.
package cli

import (
	"fmt"
	"os"

	"mvmctl/internal/service/console"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/loopmount"
	"mvmctl/internal/service/nocloudnet"

	"github.com/spf13/cobra"
)

// --- Run subcommand ---
// Each "mvm run <service>" command parses flags into the service's own Config
// type and calls the service's Run(ctx, cfg). The dependency direction is:
//
//	cli/ → services/
//
// Services never import cli/.

// newRunCmd creates the "run" subcommand for service entry points.
func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run internal services (subprocess entry points)",
	}

	cmd.AddCommand(newNoCloudNetCmd())
	cmd.AddCommand(newConsoleCmd())
	cmd.AddCommand(newProvisionCmd())
	cmd.AddCommand(newJailerServiceCmd())

	return cmd
}

func newJailerServiceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "jailer", Short: "Run the privileged Firecracker Jailer service"}
	for _, action := range []string{
		"install", "launch", "cleanup", "expose-snapshot", "expose-volume", "remove-volume", "remove-release",
	} {
		action := action
		sub := &cobra.Command{Use: action, Short: "Run the " + action + " Jailer operation"}
		sub.Flags().String("version", "", "Exact Firecracker release version")
		sub.Flags().String("arch", "", "Firecracker release architecture")
		sub.Flags().String("sha256", "", "Verified release archive SHA-256")
		sub.Flags().String("vm-id", "", "VM ID")
		sub.Flags().String("vm-dir", "", "Per-VM directory")
		sub.Flags().String("snapshot-dir", "", "Snapshot directory")
		sub.Flags().String("drive-id", "", "Firecracker drive ID")
		sub.Flags().String("host-path", "", "Managed volume path")
		sub.Flags().Bool("read-only", false, "Expose the managed volume read-only")
		sub.RunE = func(c *cobra.Command, _ []string) error {
			version, _ := c.Flags().GetString("version")
			arch, _ := c.Flags().GetString("arch")
			sha256, _ := c.Flags().GetString("sha256")
			vmID, _ := c.Flags().GetString("vm-id")
			vmDir, _ := c.Flags().GetString("vm-dir")
			snapshotDir, _ := c.Flags().GetString("snapshot-dir")
			driveID, _ := c.Flags().GetString("drive-id")
			hostPath, _ := c.Flags().GetString("host-path")
			readOnly, _ := c.Flags().GetBool("read-only")
			return jailer.Run(c.Context(), jailer.Config{
				Action: action, Version: version, Arch: arch, SHA256: sha256,
				VMID: vmID, VMDir: vmDir, SnapshotDir: snapshotDir,
				DriveID: driveID, HostPath: hostPath, ReadOnly: readOnly,
			})
		}
		cmd.AddCommand(sub)
	}
	return cmd
}

func newNoCloudNetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nocloudnet",
		Short: "nocloudnet HTTP metadata server commands",
	}

	cmd.AddCommand(newNoCloudNetServeCmd())
	return cmd
}

func newNoCloudNetServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve nocloudnet HTTP metadata",
		Long:  "Starts the nocloudnet HTTP metadata server for cloud-init. Runs in the foreground by default; pass --daemon to run as a background daemon process.",
	}

	cmd.Flags().String("cloud-init-dir", "", "Cloud-init seed directory (single-VM mode)")
	cmd.Flags().String("base-dir", "", "Shared batch directory (multi-VM mode)")
	cmd.Flags().Int("port", 0, "HTTP server port (required)")
	cmd.Flags().String("host", "", "Bind address (required)")
	cmd.Flags().String("log-file", "", "Log file path (required)")
	cmd.Flags().Duration("kill-after", 0, "Auto-kill after duration (e.g. 5m)")
	cmd.Flags().Bool("daemon", false, "Run as a background daemon process")
	cmd.MarkFlagRequired("port")
	cmd.MarkFlagRequired("host")
	cmd.MarkFlagRequired("log-file")

	cmd.RunE = func(c *cobra.Command, _ []string) error {
		cloudInitDir, _ := c.Flags().GetString("cloud-init-dir")
		baseDir, _ := c.Flags().GetString("base-dir")
		port, _ := c.Flags().GetInt("port")
		host, _ := c.Flags().GetString("host")
		logFile, _ := c.Flags().GetString("log-file")
		killAfter, _ := c.Flags().GetDuration("kill-after")
		daemon, _ := c.Flags().GetBool("daemon")

		cfg := nocloudnet.Config{
			CloudInitDir: cloudInitDir,
			BaseDir:      baseDir,
			Port:         port,
			Host:         host,
			LogFile:      logFile,
			KillAfter:    killAfter,
		}
		if cloudInitDir == "" && baseDir == "" {
			return fmt.Errorf("either --cloud-init-dir or --base-dir is required")
		}
		if daemon {
			_, err := nocloudnet.Spawn(c.Context(), cfg)
			return err
		}
		return nocloudnet.Run(c.Context(), cfg)
	}

	return cmd
}

func newConsoleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "console",
		Short: "Console relay commands",
	}

	cmd.AddCommand(newConsoleRelayCmd())
	return cmd
}

func newConsoleRelayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Run console relay",
	}

	cmd.Flags().String("vm-id", "", "VM ID (required)")
	cmd.Flags().String("vm-path", "", "VM path (required)")
	cmd.Flags().String("vm-name", "", "VM name")
	cmd.Flags().Int("pty-fd", 0, "PTY file descriptor (required)")
	cmd.Flags().Bool("daemon", false, "Run as a background daemon process")
	cmd.MarkFlagRequired("vm-id")
	cmd.MarkFlagRequired("vm-path")
	cmd.MarkFlagRequired("pty-fd")

	cmd.RunE = func(c *cobra.Command, _ []string) error {
		vmID, _ := c.Flags().GetString("vm-id")
		vmPath, _ := c.Flags().GetString("vm-path")
		vmName, _ := c.Flags().GetString("vm-name")
		ptyFD, _ := c.Flags().GetInt("pty-fd")
		daemon, _ := c.Flags().GetBool("daemon")

		cfg := console.Config{
			VMID:   vmID,
			VMPath: vmPath,
			VMName: vmName,
			PtyFD:  ptyFD,
		}
		if daemon {
			ptyFile := os.NewFile(uintptr(ptyFD), "pty")
			_, err := console.Spawn(c.Context(), cfg, ptyFile)
			return err
		}
		return console.Run(c.Context(), cfg)
	}

	return cmd
}

func newProvisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Run loopmount provision",
	}

	cmd.Flags().String("input-json", "", "Path to JSON input file (reads from stdin if omitted)")
	cmd.Flags().String("umount", "", "Path to unmount (shortcut, skips JSON input)")

	cmd.RunE = func(c *cobra.Command, _ []string) error {
		inputJSON, _ := c.Flags().GetString("input-json")
		umount, _ := c.Flags().GetString("umount")

		return loopmount.Run(c.Context(), loopmount.Config{
			InputJSON: inputJSON,
			Umount:    umount,
		})
	}

	return cmd
}
