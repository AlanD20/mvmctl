# mvmctl Go Coding Standards

Coding standards, conventions, and architectural rules for the mvmctl Go codebase.

## 1. Package Structure

| Path | Purpose | Import Rules |
|------|---------|--------------|
| `cmd/mvm/` | Binary entry point | Only imports `internal/app` |
| `internal/app/` | Application initialization and DI wiring | Orchestrates `internal/lib/db`, `pkg/api` |
| `internal/cli/` | Cobra CLI commands and user-facing output | Imports `pkg/api`, `internal/cli/common`, `internal/infra`, `internal/lib/*` |
| `internal/cli/common/` | Shared CLI helpers (tables, JSON output, prompts) | Imports `internal/infra`, `internal/lib/*` |
| `internal/core/{domain}/` | Domain logic (vm, network, image, kernel, binary, key, host, volume, config, console, logs, cloudinit, cache, ssh) | Imports `internal/infra`, `internal/lib/*`. NEVER imports other `core/*` packages. |
| `internal/enricher/` | Cross-domain enrichment (only package besides `pkg/api` that imports multiple core domains) | Imports `internal/core/*` |
| `internal/service/` | Background subprocess services (console relay, nocloudnet server, loopmount provisioner) | Imports `internal/infra`, `internal/lib/*`. Never imports `pkg/api` or `internal/cli/`. |
| `internal/workflow/env/` | Environment workflow orchestration (apply/destroy specs) | Imports `pkg/api`, `internal/infra`, `internal/lib/*`. Never imports `internal/core/` or `internal/enricher/` directly. |
| `internal/infra/` | Generic leaf utilities (constants, io, template, yaml, cast, slice, pool, ptr, event, provcontent) | Imports ONLY stdlib and external deps. Never imports core, api, cli, or service. |
| `internal/lib/` | Domain-adjacent leaf utilities (system, model, db, download, version, logging, crypto, firewall, provisioner, network, archive) | Imports ONLY stdlib and external deps. Never imports core, api, cli, or service. |
| `pkg/api/` | Public API orchestration layer | Imports `internal/core/*`, `internal/enricher/`, `internal/infra`, `internal/lib/*` |
| `pkg/api/inputs/` | Input/Request/Resolved structs for validation | Imports `internal/core/*`, `internal/enricher/`, `internal/infra`, `internal/lib/*` |
| `pkg/errs/` | Domain error type and codes | Leaf package — no internal imports |
| `internal/testutil/` | In-memory repo implementations, `FakeRunner`, and per-domain API mocks for tests | Imports `internal/lib/*`, `internal/infra/event`, `pkg/api/*`, `pkg/errs` |

**Key rule:** `internal/infra/` and `internal/lib/` are LEAF dependencies. They import nothing from `core/`, `api/`, `cli/`, or `service/`. Everything else imports them.

## 2. Layer Architecture

Three-layer flow: **CLI → API → Core**

### CLI Layer (`internal/cli/`)
- Argument parsing, output formatting, table rendering, JSON output
- User-facing output ONLY here: `fmt.Print`, `cli.Info/Success/Error`
- Calls `pkg/api/` methods, never `internal/core/` directly
- Can call `internal/workflow/env/` for environment workflow commands
- Monolithic files per domain: `vm.go` has all VM subcommands
- `PersistentPreRunE` on root command sets up verbose/debug modes

### API Layer (`pkg/api/`)
- Sole orchestrator of multiple core domains
- Cross-domain sequencing: VM creation orchestrates vm + network + image + kernel + cloudinit
- Holds all repositories, services, and enricher
- Validation lives in `pkg/api/inputs/` — `*Input` / `*Request` / `Resolved*` structs
- Returns typed responses with JSON struct tags
- Handles `--json` flag by `json.MarshalIndent`-ing typed response structs directly
- No `ToJSON()` methods on API operations

### Core Layer (`internal/core/{domain}/`)
- Strictly isolated domains — NEVER import other core domains
- Controller: stateful, per-entity (start/stop/pause/resume/snapshot)
- Service: stateless, intra-domain orchestration
- Repository: database CRUD operations
- Resolver: pure entity resolution by identifier

## 3. Domain Patterns

### Controller (stateful, per-entity)
- Struct bound to a single entity instance
- Manages lifecycle state transitions (start, stop, pause, resume, snapshot)
- Does NOT validate caller input
- Does NOT orchestrate across domains
- Does NOT handle creation or removal
- Constructor takes entity + repo: `vm.NewController(vm *model.VM, repo Repository)`
- Created per-operation in Service layer, NOT wired at startup

### Service (stateless, intra-domain)
- Stateless operations coordinator
- Handles infrastructure operations (bridges, TAPs, NAT, subprocesses, file/disk ops)
- Performs state detection (checking current system state to branch execution)
- Guards invariants that protect against system damage
- Does NOT validate caller input
- Constructor takes repos/options only: `network.NewService(repo Repository, tracker firewall.Tracker)`
- Wired once at startup in `app.Initialize()`
- Service methods that don't reference the Service struct live in `utils.go`

### Repository
- Interface defined in `repository.go`, SQLite implementation in `sqlite.go`
- ALL SQL queries live here
- Uses `github.com/jmoiron/sqlx` for struct scanning (`StructScan`, `GetContext`, `SelectContext`)
- Uses SQL-level computation (COUNT, WHERE IN), never fetch-all-then-filter
- Every method takes `ctx context.Context` as first parameter
- JSON-serialized fields use intermediate scan structs + `toVM()` conversion
- Constructor: `NewRepository(db *sqlx.DB) Repository`

### Resolver
- Pure resolution by identifier (name, ID prefix, IP, MAC)
- No enrichment — enrichment is handled by `internal/enricher/`
- Delegates to Repository for DB queries

## 4. Application Initialization

Explicit DI wiring in `app.Initialize()` — no `init()` globals, no lazy factories.

```go
func Initialize(ctx context.Context) (op *api.Operation, cleanup func(), err error) {
    // 1. Resolve cache directory
    // 2. Check database existence
    // 3. Open database (db.New → db.Handle)
    // 4. Check pending migrations
    // 5. Create api.Operation (wires all repos, services, enricher)
    // 6. Return operation + cleanup func
}
```

`api.NewOperation()` validates ALL required services are non-nil and panics on nil.
No nil checks on `op.Services.Config` or other required services in the API layer.

## 5. Error Handling

Single error type: `pkg/errs.DomainError` with fields `Code`, `Class`, `Message`, `Err`.

```go
// Creating errors
errs.New(errs.CodeVMNotFound, "VM not found: my-vm")
errs.Wrap(errs.CodeNetworkBridgeFailed, err)
errs.WrapMsg(errs.CodeDownloadFailed, "Failed to fetch image", err)
errs.NotFound(errs.CodeVMNotFound, "my-vm")
errs.AlreadyExists(errs.CodeVMExists, "my-vm")

// Checking errors
var de *errs.DomainError
if errors.As(err, &de) {
    switch de.Code { ... }
}
if errs.IsNotFound(err) { ... }
```

- Error detection uses codes: `errors.As` + `Code` comparison. No string matching.
- Log before return: `slog.Error(...)` before every error return in Service/Controller.
- Error messages: lowercase first letter (Go convention). `"failed to open file"` not `"Failed to open file"`.
- Error format: `What happened. Why it happened. Possible fix.`
- Error codes: dot-separated with domain prefix: `network.subnet.overlap`, `vm.create.binary_not_found`.

## 6. Context Propagation

- `signal.NotifyContext` in `main()` for graceful shutdown.
- CLI passes `cmd.Context()` through the full call chain.
- `ctx context.Context` as first parameter on every repository method and every infrastructure function with side effects.
- Pure computation functions (string manipulation, parsing, hashing) do NOT take context.
- All sqlx calls use context-aware variants: `SelectContext`, `GetContext`, `ExecContext`, `BeginTxx`.
- Context flows from Cobra → API → Service → Repo → sqlx, enabling cancellation propagation.
- **Cancelled-context-in-cleanup trap:** When a goroutine triggers cleanup via `<-ctx.Done()`, the context is already cancelled. Use `context.Background()` for cleanup in signal-handling goroutines.

## 7. Subprocess Execution

ONE canonical path: `system.RunCmd(ctx, args, system.RunCmdOpts{...})` in `internal/lib/system/runner.go`.

```go
// Correct
result, err := system.RunCmd(ctx, []string{"ip", "link", "set", tap, "down"}, system.RunCmdOpts{Privileged: true})

// Forbidden
exec.Command("iptables", ...) // NEVER
```

Documented exceptions for direct `os/exec`:

| Location | Why RunCmd doesn't work |
|----------|-------------------------|
| `internal/core/vm/firecracker.go` | Needs `pass_fds` for VM API socket and log file descriptors |
| `internal/core/ssh/cp.go` | Pipes two child processes (tar + ssh) via stdin/stdout |
| `internal/service/loopmount/provisioner.go` | Direct provisioning engine with chained losetup/mount/umount/chroot |
| `internal/lib/system/runner.go` | Implementation of `RunCmd` / `StreamCmd` itself |
| `internal/lib/system/spawn.go` | Implementation of `SpawnService` for service subprocess spawning |

Service subprocesses use `system.SpawnService(ctx, cfg)`.

## 8. Model Types

ALL model types in `internal/lib/model/` — single package. No domain-level `model.go` files.

- Every type has `json:"field"` and `db:"column"` struct tags.
- JSON-serialized DB fields use `db.StringSlice` or custom `Scan`/`Value` implementations.
- Resolved relations use concrete types: `Kernel *KernelItem`, `Image *ImageItem`, etc.
- No `[]any` for cross-domain references.

### Nullable fields
- Flatten `*string` to `string` where `""` means absent. Use `*T` only when zero value has distinct meaning.
- Exception: bare `string` with `omitempty` for optional fields where empty string == "no value".
- Numeric and boolean fields must use `*T` because 0/false have meaning.

## 9. Database Patterns

### `db.Handle` (not `db.Database`)
- Go convention: package name + type name should not stutter.
- `db.Handle` stores `*sqlx.DB` internally.
- `DB()` returns `*sqlx.DB` directly — panics on failure (unrecoverable error).
- Lazy opening: connection opened on first `DB()` call.
- `SetMaxOpenConns(1)` and `SetMaxIdleConns(1)` for SQLite single-writer semantics.

### SQLite driver
- `_ "modernc.org/sqlite"` imported ONLY in `internal/lib/db/connection.go`.
- PRAGMAs passed via DSN parameters: `foreign_keys(1)`, `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`.

### Repository scanning
- Direct struct scanning via `sqlx.GetContext`, `sqlx.SelectContext`, `StructScan`.
- No `map[string]interface{}` + type assertions.
- JSON fields use intermediate scan struct (e.g., `vmScanRow`) with `db` tags → `toVM()` conversion.
- Scan helpers inlined: each query owns its own `for rows.Next()` loop. No shared `scanXxx` functions.
- Context required on EVERY database method: `SelectContext`, `GetContext`, `ExecContext`, `BeginTxx`.

## 10. Concurrency

Bounded concurrency in `internal/infra/pool/`:

```go
pool.Do[T](ctx, workers, items, fn)      // fire-and-forget, collects all errors
pool.Gather[T,R](ctx, workers, items, fn) // parallel transform, returns []Result[R]
pool.Seq[T,R](ctx, items, fn)             // sequential fail-fast, returns []Result[R]
```

- Workers default to `runtime.NumCPU() * 2` when ≤ 0.
- All accept `context.Context` for cancellation.
- Go maps require synchronization for concurrent access: `map[K]V + sync.Mutex` (or `sync.RWMutex` for read-heavy).
- `sync.Map` is specialized for write-once/read-many — NOT a general replacement.

## 11. Logging

- Go standard `log/slog` throughout.
- Setup via `internal/lib/logging/setup.go`.
- `slog` only below CLI layer. `fmt.Print` / `log.Print` BANNED in `internal/` (except `internal/cli/`) and `pkg/api/`.
- CLI layer is the sole layer for user-facing output.
- Hidden subprocess commands (console relay, nocloudnet, loopmount) are exempt — they write to their own stdout/stderr.

## 12. Testing

Two layers — no separate integration layer:

### Unit tests (`*_test.go` next to source)
- Use interface mocks: `internal/testutil/` has in-memory repo implementations + `FakeRunner`.
- Run via `go test ./...`.

### System tests (`tests/system/`)
- Python-based black-box CLI subprocess tests.
- No mocking, no imports from Go code.
- Operate against the compiled `mvm` binary.

## 13. CLI Patterns

### Monolithic files per domain
One file per domain: `internal/cli/vm.go` has all VM subcommands (create, start, stop, ls, ps, inspect, rm, etc.). ~500-800 lines is manageable.

### CLI aliases
- Every `list` subcommand: `ls` (Use) + `list` (Alias).
- Every `remove` subcommand: `rm` (Use) + `remove, delete, del` (Aliases).
- Domain aliases: `net` → `network`, `img` → `image`, `vol` → `volume`.

### Table output
- Always render table headers, even when empty. No "No X found" early returns.
- Empty table body is the correct output.

### Destructive actions
- Prompt for confirmation via `common.Cli.PromptConfirm()`.
- `--force` / `-f` flag MUST skip the prompt.

### Short flags
- Add short flags (`-a`, `-d`, `-f`) for commonly-used options even if not present originally.
- CLI ergonomics takes precedence.

### Function naming
- CLI handler functions use full names: `runVMList` not `runVMLs`.
- Cobra `Use` strings keep standard names (`ls`, `ps`) since those are user-facing.

### Cobra defaults
- No `FParseErrWhitelist{UnknownFlags: true}` or `DisableSuggestions: true`.
- Accept Cobra's default behavior: unknown flags cause errors, suggestions shown.
- `SilenceErrors: true`, `SilenceUsage: true`.
- Return errors to Cobra — no `os.Exit()` in command handlers.

## 14. Code Style

### Banned keywords and patterns
- **`reflect`** — BANNED unless explicitly approved with an ADR. Use `errors.As`, type switches, interfaces, generics.
- **`goto`** — BANNED in all Go code.
- **`interface{}` / `any`** — BANNED for model fields and validators. ALLOWED with documentation for intentional sum-types (e.g., `OperationResult.Item`). REQUIRED by Go stdlib for `yaml.Unmarshal` / `json.Decoder.Decode`.
- **`new(T)` for pointer types** — Use `&Type{}` or `ptr.To(val)` instead of `new(string)`, `new(bool)`, etc.
- **`_` prefix on struct fields** — Unused fields must be removed, not silenced.
- **`init()` globals** — Everything wired explicitly in `app.Initialize()`.
- **`log.Printf` / `fmt.Fprintf` below CLI** — Only `slog` in infra/core/api.
- **No implicit defaults** — Values must be passed explicitly. `if x == "" { x = default }` is banned unless approved via ADR.
- **No recomputation of existing values** — Compute once, reference thereafter.
- **No 1:1 deep copies** — Return repo result directly. Only copy when transforming fields or changing types.
- **No cargo-cult validation** — Don't validate compile-time constants with regex.

### Package naming
- Directory name = package name. No underscores.
- No `Xcore` aliases. Use bare package names: `vm` not `vmcore`, `binary` not `binarycore`.
- Only keep aliases where stdlib collision exists.
- When two packages share a name, the `lib` package gets the `lib` prefix alias: `libnet "mvmctl/internal/lib/network"`.

### Constants
- `OverridableDefaults` lives in `internal/infra/constants.go` — single source of truth.
- Ordered binary lists are `[...]string` arrays (not slices) to prevent runtime mutation.
- Firewall chain names are compile-time constants: `infra.MVMForwardChain = "MVM-FORWARD"`.
- Timestamps: `time.RFC3339` constant. No hardcoded format strings.

### Error handling style
- `errors.As` / `errors.Is`, not multi-type assertion chains.
- `errors.As(err, &de)` + switch on `de.Code` for DomainError.
- No discarded errors: every error return must be checked or explicitly intended. If ignored, assign to `_` with a comment explaining why.

### Methods
- No hard limit on method length. 50+ lines is fine if logic is linear and clear.
- Private helpers only for reused logic or genuinely complex operations.
- Prefer early returns over nested if/else branching.
- Domain-specific utility functions that don't reference the Service struct live in `utils.go`.

## 15. Enrichment

Cross-domain enrichment in `internal/enricher/`:
- Explicit `switch/case` dispatch per relation type — NO reflect, NO string dispatch.
- Enricher wired once at startup with all repository interfaces.
- Called from API layer, not from core.
- Enrichment methods belong in service layer (e.g., `svc.EnrichWithLeases(ctx, networks, leaseRepo)`), not in API layer.

## 16. Input/Request/Resolved Triple

Every public-facing domain follows this three-struct pattern in `pkg/api/inputs/`:

1. **`*Input`** — Raw CLI input. Thin struct with typed fields. Optional fields are `*T`. No DB-backed defaults.
2. **`*Request`** — Accepts Input and dependencies (DB, repos, enricher). `Resolve(ctx)` looks up DB-backed records, validates, returns Resolved.
3. **`Resolved*`** — Immutable output. Every field explicit and validated. No `nil` for required fields.

## 17. Subprocess Services

Long-running subprocess services in `internal/service/`:
- `console/` — PTY relay goroutine
- `nocloudnet/` — NoCloud HTTP metadata server goroutine
- `loopmount/` — Loop-mount provisioner subprocess

Each follows: `Config` struct → `Run(ctx, cfg)` (blocking) → `Spawn(ctx, cfg, extraFiles...)` (background via `system.SpawnService`).

Compiled into the same `mvm` binary. Entry via `mvm run <service>`.

## 18. Performance

- N+1 query prevention: pass resolved domain objects through the pipeline, not identifiers.
- Batch /proc reads: single syscall for meminfo, single read for cpuinfo — parse all fields in one pass.
- Single OS detection pass before optimization — no re-detection.
- Parallelize HTTP-bound config resolution per config using goroutines + `sync.WaitGroup` + `sync.Mutex`, sequential between phases.
- No backend fallback: fail fast, let user fix root cause.
- Provisioner type resolved once at startup, not per-call.
- `Provisioner` struct created once, backend session created once per `Run()`.
- No duplicate content generation: generate once, pass to both check and writer.

## 19. ID Generation

All entity IDs use `internal/lib/crypto/` package-level functions:

```go
crypto.ImageID(...)
crypto.VMID(...)
crypto.ShortenID(...)
crypto.UUIDV4()
```

No raw `sha256.Sum256` or `fmt.Sprintf("%x", ...)` for ID generation.

## 20. Privilege Checking

Privilege checking in `internal/lib/system/privilege.go`:
- Cross-domain utility used by vm, cache, host, network API layers.
- Zero dependency on host domain types.
- `IsRoot()` uses `os.Geteuid()` (effective UID) for permission decisions.
- `CheckPrivileges(binary, operationDescription)` returns structured `PrivilegeDetails` error.
- `SessionHasGroup()` checks if mvm group is active in current process credentials.

## 21. Service vs Controller Construction

**Service** constructors take repos/options only (no entity):
```go
network.NewService(repo, tracker)
```

**Controller** constructors take a specific entity + repos:
```go
vm.NewController(vm, repo)
network.NewController(net, repo)
```

Rule: If a type requires a specific entity to construct, it's a Controller, not a Service.

## 22. Firewall Backends

Two independent backends: **nftables** (default) and **iptables** (legacy).
- Selected by `firewall_backend` setting.
- Exactly one active per session.
- Both DB tables persist independently; only active backend's table is queried.
- Selection: `firewall.NewFirewallTracker(backend, xtcommentAvail, db)`.

## 23. Provisioner Backends

Two independent backends: **LoopMount** (default) and **GuestFS** (opt-in).
- Mutually exclusive — single operation uses exactly one backend.
- `guestfs_enabled` setting is a toggle selector, not a preference.
- Resolved once at startup in `api.NewOperation()` by reading `settings.guestfs_enabled`.
- Backend interface in `internal/lib/provisioner/backend.go`.

## 24. Import Conventions

| Layer | Imports from |
|-------|-------------|
| CLI | `pkg/api`, `internal/cli/common`, `internal/infra`, `internal/lib/*`, `internal/workflow/env` |
| API | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/lib/*`, `internal/service/*` |
| API inputs | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/lib/*` |
| Core domain | `internal/infra`, `internal/lib/*` only |
| Enricher | `internal/core/*`, `internal/lib/model`, `internal/infra/pool` |
| Service | `internal/infra`, `internal/lib/*` |
| Workflow/env | `pkg/api`, `internal/infra`, `internal/lib/*`, `internal/workflow/*` |
| Infra/lib | stdlib, `github.com/jmoiron/sqlx`, external deps |

## 26. Shell Completion

All CLI commands with positional arguments MUST have a `ValidArgsFunction` for shell autocompletion (bash/zsh/fish/powershell).

### Where completion functions live

- **Shared/reusable completions** — `internal/cli/completion.go`. One function per entity type (e.g., `completeVMNames`, `completeVolumeNames`, `completeKeyNames`).
- **Position-aware dispatch** — `internal/cli/completion.go`. Functions that return different completions based on `len(args)` (e.g., `completeVMThenVolume`).
- **Single-use inline** — Defined inline in the command constructor for simple cases (e.g., file path completion for `image import`).

### ShellCompDirective usage guide

| Directive | When to use |
|-----------|-------------|
| `NoFileComp` | Entity completions (VM names, volume names, key names, etc.) — never show files |
| `Default` (0) | Fall back to native file/path completion (e.g., `vm snapshot` mem_file/state_file args) |
| `FilterFileExt` | File completion filtered by extension — return extensions in results (e.g., `env apply` for `*.yaml`/`*.yml`) |
| `FilterDirs` | Directory-only completion (e.g., `key export` path arg) |

### Position-aware dispatch pattern

When a command has multiple positional args of different types, dispatch on `len(args)`:

```go
func completeVMThenVolume(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
    if len(args) == 0 {
        return completeVMNames(cmd, args, toComplete)  // arg0: VM names
    }
    return completeVolumeNames(cmd, args[1:], toComplete)  // arg1+: volume names
}
```

Existing examples in codebase:
- `completeConfigGet` / `completeConfigSet` — dispatch by category vs key
- `completeVMThenVolume` — VM names then volume names
- `completeVMThenFile` — VM names then file paths
- `completeKeyThenDir` — key names then directory paths
- `completeVolumeThenSize` — volume names then size suggestions

### Smart composite completions

For arguments that accept multiple types (e.g., `env destroy` accepts both workflow IDs and spec file paths), read both sources and return a combined list:

```go
func completeEnvDestroy(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
    if len(args) > 0 {
        return nil, cobra.ShellCompDirectiveNoFileComp
    }
    var results []string
    // 1. Workflow IDs from state directory
    // 2. YAML/YML files from current directory
    return results, cobra.ShellCompDirectiveNoFileComp
}
```

### Rules

- Every command taking existing entities MUST complete them. No entity-taking command without `ValidArgsFunction`.
- Commands creating new entities (e.g., `key create [name]`) do NOT need completion for that arg.
- `ValidArgsFunction` MUST silently return empty/`NoFileComp` when `opRef == nil` (no DB initialized).
- Do NOT import packages that create side effects from completion functions — they may run before DB init.
- Inline `ValidArgsFunction` is acceptable for ≤ 3 lines. Otherwise define a named function in `completion.go`.

## 27. Verification Checklist

Before declaring code complete:
- [ ] Does `go build ./...` pass?
- [ ] Does `go vet ./...` pass?
- [ ] Is `ctx context.Context` the first param in every method/side-effect function?
- [ ] Did I avoid `reflect`, `goto`, `log.Printf`, `init()`, `os.Exit()` in handlers?
- [ ] Did I avoid `new(T)` for pointer types? (use `&Type{}` or `ptr.To()`)
- [ ] Did I avoid `_ =` for discarded errors? (check or comment why ignored)
- [ ] Do error messages start with lowercase? (Go convention)
- [ ] Did I use `*T` for every nullable field where zero value has meaning?
- [ ] Did I avoid 1:1 deep copies of model types?
- [ ] Did I avoid implicit defaults? (pass values explicitly)
- [ ] Did I avoid Python-style type names in error messages? (use `fmt.Sprintf("%T", v)`)
- [ ] Are scan helpers inlined? (no shared `scanXxx` functions)
- [ ] Is context propagated into all infra utilities with side effects?
- [ ] Did I use `time.RFC3339` for timestamps? (no hardcoded format strings)
- [ ] Are CLI aliases present (`ls`+`list`, `rm`+`remove`+`delete`+`del`)?
- [ ] Do table commands always show headers, even when empty?
- [ ] Is `OverridableDefaults` imported from `internal/infra/constants.go`? (no duplicates)
- [ ] Are utility functions in `utils.go` if they don't reference Service struct?
- [ ] Did I use `system.RunCmd()` for all subprocess calls? (no raw `os/exec` except documented exceptions)
