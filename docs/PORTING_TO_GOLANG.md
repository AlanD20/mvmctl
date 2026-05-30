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

---

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
│       ├── key_input.go, key_create.go
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

### No `sqlx`

All SQL scanning uses `database/sql` directly. No `sqlx.StructScan`. Each repository implements explicit scan functions.

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
| **`sqlx.StructScan`** | Extra dependency, not needed. | `database/sql` row scanning with explicit scan functions. |
| **Layer compliance CI tests (AST)** | Go compiler enforces import boundaries. | Compiler does it for free. |
| **`interface{}` / `any` on model fields** | No type safety. | Concrete `[]model.VM` etc. |
| **Reflection-based value rendering** | Slow, error-prone. | Type switches, interfaces, generics. |
| **Custom `String()` mimicking Python repr** | No value. | `fmt.Sprintf("%+v", v)` provides struct display. |
| **`init()` + global map resolver registry** | Mutable global state, runtime panics. | Explicit wiring in `app.Run()` — compile-time checked. |
| **Pidfile-based process management** | Goroutines don't need PID tracking. | `context.Context` cancellation. |
