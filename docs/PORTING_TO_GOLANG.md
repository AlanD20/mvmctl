# Porting mvmctl from Python to Go — Complete Specification

**Status:** Grilled & Resolved (see section 0 for change log)  
**Goal:** A unified Go CLI binary (`cmd/mvm`) that replicates all mvmctl functionality with idiomatic Go patterns, zero external runtime dependencies, and compile-time safety guarantees.

---

## 0. Grilling Session Resolutions

Changes made during the architectural grilling (2026-05-22):

| # | Ambiguity | Resolution |
|---|-----------|------------|
| 1 | Resolver DI wiring | Explicit wiring in `app.Run()` — open DB, create repos, wire enricher, wire services, wire CLI. No `init()` globals, no lazy factories. |
| 2 | Provision subcommand | Hidden `mvm _provision` subcommand in same binary. JSON stdin/stdout protocol identical to Python (with added index field for operation-result correlation). Sudoers scoped to `/path/to/mvm _provision`. |
| 3 | Context propagation | `signal.NotifyContext` in `main()`. CLI passes `cmd.Context()` through. No per-command timeouts at CLI layer — timeouts at lowest infrastructure layer (HTTP, subprocess) or explicit CLI flags (`--timeout`). |
| 4 | Enrichment pattern | Explicit Go code in `internal/enricher/` — switch/case per relation, no reflect, no string-based resolver dispatch. Enricher and `api/` are the only packages with cross-domain imports. |
| 5 | Package structure | `internal/core/{domain}/` for domains, `pkg/api/` for orchestration, `internal/cli/` for CLI, `internal/infra/` for leaf utilities, `internal/enricher/` for cross-domain enrichment. |
| 6 | Domain isolation | Core domains NEVER import other core domains. Enricher is sole exception (imported by api, not by core). |
| 7 | Error builder (E() variadic) | **Mixed approach** — common errors get helper functions (`errs.NotFound(code, entity)`), ad-hoc errors use struct literal directly. No `E()` variadic `any` builder. |
| 8 | Builder vs direct construction | **Exported fields** on resolved structs. Production uses Builder (validation + default resolution). Tests construct via Builder with in-memory repos; edge cases may construct struct literals directly with doc comment. Modeled after `zap.Config.Build()`. |
| 9 | Integration testing | **Two layers** — unit tests (`*_test.go` next to source) with interface mocks + in-memory repos, system tests (`tests/`) as black-box binary. No separate integration layer. |
| 10 | Subprocess mocking | **Interface injection** (`CommandRunner` interface in `infra/system/`). Inspired by Kubernetes `utilexec.Interface`. Tests use `testutil.FakeRunner`. |
| 11 | Version resolution infra | Pure utility in `infra/version/resolver.go`. HTTP-based resolvers (HttpDir, FirecrackerS3) in `infra/download/version.go`. Domain-specific wrappers in core domains. |
| 12 | Privilege model | **Same as Python** — warn if group not active, let sudo handle password prompting. `cmd.Stdin = os.Stdin` to forward TTY for sudo password input. Use `-n` flag for non-interactive subprocess calls (loopmount provisioner); do NOT use `-n` for commands that may need password prompt (host init). |
| 13 | Shared CLI flags | Cobra persistent flags on root command. `PersistentPreRunE` applies verbose/debug settings. `--json` is per-command flag. |
| 14 | CLI verb files | **Monolithic files per domain**, not split by verb. Each `internal/cli/{domain}.go` contains all subcommands for that domain (e.g., `vm.go` has create, start, stop, ls, ps, inspect, rm, snapshot, load, export, import, attach-volume, detach-volume, reboot, pause, resume). Reduces number of CLI files from ~60 to ~16 without sacrificing clarity — all VM commands in one file is manageable at ~500-800 lines. |
| 15 | Optional type mapping | **Strict `*T` for all `T | None`.** Every Python field declared as `str | None`, `int | None`, `bool | None`, etc. becomes `*string`, `*int`, `*bool` in Go. No exceptions for "smart defaults." Zero-value vs nil distinction must be preserved everywhere. |
| 16 | `@_graceful_read` decorator | **Abolished completely.** No `GracefulRead[T]` in Go. All DB read errors propagate explicitly. Python's silent-fallback-on-nonexistent-table behavior is gone. Callers must handle `error` from every repository read. |
| 17 | Timestamp format | **Use `time.RFC3339` constant.** No hardcoded format strings that duplicate stdlib constants. Microsecond precision loss is acceptable (Python `.isoformat()` has `.467308`, Go `time.RFC3339` drops it). All existing `"2006-01-02T15:04:05-07:00"` hardcoded strings must be replaced with `time.RFC3339`. |
| 18 | Logging strategy | **Extract to `internal/infra/logging/`** (directory, 3 files). Use Go standard `log/slog` throughout. `consoleHandler` (renamed from pythonLogHandler), `rotatingFileWriter`, `GetLogger()`, `SetupLogging()`, `LogException()` move from `io.go` to `logging/handler.go`, `logging/rotating.go`, `logging/setup.go`. No bare `log.Printf` or `fmt.Fprintf(os.Stderr)` for logging — all log output through `slog`. Documented strategy with level conventions. |
| 19 | Subprocess execution | **Consolidated `CommandRunner` interface in `infra/system/runner.go`.** Single interface: `Run(ctx, args []string, opts ...RunOption) (*Result, error)` and `Stream(ctx, args []string, opts ...RunOption) (<-chan StreamLine, error)`. `RealRunner` (zero-struct) + `testutil.FakeRunner`. Functional options pattern (`RunOption`) for timeout, cwd, env, stdin, capture, privileged. Existing `RunCmdCompat`/`RunCmd` replaced by this interface. `ProcessSignalHandler` stays in `runner.go` or moves to its own file. |
| 20 | Concurrent execution | **Consolidated to `internal/infra/parallel/`.** Two core functions: `Parallel[T](ctx, workers, items, fn)` for side-effect tasks (delete, stop, create), `Map[T,R](ctx, workers, items, fn)` for transform tasks (fetch, inspect). Both use goroutines + `sync.WaitGroup` + semaphore for bounded concurrency. Both accept `context.Context` for cancellation. Collect errors but continue on failure (matching Python's bulk operation behavior). Workers default to `runtime.NumCPU()*2` when ≤ 0. Replace dead `Execute()` stub with these functions. |
| 21 | Shared model types | **All model types centralized in `internal/infra/model/`.** No `[]any` for cross-domain references. Every domain type (VM, Network, Image, Kernel, Binary, Key, Volume, Host, CloudInit, Firecracker, Cache, Provisioner, Result, Version, Bulk, Console) lives in one package. Every other package (`core/*`, `pkg/api`, `internal/cli`, `internal/enricher`, `internal/infra/*`) imports from `infra/model` freely. Domain-level `model.go` files removed entirely — the single `infra/model/` package is the only source of truth for model types. This eliminates all circular import problems and restores type safety. |
| 22 | Template engine | **Go `text/template` only.** No Jinja2 compatibility. Cloud-init templates and any other templates adapted to Go syntax: `{{ .Field }}` instead of `{{ field }}`, `{{ if .Cond }}` instead of `{% if cond %}`, pipe functions instead of Jinja2 filters. All existing template files in `internal/assets/` updated to Go `text/template` syntax. |
| 23 | Password hashing | **bcrypt only via `golang.org/x/crypto/bcrypt`.** Remove the custom `sha512Crypt` implementation (self-reimplemented SHA-512 crypt). The `generatePasswordHash` function only supports `"bcrypt"` algorithm. Cloud-init `$6$` sha512 hashes not produced by this codebase — bcrypt `$2a$`/`$2b$` only. |
| 24 | Package naming conventions | **All packages follow Go convention: directory name = package name.** No underscored package names. Specific changes: (1) `internal/cli/_shared/` → `internal/cli/common/` (package `common`). (2) `internal/infra/errors/` → `internal/infra/errs/` (package `errs`) to avoid stdlib `"errors"` conflict. (3) Strip all `Xcore` import aliases (`binarycore`, `cachecore`, `configcore`, `hostcore`, `imagecore`, `kernelcore`, `keycore`, `networkcore`, `sshcore`, `vmcore`, `volumecore`) — use bare package names (`binary`, `cache`, `config`, `host`, `image`, `kernel`, `key`, `network`, `ssh`, `vm`, `volume`). Only keep aliases where stdlib collision exists. |
| 25 | `app.Run()` organization | **Split into focused private helpers within `app.go`.** Layer-separated: `openDB()`, `initRepos(database)`, `initServices(repos)`, `initControllers(repos)`, `initEnricher(repos)`, `initAPIs(...)`, `executeCLI(apis)`. Each returns a struct grouping its dependencies (e.g., `repos` struct with all 12 repos, `apis` struct with all 15 API operations). No DI framework. No 200-line flat function. Each helper is 20-30 lines, owns its layer, uses only the imports it needs. |
| 26 | Output layering | **Bare `log.Print/Printf/Println` and `fmt.Print/Printf/Fprintln/Fprintf(os.Stdout/Stderr)` BANNED in `internal/` (except `internal/cli/`) and `pkg/api/`.** Only `slog.Debug/Info/Warn/Error` for diagnostics. `internal/cli/` is the sole layer for user-facing output (`fmt.Print`, `cli.Info/Success/Error`). Hidden subprocess commands (nocloud, provision, console relay) are exempt — they run as standalone processes and write to their own stdout/stderr pipes. The `slog` routing infrastructure (file handler + stderr handler) is the ONLY output path below the CLI layer. |
| 27 | Reflection ban | **`reflect` package usage BANNED unless explicitly approved with an ADR.** All current `reflect` usage is poor 1:1 porting from Python's `isinstance()` / `getattr()` / `type()` and must be replaced with Go-native alternatives: (1) Error type checking → `errors.As()` / `errors.Is()`. (2) Struct field access → interfaces (`Namable`, `SubnetProvider`, etc.). (3) Type coercion → Generics or type switches. (4) CLI value rendering → Concrete type switches instead of `reflect.ValueOf().Kind()`. Exception path: if `reflect` is genuinely needed, it requires a documented ADR with explicit user approval. |
| 28 | Python type name ban | **No Python-style type names in Go code.** `getJSONTypeName()` and any similar function that returns `"dict"`, `"list"`, `"str"`, `"NoneType"` etc. is abolished. Use Go-native `fmt.Sprintf("%T", v)` for type names in error messages. Errors should be proper Go errors (`fmt.Errorf("expected map, got %T", v)`), not translations of Python's `type(x).__name__`. |
| 29 | `interface{}` / `any` governance | **Not banned outright, but requires justification per usage.** No more lazy `any`. Categories: (A) **BANNED** — model fields (`VMs []any` → `[]model.VM`), validator parameters (`interface{}` → interfaces or generics), coerce functions (`interface{}` → type switch on known types). (B) **ALLOWED with documentation** — `OperationResult.Item any` as intentional sum-type (document which types each operation sets), `BulkResult.Items` same pattern. (C) **REQUIRED by Go stdlib** — `yaml.Unmarshal(data, &result)` where `result` is `interface{}`, `json.Decoder.Decode(&v)`. Every `any`/`interface{}` must have a comment explaining why concrete typing isn't possible. |
| 30 | Shared utilities consolidation | **No private reimplementations of cross-domain utilities.** Code like `semverGreater`/`parseSemverInts` defined privately in `internal/core/binary/resolver.go` must be moved to `internal/infra/version/` and shared across all domains that need version comparison. Every domain (binary, kernel, image) imports from `infra/version`. Duplicate implementations found during audit must be consolidated to a single source. |
| 31 | Custom `String()` methods for Python compat | **No custom `String()` methods that replicate Python's `__str__` / `__repr__`.** Go's default `fmt.Sprintf("%+v", v)` or `%#v` provides struct representation for free. The `VersionSpec.String()` method manually building `"VersionSpec(major=X, minor=None)"` with Python `"None"` literals is abolished. Any `String()` method added in Go must serve a genuine Go purpose (human-readable summary), not mimic Python dataclass output. |
| 32 | Service goroutine placement | **Dedicated `internal/service/` package for goroutine-based services.** `internal/service/console/relay.go`, `internal/service/nocloudnet/server.go`, `internal/service/loopmount/provisioner.go`. These are long-running goroutines (HTTP server, PTY relay, JSON-protocol subcommand), not core domain logic. Core domains import from `service/`, not the reverse. Mirrors Python's `services/` directory structure 1:1. |
| 33 | Utility function consolidation | **Domain-scattered utility functions moved to `internal/infra/`.** Rule: if a function doesn't reference its domain's model types or repository, it belongs in infra. Specific moves: `resolvePath`, `accessRW`, `kernelRelease` → `infra/system/` or `infra/`. `groupExists`, `userInGroup`, `groupMembersViaNSS` → `infra/system/group.go`. `parsePortRange` → `infra/validators.go`. `uuidV4` → `infra/` or replaced with `crypto/rand`. `readInt` → `infra/io.go`. `dedent` → `infra/template.go`. `getDefaultCacheDir` → `infra/constants.go`. Domain-specific helpers (e.g., `generatePasswordHash`, `generateCryptSalt`, `validateTemplateData`) stay in their domain. |
| 34 | No duplicated data across packages | **`OverridableDefaults` must live in exactly ONE place: `internal/infra/constants.go`.** The duplicate copy in `internal/core/config/model.go` must be removed. Config domain imports `infra.OverridableDefaults` directly. Any other package that needs defaults imports from infra. This prevents data drift between copies. |
| 35 | Generator → Channel | Channels for continuous streams (console data, log lines). Direct return for batch operations. No callback pattern for batch iteration. |
| 36 | Broad `except Exception` | Explicit `if err != nil` per operation + log + continue. No `recover()` for control flow — only in top-level goroutine entry points to prevent crash propagation. |
| 37 | Default parameter values | Functional options pattern for configurable interfaces (variadic `Option` funcs). Config structs for 2-3 optionals. Separate functions for truly distinct behaviors. No default params on core interfaces. |
| 38 | `raise typer.Exit(1)` | Return errors to Cobra. No `os.Exit()` in CLI command handlers — Cobra's `SilenceErrors` handles exit code. `os.Exit` only in `app.Run()` for initialization failures (before Cobra runs). |
| 39 | Mixin classes | No mixins or OOP inheritance mimicry. Composition (struct fields) + shared package-level functions. Never embed types to simulate inheritance. |
| 40 | `@lru_cache` caching | **Abolished.** Python's `@lru_cache` on methods (template loading, kernel config, detector scoring) uses `sync.Once` in Go for single-computation patterns, or is simply removed where Go recomputes fast enough without caching. No `lru_cache` equivalent pattern — Go doesn't need it for these use cases. |
| 41 | **No implicit defaults** | Values MUST be passed explicitly by the caller. No fallback logic, no "if empty then guess" patterns. `if x == "" { x = default }` is banned unless explicitly approved via ADR. Python's `os.environ.get("VAR", "default")` must be resolved at the caller level and passed down. Constructors (New) take concrete values, not config structs with optional fields that have fallback logic. |
| 42 | **Domain `utils.go` for helpers** | Domain-specific utility functions that don't reference the `Service` struct or repository must live in `utils.go` within the domain package — NOT in `service.go`. This keeps `service.go` focused on orchestration methods and improves discoverability. Pattern: `service.go` = Service struct + orchestration methods, `utils.go` = pure functions, types, constants, error constructors. |
| 43 | **Cobra default flag parsing** | Do NOT use `FParseErrWhitelist{UnknownFlags: true}` or `DisableSuggestions: true`. Accept cobra's default behavior: unknown flags cause errors, and "did you mean X?" suggestions are shown. Python's permissive flag ordering is not replicated — cobra's strict parsing is preferred. |
| 44 | **No recomputation of existing values** | If a value is already stored in a variable at an earlier point in the same scope, reference that variable — do NOT recompute the expression to create a new variable holding the same value. Compute once, reference thereafter. Applies across all types, not just paths. |
| 45 | **CLI always shows table headers, even when empty** | Every `ls` command MUST always render the table with headers, even when there are zero items. No early returns with "No X found" messages. The table with empty body is the correct output. |
| 46 | **Deviations from Python CLI naming are allowed when they improve UX** | Example: `mvm key add` → `mvm key import`. Python names are not sacred — if Go naming is clearer, use it. Document the deviation. |
| 47 | **N+1 query prevention** | Always pass resolved domain objects through the pipeline instead of extracting identifiers and re-querying. Example: `GetPubkeys` receives `[]*model.SSHKeyItem` (with `PublicKeyPath` set) instead of `[]string` key names — zero DB queries instead of N individual lookups. This is a performance deviation from Python that preserves behavior. |
| 48 | **Short flags for common options** | Add short flags (`-a`, `-d`, `-f` etc.) for commonly-used options even when Python doesn't have them. CLI ergonomics trumps Python parity. Example: `--algorithm` gets `-a` even though Python's Click/Typer doesn't define it. |
| 49 | **Constraint registry is DI, not global singleton** | Python's `ConstraintRegistry` is a module-level singleton (`constraints = ConstraintRegistry()` in `_constraints.py`). Go version must NOT replicate this with a package-level var + `InitConstraints()`. Instead, create the registry explicitly in `NewOperation()`, register built-in constraints on it, and inject it into `config.NewService(repo, reg)`. This eliminates init-ordering bugs, nil-panic risks, and makes tests trivially injectable. |
| 50 | **PromptConfirm for destructive actions with --force bypass** | Destructive bulk operations (e.g., `config reset --all`) must prompt for confirmation via `common.Cli.PromptConfirm()`. A `--force` / `-f` flag MUST be provided to skip the prompt. Python doesn't have this pattern — it's a Go UX improvement for safety. |
| 51 | **Batch /proc reads into single syscalls** | Python's `HostDetector` reads `/proc/meminfo` per-field (4 reads), `/proc/cpuinfo` twice. Go consolidates these: `readMeminfo()` parses all fields in one pass, `/proc/cpuinfo` data is read once and reused for x86 + aarch64 fallback. Same behavior, fewer syscalls. This is an intentional performance deviation from Python. |
| 52 | **Privilege checking is infra, not core/host** | Python's `HostPrivilegeHelper` lives in `core/host/` because Python has no infra layer. In Go, privilege checking (`CheckPrivileges`, `SessionHasGroup`) is a cross-domain utility used by vm, cache, host, and network API layers — it has zero dependency on host domain types. Moved to `infra/system/privilege.go` as package-level functions (no empty struct). Removed dead Go additions: `IsRoot()`, `RequireRoot()`, `InMvmGroup()` (never called), and duplicate `SudoersDropInPath()`. |
| 53 | **Probe uses Detector data — no duplicate I/O** | `Probe.RunAll()` no longer reads `/proc/*` directly. It takes `*HostHardware`, `*HostLimits`, `*HostResources` as parameters. Each sub-check uses the detection models instead of re-reading system files. Example: `checkVMHost()` uses `hardware.CPUHasVMX` instead of parsing `/proc/cpuinfo` flags, `resources.DevKVMStatus` instead of stat, `limits.KernelMinimumMet` instead of regex+parse. This eliminates 3 redundant `/proc` reads and makes probe.go pure validation logic. |
| 54 | **`hostRunCmd` abolished** | Thin wrapper around `system.RunCmdCompat` with hardcoded `capture=true, check=true` at every call site. All 8 callers inlined to `system.RunCmdCompat(ctx, args, system.DefaultRunCmdOpts())` with explicit `fmt.Errorf` error wrapping. "Command not found" string matching removed in favor of direct error propagation. |
| 55 | **`Probe` kept as separate struct, not folded into Service** | `Probe` has zero dependencies (no repo, no DB, no Service). It takes detection data as parameters, runs pure validation, returns checks. Folding it into `Service` would misrepresent its stateless nature. Keep as `host.Probe{}` with `RunAll(hardware, limits, resources)`. |
| 56 | **Domain utilities in `utils.go`, not sprinkled in service.go** | `isModuleLoaded` moved from `service.go` to `utils.go` (pure utility, no Service dependency). `loadModule` converted to `(s *Service) loadModule` (uses `s.repo`). `EnsureKVMModules` converted to `(s *Service) EnsureKVMModules` (calls `s.loadModule`). Stale `TODO: Move to infra/` comments removed. |
| 57 | **No cargo-cult validation of constants** | Python validated the MVM group name with a regex because it was user-configurable. Go hardcodes it as `MVMUnixGroup = CLIName` (compile-time constant `"mvm"`). The regex `^[a-z][a-z0-9_-]{0,30}$` against a constant is dead code — removed along with the `regexp` import from `api/host.go`. Don't port validation that validates immutables. |
| 58 | **No duplicate content generation** | `GenerateSudoersContent` was called twice — once for the staleness check, once inside `WriteSudoers`. Changed `WriteSudoers` to take `content string` instead of `groupName string`. Caller generates content once, passes it to both the staleness check and `WriteSudoers`. Avoids redundant computation and keeps the content identical in both paths. |
| 59 | **`db.Database` → `db.Handle`** | Package `db` + type `Database` stutters (`db.Database`). Renamed to `db.Handle` following Go convention: the package name describes what it is, the type name describes what you do with it. A Handle is something you hold and use to access the database. |
| 60 | **Operation holds `Connection *db.Handle`, not `DB *sql.DB`** | The `Operation` struct holds the managed `*db.Handle` as field `Connection`. API methods call `op.Connection.DB()` to get the raw `*sql.DB` for constructors that still need it. Receiver methods like `RunMigrationsCtx` and `GetPendingMigrations` live on `*Handle`, keeping migration logic attached to the handle. These were package-level functions taking `*sql.DB` in the Python port. |
| 61 | **`Handle.DB()` returns `*sql.DB` directly — panics on failure** | `DB()` does NOT return `error`. The `openLazy()` failure (sql.Open driver validation) is practically impossible and unrecoverable. Every single call site would need the same 3-line error check. Removed the error return — `openLazy` panics on failure. Apply this pattern to any infrastructure method where failure means the application cannot function: don't propagate unrecoverable errors through every caller. |
| 62 | **`IsRoot()` uses `os.Geteuid()`, not `os.Getuid()`** | The kernel checks effective UID for permission decisions. `os.Geteuid() == 0` correctly detects root access via sudo, doas, or setuid binaries. Python's `os.getuid()` matches real UID behavior, but effective UID is the semantically correct check for "do I have root privileges?" All raw `os.Geteuid() != 0` / `os.Getuid() != 0` patterns replaced with `system.IsRoot()`. |
| 63 | **`ProbeCheck.Details` as `string`, not `*string` (exception to #15)** | `omitempty` on a `string` field omits `""` from JSON output — identical behavior to `*string` with `omitempty` omitting nil. The `*string` added unnecessary `new("...")` / `ptr.Str("...")` noise at every assignment site. Documented exception to verdict #15: use bare `string` with `omitempty` for optional string fields where empty string and "no value" are semantically the same. Only applies when the zero value is a valid "absent" state — NOT for numeric or boolean fields where 0/false have meaning. |
| 64 | **Ordered binary lists are `[...]string` arrays in infra, not slices** | `PrivilegedBinariesOrdered` lives in `infra/constants.go` as a `[...]string` array, not a `[]string` slice. Arrays prevent runtime mutation — adding/removing entries requires changing the map and the ordered list together, and the compiler enforces the length match. Callers use `append(arr[:], item)` to get a mutable copy. This pattern applies to any ordered collection that must stay synchronized with a map. |
| 65 | **Firewall chain names are compile-time constants, not runtime init** | `InitFirewallChains()` was a function that set package-level vars by calling `infra.MVMForwardChain()` etc — but was never called, leaving the vars at zero value. Replaced with typed compile-time constants: `infra.MVMForwardChain = "MVM-FORWARD"`, `network.FirewallChainMVMForward = model.FirewallChain(infra.MVMForwardChain)`. Zero runtime overhead, zero dead code paths. |
| 66 | **No dead type alias or const re-export cascades** | `internal/core/network/constants.go` had ~60 lines of type aliases (`type Network = model.Network`) and const re-exports (`const FirewallTableFilter = model.FirewallTableFilter`) that were transitional crutches. The port is done — every consumer already imports `infra/model` directly. All aliases removed. The 12 files in the network package now use `model.Network`, `model.FirewallTableNat`, etc directly. The 6 testutil files and 3 infra/firewall files also updated. |
| 67 | **Network Controller holds LeaseRepository** | `NewController` now takes `LeaseRepository` in addition to `Repository`. `GetLeases` uses `c.leaseRepo` directly instead of creating a new `LeaseRepository` from `*sql.DB` on every call. Removes the `db *sql.DB` parameter from `GetLeases` and the `database/sql` import from controller.go. |
| 68 | **`sqlx` for struct scanning instead of manual `database/sql`** | Overrules verdict #5's "No sqlx" rule. The entire codebase uses `github.com/jmoiron/sqlx` for all database operations. `db.Handle` stores and returns `*sqlx.DB` directly. Repo constructors and all helpers accept `*sqlx.DB`. No `*sql.DB` exists outside `internal/infra/db/`. All row scanning uses `StructScan`, `GetContext`, or `SelectContext` with `db:"column_name"` tags. |
| 69 | **Abolished dict-based row scanning (`scanRowToMap`)** | Python's `sqlite3.Row` dict-like access pattern (scan into `map[string]interface{}` via `scanRowToMap`, then type-assert to populate struct) is abolished. Every repo now uses direct struct scanning: `StructScan` for `vmScanRow` (VM with JSON fields), `sqlx.Get/Select` for all other model types. No reflection, no interface{} casts, no per-row allocation for maps. |
| 70 | **SQLite driver import lives in ONE place** | `_ "modernc.org/sqlite"` is imported ONLY in `internal/infra/db/connection.go` where `database/sql` is opened. Repos do NOT import the driver — they receive an already-configured `*sql.DB` from `db.Handle.DB()`. No stray driver imports across the codebase. |
| 71 | **nftables queries use explicit column lists with aliases** | `model.FirewallRule` uses `db:"chain_name"` (matching the `chain_name` column in `iptables_rules`). The `nftables_rules` table names this column `chain`, so nftables queries use `chain AS chain_name` and explicitly list all columns — excluding `nft_handle` which is not in the model struct. This lets one `FirewallRule` struct serve both backends via `StructScan`. |
| 72 | **JSON fields use intermediate scan struct, not direct model scan** | Model types with JSON-serialized fields (VM's `SSHKeys []string`, `VolumeIDs []string`, `CPUConfig *CpuConfig`) cannot be scanned by `sqlx` directly. These use a package-level scan struct (e.g., `vmScanRow`) with `db` tags, scanned via `StructScan`, then converted to the model type via a `toVM()` method that handles JSON deserialization. This is the only exception to direct `db` tag scanning. |
| 73 | **Scan helper functions inlined, not shared** | Shared `scanXxx` / `scanXxxs` helper functions (e.g., `scanVMs`) are inlined directly into each query method to reduce indirection. Each multi-row method owns its own `for rows.Next()` loop with `StructScan`. Single-row methods use `sqlx.GetContext` directly. No intermediate scan helpers that accept `*sql.Rows` or `*sql.Row`. |
| 74 | **Context required on EVERY database method** | Every repository method that touches the database takes `ctx context.Context` as its first parameter. All sqlx calls use the context-aware variants: `SelectContext`, `GetContext`, `ExecContext`, `BeginTxx`. Transactions use `BeginTxx(ctx, nil)` and `tx.ExecContext`. No bare `Select`, `Get`, `Exec`, or `Begin` anywhere outside the db connection layer. Context flows from Cobra command → API operation → service → repo → sqlx, enabling cancellation propagation end-to-end. |
| 75 | **Context propagates into infrastructure utilities** | All infrastructure functions that perform subprocess calls, network operations, or filesystem operations accept `ctx context.Context` and pass it to `system.RunCmdCompat` / subprocess calls. Functions that previously used `context.Background()` for subprocess calls now receive context from their callers, enabling cancellation and timeout propagation from API layer down to the lowest subprocess. Pure computation functions (string manipulation, parsing, hashing) do NOT take context — they have no external side effects. |
| 76 | **No pointless 1:1 deep copies of model types** | When a repository method already returns `*model.SomeItem` or `[]*model.SomeItem`, service/API code MUST NOT create a new `&model.SomeItem{...}` copying every field 1:1 from the result. Return the repo result directly. Exceptions: (a) the destination type is different from the source (scan-row conversion, type adaptation), (b) some fields are actually transformed (different value, computed field, status change), (c) fields are being added/filtered. Pure field-by-field copies with identical types are banned — they waste allocations and add maintenance burden when new fields are added to the model. |
| 77 | **Input structs use unified `Identifiers []string`** | All entity input structs (`VMInput`, `NetworkInput`, `ImageInput`, `KernelInput`, `BinaryInput`, `KeyInput`, `VolumeInput`) use a single `Identifiers []string` field instead of separate `Name []string`, `ID []string`, or `Type []string` fields. The resolver handles all identifier types (by ID, name, prefix). This matches the pattern established by `VMInput`. |
| 78 | **Entity ID generation uses `infra.HashGenerator`** | All entity ID generation MUST use `infra.HashGenerator{}` methods (`VM()`, `Network()`, `Image()`, `Kernel()`, `Binary()`, `Volume()`). No raw `sha256.Sum256` or `fmt.Sprintf("%x", ...)` for ID generation. The HashGenerator produces content-addressed SHA256 hashes matching Python's `mvmctl.utils.crypto.HashGenerator`. |
| 79 | **No `ToJSON` in API layer** | The API layer returns typed responses with JSON struct tags. CLI commands handle `--json` output by `json.MarshalIndent`-ing the typed response directly. No `ToJSON()` methods on API operations — they duplicate struct tag logic and bypass type safety. Exception: export/import serialization methods that produce a specific file format (e.g., `VMExportConfig.ToJSON()`). |
| 80 | **Infrastructure utilities in `infra/network/`, domain logic in `core/network/`** | Pure infrastructure functions (subprocess calls for bridge/TAP operations, IP math, system queries) live in `internal/infra/network/`. Domain-level logic (naming, bridge address computation, allocation strategies) stays in `internal/core/network/`. The `core/network/` package must not duplicate functions that already exist in `infra/network/` — use the `infranet.` import alias. |
| 81 | **Enrichment methods belong in service layer** | Cross-entity enrichment (e.g., attaching leases to networks) belongs in the domain service layer (`internal/core/network/service.go`), not in the API layer (`pkg/api/`). The API layer should call `svc.EnrichWithLeases(ctx, networks, leaseRepo)` rather than implementing the enrichment logic directly. |
| 82 | **No `goto` in Go code** | The `goto` keyword is banned in all new Go code. Patterns that use `goto` for guard clauses (e.g., Python's `shlex.quote()` port) must be rewritten as clean control flow with early returns or helper flags. |
| 83 | **No function name abbreviations in CLI** | CLI handler functions use full names: `runVMList` not `runVMLs`, `runBinaryList` not `runBinaryLs`. Cobra `Use` strings keep the standard CLI command names (`ls`, `ps`) since those are user-facing. Only internal Go function/variable names must be unabbreviated. |
| 84 | **CLI aliases: `ls` + `list`, `rm` + `remove` + `delete` + `del`** | Every `list` subcommand MUST have both `ls` (Use) and `list` (Alias). Every `remove` subcommand MUST have `rm` (Use) and `remove, delete, del` (Aliases). This ensures all four names work for any destroy action and both `ls` and `list` work for any listing action, regardless of which one individual commands chose during initial porting. |
| 85 | **`Nullable fields use `*T`, NOT if you can avoid it by using empty-sentinel`** | Flatten `*string` to `string` wherever possible using `""` as the zero-sentinel. Fields like `Distro`, `FSUUID`, `SHA256URL`, `SHA256` do NOT need pointers — empty string is a valid "not set" value. Only use `*T` when you genuinely need to distinguish between "not provided" and "provided as zero" (e.g., JSON deserialization with `omitempty` for CLI input structs). |
| 86 | **`Provisioner struct created once, backend session created once per Run()`** | The `Provisioner` struct is just config — create it once and reuse across all phases. The backend session (mount/launch) is created once inside `Run()` and shared across all phases (convert, deblob, shrink). Do NOT create a fresh `Provisioner` nor a fresh backend per phase — that causes unnecessary mount/umount cycles. Python's `ImageProvisioner.run()` created fresh backends per phase; that was wasteful and has been corrected. |
| 87 | **Service initialization enforced at startup** | `NewOperation()` validates ALL required services are non-nil and panics on nil. No nil checks on `op.Services.Config` or other required services anywhere in the API layer. If a service is genuinely optional, document that explicitly and add a typed method to check availability. |
| 88 | **Provisioner type resolved once at startup** | `Operation.ProvisionerType` is set in `NewOperation()` by reading `settings.guestfs_enabled` once. All callers use `op.ProvisionerType` directly instead of calling a resolver method. This eliminates repeated Config.Get calls for the same setting. |
| 89 | **`Coerce` belongs in `infra`, not `core/config`** | The `Coerce(value any, expectedType string) (any, error)` function is a generic utility that coerces runtime-typed values. It does not depend on any config-specific state. Must live in `infra/`. |
| 90 | **Hash generator functions are package-level, not methods on a zero-field struct** | `HashGenerator` was a struct with no fields — just a namespace for methods. Convert all methods to package-level functions in `infra/crypto/`: `crypto.ImageID(...)`, `crypto.VMID(...)`, `crypto.ShortenID(...)`, `crypto.UUIDV4()`, etc. Callers use `crypto.ImageID(...)` instead of `var hg infra.HashGenerator; hg.Image(...)`. |
| 91 | **Error detection uses codes, not string matching** | `RootPartitionDetectionError` and `TieDetectedError` must have DISTINCT error codes (`CodeRootPartitionDetection`, `CodeTieDetected`). The `isPartitionDetectionError()` function must use `errors.As` + code comparison, not fragile string pattern matching on error messages. |
| 92 | **`GetWarmImageDir()` takes no parameters** | Always uses `infra.GetTempDir()` internally. The `tmpPath string` parameter was removed — callers that passed `""` or `infra.GetTempDir()` both get the same behavior. The `ProjectName` subdirectory was also removed (previously `base + "/mvm/ready"`, now `GetTempDir() + "/ready"`). |
| 93 | **Reserved names must be comprehensive and proactive** | `ReservedNames` in `infra/constants.go` must include all current CLI subcommands, resource types, common identifiers, primitive/language type names, and potential future subcommands/resource types. Add proactively when introducing new subcommands. The `IsReservedName(name)` function lowercases input before checking. |

## 1. Project Layout

```
mvmctl/
├── cmd/mvm/
│   ├── main.go                       # 10 lines — signal handler + app.Run() / _provision dispatch
│   └── provision.go                  # Hidden _provision subcommand for loopmount
│
├── internal/
│   ├── app/
│   │   ├── app.go                    # Explicit wiring: openDB → repos → enricher → apis → CLI
│   │   └── version.go                # Build-time version string (ldflags)
│   │
│   ├── cli/                          # Cobra commands. ONLY layer for user-facing output.
│   │   ├── root.go                   # Root command + persistent flags (--verbose, --debug)
│   │   ├── common/                   # helpers.go, output.go — table/JSON/error display
│   │   ├── vm.go, network.go, image.go, kernel.go, binary.go
│   │   ├── key.go, host.go, config.go, console.go, logs.go
│   │   ├── volume.go, cache.go, ssh.go, cp.go, init.go
│   │   └── helpers.go                # Per-domain CLI helper utilities
│   │
│   ├── core/{domain}/                # 14 domain packages. NEVER cross-import.
│   │   ├── vm/                       # controller, service, repository, sqlite, resolver,
│   │   │                             #   firecracker, provisioner, serialize, errors
│   │   ├── network/                  # controller, service, repository, sqlite, resolver,
│   │   │                             #   lease_service, lease_repository, lease_sqlite,
│   │   │                             #   lease_resolver, constants, errors
│   │   ├── image/                    # controller, service, repository, sqlite, resolver,
│   │   │                             #   provisioner, version_resolver, constants, errors
│   │   ├── kernel/                   # controller, service, repository, sqlite, resolver, errors
│   │   ├── binary/                   # controller, service, repository, sqlite, resolver
│   │   ├── key/                      # controller, service, repository, sqlite, resolver, errors
│   │   ├── host/                     # controller, service, repository, sqlite, detector,
│   │   │                             #   helper, probe, host_info, errors
│   │   ├── volume/                   # controller, service, repository, sqlite, resolver, errors
│   │   ├── config/                   # service, repository, sqlite, constraints, settings, errors
│   │   ├── console/                  # controller, errors
│   │   ├── logs/                     # controller, service, errors
│   │   ├── cloudinit/                # manager, provisioner, errors (+ nocloud.go stub, migrated to service/)
│   │   ├── cache/                    # service
│   │   └── ssh/                      # service, cp, errors
│   │
│   ├── enricher/
│   │   ├── enrich.go                 # Cross-domain enrichment. switch/case per relation.
│   │   └── batch.go                  # Batch-loading helpers for N+1 prevention
│   │
│   ├── infra/                        # Leaf infrastructure. Core imports freely.
│   │   ├── asset/manager.go          # Bundled YAML asset loading
│   │   ├── db/                       # connection.go, migrations.go, migrations/*.sql
│   │   ├── download/                 # http.go, version.go
│   │   ├── errs/                     # domain.go, codes.go, batch.go, result.go
│   │   ├── firewall/                 # tracker.go, nftables.go, nftables_repository.go,
│   │   │                             #   iptables.go, iptables_repository.go
│   │   ├── guestfs/                  # base.go, provisioner.go, service.go, kernel_detector.go
│   │   ├── logging/                  # handler.go, rotating.go, setup.go
│   │   ├── loopmount/                # manager.go, provisioner.go, backend.go
│   │   ├── model/                    # ALL model types — vm, network, image, kernel, binary, key,
│   │   │                             #   volume, host, config, console, logs, cloudinit, cache,
│   │   │                             #   ssh, firecracker, provisioner, result, bulk, version
│   │   ├── parallel/executor.go      # Parallel[T], Map[T,R]
│   │   ├── provisioner/              # backend.go, content.go (re-exports), model.go
│   │   ├── provisionercontent/       # content.go — shared provisioning content builders
│   │   ├── system/                   # runner.go, exec.go, group.go
│   │   ├── version/                  # resolver.go, model.go
│   │   ├── audit.go, constants.go, crypto.go, disk.go, io.go
│   │   ├── network.go, operation.go, progress.go, template.go
│   │   ├── time.go, validators.go, yaml.go
│   │   └── (no graceful.go — abolished)
│   │
│   ├── service/                      # Goroutine-based services (NOT core domain logic)
│   │   ├── console/relay.go          # PTY relay goroutine
│   │   ├── nocloudnet/server.go      # NoCloud HTTP server goroutine
│   │   └── loopmount/provisioner.go  # Loop-mount provisioner subprocess
│   │
│   ├── testutil/                     # In-memory repos + FakeRunner for unit tests
│   │   ├── vm.go, network.go, image.go, kernel.go, binary.go
│   │   ├── key.go, host.go, volume.go, config.go, lease.go
│   │   └── fake_runner.go
│   │
│   └── assets/                       # Embedded YAML/template files
│       ├── images.yaml, kernels.yaml
│       ├── cloud-init.template.yaml, firecracker.template.json
│       └── assets.go                 # //go:embed declarations
│
├── pkg/api/                          # PUBLIC orchestration layer. Cross-domain — imports core + enricher.
│   ├── vm.go, network.go, image.go, kernel.go, binary.go
│   ├── key.go, host.go, config.go, console.go, logs.go
│   ├── volume.go, cache.go, ssh.go, cp.go, init.go
│   └── inputs/                       # Input / Builder / Resolved structs for each operation
│       ├── vm_create.go, vm_create_builder.go, vm_input.go, vm_export_config.go, vm_import.go
│       ├── network_create.go, network_input.go
│       ├── image_input.go, image_acquire.go
│       ├── kernel_input.go, kernel_pull.go, kernel_import.go
│       ├── binary_input.go, binary_pull.go
│       ├── key_input.go, key_create.go, key_import.go
│       ├── ssh_input.go, console_input.go, logs_input.go
│       ├── volume_input.go, volume_create.go
│       ├── config_input.go, cp_input.go
│
├── go.mod, go.sum
├── Makefile
└── docs/
    └── PORTING_TO_GOLANG.md          # This file
```

## 2. Key Architecture Rules

| Rule | Enforced by |
|------|-------------|
| Core domains NEVER import other core/* packages | Go compiler (circular import error) |
| CLI only imports `pkg/api/` + `internal/infra/` + `internal/cli/common/` | Go compiler + code review |
| API imports `internal/core/*` + `internal/enricher/` + `internal/infra/` | Go compiler + code review |
| `internal/infra/` is the LEAF dependency — imports NOTHING from core, api, cli, or service | Go compiler |
| `internal/infra/` is imported by EVERY layer above it (core, service, api, cli, app) | By design — infra IS shared infrastructure |
| `internal/service/` MAY import from `internal/infra/` (model types, errs, logging, utilities) | Code review — avoid circular deps |
| `internal/service/` MUST NOT import from `pkg/api/` or `internal/cli/` | Go compiler |
| `pkg/api/` and `internal/enricher/` are the ONLY cross-domain packages | Convention + code review |
| Everything wired explicitly in `app.Run()` — no `init()` globals | Convention |
| Controller has no Create() or Remove() — state management only (start/stop/pause/resume) | Code review |
| Validation lives in API `*Input` / `*Builder` classes, not in Service/Controller | Code review |
| ALL subprocess calls through `CommandRunner` — no raw `os/exec` outside documented exceptions | Code review |
| Output below CLI layer uses `slog` only — no `fmt.Print`, no `log.Printf` | Code review + lint |

## 3. Package Naming Conventions

All packages follow Go convention: **directory name = package name**. No underscored names.

| Directory | Package | Notes |
|-----------|---------|-------|
| `internal/infra/errs/` | `errs` | Avoids collision with stdlib `"errors"` |
| `internal/infra/model/` | `model` | Single package for ALL model types |
| `internal/cli/common/` | `common` | Shared CLI display helpers |
| `internal/core/vm/` | `vm` | No `domainvm` prefix |
| `internal/core/network/` | `network` | No `domainnetwork` prefix |
| `internal/core/image/` | `image` | No `domainimage` prefix |
| `internal/core/kernel/` | `kernel` | |
| `internal/core/binary/` | `binary` | |
| `internal/core/key/` | `key` | |
| `internal/core/host/` | `host` | |
| `internal/core/volume/` | `volume` | |
| `internal/core/config/` | `config` | |
| `internal/core/console/` | `console` | |
| `internal/core/logs/` | `logs` | |
| `internal/core/cloudinit/` | `cloudinit` | |
| `internal/core/cache/` | `cache` | |
| `internal/core/ssh/` | `ssh` | |
| `internal/service/console/` | `console` | |
| `internal/service/nocloudnet/` | `nocloudnet` | |
| `internal/service/loopmount/` | `loopmount` | |
| `pkg/api/` | `api` | |
| `internal/enricher/` | `enricher` | |

**Rule:** Import aliases (`Xcore`) are forbidden. Use bare package names. The only exceptions are when stdlib collision requires an alias (e.g., `"errors"` vs `"mvmctl/internal/infra/errs"`).

**Conflict aliases:** When two packages share the same name (e.g., `"mvmctl/internal/core/network"` and `"mvmctl/internal/infra/network"`), the infra package gets the `infra_` prefix alias:
- `infranet "mvmctl/internal/infra/network"`
- `infraslice "mvmctl/internal/infra/slice"`
- `infraversion "mvmctl/internal/infra/version"` (when needed)

Non-infra packages keep the bare name (`network` for `core/network`, `version` for `core/version`).

## 4. Layer Responsibilities

### CLI (`internal/cli/`)
The sole layer for user-facing output. Cobra command files (one per domain, monolithic per verdict #14) parse flags, build API Input structs, call API operation methods, and format output via `internal/cli/common/` (table rendering, JSON output, error display). Imports only from `pkg/api/` (operations), `internal/infra/model/` (for display types), and `internal/cli/common/`. No business logic. No DB queries.

### API (`pkg/api/`)
The PUBLIC orchestration layer and the **only** place where multiple core domains are imported and sequenced. Each `*Operation` struct (e.g., `VMOperation`) holds references to core services, controllers, and the enricher. Static-style methods orchestrate cross-domain workflows: VM create (network lease → image provision → cloud-init → Firecracker spawn → DB register), VM delete (stop → release lease → remove TAP → delete rootfs → DB delete). Validation lives in `pkg/api/inputs/` (Builder pattern). Handles typed errors and returns clean results.

### Core (`internal/core/{domain}/`)
Business logic isolated by domain. Each domain contains:

- **Controller** — Stateful, instantiated with a single entity. Manages state transitions (start/stop/pause/resume). No Create() or Remove().
- **Service** — Stateless intra-domain operations (bulk actions, infrastructure setup/teardown). Detects system state as part of execution. Guards invariants that prevent system damage. Does NOT validate caller input.
- **Repository** — DB CRUD interface (defined in `repository.go`) + SQLite implementation (`sqlite.go`). ALL domain queries live here. SQL-level computation (COUNT, WHERE IN) — no fetch-all-in-Python patterns.
- **Resolver** — Entity resolution by name/ID/IP/MAC. Delegates to repository for DB queries. Pure domain resolution (cross-domain enrichment is the enricher's job).

Core domains NEVER import each other. They import only from `internal/infra/` (utilities, models, shared infrastructure).

### Infra (`internal/infra/`)
Leaf-level utilities, shared types, error types, model types. Zero knowledge of core domains, API, or CLI. Every package in `internal/infra/` follows the "leaf dependency" rule — it imports nothing from `core/`, `api/`, `cli/`, or `service/`. Key packages: `model/` (all domain types), `errs/` (DomainError + codes), `db/` (SQLite connection + migrations), `system/` (CommandRunner), `parallel/` (Parallel/Map), `logging/` (slog setup), `firewall/` (nftables/iptables backends).

### Service (`internal/service/`)
Goroutine-based long-running services that were separate subprocesses in Python. `console/relay.go` — PTY relay goroutine with Unix socket. `nocloudnet/server.go` — NoCloud HTTP metadata server goroutine. `loopmount/provisioner.go` — loop-mount provisioning subprocess dispatched via `mvm _provision`. These are NOT core domain logic — they are infrastructure processes managed via goroutines and `context.Context`. Core domains may import from `internal/service/` but not the reverse.

## 5. Error Handling

### Design

Go has no inheritance. Python's 40-class error hierarchy becomes a flat `DomainError` struct with a `Code` field for programmatic branching:

```go
type DomainError struct {
    Code    Code   // dot-separated, e.g. "vm.not_found", "network.subnet.overlap"
    Message string // user-facing: "What. Why. Possible fix."
    Op      string // operation context ("vm.create")
    Entity  string // affected resource ID
    Class   Class  // classification: Validation, Conflict, Retryable, Internal, NeedsInteraction
    Err     error  // underlying cause (wrapped with %w)
}
```

**Package:** `internal/infra/errs/` — package name `errs` to avoid collision with stdlib `"errors"`.

**Codes file:** `internal/infra/errs/codes.go` — all error code constants as `type Code string`. Dot-separated format: `vm.not_found`, `network.subnet.overlap`, `vm.create.binary_not_found`, `firecracker.socket.not_found`, etc.

**Helpers for common errors:** `NotFound(code, entity)`, `AlreadyExists(code, entity)`, `ValidationFailed(code, msg)`, `Wrap(code, err)`. Ad-hoc errors use struct literal directly.

**Error classification:** `IsNotFound(err)`, `IsRetryable(err)`, `IsNeedsInteraction(err)` — use `errors.As()` to unwrap `*DomainError`.

**Batch errors:** `internal/infra/errs/batch.go` — `BatchResult` struct for multi-item operations. Collects per-item errors, provides `Errors()`, `Successes()` methods.

**`@_graceful_read` abolished entirely.** No `GracefulRead[T]` wrapper exists. All DB read errors propagate explicitly.

**Error display:** `internal/infra/errs/result.go` — `OperationResult`, `OperationStatus`, `NeedsInteraction`, `ProgressEvent` for the CLI layer.

## 6. Data Model Strategy

### Centralized in `internal/infra/model/`

All model types live in a single package (`package model`). No domain-level `model.go` files. Every package (`core/*`, `pkg/api`, `internal/cli`, `internal/enricher`, `internal/infra/*`) imports `mvmctl/internal/infra/model` freely.

**Files** (one per domain concept):
```
internal/infra/model/
├── vm.go            # VM, VMStatus, VMMetadata, etc.
├── network.go       # Network, NetworkLease, FirewallRule, IPReservation
├── image.go         # Image, ImageSpec, ImageVersion
├── kernel.go        # Kernel, KernelSpec, KernelFeature, KernelPullResult
├── binary.go        # Binary, BinarySpec
├── key.go           # SSHKey
├── volume.go        # Volume, VolumeStatus
├── host.go          # HostState, HostHardware, HostLimits, HostResources
├── config.go        # ConfigKey, ConfigValue, OverridableDefaults
├── console.go       # ConsoleState
├── logs.go          # LogEntry
├── cloudinit.go     # CloudInitMode, CloudInitStatus
├── cache.go         # PruneAllResult, CleanResult
├── ssh.go           # SSHConnection
├── firecracker.go   # FirecrackerConfig, CpuConfig, DriveConfig, etc.
├── provisioner.go   # ProvisionerType
├── result.go        # OperationResult, OperationStatus, NeedsInteraction, ProgressEvent
├── bulk.go          # BulkResult, BulkResultItem
└── version.go       # VersionInfo, VersionSpec
```

### Optional fields: `*T`

Every Python `str | None` → `*string`, `int | None` → `*int`, `bool | None` → `*bool`. Zero-value vs nil distinction is critical — zero means "explicitly set to zero/empty", nil means "not provided / use default". No exceptions.

### `sqlx` for struct scanning

All repositories use `github.com/jmoiron/sqlx` for scanning rows into structs via `db:"column_name"` tags. See verdict #68 for the rationale. Repo constructors accept `*sql.DB` and wrap internally with `sqlx.NewDb(db, "sqlite3")`, so callers are unchanged.

## 7. Subprocess Execution

### Canonical interface: `CommandRunner`

```go
// internal/infra/system/runner.go
type CommandRunner interface {
    Run(ctx context.Context, args []string, opts ...RunOption) (*Result, error)
    Stream(ctx context.Context, args []string, opts ...RunOption) (<-chan StreamLine, error)
}

// RealRunner — zero-struct, actual os/exec wrapper
type RealRunner struct{}
```

Functional options pattern (`RunOption`) for: timeout, cwd, env, stdin, capture output (`true`/`false`), privileged (auto-prepends `sudo`). All subprocess calls through this interface.

### Tests: `testutil.FakeRunner`

Pre-recorded command expectations via `FakeRunner.Expect(args, output, err)`. Tests inject `FakeRunner` into services via the `CommandRunner` interface.

### Exceptions (raw `os/exec` or `subprocess.Popen`)

Six documented locations where the runner interface doesn't suffice (pass_fds, inter-process piping, detached daemon):

1. **`internal/core/vm/firecracker.go`** — Firecracker spawn; needs `pass_fds` + `start_new_session`.
2. **`internal/service/console/relay.go`** — Console relay PTY relay goroutine (not a subprocess); raw syscall access needed for PTY FD management. |
3. **`internal/core/ssh/cp.go`** — Tar-pipe file copy; pipes two child processes together.
4. **`internal/service/nocloudnet/server.go`** — NoCloud server detach; `start_new_session=true`.
5. **`cmd/mvm/provision.go`** — Loop-mount provisioning standalone binary.
6. **`internal/infra/system/runner.go`** — The runner itself uses `os/exec` internally.

### Privilege model

`RunOption.Privileged(true)` checks `os.Getuid()`. If non-root, prepends `sudo`. Two modes: (1) **Non-interactive** (`sudo -n`) for loopmount provisioner — fails immediately if password required. (2) **Interactive** (`sudo` with `cmd.Stdin = os.Stdin`) for host init — forwards TTY to let sudo prompt for password. Use `-n` flag for non-interactive subprocess calls; do NOT use `-n` for commands that may need password prompt.

## 8. Concurrent Execution

### `internal/infra/parallel/executor.go`

Two generic functions with bounded concurrency:

```go
// Side-effect tasks (delete, stop, create). Collects errors, continues on failure.
func Parallel[T any](ctx context.Context, workers int, items []T, fn func(context.Context, T) error) []error

// Transform tasks (fetch, inspect). Collects results + errors.
func Map[T, R any](ctx context.Context, workers int, items []T, fn func(context.Context, T) (R, error)) ([]R, []error)
```

- Uses goroutines + `sync.WaitGroup` + channel-based semaphore for bounded concurrency.
- Both accept `context.Context` for cancellation (signal-aware via `signal.NotifyContext` in main).
- Workers defaults to `runtime.NumCPU()*2` when ≤ 0.
- Matches Python's bulk operation behavior: collect errors but continue on failure.

## 9. Logging

### `log/slog` throughout — no `log.Printf`, no `fmt.Fprintf(os.Stderr)` for diagnostics

**Package:** `internal/infra/logging/` — three files:
- `setup.go` — `SetupLogging()` configures stderr handler (human-readable, colorized) + optional file handler (JSON structured).
- `handler.go` — Custom `slog.Handler` for Python-compatible log format (level prefixes, source location).
- `rotating.go` — `RotatingFileWriter` for size-based log rotation.

**Output layering:**
- Below CLI layer (`internal/core/`, `internal/infra/`, `pkg/api/`, `internal/service/`): `slog.Debug/Info/Warn/Error` ONLY.
- CLI layer (`internal/cli/`): `fmt.Print*` for user-facing output, `slog` for internal diagnostics.
- Hidden subprocess commands (nocloud, provision, console relay): stdout/stderr pipes are their own, exempt from this rule.

## 10. Testing

### Unit tests (`*_test.go` next to source)

- **In-memory repositories** in `internal/testutil/` — `VMRepo`, `NetworkRepo`, `ImageRepo`, etc. Each implements the domain's repository interface using `map[string]*model.VM` with `sync.RWMutex`.
- **FakeRunner** in `internal/testutil/fake_runner.go` — pre-record command expectations, returns configured output/error.
- Services and controllers accept interfaces (repository + runner) — tests inject in-memory implementations.
- Unit tests cover all business logic paths, error conditions, state transitions.

### System tests (`tests/`)

Black-box binary tests. Run against the compiled `mvm` binary. Test end-to-end flows with real (or mocked) infrastructure. Owned by the QA engineer agent — engineer agent never touches `tests/`.

### No integration test layer

Python had three layers (unit + integration + system). Go has two: unit tests (fast, in-memory) + system tests (black-box binary).

## 11. What NOT to Port

These Python patterns have NO equivalent in Go and are either abolished or handled by Go's native features:

| Python Pattern | Why NOT to Port | Go Alternative |
|---|---|---|
| **40-class error hierarchy** | Go has no inheritance. | Flat `DomainError` with `Code` field + `errs` package. |
| **`@_graceful_read` decorator** | Silent fallback is not idiomatic. | Abolished entirely. All errors propagate. |
| **PEP 562 lazy imports** | Go has static imports (~µs, not 230ms). | Standard imports at package init. |
| **Nuitka multidist binary** | Go compiles to a single static binary. | One binary. Services are goroutines. |
| **Manager+Process PID-file pattern** | Python needed separate processes (GIL). | Goroutines + channels + `context.Context`. |
| **`__post_init__` / JSON deserialization** | Go has no magic methods. | Explicit scan functions. |
| **Dynamic resolver auto-discovery** | Python needed `importlib` to avoid circular imports. | Explicit wiring in `app.Run()`. Compiler guarantees. |
| **`from __future__ import annotations`** | Go types are forward-reference-safe. | Not applicable. |
| **`StrEnum` with `auto()`** | Go has `iota`. | `type Status string` with const values. |
| **`@dataclass(frozen=True)`** | Go has no immutability enforcement. | Convention + constructor-only creation. |
| **`@staticmethod` on Operation classes** | Go has package-level functions. | Struct methods on `*Operation`. |
| **`TYPE_CHECKING` imports** | No conditional imports in Go. | Standard imports — no circular import risk. |
| **`GracefulRead[T]` generic** | Silent fallbacks hide bugs. | Abolished. Callers handle errors. |
| **`sqlx.StructScan`** | Required for consistent struct scanning. | See verdict #68 — `db` tags + `StructScan` replace manual `rows.Scan` everywhere. |
| **Layer compliance CI tests (AST)** | Go compiler enforces import boundaries. | Compiler does it for free. |
| **`interface{}` / `any` on model fields** | No type safety. | Concrete `[]model.VM` etc. |
| **Reflection-based value rendering** | Slow, error-prone. | Type switches, interfaces, generics. |
| **Custom `String()` mimicking Python repr** | No value. | `fmt.Sprintf("%+v", v)` provides struct display. |
| **`init()` + global map resolver registry** | Mutable global state, runtime panics. | Explicit wiring in `app.Run()` — compile-time checked. |
| **Pidfile-based process management** | Goroutines don't need PID tracking. | `context.Context` cancellation. |

---

## 12. Domain User Review Status

- [ ] vm
- [x] network
- [ ] image
- [ ] kernel
- [x] binary
- [x] key
- [x] host
- [ ] volume
- [x] config
- [ ] console
- [ ] logs
- [ ] cloudinit
- [x] cache
- [ ] ssh
- [x] guestfs
- [x] firewall tracker
- [x] init
- [ ] logging
- [ ] loopmount
- [ ] infra network
- [ ] infra operation
- [ ] infra parallel
- [ ] infra provisioner
- [ ] infra system
- [ ] service console
- [ ] cloudinit
- [ ] service nocloudnet
- [ ] infra errs
- [ ] infra download
