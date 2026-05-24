// Package cli implements the full CLI command tree matching Python's main.py.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/pkg/api"
)

// vmAPIRef holds a reference to the VM API for shell completion.
// Set during wiring in NewRootCmd.
var vmAPIRef *api.VMOperation

// networkAPIRef holds a reference to the Network API for shell completion.
var networkAPIRef *api.NetworkOperation

// imageAPIRef holds a reference to the Image API for shell completion.
var imageAPIRef *api.ImageOperation

// kernelAPIRef holds a reference to the Kernel API for shell completion.
var kernelAPIRef *api.KernelOperation

// binaryAPIRef holds a reference to the Binary API for shell completion.
var binaryAPIRef *api.BinaryOperation

// keyAPIRef holds a reference to the Key API for shell completion.
var keyAPIRef *api.KeyOperation

// volumeAPIRef holds a reference to the Volume API for shell completion.
var volumeAPIRef *api.VolumeOperation

// configAPIRef holds a reference to the Config API for shell completion.
var configAPIRef *api.ConfigOperation

// vmAPICtxKey is the context key for storing the VM API.
type vmAPICtxKey struct{}

// WithVMAPI stores the VM API in the context for shell completion.
func WithVMAPI(ctx context.Context, vmAPI *api.VMOperation) context.Context {
	return context.WithValue(ctx, vmAPICtxKey{}, vmAPI)
}

// GetVMAPIFromCmd retrieves the VM API from a command's context.
func GetVMAPIFromCmd(cmd *cobra.Command) *api.VMOperation {
	if vmAPI, ok := cmd.Context().Value(vmAPICtxKey{}).(*api.VMOperation); ok {
		return vmAPI
	}
	return nil
}

// NewRootCmd creates the root command matching Python's LazyMVMGroup + app().
func NewRootCmd(
	vmAPI *api.VMOperation,
	networkAPI *api.NetworkOperation,
	imageAPI *api.ImageOperation,
	kernelAPI *api.KernelOperation,
	binaryAPI *api.BinaryOperation,
	keyAPI *api.KeyOperation,
	hostAPI *api.HostOperation,
	configAPI *api.ConfigOperation,
	consoleAPI *api.ConsoleOperation,
	logAPI *api.LogOperation,
	volumeAPI *api.VolumeOperation,
	cacheAPI *api.CacheOperation,
	sshAPI *api.SSHOperation,
	cpAPI *api.CPOperation,
	initAPI *api.InitOperation,
	version string,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:           infra.CLIName,
		Short:         "MicroVM Manager - Container speed, VM Isolation",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(c *cobra.Command, args []string) error {
			return c.Help()
		},
	}

	// Persistent flags matching Python: --verbose, --debug
	// SortFlags = false to preserve flag display order consistent with Python's Click.
	cmd.PersistentFlags().SortFlags = false
	cmd.PersistentFlags().Bool("verbose", false, "Enable verbose output")
	cmd.PersistentFlags().Bool("debug", false, "Enable debug mode")

	// Version flag matching Python (not persistent, just on root)
	var showVersion bool
	cmd.Flags().BoolVar(&showVersion, "version", false, "Show version and exit")
	originalRunE := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if showVersion {
			// Python: click.echo(f"{_get_cli_name()} {_get_version()}")
			// Use canonical FormatVersion from infra, matching Python _get_version().
			fmt.Printf("%s %s\n", infra.CLIName, infra.FormatVersion(version))
			return nil
		}
		return originalRunE(c, args)
	}

	// PersistentPreRunE: logging setup + DB check + root warning matching Python app()
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		// Python's _version_callback is is_eager=True and runs BEFORE app().
		// Short-circuit everything when --version is set — no logging setup,
		// no DB check. Version output happens in RunE.
		if c.Flags().Changed("version") || c.Root().Flags().Changed("version") {
			return nil
		}

		// If this is the root (no subcommand), skip completely — help is shown in RunE
		subCmd := c.CalledAs()
		if subCmd == "" || subCmd == c.CommandPath() {
			return nil
		}

		// Matching Python app() lines 310-318: skip logging + DB setup for these commands.
		// Python uses ctx.invoked_subcommand which returns the first-level subcommand name
		// (e.g., "host" for "mvm host init"). In Cobra, we check the command path to match
		// Python's behavior of skipping ALL commands under 'host' and 'cache' groups.
		skipCmdPath := false
		for cc := c; cc != nil; cc = cc.Parent() {
			if cc.Name() == "help" || cc.Name() == "version" || cc.Name() == "init" ||
				cc.Name() == "completion" || cc.Name() == "host" || cc.Name() == "cache" {
				skipCmdPath = true
				break
			}
		}
		if skipCmdPath {
			return nil
		}

		// Warn if running as root (matching Python _warn_if_running_as_root)
		// Python uses env.get("ESCALATED") which with auto_envvar_prefix="MVM_" resolves to "MVM_ESCALATED".
		if os.Getuid() == 0 && os.Getenv("MVM_ESCALATED") == "" {
			common.MVMCLI.Warning(
				fmt.Sprintf("Warning: running as root. Consider using the '%s' group instead (set up via 'sudo %s host init').",
					infra.CLIName, infra.CLIName))
		}

		// Setup debug mode and logging (matching Python's set_debug_mode + setup_logging)
		verbose, _ := c.Flags().GetBool("verbose")
		debug, _ := c.Flags().GetBool("debug")
		infra.SetDebugMode(debug)
		infra.SetupLogging(verbose, debug)

		// Check that the database exists (matching Python lines 329-337).
		// Python: click.echo("Error: '...' requires initialization...", err=True); ctx.exit(1)
		dbPath := getDBPath()
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return fmt.Errorf("'%s %s' requires initialization. Run '%s init' first",
				infra.CLIName, subCmd, infra.CLIName)
		}

		return nil
	}

	// Custom help command matching Python help_cmd()
	cmd.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: fmt.Sprintf("Show help for %s or a subcommand", infra.CLIName),
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Root().Help()
			}

			root := c.Root()
			command := root
			for _, arg := range args {
				subCmd, _, err := command.Traverse([]string{arg})
				if err != nil || subCmd == nil || subCmd == command {
					// Python: click.echo(f"Unknown command: {' '.join(args)}", err=True) then ctx.exit(1)
					return fmt.Errorf("Unknown command: %s", strings.Join(args, " "))
				}
				command = subCmd
			}
			if err := command.Help(); err != nil {
				return err
			}
			return nil
		},
	})

	// Version subcommand matching Python version_cmd()
	// versionCmd matches Python version_cmd() in main.py lines 364-375.
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show the version and exit",
		RunE: func(c *cobra.Command, args []string) error {
			// Python: version = _get_version()
			// Use canonical FormatVersion from infra, matching Python _get_version().
			fullVersion := infra.FormatVersion(version)
			// Python: git_info = _get_git_version_info()
			gitInfo := infra.GetGitVersionInfo()

			// Python: click.echo(f"{_get_cli_name()} {version}")
			fmt.Printf("%s %s\n", infra.CLIName, fullVersion)

			// Python: if git_info: ... click.echo(...)
			if gitInfo != "" {
				if strings.HasPrefix(gitInfo, "git+") {
					fmt.Printf("  built from: %s\n", gitInfo[4:])
				} else {
					fmt.Printf("  tagged: %s\n", gitInfo)
				}
			}
			return nil
		},
	}
	cmd.AddCommand(versionCmd)

	// Shell completion subcommand matching Python completion_cmd()
	// Uses Cobra's built-in completion generators instead of hardcoded scripts.
	completionCmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion script",
		Long: fmt.Sprintf(`Generate shell completion script for %[1]s.

Install completion by adding the output to your shell config:

    source <(%[1]s completion bash)

For zsh, place the output in a file on your fpath:

    %[1]s completion zsh > "${fpath[1]}/_%[1]s"

For fish:

    %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish

For PowerShell:

    %[1]s completion powershell | Out-String | Invoke-Expression`, infra.CLIName),
		Args:      cobra.ExactValidArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(c *cobra.Command, args []string) error {
			shell := args[0]
			rootCmd := c.Root()
			switch shell {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletion(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell: %s (supported: bash, zsh, fish, powershell)", shell)
			}
		},
	}
	cmd.AddCommand(completionCmd)

	// Store API references for shell completion
	vmAPIRef = vmAPI
	networkAPIRef = networkAPI
	imageAPIRef = imageAPI
	kernelAPIRef = kernelAPI
	binaryAPIRef = binaryAPI
	keyAPIRef = keyAPI
	volumeAPIRef = volumeAPI
	configAPIRef = configAPI

	// Register all subcommand groups
	cmd.AddCommand(NewVMCmd(vmAPI, configAPI))
	cmd.AddCommand(NewNetworkCmd(networkAPI))
	cmd.AddCommand(NewImageCmd(imageAPI))
	cmd.AddCommand(NewKernelCmd(kernelAPI))
	cmd.AddCommand(NewBinaryCmd(binaryAPI))
	cmd.AddCommand(NewKeyCmd(keyAPI))
	cmd.AddCommand(NewHostCmd(hostAPI))
	cmd.AddCommand(NewConfigCmd(configAPI))
	cmd.AddCommand(NewConsoleCmd(consoleAPI))
	cmd.AddCommand(NewLogsCmd(logAPI))
	cmd.AddCommand(NewVolumeCmd(volumeAPI, configAPI))
	cmd.AddCommand(NewCacheCmd(cacheAPI))
	cmd.AddCommand(NewSSHCmd(sshAPI))
	cmd.AddCommand(NewCpCmd(cpAPI))
	cmd.AddCommand(NewInitCmd(initAPI))

	return cmd
}

// getDBPath returns the expected path to the mvm database file.
func getDBPath() string {
	cacheDir := os.Getenv("MVM_CACHE_DIR")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".cache", "mvmctl", infra.MVMDBFilename)
		}
		cacheDir = filepath.Join(home, ".cache", "mvmctl")
	}
	return filepath.Join(cacheDir, infra.MVMDBFilename)
}
