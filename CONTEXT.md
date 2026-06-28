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
  - [Input pattern (public-facing domains)](#input-pattern-public-facing-domains)
  - [SQLite schema overview](#sqlite-schema-overview)
  - [Layer compliance enforcement](#layer-compliance-enforcement)
  - [Public API boundary](#public-api-boundary)
  - [CLI is the canonical interface](#cli-is-the-canonical-interface)
  - [Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)](#provisioner-backend-loopmount-vs-guestfs--mutual-exclusion)
  - [Firewall Backend (nftables vs iptables -- mutual exclusion)](#firewall-backend-nftables-vs-iptables--mutual-exclusion)
- [Build output (REQUIRED)](#build-output-required)
- [Asset mirror environment variable (OPTIONAL)](#asset-mirror-environment-variable-optional)
- [Relationships](#relationships)
  - [Error handling](#error-handling)
  - [API result types](#api-result-types)
  - [Error message format](#error-message-format)
  - [Error codes format](#error-codes-format)
  - [Coding style](#coding-style)
  - [Subprocess invocation](#subprocess-invocation)
  - [Timeout taxonomy](#timeout-taxonomy)
  - [Timeout policy: CLI flags vs test infrastructure](#timeout-policy-cli-flags-vs-test-infrastructure)
  - [Model types](#model-types)
  - [Import conventions](#import-conventions)
  - [Parallel execution](#parallel-execution)
- [Test types](#test-types)
- [System Tests](#system-tests)

## Language

### Domain

A domain is a self-contained business capability with its own logic, data model, and test suite. Each domain lives in a directory under `internal/core/` named after the capability — for example, `internal/core/vm/` for VM lifecycle, `internal/core/network/` for networking, and `internal/core/image/` for image management. The project currently has sixteen domains covering everything from SSH keys to snapshots.

Domains are strictly isolated from each other. A domain in `internal/core/vm/` can never import from `internal/core/network/` or any other domain package. The Go compiler enforces this isolation through circular import detection: if a domain tried to import another domain, the compiler would produce an import cycle error. This means each domain can be tested, modified, and replaced independently without affecting the rest of the system.

What unifies the domains is the shared model layer at `internal/lib/model/`. Every domain imports its types — concrete structs with `db:"column"` and `json:"field"` tags for SQL and JSON serialization — from this single package. No domain defines its own model types. The model package contains 21 files covering VM instances, networks, images, kernels, binaries, volumes, SSH keys, leases, firewall rules, console info, Firecracker config, cloud-init modes, provisioner types, relation specs, VM status, operation status, and workflow state.

Not every domain follows the same internal structure. The pattern varies by complexity:
- **Controller/Service/Repository/Resolver**: vm, network, image, kernel, key, volume, host
- **Controller + Repository**: snapshot (resolver included)
- **Controller only**: console
- **Service only**: cache, ssh
- **Service + Repository**: config (includes constraints registry)
- **Controller + Service**: logs
- **Manager + Provisioner**: cloudinit
- **Agent + client + service + repository + resolver**: vsock

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

Constructed with repos/options only: `network.NewService(repo Repository, tracker *firewall.FirewallTracker)`. Wired once at startup in `app.Initialize()`.

*Litmus test: if the operation would work the same way without a specific entity instance, it's Service. If it needs to communicate with a running entity's Firecracker API socket, it belongs in Controller. If it sequences multiple infrastructure steps (teardown NAT -> remove bridge -> delete DB record), it's intra-domain orchestration in Service.*

### Repository

A per-domain interface for database CRUD operations. Each domain defines its own `Repository` interface in its `repository.go` file. ALL SQL queries live in the sole concrete implementation (`sqlite.go` per domain), using `github.com/jmoiron/sqlx` for struct scanning. Every method takes `ctx context.Context` as its first parameter. SQL-level computation (COUNT, WHERE IN) is preferred over fetch-all-then-filter patterns. JSON-serialized fields (VM's SSH keys, volume IDs, CPU config) use intermediate scan structs with conversion methods.

Constructor pattern: `NewRepository(db *sqlx.DB) Repository`.

### Service subprocess pattern (internal/service/)

Long-running subprocess services (console relay, nocloud-net server, loopmount provisioner) live in `internal/service/{name}/`. These are compiled into the same `mvm` binary — no separate binaries. The CLI layer has an `mvm run <service>` subcommand that serves as the entry point for each service. An additional embedded service (`vsockagent/`) provides a cross-compiled guest agent binary that is compressed and embedded into the `mvm` binary at build time, then injected into the VM at runtime.

Each service follows a consistent three-function pattern:
- **`Config`** struct — holds all configuration for the service.
- **`Run(ctx, cfg)`** — runs the service in the foreground (blocking).
- **`Spawn(ctx, cfg, extraParams...)`** — launches the service as a background subprocess via `system.SpawnService()`. The context parameter is typically `nil` (background/nil) for daemon services (console relay, nocloud-net server) and a real context for synchronous services (loopmount provisioning). Extra parameters carry service-specific data: `console.Spawn()` passes a PTY file descriptor, `nocloudnet.Spawn()` passes the config only, `loopmount.Spawn()` passes a wire protocol input struct.

Services in `internal/service/`: `console/` (console relay PTY proxy), `nocloudnet/` (NoCloud HTTP metadata server), `loopmount/` (loop-mount provisioner wire protocol), `vsockagent/` (embedded guest agent binary — cross-compiled, compressed, and injected into the VM at runtime via vsock).

Dependency direction: `cli/` -> `services/`. Services never import `cli/` or `pkg/api/`.

Cloud-init domain (`internal/core/cloudinit/`) is distinct from services — it is a core domain that handles cloud-init config generation, not a subprocess.

### Resolver

A struct for entity resolution by identifier (name, ID prefix, IP, MAC to domain object). Pure resolution -- no enrichment. Enrichment is handled by the `internal/enricher/` package. Resolver delegates to Repository for DB queries.

*Example: `vm.Resolver` has `ByID(ctx, id)`, `ByName(ctx, name)`, `ByIP(ctx, ip)`, `ResolveMany(ctx, identifiers)`. Returns `*model.VMItem` or `ResolveResult{VMs, Errors, ExitCode}`.*

### Enrichment pattern

Cross-domain enrichment is handled by the `internal/enricher/` package -- the ONLY package that imports across multiple core/* packages (besides the API orchestrator `pkg/api/`). Uses explicit Go switch/case dispatch per relation (NO reflect, NO string dispatch, NO resolver registry).

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
    "vsock":          {FKField: "id", Resolver: "vsock", Method: "get_by_vm_id", RelationName: "vsock", IsReverse: true},
}
```

The `Enricher` struct holds all repository interfaces and is wired once at startup in `app.Initialize()`. Called from the API layer: `enr.EnrichVM(ctx, vms, "kernel", "image", "network")`.

### Validation (caller's responsibility)

Checks that input is structurally valid: format, existence, cross-field constraints. Belongs in API layer (`pkg/api/inputs/` -- `*Input` structs with `Validate()`/`Resolve()`). Does NOT belong in Service or Controller. The caller (API layer) is responsible for passing clean, validated data down.

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

### Input pattern (public-facing domains)

Every domain with public-facing input uses a single `*Input` struct in `pkg/api/inputs/` with `Validate()` and `Resolve()` methods (ADR-0011). No `*Request` wrapper.

1. **`*Input`** -- Raw CLI or external input. Thin struct with typed fields. Optional fields are `*T` -- no DB-backed defaults, no constants-backed defaults. The CLI layer resolves constants before creating this; the API layer calls `Resolve()` to look up DB-backed defaults and validate.

2. **`Validate() error`** -- Checks input fields. Called inside `Resolve()`, but callers may call it separately for early-exit patterns (e.g., batch removal).

3. **`Resolve(ctx, deps...) (result, error)`** -- Looks up DB records, resolves defaults, returns domain entities or a `Resolved*` struct (kept only when output shape differs from input).

Examples:

```go
// Simple lookup — returns domain entities directly
vms, err := input.Resolve(ctx, op.Repos.VM)

// Create with resolved defaults — returns Resolved* struct
resolved, err := input.Resolve(ctx, op.Repos.Volume)

// Multi-domain resolution — separate methods for each domain
snap, _ := input.ResolveSnapshot(ctx, op.Repos.Snapshot)
net, _ := input.ResolveNetwork(ctx, op.Repos.Network)
```

### SQLite schema overview

Defined in `internal/lib/db/migrations/*.sql`. Accessed via `github.com/jmoiron/sqlx` with `modernc.org/sqlite` driver. PRAGMAs (foreign_keys=ON, journal_mode=WAL, synchronous=NORMAL, busy_timeout=5000, wal_autocheckpoint=1000, cache_size=-64000) set via DSN parameters in `db.Handle.openLazy()`. Connection pool has `SetMaxOpenConns(1)` and `SetMaxIdleConns(1)` for SQLite's single-writer semantics.

Tables include: `images`, `kernels`, `binaries`, `volumes`, `networks`, `network_leases`, `vm_instances`, `host_state`, `host_state_changes`, `iptables_rules`, `nftables_rules`, `ssh_keys`, `user_settings`, `vm_vsock_config`, `snapshots`, `db_migrations` (16 tables).

### Layer compliance enforcement

Architecture rules are enforced by the Go compiler (circular import errors prevent cross-domain imports in core) and code review. Key rules:
- Core domains NEVER import other core/* packages -- enforced by Go compiler.
- CLI imports from `pkg/api/`, `pkg/api/inputs`, `pkg/api/results`, `pkg/errs`, `internal/cli/common/`, `internal/infra/`, `internal/lib/`, `internal/service/` -- enforced by code review.
- API imports `internal/core/*` + `internal/enricher/` + `internal/infra/` + `internal/infra/event` + `internal/lib/*` + `internal/assets` + `internal/service/*` + `pkg/errs` + `pkg/api/inputs` + `pkg/api/results`.
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

### Asset mirror environment variable (OPTIONAL)

**The `MVM_ASSET_MIRROR` env var is optional.** When set, the downloader checks the mirror path first before fetching from the network. If the file exists in the mirror, it is copied locally (saving bandwidth). Successfully downloaded files are also auto-populated into the mirror for future use. Without it, downloads proceed normally from the original URLs.

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mvm <subcommand>
```

This variable directs the local asset cache for downloaded kernel images, root filesystems, and firmware blobs.

### CLI is the canonical interface

The `mvm` CLI is the ONLY supported interface for all mvmctl operations. Do NOT bypass it with raw commands. The CLI handles privilege escalation, database state tracking, and dynamic resolution of assets (IPs, keys, images, kernels).

Built with `github.com/spf13/cobra`. Root command in `internal/cli/root.go`. Subcommands in `internal/cli/{domain}.go` -- monolithic files per domain (one file contains all subcommands for that domain).

CLI aliases: Domain-level aliases for faster typing: `mvm net` for `mvm network`, `mvm img` for `mvm image`, `mvm vol` for `mvm volume`, `mvm bin` for `mvm binary`, and `mvm ss` for `mvm snapshot`. Environment workflow subcommands have shorter aliases: `mvm env up` for `mvm env apply` and `mvm env down` for `mvm env destroy`. Every `list` subcommand has both `ls` (Use) and `list` (Alias). Every `remove` subcommand has `rm` (Use), `remove`, `delete`, and `del` (Aliases).

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

A single `DomainError` type in `pkg/errs/` handles all error scenarios. Every error has a `Code`, `Message`, `Op`, `Entity`, `Class`, `Err`, and optional `Details`. Codes are dot-separated: `vm.not_found`, `network.subnet.overlap`, `host.init.sudoers.failed`.

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

Classes categorize errors semantically: `ClassUnknown` (zero value / default), `ClassValidation`, `ClassConflict`, `ClassRetryable`, `ClassInternal`, `ClassNeedsInteraction`.

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
- **`errs.BatchResult`** -- Collection of `OperationResult` from batch operations (e.g., batch remove).
- **`errs.NeedsInteraction`** -- Returned when the operation requires user action (e.g., sudo password prompt). Implements the `error` interface for (result, error) return types.
- **`pkg/api/results/`** -- Domain-specific inspect/list types for JSON serialization (e.g., `results.VMInspect`, `results.NetworkInspect`, `results.HostInfo`).

### Error message format

```
What happened. Why it happened. Possible fix.
```

### Error codes format

Dot-separated with domain prefix:
```
network.subnet.overlap
vm.create.binary_not_found
host.init.sudoers.failed
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

The `RunCmdOpts` struct configures execution: `Check`, `Capture`, `Cwd`, `Timeout`, `Input`, `Env`, `Privileged`, `Interactive`, `StartOnly`. `Timeout` is an absolute cap for operations that genuinely need one (downloads, builds, cleanup). User commands run until completion or context cancellation; pass `Timeout: 0` for those.

### Timeout taxonomy

| Type | Meaning | User-facing? | Examples |
|---|---|---|---|
| **Connect/probe timeout** | Time to establish connection / first response | Yes (`--timeout`) | `mvm ssh`, `mvm exec` |
| **Idle timeout** | Max silence between bytes/events | Optional future flag | (not exposed today) |
| **Absolute timeout** | Hard cap on total duration | No | HTTP downloads, builds, cleanup |
| **Graceful shutdown timeout** | Time after SIGTERM before SIGKILL | No | Firecracker stop, relay shutdown |
| **Service lifetime** | How long a background service runs | Yes (`--kill-after`) | `mvm run nocloudnet serve` |

User-facing `--timeout` means **connect/probe timeout only**. Once the target is responsive, the operation runs unbounded. See [ADR-0013](docs/adr/0013-user-facing-timeouts-are-connect-timeouts.md).

### Timeout policy: CLI flags vs test infrastructure

**Two distinct concerns that are NOT the same thing.**

**1. CLI `--timeout` flags (user-facing, e.g., `mvm exec --timeout`, `mvm ssh --timeout`)**

These are **connect/probe timeouts** only (per ADR-0013). Default: **5 seconds**, absolute max: **10 seconds**. Once the target responds, the operation runs unbounded. This is the speed-first principle: if a connection can't be established in 10s, something is wrong.

**2. Subprocess timeouts in test infrastructure (the `timeout=` parameter of `_run_mvm()` / `_guest_run()`)**

These control how long `subprocess.run()` waits for an `mvm` command to complete. They are **NOT** the same as the CLI `--timeout` flag and have **different guidance**:

| Operation | Typical duration | Test subprocess timeout |
|---|---|---|
| `vm ls --json`, `image ls --json` | <1s | Default (60s) |
| `network create`, `key create` | 1-5s | Default (60s) |
| `vm create` (cached image) | 3-10s | 180s |
| `vm rm --force` | 2-8s | 120s |
| `image pull`, `kernel pull`, `bin pull` | 10-120s | 300s |

Default subprocess timeout (in `conftest.py`): **60 seconds** with **+30 second buffer** = **90s total**. Individual operations may specify higher explicit timeouts for slow I/O (pulls, nested VM creation, network-heavy ops).

Rationale: subprocess timeouts are a **safety net**, not a performance floor. They prevent hung tests without imposing artificial limits on legitimate I/O. The speed-first principle applies to the CLI `--timeout` flag, NOT to test infrastructure subprocess timeouts.

**Documented exceptions** -- code that directly uses `os/exec.Command` or `os/exec.CommandContext` because `DefaultRunner.Run()` cannot fulfill the requirement:

| Location | Why `DefaultRunner.Run()` doesn't work |
|---|---|---|
| `internal/core/vm/firecracker.go` (Firecracker spawn) | Fine-grained control over stdin/stdout/stderr FD redirection and `Setsid` session management for the Firecracker child process |
| `internal/core/ssh/utils.go` (SSH connectivity probe) | Uses `exec.CommandContext` with a short-lived probe context for SSH connectivity detection |
| `internal/service/loopmount/provisioner.go` | Direct provisioning engine running losetup/mount/umount/chroot in chained operations with precise error recovery |
| `internal/service/vsockagent/exec.go` (command execution) | Uses `exec.CommandContext` for `su` user switching and `sh -c` command execution inside the guest agent |
| `internal/service/vsockagent/pty.go` (PTY session) | Uses `exec.CommandContext` for `su` user switching to establish PTY sessions |
| `internal/lib/archive/archive.go` (xz decompression) | Uses `exec.CommandContext` for `xz -d --stdout` pipe-based decompression |
| `internal/lib/system/runner.go`, `interactive_run.go`, `spawn.go` | Implementation of the subprocess abstraction layer (`DefaultRunner`, `RunInteractive`, `SpawnService`). These use raw `os/exec` because they ARE the abstraction boundary |

**Binary lookup carve-out:** Utility files across the codebase use `exec.LookPath()` (not `exec.Command`/`exec.CommandContext`) solely to check whether a system binary exists before calling it through `DefaultRunner.Run()`. This is NOT a subprocess execution — it's a filesystem existence check that happens to use the `os/exec` package. These files are not listed as exceptions above and do not violate the subprocess rule. Key locations: `internal/core/host/detector.go`, `internal/core/host/probe.go`, `internal/core/network/service.go`, `internal/core/config/utils.go`, and others in `internal/lib/`, `internal/core/`, `internal/service/`.

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
| **CLI** | `pkg/api`, `pkg/api/inputs`, `pkg/api/results`, `pkg/errs`, `internal/cli/common`, `internal/infra`, `internal/lib/*`, `internal/service/*` (for `mvm run <service>` wiring) | `import "mvmctl/pkg/api"` |
| **API** | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/infra/event`, `internal/lib/*`, `internal/assets`, `pkg/errs`, `pkg/api/inputs`, `pkg/api/results` | `import "mvmctl/internal/core/vm"` |
| **API inputs** | `internal/core/{domain}`, `internal/enricher`, `internal/infra`, `internal/lib/*` | `import "mvmctl/internal/lib/model"` |
| **Core domain** | `internal/infra`, `internal/lib/*`, `internal/assets`, `internal/service/*` (for subprocess spawning) — no other core domains | `import "mvmctl/internal/lib/model"` |
| **Infra/lib** | stdlib, `github.com/jmoiron/sqlx`, `pkg/errs`, `internal/assets`, other `internal/lib/*` sub-packages, external deps | N/A -- leaf nodes that never import core, api, cli, or service |

Key conventions:
- Import aliases like `Xcore` are forbidden. Use bare package names.
- When two packages share a name (e.g., `internal/lib/network` and `internal/core/network`), the lib package gets the `lib` prefix alias: `libnet "mvmctl/internal/lib/network"`.
- The driver import `_ "modernc.org/sqlite"` lives ONLY in `internal/lib/db/connection.go`.

### Parallel execution

Concurrent operations use `internal/infra/pool/`:
- `pool.Do[T](ctx, workers, items, fn)` -- fire-and-forget with bounded concurrency. Collects all errors, continues on failure.
- `pool.Gather[T,R](ctx, workers, items, fn)` -- parallel transform, returns `[]Result[R]`.
- `pool.Seq[T,R](ctx, items, fn)` -- sequential fail-fast execution.

Workers defaults to `min((runtime.NumCPU() or 4) * 2, len(items))` (minimum 1) when ≤ 0. The CPU count falls back to 4 if `runtime.NumCPU()` returns less than 1. All accept `context.Context` for cancellation.

## Test types

Three-level architecture — see `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` for the full specification.

### L0: Pure Function Tests (Go `*_test.go`)

Table-driven tests with `map[string]struct{...}` and `t.Run()`. No I/O, no DB, no subprocess. Pure input → output assertions. Runs in microseconds.

```bash
go test ./...
go test ./internal/core/vm/...
```

### L1: Hermetic Integration Tests (Go `*_test.go`)

Uses real I/O in controlled environments: in-memory SQLite (`:memory:` via `testutil.NewInMemoryDB`), `t.TempDir()` for filesystem, `FakeRunner` for subprocess calls that can't run in CI. No networking, no KVM, no sudo. Catches bugs earlier during `go test ./...`.

```bash
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
```

### L2: Runner VM System Tests (Python `tests/system/`)

**Ground truth.** Every user-facing feature must have an L2 test. Real binary, real subprocess, real infrastructure inside a disposable Firecracker VM with nested KVM. No mocking of any kind. Operates against the compiled `mvm` binary. Verifies actual business outcomes at the OS level: JSON state, filesystem state, process state, iptables rules.

```bash
# Run inside the runner VM (disposable Firecracker VM with nested KVM)
pytest tests/system/

# Single file
pytest tests/system/network/test_network.py --tb=short -q
```

## System Tests (L2 E2E)

L2 tests are the **ground truth** — every user-facing feature must have one.
They run inside a disposable Firecracker VM (runner VM) with nested KVM.

### Option C verification
The thoroughness standard for L2 test assertions. Every test verifies system state at the deepest practical level: JSON field assertions from `* ls --json`, file existence/symlink checks, process presence via `/proc`, iptables rule presence, and/or direct SQLite queries. A test that only checks `returncode == 0` is incomplete.

### Gap matrix (no longer a separate file)
Coverage is tracked by the quick-reference table in `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md`. Every CLI subcommand and flag is classified as L0, L1, or L2. All gaps must be filled before release.

### Edge case categories (8 categories)
For every CLI flag, check all eight: happy path (with state verify), missing required args, invalid values, boundary values, JSON output format, confirmation prompts, non-existent resources, duplicate creation.

### Marker
A `pytest.mark.*` annotation on a test class or function. L2 test markers include: `system` (always), `domain_<name>` (file-level filter), `slow` (>30s), `requires_kvm` (needs /dev/kvm), `requires_network` (needs real bridges), `kernel_build` (build from source, excluded from default run), `host_reset` (host clean/reset with sudo, excluded from default run), `tier2` (requires nested virtualization), `tier3` (runs host-direct).

### Serial test
A test that modifies shared system state (default image, default network, cached binaries, kernel defaults) must not run in parallel. In the current architecture, serial execution is handled by tier and fixture scoping, not a marker.

### Non-destructive test
A test that does not modify persistent state -- reads JSON, inspects resources, lists records. Runs FIRST in every file.

### Destructive test
A test that modifies persistent state -- removes a resource, changes a default, prunes cache. Defined at the END of their file. Every destructive test must restore removed state in a `finally` block.

### Kernel build marker (`pytest.mark.kernel_build`)
Designates tests requiring kernel compilation from source. EXCLUDED from default test runs. Invoke explicitly: `pytest -m kernel_build`.

### Host reset marker (`pytest.mark.host_reset`)
Designates tests executing `host clean` or `host reset` with sudo. EXCLUDED from default test runs. Invoke explicitly: `pytest -m host_reset`.

### Tautological test
A test that verifies something trivially true by construction. Forbidden in L2 tests.
