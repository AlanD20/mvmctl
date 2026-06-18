# mvmctl

MicroVM Manager -- a speed-first CLI for managing Firecracker microVMs. Provides fast VM lifecycle management, networking, image provisioning, and console/SSH access.

## Table of Contents

- [Language](#language)
  - [Domain](#domain)
  - [Intra-domain orchestration](#intra-domain-orchestration)
  - [Cross-domain orchestration](#cross-domain-orchestration)
  - [Controller (stateful, per-entity)](#controller-stateful-per-entity)
  - [Service (stateless, intra-domain)](#service-stateless-intra-domain)
  - [Repository](#repository)
  - [Service subprocess pattern (internal/service/)](#service-subprocess-pattern-internalservice)
  - [Resolver](#resolver)
  - [Enrichment pattern](#enrichment-pattern)
  - [Validation (caller's responsibility)](#validation-callers-responsibility)
  - [State detection (operation's responsibility)](#state-detection-operations-responsibility)
  - [Invariant guard](#invariant-guard)
  - [Speed-first principle](#speed-first-principle)
  - [Operation struct](#operation-struct)
  - [Direct repository calls in the API layer](#direct-repository-calls-in-the-api-layer)
  - [Input/Input/Resolved triple (public-facing domains only)](#inputinputresolved-triple-public-facing-domains-only)
  - [SQLite schema overview](#sqlite-schema-overview)
  - [Layer compliance enforcement](#layer-compliance-enforcement)
  - [Public API boundary](#public-api-boundary)
  - [CLI is the canonical interface](#cli-is-the-canonical-interface)
  - [Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)](#provisioner-backend-loopmount-vs-guestfs--mutual-exclusion)
  - [Firewall Backend (nftables vs iptables -- mutual exclusion)](#firewall-backend-nftables-vs-iptables--mutual-exclusion)
- [Relationships](#relationships)
- [Test types](#test-types)
- [System Tests](#system-tests)

## Language

### Domain

A business capability with isolated logic. Each domain (vm, network, image, kernel, binary, key, host, config, cache, volume, console, logs, cloudinit, ssh) lives in `internal/core/{domain}/`. Data-heavy domains follow the Controller / Service / Repository / Resolver pattern; simpler domains may have fewer files (e.g., `cache/` has a Service and utils, `ssh/` has a Service, a file-copy module, and utils, `console/` has only a Controller, `host/` has the full Controller/Service/Repository pattern plus a detector, probe, and utils, `config/` has constraints, a Service, a Repository, settings, and utils (no controller), `logs/` has controller + service, `cloudinit/` uses manager + provisioner plus config and utils). Domains do NOT import other domains.

All model types are centralized in `internal/lib/model/` -- a single package with zero domain imports. Every domain and every layer imports from `model` directly. Model types are concrete structs with `db:"column"` and `json:"field"` tags for sqlx and JSON serialization.

### Intra-domain orchestration

Work that sequences multiple operations within a single domain (e.g., teardown NAT -> remove bridge -> delete DB record). Lives in `internal/core/` Service classes.

### Cross-domain orchestration

Work that coordinates across multiple domains (e.g., VM creation orchestrates vm + network + image + kernel + cloudinit). Lives exclusively in `pkg/api/` Operation methods.

### Controller (stateful, per-entity)

A struct bound to a single entity instance. Manages lifecycle state transitions for that entity (start, stop, pause, resume, snapshot). The litmus test: if the operation doesn't need a specific entity instance to exist, it doesn't belong in Controller. Controller communicates with the running entity's Firecracker API socket -- but that's a consequence of the entity-orientation, not the definition. Does NOT validate input. Does NOT orchestrate across domains. Does NOT handle CRUD creation or removal.

Constructed with the entity + its Repository: `vm.NewController(vm *model.VMItem, repo Repository)`. NOT wired at startup -- created per-operation in the Service layer.

*Example: `vm.NewController(vmItem, repo).Snapshot(ctx)` -- snapshots this specific VM. `network.NewService(repo, tracker).RemoveBridge(ctx, bridge)` -- removes a bridge, no single network entity needed.*

### Service (stateless, intra-domain)

A struct for stateless intra-domain operations. Handles infrastructure operations (bridges, TAPs, NAT, subprocesses, file/disk operations). Performs state detection (checking current system state as part of an operation -- "does this bridge exist?" to branch execution). Guards invariants that protect against system damage. Does NOT validate caller input. Does NOT manage state for a single entity -- Service operates on infrastructure, not on a bound instance.

Constructed with repos/options only: `network.NewService(repo Repository, tracker firewall.Tracker)`. Wired once at startup in `app.Initialize()`.

*Litmus test: if the operation would work the same way without a specific entity instance, it's Service. If it needs to communicate with a running entity's Firecracker API socket, it belongs in Controller. If it sequences multiple infrastructure steps (teardown NAT -> remove bridge -> delete DB record), it's intra-domain orchestration in Service.*

### Repository

An interface (defined in `repository.go`) for database CRUD operations. ALL SQL queries live here -- single SQLite implementation in `sqlite.go`. Uses `github.com/jmoiron/sqlx` for struct scanning (`StructScan`, `GetContext`, `SelectContext`). Uses SQL-level computation (COUNT, WHERE IN), never fetch-all-then-filter. Every method takes `ctx context.Context` as its first parameter. JSON-serialized fields (VM's SSHKeys, VolumeIDs, CPUConfig) use intermediate scan structs + `toVM()` conversion method.

The interface is defined in `repository.go`; the SQLite implementation is in `sqlite.go`. Constructor: `NewRepository(db *sqlx.DB) Repository`.

### Service subprocess pattern (internal/service/)

Long-running subprocess services (console relay, nocloud-net server, loopmount provisioner) live in `internal/service/{name}/`. These are compiled into the SAME `mvm` binary -- no separate multidist binary. The CLI layer has an `mvm run <service>` subcommand that serves as the entry point for each service.

Each service follows a consistent three-function pattern:
- **`Config`** struct -- holds all configuration for the service.
- **`Run(ctx, cfg)`** -- runs the service in the foreground (blocking).
- **`Spawn(ctx, cfg, extraFiles...)`** -- launches the service as a background subprocess via `system.SpawnService()`.

Services in `internal/service/`: `console/` (console relay PTY proxy), `nocloudnet/` (NoCloud HTTP metadata server), `loopmount/` (loop-mount provisioner wire protocol).

The dependency direction is: `cli/` -> `services/`. Services never import `cli/` or `pkg/api/`.

Cloud-init domain (`internal/core/cloudinit/`) is distinct from services -- it's a core domain that handles cloud-init config generation, not a subprocess.

### Resolver

A struct for entity resolution by identifier (name, ID prefix, IP, MAC to domain object). Pure resolution -- no enrichment. Enrichment is handled by the `internal/enricher/` package. Resolver delegates to Repository for DB queries.

*Example: `vm.Resolver` has `ByID(ctx, id)`, `ByName(ctx, name)`, `ByIP(ctx, ip)`, `ResolveMany(ctx, identifiers)`. Returns `*model.VMItem` or `ResolveResult{VMs, Errors, ExitCode}`.*

### Enrichment pattern

Cross-domain enrichment is handled by the `internal/enricher/` package -- the ONLY package (besides `pkg/api/`) that imports across multiple core domains. Uses explicit Go switch/case dispatch per relation (NO reflect, NO string dispatch, NO resolver registry).

RelationSpec is defined in `internal/lib/model/relation.go`:
- `FKField` -- field name on the source entity containing the FK value.
- `Resolver` -- string name for soft-fail debug messages.
- `Method` -- resolver method name for single-value resolution.
- `RelationName` -- field name set on the entity after enrichment.
- `IsReverse` -- true for reverse relations (source.id -> list[targets]).
- `BatchMethod` -- optional batch method for N+1 prevention.

Per-domain relation registries are exported variables in `internal/enricher/enrich.go`:
```go
var VMRelations = map[string]model.RelationSpec{
    "kernel":         {FKField: "kernel_id", Resolver: "kernel", Method: "get_kernel", RelationName: "kernel"},
    "image":          {FKField: "image_id",  Resolver: "image",  Method: "get_image",  RelationName: "image"},
    "binary":         {FKField: "binary_id", Resolver: "binary", Method: "get_binary", RelationName: "binary"},
    "network":        {FKField: "network_id", Resolver: "network", Method: "get_network", RelationName: "network"},
    "network.leases": {FKField: "network", Resolver: "network_lease", Method: "list_by_network_id_batch", RelationName: "leases", BatchMethod: "list_by_network_id_batch"},
    "volumes":        {FKField: "volume_ids", Resolver: "volume", Method: "", RelationName: "volumes", BatchMethod: "resolve_by_vm_volume_ids"},
}
```

The `Enricher` struct holds all repository interfaces and is wired once at startup in `app.Initialize()`. Called from the API layer: `enr.EnrichVM(ctx, vms, "kernel", "image", "network")`.

### Validation (caller's responsibility)

Checks that input is structurally valid: format, existence, cross-field constraints. Belongs in API layer (`pkg/api/inputs/` -- `*Input` or `*Request` structs). Does NOT belong in Service or Controller. The caller (API layer) is responsible for passing clean, validated data down.

**Why not validate at every layer:** mvmctl is a speed-first CLI. Redundant subprocess calls in defensive validation add 10-50ms latency each. Many checks duplicate what the operation naturally detects -- `bridge_exists()` called once to "validate" and again to branch execution. Validation in Service conflates concerns and slows operations. The caller-trusts-callee convention means Service receives clean data and executes without defensive checks. If a bug in API validation reaches Service, it's caught by testing at the API boundary.

### State detection (operation's responsibility)

Checks that are inherently part of executing an operation -- detecting whether the system is in state A or B to decide the execution path (e.g., "does this bridge exist?" to branch between create vs reconcile). Belongs in Service/Controller as part of the operation's logic.

### Invariant guard

A check in Service that protects against system damage (e.g., "are TAPs still attached?" before removing NAT rules). The one exception to "Service does not validate" -- these guard against partial-failure states, not invalid input. These are part of the operation, not validation -- they protect against system-level damage from irreversible actions.

### Speed-first principle

Every architectural decision is weighed against its runtime cost. Avoid redundant subprocess calls, unnecessary allocations, and deep call chains. A 10ms subprocess check that duplicates what the operation already detects is a bug.

### Operation struct

A single `api.Operation` struct in `pkg/api/operation.go` with methods for each domain (e.g., `op.VMCreate(ctx, input, onProgress)`, `op.NetworkCreate(ctx, input)`). The ONLY place where multiple domains are imported and sequenced. Handles cross-domain orchestration, cross-domain data passing. The Operation struct holds all repositories, services, and the enricher -- wired once at startup via `api.NewOperation(ctx, conn, cacheDir)`.

### Direct repository calls in the API layer

The API layer may call a Repository directly (e.g., `op.Repos.VM.NamesExist(ctx, names)`, `op.Repos.VM.CountByStatus(ctx, statuses...)`) without going through the Request pipeline when:
- The data is for enrichment or internal orchestration, not user-facing input processing.
- The call is inside an Operation method as part of a multi-step workflow (e.g., checking VM references before removing a network).

The rule: **user-facing input must go through the Request pipeline.** Internal cross-domain data lookups can call Repositories directly from the Operation method, but the result must be passed to Core Service classes (not queried from within Core).

### Input/Input/Resolved triple (public-facing domains only)

Every domain with public-facing input must follow this three-struct pattern in `pkg/api/inputs/`:

1. **`*Input`** -- Raw CLI or external input. Thin struct with typed fields. Optional fields are `*T` -- no DB-backed defaults, no constants-backed defaults. The CLI layer resolves constants before creating this; the API layer resolves DB-backed defaults from `nil` in the Request.

2. **`*Request`** -- Accepts the Input and dependencies (DB, repos, enricher). The `Resolve(ctx)` method looks up DB-backed records for any `nil` identifiers, resolves FK references, validates, and returns a Resolved struct.

3. **`Resolved*`** -- Immutable output of Request.Resolve(). Every field is explicit and validated. No `nil` for required fields.

Mandatory for any domain that has public CLI commands or API endpoints.

*Example: `VMInput{Identifiers, Force}` -> `VMRequest{db, input, resolver, enricher}.Resolve(ctx)` -> `ResolvedVMInput{VMs []*model.VMItem, Force bool}`.*

### SQLite schema overview

Defined in `internal/lib/db/migrations/*.sql`. Accessed via `github.com/jmoiron/sqlx` with `modernc.org/sqlite` driver. PRAGMAs (foreign_keys=ON, journal_mode=WAL, synchronous=NORMAL, busy_timeout=5000) set via DSN parameters in `db.Handle.openLazy()`. Connection pool has `SetMaxOpenConns(1)` and `SetMaxIdleConns(1)` for SQLite's single-writer semantics.

Tables include: `images`, `kernels`, `binaries`, `volumes`, `networks`, `network_leases`, `vm_instances`, `host_state`, `host_state_changes`, `iptables_rules`, `nftables_rules`, `ssh_keys`, `user_settings`.

### Layer compliance enforcement

Architecture rules are enforced by the Go compiler (circular import errors prevent cross-domain imports in core) and code review. Key rules:
- Core domains NEVER import other core/* packages -- enforced by Go compiler.
- CLI imports from `pkg/api/`, `pkg/api/inputs`, `pkg/api/responses`, `pkg/errs`, `internal/cli/common/`, `internal/infra/`, `internal/lib/`, `internal/service/` -- enforced by code review.
- API imports `internal/core/*` + `internal/enricher/` + `internal/infra/` + `internal/infra/event` + `internal/lib/*` + `internal/assets` + `pkg/errs` + `pkg/api/inputs` + `pkg/api/responses`.
- `internal/infra/` and `internal/lib/` are LEAVES -- import NOTHING from core, api, cli, or service.
- `internal/service/` MAY import from `internal/infra/` / `internal/lib/` but NOT from `pkg/api/` or `internal/cli/`.

### Public API boundary

The `pkg/api` package IS the stable, curated public interface for all consumers -- CLI, future TUI/GUI, and external scripts. The `api.Operation` struct exposes all domain operations as methods. The `pkg/api/inputs` package exposes all Input types. The `internal/core` package is an implementation detail.

### Build output (REQUIRED)

**The `mvm` binary MUST be built to `~/.local/bin/mvm`.** This path has passwordless sudo privileges via the mvmctl sudoers rules, so subcommands requiring privilege escalation run without password prompts.

For **release testing / RC QA / system tests**, always use the release build script:
```bash
./scripts/build.sh release          # produces dist/mvm
cp dist/mvm ~/.local/bin/mvm        # copy for sudo operations
```

A bare `go build -o ~/.local/bin/mvm ./cmd/mvm` works for dev but produces a
binary without version info, symbol stripping, or PIE. **Never use it for
release qualification.**

### Asset mirror environment variable (REQUIRED)

**The `MVM_ASSET_MIRROR` env var MUST be set before any `mvm` command.** Without it, asset downloads will fail.

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mvm <subcommand>
```

This variable directs the local asset cache for downloaded kernel images, root filesystems, and firmware blobs.

### CLI is the canonical interface

The `mvm` CLI is the ONLY supported interface for all mvmctl operations. Do NOT bypass it with raw commands. The CLI handles privilege escalation, database state tracking, and dynamic resolution of assets (IPs, keys, images, kernels).

Built with `github.com/spf13/cobra`. Root command in `internal/cli/root.go`. Subcommands in `internal/cli/{domain}.go` -- monolithic files per domain (one file contains all subcommands for that domain).

CLI aliases: Three commands have shorter aliases for faster typing: `mvm net` is an alias for `mvm network`, `mvm img` is an alias for `mvm image`, and `mvm vol` is an alias for `mvm volume`. Every `list` subcommand has both `ls` and `list`. Every `remove` subcommand has `rm`, `remove`, `delete`, and `del`.

### Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)

Two independent rootfs provisioning backends. They are **mutually exclusive** -- a single VM or image operation uses exactly ONE backend, never a combination. The `guestfs_enabled` user setting is a **toggle selector**, not a preference.

- **LoopMount** (default, `guestfs_enabled=false`): Uses the compiled `mvm run provision` subcommand via `losetup` + `mount` + `chroot`. No system package dependencies beyond the `mvm` binary itself.
- **GuestFS** (opt-in, `guestfs_enabled=true`): Uses `libguestfs` via `internal/lib/provisioner/guestfs/`. Requires `libguestfs` system packages installed on the host.

Provisioner type resolved ONCE at startup in `api.NewOperation()` by reading `settings.guestfs_enabled`. All callers use `op.ProvisionerType` directly. Backend interface defined in `internal/lib/provisioner/backend.go`.

**Performance:** GuestFS is 3–5x slower than LoopMount for VM creation (9–14s vs 2–5s wall-clock, sequential). Under parallel load GuestFS degrades further due to QEMU lock contention. See `docs/adr/0003-loopmount-guestfs-mutual-exclusion.md` for full benchmark data.

**Why no fallback chain:** A fallback (try loop-mount, fall back to guestfs) was rejected because: (1) if a user enables GuestFS, they expect GuestFS behavior -- silent fallback to loop-mount violates least surprise; (2) each backend has different sudoers requirements -- mixing them in one session increases the privilege surface; (3) each backend has independent test suites -- a fallback chain requires testing all combinations; (4) an earlier version incorrectly described GuestFS as a "fallback," which caused regression bugs where a stale `guestfs_enabled=true` silently selected the slow backend.

### Firewall Backend (nftables vs iptables -- mutual exclusion)

Two independent firewall backends -- **nftables** (default) and **iptables** (legacy) -- selected by the `firewall_backend` setting. Exactly one is active per session.

- **nftables** (default): Uses `internal/lib/firewall/nftables.go` -- atomic `nft -f -` batch files.
- **iptables** (legacy): Uses `internal/lib/firewall/iptables.go` -- per-rule `iptables` calls.

Selection logic in `firewall.NewFirewallTracker(backend, xtcommentAvail, db)`. Both DB tables persist independently; only the active backend's table is queried.

**Why nftables is default:** nftables is the modern replacement for iptables and is the default firewall on RHEL 9+, Debian 11+, Ubuntu 22.04+, and Arch Linux. It provides atomic batch operations via `nft -f -` (all rules applied or none), cleaner rule management (no separate save/restore workflow), and better performance for large rule sets.

**UFW compatibility:** The nftables backend uses non-hook chains inside the system `ip filter` and `ip nat` tables, with jump rules inserted at position 0 of built-in chains (FORWARD, POSTROUTING, INPUT). This ensures MVM rules evaluate before UFW's. When UFW reloads (`ufw reload`), it flushes built-in chains and removes MVM's jump rules -- these are re-created lazily on the next `mvm network` or `mvm vm create` operation. The iptables backend has the same limitation.

The `FirewallTracker` also reads an `iptables_xtcomment` user setting that adds comment tags to iptables rules for easier identification.

## Relationships

- An **Operation** struct method orchestrates across **Domains**
- A **Service** performs **intra-domain orchestration** using **Controller** and **Repository**
- A **Controller** manages state transitions for exactly one entity
- A **Repository** provides data access for its **Domain** only
- **Validation** runs in the API layer before data reaches **Service** or **Controller**
- **State detection** runs inside **Service** or **Controller** as part of execution
- **Invariant guards** may appear in **Service** when the guard prevents system damage

### Error handling

A single `DomainError` type in `pkg/errs/` handles all error scenarios. Every error has a `Code`, `Message`, `Op`, `Entity`, `Class`, `Err`, and optional `Details`. Codes are dot-separated: `vm.not_found`, `network.subnet.overlap`, `host.init.sudoers_failed`.

```go
// Creating errors
errs.New(errs.CodeVMNotFound, "VM not found: my-vm")
errs.Wrap(errs.CodeNetworkBridgeFailed, err)
errs.WrapMsg(errs.CodeDownloadFailed, "Failed to fetch image", err)

// Checking errors
var de *DomainError
if errors.As(err, &de) {
    switch de.Code { ... }
}
if errs.IsNotFound(err) { ... }
```

Classes categorize errors semantically: `ClassValidation`, `ClassConflict`, `ClassRetryable`, `ClassInternal`, `ClassNeedsInteraction`.

**Logging pattern:**
- **Log before return**: Every error return in Service/Controller has a preceding `slog.Error()` or `slog.Warn()` with operational context.
- **Log message**: Operator-facing -- includes module context, parameter values, and root cause.
- **Error message**: User-facing -- "what happened. why. possible fix." short summary.
- **API layer**: `slog.Info()` for success, `slog.Warn()` for recoverable issues.

Logging uses Go standard `log/slog` throughout. Setup via `internal/lib/logging/`. CLI layer is the sole layer for user-facing output.

### API result types

The API layer returns typed responses:
- **`errs.OperationResult`** -- Single operation result with `Status`, `Code`, `Message`, `Item`, `Exception`, `Metadata`, `Warnings`. JSON-serializable.
- **`errs.BulkResult`** -- Collection of `BulkResultItem` from bulk operations.
- **`errs.NeedsInteraction`** -- Returned when the operation requires user action (e.g., sudo password prompt). Implements the `error` interface for (result, error) return types.
- **`pkg/api/responses/`** -- Domain-specific inspect/list types for JSON serialization (e.g., `responses.VMInspect`, `responses.NetworkInspect`, `responses.HostInfo`).

### Error message format

```
What happened. Why it happened. Possible fix.
```

### Error codes format

Dot-separated with domain prefix:
```
network.subnet.overlap
vm.create.binary_not_found
host.init.sudoers_failed
```

### Coding style

- **Method length**: No hard limit. 50+ lines is fine if logic is linear and clear.
- **Private helpers**: Only for reused logic or genuinely complex operations.
- **Early returns**: Prefer early returns over nested if/else branching.
- **Explicit error handling**: Every error is checked. No bare `recover()` for control flow.
- **Context propagation**: Every repository method, every infrastructure function with side effects takes `ctx context.Context` as its first parameter.
- **No implicit defaults**: Values must be passed explicitly. `if x == "" { x = default }` is banned unless explicitly approved via ADR.
- **No `any` without justification**: `OperationResult.Item any` is documented as intentional sum-type. Other `any` usage must have a comment explaining why concrete typing isn't possible.
- **No Python-style type names**: Go-native `fmt.Sprintf("%T", v)` for type names in error messages.
- **No `reflect`**: Banned unless explicitly approved with an ADR.
- **No `goto`**: Banned in all Go code.
- **Centralized subprocess**: All subprocess calls through `system.DefaultRunner.Run()` / `system.DefaultRunner.Stream()` with `system.RunCmdOpts` (`internal/lib/system/runner.go`). No raw `os/exec` outside documented exceptions.
- **Domain `utils.go` for helpers**: Domain-specific utility functions that don't reference the Service struct live in `utils.go` within the domain package.

### Subprocess invocation

ONE canonical path for all subprocess calls: `system.DefaultRunner.Run()` / `system.DefaultRunner.Stream()` with `system.RunCmdOpts` in `internal/lib/system/runner.go`. No raw `os/exec.Command` except in documented exceptions.

```go
// Correct -- everything routes through the centralized runner
result, err := system.DefaultRunner.Run(ctx, []string{"ip", "link", "set", tap, "down"}, system.RunCmdOpts{Privileged: true})

// Forbidden -- raw os/exec scattered across modules
exec.Command("iptables", ...) // NEVER
```

The `RunCmdOpts` struct configures execution: `Check`, `Capture`, `Cwd`, `Timeout`, `Input`, `Env`, `Privileged`, `Interactive`, `StartOnly`.

**Documented exceptions** -- code that directly uses `os/exec.Command` because `DefaultRunner.Run()` cannot fulfill the requirement:

| Location | Why `DefaultRunner.Run()` doesn't work |
|---|---|
| `internal/core/vm/firecracker.go` (Firecracker spawn) | Fine-grained control over stdin/stdout/stderr FD redirection and `Setsid` session management for the Firecracker child process |
| `internal/core/ssh/cp.go` (tar-pipe transfer) | Pipes two child processes (tar + ssh) together via stdin/stdout |
| `internal/service/loopmount/provisioner.go` | Direct provisioning engine running losetup/mount/umount/chroot in chained operations with precise error recovery |
| `internal/lib/system/runner.go` | Implementation of `DefaultRunner.Run()` / `DefaultRunner.Stream()` itself |
| `internal/lib/system/spawn.go` | Implementation of `SpawnService` for service subprocess spawning |

Services running as subprocesses (`mvm run <service>`) use `system.SpawnService(ctx, cfg)` which resolves the executable, optionally prepends `sudo`, and manages process groups. The services themselves (console relay, nocloudnet server, loopmount entry point) do NOT use `os/exec` except for the provisioning engine noted above.

### Model types

ALL model types live in `internal/lib/model/` -- a single shared package. No domain imports anything outside the model package. Every type has `json:"name"` struct tags and `db:"column"` struct tags for sqlx scanning.

Key types:
- `model.VMItem` -- VM instance
- `model.NetworkItem` -- network
- `model.ImageItem` -- image record
- `model.KernelItem` -- kernel record
- `model.BinaryItem` -- binary record
- `model.SSHKeyItem` -- SSH key record
- `model.VolumeItem` -- volume record
- `model.NetworkLeaseItem` -- IP lease
- `model.FirewallRule` -- firewall rule entry
- `model.ConsoleInfo`, `model.ConsoleState` -- console info
- `model.FirecrackerConfig` -- Firecracker VM config
- `model.CloudInitMode`, `model.ProvisionerType` -- enum types
- `model.RelationSpec` -- enrichment relation spec
- `model.VMStatus` -- VM lifecycle status enum
- `model.OpStatus` -- operation result status enum (success/skipped/warning/error/failure)

JSON-serialized DB fields use `db.StringSlice` (for `[]string`) or custom `Scan`/`Value` implementations.

### Import conventions

| Layer | Imports from | Example |
|---|---|---|
| **CLI** | `pkg/api`, `pkg/api/inputs`, `pkg/api/responses`, `pkg/errs`, `internal/cli/common`, `internal/infra`, `internal/lib/*`, `internal/service/*` (for `mvm run <service>` wiring) | `import "mvmctl/pkg/api"` |
| **API** | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/infra/event`, `internal/lib/*`, `internal/assets`, `pkg/errs`, `pkg/api/inputs`, `pkg/api/responses` | `import "mvmctl/internal/core/vm"` |
| **API inputs** | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/lib/*` | `import "mvmctl/internal/lib/model"` |
| **Core domain** | `internal/infra`, `internal/lib/*`, `internal/assets`, `internal/service/*` (for subprocess spawning) — no other core domains | `import "mvmctl/internal/lib/model"` |
| **Infra/lib** | stdlib, `github.com/jmoiron/sqlx`, external deps | N/A -- leaf nodes |

Key conventions:
- Import aliases like `Xcore` are forbidden. Use bare package names.
- When two packages share a name (e.g., `internal/lib/network` and `internal/core/network`), the lib package gets the `lib` prefix alias: `libnet "mvmctl/internal/lib/network"`.
- The driver import `_ "modernc.org/sqlite"` lives ONLY in `internal/lib/db/connection.go`.

### Parallel execution

Concurrent operations use `internal/infra/pool/`:
- `pool.Do[T](ctx, workers, items, fn)` -- fire-and-forget with bounded concurrency. Collects all errors, continues on failure.
- `pool.Gather[T,R](ctx, workers, items, fn)` -- parallel transform, returns `[]Result[R]`.
- `pool.Seq[T,R](ctx, items, fn)` -- sequential fail-fast execution.

Workers defaults to `min(runtime.NumCPU() * 2, len(items))` (minimum 1) when ≤ 0. All accept `context.Context` for cancellation.

## Test types

Two test types exist (no separate integration layer -- per PORTING_TO_GOLANG.md verdict #9):

### Unit tests (Go `*_test.go`)

Go test files alongside source code in `internal/` and `pkg/`. Use interface mocks (`internal/testutil/` has in-memory repo implementations + `FakeRunner` for subprocess mocking). Run via `go test ./...`. Fast (~ms per test).

```bash
go test ./...
go test ./internal/core/vm/...
go test -v ./internal/core/network/...
```

### System tests (Python `tests/system/`)

Python-based black-box CLI subprocess tests (no mocking, no imports from Go code). Operate against the compiled `mvm` binary. Verify actual business outcomes at the OS level: JSON state, filesystem state, process state. Run in `tests/system/`.

**Execution strategy -- per-file, not as a single batch:**
```bash
# Per-domain:
MVM_BINARY=dist/mvm python3 scripts/run_tests.py --domain network

# Per-file:
MVM_BINARY=dist/mvm python3 scripts/run_tests.py --test tests/system/network/test_network.py
```

## System Tests

### Option C verification
The thoroughness standard for system test assertions. Every system test verifies system state at the deepest practical level: JSON field assertions from `* ls --json`, file existence/symlink checks, process presence via `/proc`, iptables rule presence, and/or direct SQLite queries. A test that only checks `returncode == 0` is incomplete.

### Gap matrix
A cross-reference of every CLI subcommand and flag against its system test coverage. All gaps must be filled.

### Edge case categories (8 categories)
For every CLI flag, check all eight: happy path (with state verify), missing required args, invalid values, boundary values, JSON output format, confirmation prompts, non-existent resources, duplicate creation.

### Marker
A `pytest.mark.*` annotation on a test class or function. System test markers include: `system` (always), `domain_<name>` (file-level filter), `serial` (modifies shared state -- prevent race conditions), `slow` (>30s), `requires_kvm` (needs /dev/kvm), `requires_network` (needs real bridges), `kernel_build` (build from source, excluded from default run), `host_reset` (host clean/reset with sudo, excluded from default run).

### Serial test
A test marked `pytest.mark.serial` because it modifies shared system state (default image, default network, cached binaries, kernel defaults). Must not run in parallel.

### Non-destructive test
A test that does not modify persistent state -- reads JSON, inspects resources, lists records. Runs FIRST in every file.

### Destructive test
A test that modifies persistent state -- removes a resource, changes a default, prunes cache. Defined at the END of their file. Every destructive test must restore removed state in a `finally` block.

### Kernel build marker (`pytest.mark.kernel_build`)
Designates tests requiring kernel compilation from source. EXCLUDED from default system test runs. Invoke explicitly: `pytest -m kernel_build`.

### Host reset marker (`pytest.mark.host_reset`)
Designates tests executing `host clean` or `host reset` with sudo. EXCLUDED from default system test runs. Invoke explicitly: `pytest -m host_reset`.

### Tautological test
A test that verifies something trivially true by construction. Forbidden in system tests.
