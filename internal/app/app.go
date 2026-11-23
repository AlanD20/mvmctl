package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/timinglog"
	"mvmctl/internal/lib/db"
	"mvmctl/internal/lib/download"
	libversion "mvmctl/internal/lib/version"
	"mvmctl/pkg/api"
	"mvmctl/pkg/errs"
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

// --- Initialize ---

// Initialize sets up the application (cache dir, DB, migrations) and returns the
// Operation API handle and a cleanup function. For "mvm run <service>" commands,
// it returns nil, nil, nil — no DB or Operation is needed for subprocess services.
func Initialize(ctx context.Context) (op *api.Operation, cleanup func(), err error) {
	// "mvm run <service>" mode — skip all initialization.
	if len(os.Args) > 1 && os.Args[1] == "run" {
		return nil, nil, nil
	}

	// Logging and debug mode are set up later via cli/root.go's PersistentPreRunE —
	// NOT at import time or before CLI wiring.

	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return nil, nil, errs.WrapMsg(errs.CodeInternal, "cannot resolve cache directory", err)
	}

	// Check DB exists before non-exempt commands.
	dbPath := filepath.Join(cacheDir, infra.MVMDBFilename)
	if !isDBSkipCommand(os.Args) {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return nil, nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("run %s init first", infra.CLIName))
		}
	}

	database := db.New(dbPath)

	// Pending migration gate: block non-init commands when migrations are pending.
	if !isDBSkipCommand(os.Args) {
		pending, err := database.GetPendingMigrations(ctx)
		if err != nil {
			database.Close()
			return nil, nil, errs.WrapMsg(errs.CodeInternal, "failed to check pending migrations", err)
		}
		if len(pending) > 0 {
			database.Close()
			return nil, nil, errs.New(errs.CodeValidationFailed,
				fmt.Sprintf("pending migrations detected. Run %s init to apply them", infra.CLIName))
		}
	}

	// Set HTTP User-Agent.
	download.SetUserAgent(libversion.GetVersion(ctx))

	op = api.NewOperation(ctx, database, cacheDir)
	config.InitSettings()
	timinglog.SetTimingEnabled(os.Getenv("MVM_TIMING_ENABLED") == "1")

	cleanupFunc := func() { database.Close() }

	return op, cleanupFunc, nil
}
