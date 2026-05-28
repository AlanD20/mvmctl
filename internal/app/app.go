package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/cli"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/core/config"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/download"
	infraversion "mvmctl/internal/infra/version"
	"mvmctl/pkg/api"
)

func isDBSkipCommand(args []string) bool {
	cmd := ""
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			cmd = a
			break
		}
	}
	switch cmd {
	case "", "help", "version", "init", "completion", "host", "cache", "run":
		return true
	}
	return false
}

// executeCLI creates the root CLI command and executes it.
func executeCLI(ctx context.Context, op *api.Operation) {
	rootCmd := cli.NewRootCmd(op)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// Delegate ALL error handling to the single shared handler in helpers.go.
		// This wraps the error back through HandleErrors so there is exactly ONE
		// place where errors are formatted for CLI output.
		common.HandleErrors(func() error { return err })()
	}
}

// ── Run ──────────────────────────────────────────────────────────────────────

func Run(ctx context.Context) {
	// Logging and debug mode are set up later via cli/root.go's PersistentPreRunE,
	// matching Python's app() which calls set_debug_mode(debug) and
	// setup_logging(verbose, debug) inside the Click group callback — NOT at
	// import time or before CLI wiring.

	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		slog.Error("cannot resolve cache dir",
			"error", err,
		)
		os.Exit(1)
	}

	// Python: Check DB exists before non-exempt commands — matching app() callback.
	// Python: if not CacheUtils.get_mvm_db_path().exists(): click.echo("Error: ...", err=True); ctx.exit(1)
	if !isDBSkipCommand(os.Args) {
		dbPath := filepath.Join(cacheDir, infra.MVMDBFilename)
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			slog.Error("not initialized",
				"cli", infra.CLIName,
				"command", os.Args[1],
				"hint", fmt.Sprintf("Run '%s init' first", infra.CLIName),
			)
			os.Exit(1)
		}
	}

	database := db.New(filepath.Join(cacheDir, infra.MVMDBFilename))
	defer database.Close()

	sqlDB, err := database.DB()
	if err != nil {
		slog.Error("failed to get database handle", "error", err)
		os.Exit(1)
	}

	// Pending migration gate: block non-init commands when migrations are pending.
	if !isDBSkipCommand(os.Args) {
		pending, err := db.GetPendingMigrations(sqlDB)
		if err != nil {
			slog.Error("failed to check pending migrations", "error", err)
			os.Exit(1)
		}
		if len(pending) > 0 {
			slog.Error("pending migrations detected",
				"count", len(pending),
				"hint", fmt.Sprintf("Run '%s init' to apply pending migrations", infra.CLIName),
			)
			os.Exit(1)
		}
	}

	// Set HTTP User-Agent matching Python's HTTP_USER_AGENT = f"{CLI_NAME}/{_resolve_version()}".
	download.SetUserAgent(infraversion.GetVersion())

	op := api.NewOperation(sqlDB, cacheDir)
	config.InitSettings()
	executeCLI(ctx, op)
}
