// Package cli implements the full CLI command tree matching Python's main.py.
package cli

import (
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/system"
	infraversion "mvmctl/internal/infra/version"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

// ── Global state ─────────────────────────────────────────────────────────────
// Matching Python's module-level helpers and lazy imports.

// opRef holds a reference to the Operation API for shell completion.
// Set during wiring in NewRootCmd.
var opRef *api.Operation

// ── Root command ─────────────────────────────────────────────────────────────
// Matching Python's LazyMVMGroup + app() + subcommands.

// NewRootCmd creates the root command matching Python's LazyMVMGroup + app().
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

	// Persistent flags matching Python: --verbose, --debug
	// SortFlags = false to preserve flag display order consistent with Python's Click.
	cmd.PersistentFlags().SortFlags = false
	cmd.PersistentFlags().Bool("verbose", false, "Enable verbose output")
	cmd.PersistentFlags().Bool("debug", false, "Enable debug mode")

	// Version flag matching Python (not persistent, just on root)
	// Python's _version_callback is is_eager=True and runs BEFORE app().
	// In Cobra, we handle it in RunE: the PersistentPreRunE short-circuits
	// when --version is set, and the RunE prints the version.
	var showVersion bool
	cmd.Flags().BoolVar(&showVersion, "version", false, "Show version and exit")
	originalRunE := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if showVersion {
			fmt.Printf(
				"%s %s\n",
				infra.CLIName,
				infraversion.FormatVersion(c.Context(), infraversion.GetVersion(c.Context())),
			)
			return nil
		}
		return originalRunE(c, args)
	}

	// PersistentPreRunE: logging setup + DB check + root warning matching Python app()
	cmd.PersistentPreRunE = makePersistentPreRunE()

	// Override built-in help command to return error for unknown topics.
	// Cobra's default uses Run (not RunE) and silently returns 0 even for "help nonexistent".
	// Matching Python: unknown help topics should exit with code 1.
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

	// Subcommands matching Python's version_cmd, completion_cmd
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newCompletionCmd())
	cmd.AddCommand(newRunCmd())

	// Store API reference for shell completion
	opRef = op

	// Domain commands require a fully initialized Operation. When op is nil
	// (e.g., "mvm run <service>" mode), we register only the infrastructure
	// commands above — version, completion, run.
	if op != nil {
		cmd.AddCommand(NewVMCmd(op))
		cmd.AddCommand(NewNetworkCmd(op))
		cmd.AddCommand(NewImageCmd(op))
		cmd.AddCommand(NewKernelCmd(op))
		cmd.AddCommand(NewBinaryCmd(op))
		cmd.AddCommand(NewKeyCmd(op))
		cmd.AddCommand(NewHostCmd(op))
		cmd.AddCommand(NewConfigCmd(op))
		cmd.AddCommand(NewConsoleCmd(op))
		cmd.AddCommand(NewLogsCmd(op))
		cmd.AddCommand(NewVolumeCmd(op, op))
		cmd.AddCommand(NewCacheCmd(op))
		cmd.AddCommand(NewSSHCmd(op))
		cmd.AddCommand(NewCpCmd(op))
		cmd.AddCommand(NewInitCmd(op))
	}

	return cmd
}

// ── PersistentPreRunE ────────────────────────────────────────────────────────
// Matching Python's app() callback in main.py lines 300-337.

func makePersistentPreRunE() func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		// Python's _version_callback is is_eager=True and runs BEFORE app().
		// Short-circuit everything when --version is set — no logging setup,
		// no DB check. Version output happens in RunE.
		if c.Flags().Changed("version") || c.Root().Flags().Changed("version") {
			return nil
		}

		// If this is the root (no subcommand), skip completely — help is shown in RunE
		// Matching Python: if ctx.invoked_subcommand is None: ctx.command.format_help(...); ctx.exit()
		subCmd := c.CalledAs()
		if subCmd == "" || subCmd == c.CommandPath() {
			return nil
		}

		// Matching Python app() lines 310-318: skip logging + DB setup for these commands.
		// Python uses ctx.invoked_subcommand which returns the first-level subcommand name
		// (e.g., "host" for "mvm host init"). In Cobra, we check the command path to match
		// Python's behavior of skipping ALL commands under 'host' and 'cache' groups.
		if shouldSkipPreRun(c) {
			return nil
		}

		// Warn if running as root (matching Python _warn_if_running_as_root)
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

		// Setup debug mode and logging (matching Python's set_debug_mode + setup_logging)
		verbose, _ := c.Flags().GetBool("verbose")
		debug, _ := c.Flags().GetBool("debug")
		infra.SetDebugMode(debug)
		logging.SetupLogging(verbose, debug)

		// Check that the database exists (matching Python lines 329-337).
		// Python: click.echo("Error: '...' requires initialization...", err=True); ctx.exit(1)
		if opRef != nil && !db.DBExists(opRef.CacheDir) {
			return fmt.Errorf("'%s %s' requires initialization. Run '%s init' first",
				infra.CLIName, subCmd, infra.CLIName)
		}

		return nil
	}
}

// shouldSkipPreRun checks if the command path should skip PersistentPreRunE
// setup. Matches Python app() lines 310-318 skip logic.
func shouldSkipPreRun(c *cobra.Command) bool {
	for cc := c; cc != nil; cc = cc.Parent() {
		if cc.Name() == "help" || cc.Name() == "version" || cc.Name() == "init" ||
			cc.Name() == "completion" || cc.Name() == "host" || cc.Name() == "cache" ||
			cc.Name() == "run" {
			return true
		}
	}
	return false
}

// ── Subcommands ──────────────────────────────────────────────────────────────
// Matching Python's help_cmd, version_cmd, completion_cmd.

// newVersionCmd creates the version subcommand matching Python's version_cmd().
func newVersionCmd() *cobra.Command {
	// Python: @click.command(name="version", help="Show the version and exit")
	return &cobra.Command{
		Use:   "version",
		Short: "Show the version and exit",
		RunE: func(c *cobra.Command, args []string) error {
			fullVersion := infraversion.FormatVersion(c.Context(), infraversion.GetVersion(c.Context()))
			gitInfo := infraversion.GetGitVersionInfo(c.Context())

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

// newCompletionCmd creates the completion subcommand matching Python's completion_cmd().
// Uses Cobra's built-in completion generators instead of hardcoded scripts.
func newCompletionCmd() *cobra.Command {
	// Python: @click.command(name="completion", help="Print shell completion script")
	return &cobra.Command{
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
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
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
			case "powershell":
				return rootCmd.GenPowerShellCompletion(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell: %s (supported: bash, zsh, fish, powershell)", shell)
			}
		},
	}
}
