// Package cli implements the full CLI command tree.
package cli

import (
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/db"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/system"
	libversion "mvmctl/internal/lib/version"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

// --- Global state ---
// Module-level references for shell completion and lazy initialization.

// opRef holds a reference to the Operation API for shell completion.
// Set during wiring in NewRootCmd.
var opRef *api.Operation

// --- Root command ---

// NewRootCmd creates the root command with all subcommands registered.
func NewRootCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:           infra.CLIName,
		Short:         "MicroVM Manager - Container speed, VM Isolation",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(c *cobra.Command, args []string) error {
			return c.Help()
		},
	}

	// Persistent flags: --verbose, --debug
	// SortFlags = false to preserve flag display order.
	cmd.PersistentFlags().SortFlags = false
	cmd.PersistentFlags().Bool("verbose", false, "Enable verbose output")
	cmd.PersistentFlags().Bool("debug", false, "Enable debug mode")

	// Version flag on root command only (not persistent).
	// Handled in RunE: PersistentPreRunE short-circuits when --version is set,
	// and the RunE prints the version.
	var showVersion bool
	cmd.Flags().BoolVar(&showVersion, "version", false, "Show version and exit")
	originalRunE := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if showVersion {
			fmt.Printf(
				"%s %s\n",
				infra.CLIName,
				libversion.FormatVersion(c.Context(), libversion.GetVersion(c.Context())),
			)
			return nil
		}
		return originalRunE(c, args)
	}

	// PersistentPreRunE: logging setup + DB check + root warning
	cmd.PersistentPreRunE = makePersistentPreRunE()

	// Override built-in help command to return error for unknown topics.
	// Cobra's default uses Run (not RunE) and silently returns 0 even for "help nonexistent".
	cmd.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Long: `Help provides help for any command in the application.
	Type ` + "`" + cmd.Use + " help [command]`" + ` for help about a command.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Root().Help()
			}
			target, _, err := c.Root().Find(args)
			if target == nil || err != nil {
				for _, suggestion := range c.Root().SuggestionsFor(args[0]) {
					fmt.Fprintf(c.ErrOrStderr(), "Did you mean this?\n\t%s\n", suggestion)
				}
				return fmt.Errorf("unknown help topic: %q", strings.Join(args, " "))
			}
			return target.Help()
		},
	})

	// Infrastructure subcommands: version, completion, run, self-update
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newCompletionCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(NewSelfUpdateCmd(op))

	// Store API reference for shell completion
	opRef = op

	// Domain commands require a fully initialized Operation. When op is nil
	// (e.g., "mvm run <service>" mode), we register only the infrastructure
	// commands above — version, completion, run.
	if op != nil {
		cmd.AddCommand(NewVMCmd(op, op))
		cmd.AddCommand(NewNetworkCmd(op, op))
		cmd.AddCommand(NewImageCmd(op, op))
		cmd.AddCommand(NewKernelCmd(op, op))
		cmd.AddCommand(NewBinaryCmd(op, op))
		cmd.AddCommand(NewKeyCmd(op, op))
		cmd.AddCommand(NewHostCmd(op))
		cmd.AddCommand(NewConfigCmd(op))
		cmd.AddCommand(NewConsoleCmd(op))
		cmd.AddCommand(NewLogsCmd(op))
		cmd.AddCommand(NewVolumeCmd(op, op))
		cmd.AddCommand(NewCacheCmd(op))
		cmd.AddCommand(NewSSHCmd(op))
		cmd.AddCommand(NewCpCmd(op))
		cmd.AddCommand(NewExecCmd(op))
		cmd.AddCommand(NewInitCmd(op, op))
		cmd.AddCommand(NewEnvCmd(op))
		cmd.AddCommand(NewSnapshotCmd(op))
	}

	return cmd
}

// --- PersistentPreRunE ---
// Logging setup, DB check, and root warning before each command execution.

func makePersistentPreRunE() func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
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

		// Skip logging + DB setup for infrastructure commands (host, cache, help,
		// version, init, completion, run, tui). These commands work without a database.
		// We check the command path to match the first-level subcommand name.
		if shouldSkipPreRun(c) {
			return nil
		}

		// Warn if running as root
		if system.IsRoot() {
			_, escalated := infra.EnvGet("ESCALATED")
			if !escalated {
				common.Cli.Warning(
					fmt.Sprintf(
						"Warning: running as root. Consider using the '%s' group instead (set up via 'sudo %s host init').",
						infra.CLIName,
						infra.CLIName,
					),
				)
			}
		}

		// Setup debug mode and logging
		verbose, _ := c.Flags().GetBool("verbose")
		debug, _ := c.Flags().GetBool("debug")
		infra.SetDebugMode(debug)
		logging.SetupLogging(verbose, debug)

		// Check that the database exists. If not, suggest running 'mvm init'.
		if opRef != nil && !db.DBExists(opRef.CacheDir) {
			return fmt.Errorf("'%s %s' requires initialization. Run '%s init' first",
				infra.CLIName, subCmd, infra.CLIName)
		}

		return nil
	}
}

// shouldSkipPreRun checks if the command path should skip PersistentPreRunE
// setup. These infrastructure commands work without a database.
func shouldSkipPreRun(c *cobra.Command) bool {
	for cc := c; cc != nil; cc = cc.Parent() {
		if cc.Name() == "help" || cc.Name() == "version" || cc.Name() == "init" ||
			cc.Name() == "completion" || cc.Name() == "host" || cc.Name() == "cache" ||
			cc.Name() == "run" || cc.Name() == "tui" || cc.Name() == "self-update" {
			return true
		}
	}
	return false
}

// --- Subcommands (infrastructure) ---

// newVersionCmd creates the version subcommand.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the version and exit",
		RunE: func(c *cobra.Command, args []string) error {
			fullVersion := libversion.FormatVersion(c.Context(), libversion.GetVersion(c.Context()))
			gitInfo := libversion.GetGitVersionInfo(c.Context())

			fmt.Printf("%s %s\n", infra.CLIName, fullVersion)

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
}

// newCompletionCmd creates the completion subcommand.
// Uses Cobra's built-in completion generators instead of hardcoded scripts.
func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Generate shell completion script",
		Long: fmt.Sprintf(`Generate shell completion script for %[1]s.

Install completion by adding the output to your shell config:

    source <(%[1]s completion bash)

For zsh, place the output in a file on your fpath:

    %[1]s completion zsh > "${fpath[1]}/_%[1]s"

For fish:

    %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish`, infra.CLIName),
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(c *cobra.Command, args []string) error {
			shell := args[0]
			rootCmd := c.Root()
			switch shell {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				if err := rootCmd.GenFishCompletion(os.Stdout, true); err != nil {
					return err
				}
				// Add the _mvm_completion helper function expected by tests.
				// Wraps Cobra's __mvm_get_completions which is defined in the generated output above.
				fmt.Fprintln(os.Stdout)
				fmt.Fprintf(os.Stdout, "function _mvm_completion -d 'mvm completions'\n")
				fmt.Fprintf(os.Stdout, "    __mvm_get_completions (commandline -opc) (commandline -t)\n")
				fmt.Fprintf(os.Stdout, "end\n")
				return nil
			default:
				return fmt.Errorf("unsupported shell: %s (supported: bash, zsh, fish)", shell)
			}
		},
	}
}
