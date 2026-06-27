# mvmctl Go Coding Standards

Coding standards, conventions, and architectural rules for the mvmctl Go codebase.

## Table of Contents

- [1. Package Structure](#1-package-structure)
- [2. Layer Architecture](#2-layer-architecture)
- [3. Domain Patterns](#3-domain-patterns)
- [4. Application Initialization](#4-application-initialization)
- [5. Error Handling](#5-error-handling)
- [6. Context Propagation](#6-context-propagation)
- [7. Subprocess Execution](#7-subprocess-execution)
- [8. Model Types](#8-model-types)
- [9. Database Patterns](#9-database-patterns)
- [10. Concurrency](#10-concurrency)
- [11. Logging](#11-logging)
- [12. Testing](#12-testing)
- [13. CLI Patterns](#13-cli-patterns)
- [14. Code Style](#14-code-style)
- [15. Enrichment](#15-enrichment)
- [16. Input Pattern v2](#16-input-pattern-v2)
- [17. Subprocess Services](#17-subprocess-services)
- [18. Performance](#18-performance)
- [19. ID Generation](#19-id-generation)
- [20. Privilege Checking](#20-privilege-checking)
- [21. Service vs Controller Construction](#21-service-vs-controller-construction)
- [22. Import Conventions](#22-import-conventions)
- [23. Shell Completion](#23-shell-completion)
- [24. Verification Checklist](#24-verification-checklist)
- [25. Commenting Standards](#25-commenting-standards)

## 1. Package Structure

| Path | Purpose | Import Rules |
|------|---------|--------------|
| `cmd/mvm/` | Binary entry point | Imports `internal/app`, `internal/cli`, `internal/cli/common` |
| `internal/app/` | Application initialization and DI wiring | Orchestrates `internal/lib/db`, `internal/lib/download`, `internal/lib/version`, `internal/infra`, `internal/core/config`, `pkg/api` |
| `internal/cli/` | Cobra CLI commands and user-facing output | Imports `pkg/api`, `internal/cli/common`, `internal/infra`, `internal/lib/*` |
| `internal/cli/common/` | Shared CLI helpers (tables, JSON output, prompts) | Imports `internal/infra`, `internal/lib/*` |
| `internal/core/{domain}/` | Domain logic (vm, network, image, kernel, binary, key, host, volume, config, console, logs, cloudinit, cache, ssh, snapshot, vsock) | Imports `internal/infra`, `internal/lib/*`. NEVER imports other `core/*` packages. |
| `internal/enricher/` | Cross-domain enrichment (only package besides `pkg/api` that imports multiple core domains) | Imports `internal/core/*` |
| `internal/service/` | Background subprocess services (console relay, nocloudnet server, loopmount provisioner) plus embedded vsock guest agent (`vsockagent/`) | Imports `internal/infra`, `internal/lib/*`. Never imports `pkg/api` or `internal/cli/`. |
| `internal/workflow/env/` | Environment workflow orchestration (apply/destroy specs) | Imports `pkg/api`, `internal/infra`, `internal/lib/*` (specifically `internal/lib/workflow`). Never imports `internal/core/` or `internal/enricher/` directly. |
| `internal/infra/` | Generic leaf utilities (constants, io, template, yaml, cast, slice, pool, ptr, event, provcontent, timinglog, progress, vm) | Imports ONLY stdlib and external deps. Never imports core, api, cli, or service. |
| `internal/lib/` | Domain-adjacent leaf utilities (system, model, db, download, version, logging, crypto, firewall, firecracker, provisioner, network, archive, asset, disk, validators, workflow) | Imports ONLY stdlib and external deps. Never imports core, api, cli, or service. |
| `pkg/api/` | Public API orchestration layer | Imports `internal/core/*`, `internal/enricher/`, `internal/infra`, `internal/lib/*` |
| `pkg/api/inputs/` | Input Validate/Resolve structs (ADR-0011) | Imports `internal/core/*`, `internal/enricher/`, `internal/infra`, `internal/lib/*` |
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
- Validation lives in `pkg/api/inputs/` — `*Input` structs with `Validate()` / `Resolve()` (ADR-0011)
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
- Constructor takes entity + repo: `vm.NewController(vm *model.VMItem, repo Repository)`
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

Single error type: `pkg/errs.DomainError` with fields `Code`, `Class`, `Message`, `Err`, `Op`, `Entity`, `Details`.

```go
// Creating errors
errs.New(errs.CodeVMNotFound, "VM not found: my-vm")
errs.Wrap(errs.CodeNetworkBridgeFailed, err)
errs.WrapMsg(errs.CodeDownloadFailed, "failed to fetch image", err)
errs.NotFound(errs.CodeVMNotFound, "my-vm")
errs.AlreadyExists(errs.CodeVMAlreadyExists, "my-vm")

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

ONE canonical path: `system.DefaultRunner.Run(ctx, args, system.RunCmdOpts{...})` or `system.DefaultRunner.Stream(ctx, args, system.RunCmdOpts{...})` in `internal/lib/system/runner.go`.

```go
// Correct
result, err := system.DefaultRunner.Run(ctx, []string{"ip", "link", "set", tap, "down"}, system.RunCmdOpts{Privileged: true})

// Forbidden
exec.Command("iptables", ...) // NEVER
```

Documented exceptions for direct `os/exec` / `os/exec.CommandContext`:

| Location | Why DefaultRunner doesn't work |
|----------|-------------------------------|
| `internal/core/vm/firecracker.go` | Needs `pass_fds` for VM API socket and log file descriptors |
| `internal/core/ssh/utils.go` | SSH connectivity probe uses `exec.CommandContext` with short-lived probe context |
| `internal/service/loopmount/provisioner.go` | Direct provisioning engine with chained losetup/mount/umount/chroot |
| `internal/service/vsockagent/exec.go` | Guest agent command execution via `su`/`sh` with `exec.CommandContext` |
| `internal/service/vsockagent/pty.go` | Guest agent PTY session via `su` with `exec.CommandContext` |
| `internal/lib/archive/archive.go` | xz decompression via pipe-based `exec.CommandContext` |
| `internal/lib/system/runner.go`, `interactive_run.go`, `spawn.go` | Implementation of `DefaultRunner.Run()` / `Stream()` / `SpawnService` itself |

**Binary lookup carve-out:** Utility files across the codebase use `exec.LookPath()` (not `exec.Command`/`exec.CommandContext`) solely to check whether a system binary exists before calling it through `DefaultRunner.Run()`. This is NOT a subprocess execution — it's a filesystem existence check. These files are not listed as exceptions above and do not violate the subprocess rule.

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

- Workers default to `min((runtime.NumCPU() or 4) * 2, len(items))` (minimum 1) when ≤ 0.
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

Three-level architecture — see `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` for the full specification.

### L0: Pure Function Tests (Go)
- Table-driven `map[string]struct{...}` with `t.Run()`. No I/O, no DB, no subprocess.
- Input → output only. Runs in microseconds.
- Example: `ParseDiskSize("1G") == 1073741824`

### L1: Hermetic Integration Tests (Go)
- Uses real I/O in controlled environments: file-based SQLite, `t.TempDir()`, `FakeRunner` for subprocess calls that can't run in CI.
- No networking, no KVM, no sudo.
- Example: seed DB → call handler → verify JSON output via `cmp.Diff`.
- Run via `go test ./...`.

### L2: Runner VM System Tests (Python in `tests/system/`)
- Real binary, real subprocess, real infrastructure inside a disposable Firecracker VM with nested KVM.
- No mocking of any kind — the binary is real, the subprocesses are real.
- **Ground truth:** A feature is not tested until it has an L2 test. L0/L1 are fast pre-filters, not replacements.
- Run via `pytest tests/system/` inside the runner VM.

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
- **`new(T)` for pointer types** — Use `&Type{}` or `ptr.Ptr(val)` instead of `new(string)`, `new(bool)`, etc.
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
- Enrichment methods belong on the `*Enricher` struct (e.g., `enr.EnrichNetwork(ctx, networks, "leases")`), called from API layer, not from core.

## 16. Input Pattern v2

Every public-facing domain in `pkg/api/inputs/` uses a single `*Input` struct with
`Validate()` and `Resolve()` methods (ADR-0011). No `*Request` wrapper struct.

### Rules

1. **`*Input`** — Raw user input. Optional fields are `*T`. No DB-backed defaults.
2. **`Validate() error`** — Checks input fields before resolution. Called first
   in `Resolve()`, but callers may call it separately for early-exit patterns
   (e.g., `Remove` with `BatchResult`).
3. **`Resolve(ctx, deps...) (result, error)`** — Looks up DB records, resolves
   defaults, returns domain entities or a `Resolved*` struct (kept only when
   output shape differs from input).
4. **No `*Request` struct** — Deps passed as function parameters, not stored on
   a wrapper.

### Simple lookup (no Resolved*):

```go
type VMInput struct {
    Identifiers []string
    Force       bool
}

func (i *VMInput) Validate() error { ... }
func (i *VMInput) Resolve(ctx, vmRepo) ([]*model.VMItem, error) { ... }

// Caller:
vms, err := input.Resolve(ctx, op.Repos.VM)
```

### Create with Resolved* (output shape differs):

```go
type VolumeCreateInput struct {
    Name   string
    Size   string
    Format *string
}

type ResolvedVolumeCreateInput struct {
    Name      string
    SizeBytes int64
    Format    model.VolumeFormat
    Path      string
}

func (i *VolumeCreateInput) Validate() error { ... }
func (i *VolumeCreateInput) Resolve(ctx, repo) (*ResolvedVolumeCreateInput, error) { ... }

// Caller:
resolved, err := input.Resolve(ctx, op.Repos.Volume)
```

### Multi-domain resolution:

```go
type SnapshotRestoreInput struct {
    SnapshotID string
    Network    *string
}

func (i *SnapshotRestoreInput) ResolveSnapshot(ctx, snapRepo) (*model.SnapshotItem, error) { ... }
func (i *SnapshotRestoreInput) ResolveNetwork(ctx, netRepo) (*model.NetworkItem, error) { ... }

// API layer branches on results:
snap, _ := input.ResolveSnapshot(ctx, snapRepo)
net, _ := input.ResolveNetwork(ctx, netRepo)
if net == nil { net = snap.Network }
```

## 17. Subprocess Services

Long-running subprocess services in `internal/service/`:
- `console/` — PTY relay goroutine (entry: `mvm run console relay`)
- `nocloudnet/` — NoCloud HTTP metadata server goroutine (entry: `mvm run nocloudnet serve`)
- `loopmount/` — Loop-mount provisioner subprocess (entry: `mvm run provision`)
- `vsockagent/` — Embedded guest agent binary (cross-compiled, zstd-compressed, injected at runtime via vsock — no `mvm run` entry)

Each subprocess service follows: `Config` struct → `Run(ctx, cfg)` (blocking) → `Spawn(ctx, cfg, extraFiles...)` (background via `system.SpawnService`).

All compiled into the same `mvm` binary.

## 18. Performance

- N+1 query prevention: pass resolved domain objects through the pipeline, not identifiers.
- Batch /proc reads: single syscall for meminfo, single read for cpuinfo — parse all fields in one pass.
- Single OS detection pass before optimization — no re-detection.
- Parallelize HTTP-bound config resolution per config using goroutines + `sync.WaitGroup` + `sync.Mutex`, sequential between phases.
- No backend fallback: fail fast, let user fix root cause.
- Provisioner type resolved once at startup, not per-call.
- `Backend` created per VM creation via `provisioner.NewBackend()`; operations queued and executed via `backend.Run()`.
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

## 22. Import Conventions

| Layer | Imports from |
|-------|-------------|
| CLI | `pkg/api`, `pkg/api/inputs`, `pkg/api/results`, `pkg/errs`, `internal/cli/common`, `internal/infra`, `internal/lib/*`, `internal/workflow/env`, `internal/service/*` (for `mvm run <service>`) |
| API | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/infra/event`, `internal/lib/*`, `internal/assets`, `internal/service/*`, `pkg/errs`, `pkg/api/inputs`, `pkg/api/results` |
| API inputs | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/lib/*`, `pkg/errs` |
| Core domain | `internal/infra`, `internal/lib/*`, `internal/assets`, `internal/service/*` (for subprocess spawning) — no other core domains |
| Enricher | `internal/core/*`, `internal/lib/model`, `pkg/errs` |
| Service | `internal/infra`, `internal/lib/*` |
| Workflow/env | `pkg/api`, `pkg/api/inputs`, `pkg/api/results`, `internal/infra`, `internal/lib/*`, `internal/workflow/*` |
| Infra/lib | stdlib, `github.com/jmoiron/sqlx`, external deps |

## 23. Shell Completion

All CLI commands with positional arguments MUST have a `ValidArgsFunction` for shell autocompletion (bash/zsh/fish/powershell).

### Where completion functions live

- **Shared/reusable completions** — `internal/cli/completion.go`. One function per entity type (e.g., `completeVMNames`, `completeVolumeNames`, `completeKeyNames`).
- **Position-aware dispatch** — `internal/cli/completion.go`. Functions that return different completions based on `len(args)` (e.g., `completeVMThenVolume`).
- **Single-use inline** — Defined inline in the command constructor for simple cases (e.g., file path completion for `image import`).

### ShellCompDirective usage guide

| Directive | When to use |
|-----------|-------------|
| `NoFileComp` | Entity completions (VM names, volume names, key names, etc.) — never show files |
| `Default` (0) | Fall back to native file/path completion (e.g., `snapshot restore` mem_file/state_file args) |
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

## 24. Verification Checklist

Before declaring code complete:
- [ ] Does `go build ./...` pass?
- [ ] Does `go vet ./...` pass?
- [ ] Is `ctx context.Context` the first param in every method/side-effect function?
- [ ] Did I avoid `reflect`, `goto`, `log.Printf`, `init()`, `os.Exit()` in handlers?
- [ ] Did I avoid `new(T)` for pointer types? (use `&Type{}` or `ptr.Ptr()`)
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
- [ ] Did I use `system.DefaultRunner.Run()` for all subprocess calls? (no raw `os/exec` except documented exceptions)
- [ ] Commenting checklist checked? (see §25.18)

## 25. Commenting Standards

### 25.1. Purpose: Why Comments Exist

Comments exist for one reason only: **to preserve decisions for future
contributors (including yourself in 6 months).**

When you write a comment, you are answering a question that someone will ask
later: *"Why was this done this way?"* The code always answers *"What was
done."* If the code is expressive enough that a reader never asks "why," no
comment is needed.

**The test:** Imagine a new contributor lands on this code. They read the
function name, the types, the logic. Do they need more context to understand
the reasoning? If yes, write a comment. If no, the comment is noise.

```go
// BAD — code already says what's happening. No decision to preserve.
// Increment the counter by 1.
counter++

// GOOD — preserves a non-obvious decision.
// Pre-decrement: POSIX shell treats `exit -1` as `exit 255`.
// We pass the signal number as a negative exit code so the parent
// can recover the signal number from the exit status.
exitCode := -signalNum
```

**A comment that merely paraphrases the next line of code is chatter.**
Every comment earns its place by explaining something the code cannot.

### 25.2. The Value Test

Every comment must pass: *"If this comment were gone, would the code be
harder to understand, modify, or review?"*

If no, the comment is noise. Delete it.

### 25.3. Godoc (Exported Symbols)

Every exported type, function, method, constant, and variable MUST have a
godoc comment. Follow Go convention:

```go
// VM represents a single microVM instance.
type VM struct { ... }

// Status returns the current lifecycle status of the VM.
func (c *Controller) Status() model.VMStatus { ... }
```

**Godoc rules specific to this project:**
- Start with the identifier name (`// VM represents...`, not `// This struct represents...`).
- Do NOT reference predecessor implementations or porting history. The
  godoc is the public face of the package.
- Do NOT document JSON/DB struct tags in godoc. Tag documentation goes on
  the field itself (see §25.9).
- `// Package <name>` comment is MANDATORY for every package. Include the
  layer role (CLI, API, Core, Infra, Lib) and what the package does:

```go
// Package vm provides VM lifecycle operations.
// Layer: Core domain — never imports other core/* packages.
package vm
```

### 25.4. Package-Level Comments

Every `.go` file in a package does NOT need its own file-level comment. Only
the package doc comment (in one canonical file, typically the main file or
`types.go`) is required.

File-level comments are reserved for files that have a non-obvious reason to
exist as a separate file (e.g., `// utils.go contains helpers that don't
reference the Service struct.`).

### 25.5. Section Headers

Section headers visually divide large files. Use plain ASCII only:

```go
// --- Section Name ---
```

**Rules:**
- Keep the total width ≤ 60 characters (including the `// ` prefix).
- ONE blank line before the header, ONE after.
- Do NOT use full-width rulers (`// ===...===` or `// ═══...═══`). They are
  visual noise and waste vertical space.
- Do NOT use section headers as a substitute for proper function naming. If
  a section has only one function, drop the header.
- Acceptable in files >200 lines. Omit in small files.

```go
// GOOD (≤ 60 chars):
// --- VM Lifecycle ---

// BAD (full-width ruler):
// ================================================================
```

### 25.6. Implementation Notes ("Why" Comments)

This is the MOST VALUABLE comment type. Use it liberally.

```go
// Check IP address BEFORE dangerous chars. IPv4 addresses contain dots,
// which are in DangerousChars (path traversal). Without this ordering,
// "192.168.1.1" would be rejected as having dangerous chars.
if isIP(s) { return s }
if hasDangerousChars(s) { return err }
```

**Rules:**
- State the reasoning, not the mechanics.
- If a decision was debated in an ADR or PR discussion, link to it:
  `// See ADR-0003: loopmount-guestfs-mutual-exclusion.`
- If behavior is dictated by an external standard, cite it:
  `// POSIX.1-2017: kill(2) with signal 0 checks process existence.`

### 25.7. Structured Annotation Prefixes

Use these prefixes for cross-cutting concerns. They make comments greppable
and signal urgency:

| Prefix | When to use |
|--------|-------------|
| `// NOTE:` | Notable behavior that might surprise a reader |
| `// IMPORTANT:` | A precondition or postcondition that MUST be understood |
| `// CRITICAL:` | A correctness requirement — getting this wrong causes data loss, corruption, or security holes |
| `// THREAD-SAFE:` | Marks a type or function as safe for concurrent access |
| `// CALLER MUST HOLD:` | Documents which lock must be held when calling a function |
| `// DEPRECATED:` | Marks a symbol for removal (use Go's `// Deprecated:` godoc convention) |
| `// HACK:` | A temporary workaround that should be removed when the underlying issue is fixed |
| `// PERF:` | Performance note explaining why a non-obvious optimization exists |

```go
// NOTE: cancel is NOT deferred here. Stream returns the channel
// immediately. defer cancel() would cancel runCtx right away.
cancel()

// IMPORTANT: Go zero-initializes Mode to 0, NOT 0644.
// The caller MUST set Mode explicitly before writing.
type FileSpec struct { Mode os.FileMode }

// CRITICAL: SIGKILL cannot be trapped. If we send SIGKILL instead of
// SIGTERM, cleanup never runs and the VM is left in an unknown state.
```

**Rules:**
- Use the EXACT prefix (colon + space). `// NOTE:` not `// Note:` or `// note:`
- Do NOT create new prefixes without updating this standard.
- Multiple annotations on one comment: `// NOTE: IMPORTANT:` is allowed.

### 25.8. Safety, Security, and Concurrency Annotations

Safety, security, and concurrency concerns MUST be annotated. These are the
highest-value comments in the codebase.

```go
// THREAD-SAFE: Uses sync.RWMutex. Readers do not block readers.
func (c *Controller) Status() model.VMStatus { ... }

// CALLER MUST HOLD: c.mu.Lock()
func (c *Controller) transitionState(to model.VMStatus) { ... }

// CRITICAL: This function runs as root. Validate all input paths
// before passing them to shell commands.
func (s *Service) ApplyRules(ctx context.Context, rules []Rule) error { ... }
```

**Rules specific to this project:**
- Every exported function that is NOT thread-safe MUST document the lack of
  safety if the type name suggests it might be (e.g., a `Manager` or `Pool`).
- Every function that executes with privilege escalation (`Privileged: true`)
  in a subprocess call must have a security annotation explaining what input
  is validated.
- Every `sync.Mutex` or `sync.RWMutex` field must have a comment explaining
  what it guards.

### 25.9. Struct Field Comments

Field comments go on the field itself, inline or above it:

```go
type VM struct {
    ID   string `db:"id"   json:"id"`
    Name string `db:"name" json:"name"`

    // RelaySocketPath is set at runtime when the console relay connects.
    // Not persisted in the database.
    RelaySocketPath *string `db:"relay_socket_path" json:"relay_socket_path"`
}
```

**Rules:**
- Document fields with non-obvious semantics: runtime-only, optional with
  special meaning, mutually exclusive with another field, etc.
- Do NOT document fields where the name + type + tags are self-explanatory
  (`ID string`, `Name string`).
- For structs with 6+ fields, group logically related fields with an inline
  `// --- group name ---` where helpful.
- When a field stores a JSON or YAML blob, document the expected schema.

### 25.10. Test Comments

Use the `// Rationale:` pattern in tests:

```go
func TestPause_AlreadyPaused(t *testing.T) {
    // Rationale: Pause must reject invalid state transitions before
    // any I/O. Calling Pause on an already-paused VM should be a
    // no-op or error, not a double-pause attempt.
}
```

**Rules:**
- Every test covering a non-obvious edge case MUST have a `// Rationale:`
  comment explaining what scenario it covers and why it matters.
- Happy-path tests do NOT need `// Rationale:` if the name is clear
  (e.g., `TestCreate_DefaultImage`).
- Table-driven test entries do NOT need per-entry `// Rationale:` if the
  entry `name` field carries the scenario description.
- Test helper functions MUST be documented if they set up non-trivial state.

### 25.11. Architecture Boundary Comments

Packages that enforce architectural rules through convention (not the
compiler) MUST document their boundary in the package comment:

```go
// Package enricher provides cross-domain enrichment.
// This is the ONLY package that imports across multiple core/* packages.
// Layer: API-adjacent — called from pkg/api/ only.
```

These are the closest thing to automated architecture enforcement without a
custom linter. They MUST be present in every package that has non-obvious
import restrictions.

### 25.12. Reference Comments (External Links)

When code behavior is dictated by an external specification, RFC, or standard:

```go
// See https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
// Vsock port 52 is the well-known VM vsock port.
```

**Format:**
- Always use the full URL.
- Include what aspect of the spec is relevant in surrounding text.

### 25.13. TODO / HACK / DEPRECATED Conventions

**TODO:** Use only when paired with an actionable plan.

```go
// TODO(alcortes): call SetTimingEnabled() from app/app.go explicitly
// when the config package supports runtime reloads.
// Issue: #NNN
```

A bare `// TODO: implement X` with no owner, no issue, and no timeline is a
version of "never." Do not write it. If the task is real, file an issue.

**HACK:** Every HACK must name the root cause and the conditions under which
it can be removed.

```go
// HACK: The upstream library v0.4.2 drops the last byte on EOF.
// Remove this workaround when we upgrade to v0.5.0+.
// Tracked in: #NNN
```

**DEPRECATED:** Use Go's standard `// Deprecated:` godoc convention:

```go
// Deprecated: Use NewController instead. Will be removed in v0.8.0.
```

### 25.14. Commented-Out Code

**BANNED.** Do not leave commented-out code in the codebase.

```go
// BAD: commented-out code
// var result, err = doSomething(ctx)

// GOOD — delete it entirely. Git history exists for a reason.
```

**Only exception:** A one-line commented-out import or constant with a
comment explaining when to uncomment it.

### 25.15. Banned Comment Patterns

| Pattern | Example | Why |
|---------|---------|-----|
| Full-width rulers | `// =======...=======` | Visual noise, adds no information |
| Chatter (code restatement) | `// Return nil` above `return nil` | Zero information |
| Stub comments | `// Extra networks — future improvement` | Vague, untracked |
| Pre/post decorations | `// ----- Start Section -----` | Use `// --- Section ---` instead |
| Porting ancestry references | `// Matches Python's X exactly` | Irrelevant in a stable Go codebase |
| Inline foreign-code pseudocode | A comment showing code in another language | The code is Go. Show Go. |

### 25.16. Comment Style Conventions

- Use `//` line comments exclusively. Never `/* */` block comments for
  documentation (acceptable only in CGO export blocks).
- Sentence fragments are acceptable in field comments and inline notes.
  Use complete sentences for godoc and implementation notes.
- Wrap comment lines at 100 characters as a soft guideline. Longer lines are acceptable
  when wrapping would harm readability (e.g., godoc descriptions, inline URLs).
- One space after `//`: `// text`, not `//  text`.

### 25.17. Automated Enforcement

Not all rules can be automated, but these can:

| Rule | How to check |
|------|-------------|
| Full-width rulers | `grep -rn '// ====\|// ════' internal/ pkg/ cmd/` |
| Porting ancestry references | `grep -rn '\bPython\b' internal/ pkg/ cmd/` (post-cleanup, should be zero) |
| Commented-out Go code | Hard to automate fully. Watch for in review. |
| Section header format | `grep -rn '// ──\|// ══' internal/ pkg/ cmd/` — should be zero after migration |

Consider adding a `scripts/check-comments.sh` that runs these checks in CI
once the standards are finalized and the big-bang cleanup is complete.

### 25.18. Verification Checklist Additions

Add these to the verification checklist when adding or modifying comments:

- [ ] Does every comment I added explain **why**, not **what**?
- [ ] Did I use `// NOTE:`, `// IMPORTANT:`, or `// CRITICAL:` for safety concerns?
- [ ] Did I avoid full-width section rulers?
- [ ] Did I check for and remove any commented-out code?
- [ ] If I added a section header, is it `// --- Section Name ---` (≤ 60 chars)?
- [ ] If I referenced an external spec, did I include a full URL?
