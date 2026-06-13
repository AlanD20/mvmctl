package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/db"
	"mvmctl/internal/lib/download"
	libversion "mvmctl/internal/lib/version"
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

// ── Initialize ────────────────────────────────────────────────────────────────

// Initialize sets up the application (cache dir, DB, migrations) and returns the
// Operation API handle and a cleanup function. For "mvm run <service>" commands,
// it returns nil, nil, nil — no DB or Operation is needed for subprocess services.
func Initialize(ctx context.Context) (op *api.Operation, cleanup func(), err error) {
	// "mvm run <service>" mode — skip all initialization.
	if len(os.Args) > 1 && os.Args[1] == "run" {
		return nil, nil, nil
	}

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
	dbPath := filepath.Join(cacheDir, infra.MVMDBFilename)
	if !isDBSkipCommand(os.Args) {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			slog.Error("not initialized",
				"cli", infra.CLIName,
				"command", os.Args[1],
				"hint", fmt.Sprintf("Run '%s init' first", infra.CLIName),
			)
			os.Exit(1)
		}
	}

	database := db.New(dbPath)

	// Pending migration gate: block non-init commands when migrations are pending.
	if !isDBSkipCommand(os.Args) {
		pending, err := database.GetPendingMigrations(ctx)
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
	download.SetUserAgent(libversion.GetVersion(ctx))

	op = api.NewOperation(ctx, database, cacheDir)
	config.InitSettings()
	infra.SetTimingEnabled(os.Getenv("MVM_TIMING_ENABLED") == "1")

	cleanupFunc := func() { database.Close() }

	return op, cleanupFunc, nil
}
