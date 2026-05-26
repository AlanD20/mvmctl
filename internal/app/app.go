package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"mvmctl/internal/cli"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/cache"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/host"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/ssh"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/enricher"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/version"
	"mvmctl/pkg/api"
)

// ── Structs ─────────────────────────────────────────────────────────────────

// Version holds the current base version string, used for User-Agent headers
// and build metadata. Defaults to "0.1.0" (matching Python's __version__).
// Set via ldflags when building release binaries.
var Version = "0.1.0"

type repos struct {
	vm      vm.Repository
	network network.Repository
	lease   network.LeaseRepository
	image   image.Repository
	kernel  kernel.Repository
	binary  binary.Repository
	key     key.Repository
	volume  volume.Repository
	host    host.Repository
	config  config.SettingsRepository
}

type services struct {
	network *network.Service
	image   *image.Service
	kernel  *kernel.Service
	binary  *binary.Service
	key     *key.Service
	host    *host.Service
	config  *config.Service
	volume  *volume.Service
	cache   *cache.Service
	cp      *ssh.CPService
}

type controllers struct {
	host *host.Controller
}

type apis struct {
	config  *api.ConfigOperation
	binary  *api.BinaryOperation
	image   *api.ImageOperation
	kernel  *api.KernelOperation
	vm      *api.VMOperation
	key     *api.KeyOperation
	network *api.NetworkOperation
	host    *api.HostOperation
	console *api.ConsoleOperation
	log     *api.LogOperation
	volume  *api.VolumeOperation
	cache   *api.CacheOperation
	ssh     *api.SSHOperation
	cp      *api.CPOperation
	init    *api.InitOperation
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// isUnderSafeDir checks whether path is under one of the safe parent directories
// using proper path hierarchy comparison (matching Python's is_relative_to).
// Python uses Path.is_relative_to() which properly checks path hierarchy;
// this avoids the false-positive issue with string prefix matching
// (e.g., "/home/user1" incorrectly matching "/home/user").
func isUnderSafeDir(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	// rel == "." means path == parent
	// rel not starting with ".." means path is under parent
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// getCacheDir returns the MVM cache directory.
// Matches Python CacheUtils.get_cache_dir() in src/mvmctl/utils/common.py lines 193-219.
func getCacheDir() string {
	override := os.Getenv(infra.EnvKey("CACHE_DIR"))
	if override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			slog.Error("cannot resolve cache dir",
				"env", infra.EnvKey("CACHE_DIR"),
				"value", override,
				"error", err,
			)
			os.Exit(1)
		}
		// Python: resolved.is_relative_to(home), is_relative_to(tmp), is_relative_to(var_tmp)
		// Use isUnderSafeDir for proper path hierarchy comparison (avoids string prefix false positives).
		home, _ := os.UserHomeDir()
		homeResolved, _ := filepath.Abs(home)
		tmpResolved := "/tmp"
		varTmpResolved := "/var/tmp"
		underHome := isUnderSafeDir(resolved, homeResolved)
		underTmp := isUnderSafeDir(resolved, tmpResolved)
		underVarTmp := isUnderSafeDir(resolved, varTmpResolved)
		if !(underHome || underTmp || underVarTmp) {
			slog.Error("unsafe cache dir path",
				"env", infra.EnvKey("CACHE_DIR"),
				"value", override,
				"home", homeResolved,
			)
			os.Exit(1)
		}
		return resolved
	}

	// Python: _get_real_home() — when running under sudo, use SUDO_USER's home
	homeDir := ""
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser != "" {
		u, err := user.Lookup(sudoUser)
		if err == nil {
			homeDir = u.HomeDir
		}
	}
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			homeDir = "/root"
		}
	}
	return filepath.Join(homeDir, ".cache", "mvmctl")
}

func isDBSkipCommand(args []string) bool {
	// Python: ctx.invoked_subcommand in {"help", "version", "init", "completion", "host", "cache"}
	if len(args) < 2 {
		return false
	}
	cmd := args[1]
	switch cmd {
	case "help", "version", "init", "completion", "host", "cache", "run":
		return true
	}
	if strings.HasPrefix(cmd, "-") {
		return true
	}
	return false
}

// openDB opens the database, runs migrations, and returns the database handle.
func openDB(cacheDir string) *db.Database {
	dbCfg := db.Config{CacheDir: cacheDir}
	database := db.New(dbCfg)
	sqlDB, err := database.DB()
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	if _, err := db.RunMigrations(sqlDB); err != nil {
		slog.Error("failed to run database migrations", "error", err)
		os.Exit(1)
	}
	return database
}

// initRepos creates all repository instances.
func initRepos(database *db.Database) repos {
	sqlDB, err := database.DB()
	if err != nil {
		slog.Error("failed to get database handle for repos", "error", err)
		os.Exit(1)
	}
	return repos{
		vm:      vm.NewRepository(sqlDB),
		network: network.NewRepository(sqlDB),
		lease:   network.NewLeaseRepository(sqlDB),
		image:   image.NewRepository(sqlDB),
		kernel:  kernel.NewRepository(sqlDB),
		binary:  binary.NewRepository(sqlDB),
		key:     key.NewRepository(sqlDB),
		volume:  volume.NewRepository(sqlDB),
		host:    host.NewRepository(sqlDB),
		config:  config.NewRepository(sqlDB),
	}
}

// initServices creates all service instances.
func initServices(r repos, cacheDir string, db *sql.DB) services {
	return services{
		network: network.NewService(r.network, db),
		image:   image.NewService(r.image, cacheDir),
		kernel:  kernel.NewService(r.kernel, cacheDir),
		binary:  binary.NewService(r.binary, filepath.Join(cacheDir, "bin"), cacheDir),
		key:     key.NewService(r.key),
		host:    host.NewService(r.host),
		config:  config.NewService(r.config),
		volume:  volume.NewService(r.volume),
		cache:   cache.NewService(cacheDir),
		cp:      ssh.NewCPService(),
	}
}

// initControllers creates all controller instances.
func initControllers(r repos) controllers {
	return controllers{
		host: host.NewController(r.host),
	}
}

// initEnricher creates the cross-domain enricher.
func initEnricher(r repos) *enricher.Enricher {
	return enricher.New(
		r.vm,
		r.network,
		r.lease,
		r.image,
		r.kernel,
		r.binary,
		r.volume,
	)
}

// initAPIs creates all API operation instances in correct dependency order.
func initAPIs(r repos, s services, c controllers, enr *enricher.Enricher, cacheDir string, db *sql.DB) apis {
	configAPI := api.NewConfigOperation(s.config, r.config, db, cacheDir)
	binaryAPI := api.NewBinaryOperation(s.binary, r.vm, cacheDir, s.config, enr)
	imgAPI := api.NewImageOperation(s.image, db, cacheDir, s.config, enr)
	kernelAPI := api.NewKernelOperation(s.kernel, r.vm, cacheDir, s.config, db, enr)
	vmAPI := api.NewVMOperation(cacheDir, db, r.vm, r.network, r.image, r.kernel, r.binary, r.key, r.volume, enr)
	keyAPI := api.NewKeyOperation(s.key, r.key, r.vm, cacheDir)
	netAPI := api.NewNetworkOperation(s.network, r.network, r.lease, r.vm, enr, configAPI, r.host, cacheDir, db)
	hostAPI := api.NewHostOperation(r.host, s.host, c.host, r.network, s.network, r.config, s.config, r.vm, netAPI, cacheDir)
	consoleAPI := api.NewConsoleOperation(r.vm, db, cacheDir)
	logAPI := api.NewLogOperation(r.vm, db, cacheDir)
	volumeAPI := api.NewVolumeOperation(s.volume, r.volume, r.vm, cacheDir, db, enr)
	cacheAPI := api.NewCacheOperation(s.cache, r.vm, vmAPI, netAPI, imgAPI, kernelAPI, binaryAPI, s.binary, cacheDir, db, hostAPI)
	sshAPI := api.NewSSHOperation(db, r.vm, r.key, cacheDir)
	cpAPI := api.NewCPOperation(s.cp, r.vm, db, cacheDir, r.key)
	initAPI := api.NewInitOperation(hostAPI, cacheAPI, binaryAPI, netAPI, s.config, r.host, r.binary, cacheDir, db)

	return apis{
		config:  configAPI,
		binary:  binaryAPI,
		image:   imgAPI,
		kernel:  kernelAPI,
		vm:      vmAPI,
		key:     keyAPI,
		network: netAPI,
		host:    hostAPI,
		console: consoleAPI,
		log:     logAPI,
		volume:  volumeAPI,
		cache:   cacheAPI,
		ssh:     sshAPI,
		cp:      cpAPI,
		init:    initAPI,
	}
}

// executeCLI creates the root CLI command and executes it.
func executeCLI(ctx context.Context, a apis, version string) {
	rootCmd := cli.NewRootCmd(
		a.vm,
		a.network,
		a.image,
		a.kernel,
		a.binary,
		a.key,
		a.host,
		a.config,
		a.console,
		a.log,
		a.volume,
		a.cache,
		a.ssh,
		a.cp,
		a.init,
		version,
	)
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

	cacheDir := getCacheDir()

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

	database := openDB(cacheDir)
	defer database.Close()

	// Set HTTP User-Agent matching Python's HTTP_USER_AGENT = f"{CLI_NAME}/{_resolve_version()}".
	download.SetUserAgent(Version)

	// Sync version to infra package for components that reference it.
	version.SetBuildVersion(Version)

	repos := initRepos(database)

	sqlDB, err := database.DB()
	if err != nil {
		slog.Error("failed to get database handle", "error", err)
		os.Exit(1)
	}

	svcs := initServices(repos, cacheDir, sqlDB)
	config.InitSettings()
	ctrls := initControllers(repos)
	enr := initEnricher(repos)

	apis := initAPIs(repos, svcs, ctrls, enr, cacheDir, sqlDB)
	executeCLI(ctx, apis, Version)
}
