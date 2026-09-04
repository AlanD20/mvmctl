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
  - [VM-to-VM execution transition](#vm-to-vm-execution-transition)
  - [Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)](#provisioner-backend-loopmount-vs-guestfs--mutual-exclusion)
  - [Firewall backend transition](#firewall-backend-transition)
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

A domain is a self-contained business capability with its own logic, data model, and test suite. Each active domain is an
immediate directory under `internal/core/`. For example, `internal/core/vm/` owns VM lifecycle,
`internal/core/network/` owns networking, and `internal/core/image/` owns image management. Inspect `internal/core/`
instead of copying a domain count into documentation.

Domains are strictly isolated from each other. A domain in `internal/core/vm/` cannot import
`internal/core/network/` or another sibling domain. The Go compiler rejects import cycles, but it does not reject every
one-way sibling import. The deterministic `internal/architecture` CI gate scans every production Go file beneath each
immediate core domain and rejects imports of a different sibling domain while allowing same-domain subpackages.

The shared model layer at `internal/lib/model/` unifies the domains. It contains concrete structs with `db:"column"`
and `json:"field"` tags for SQL and JSON serialization. Core domains use these shared types instead of defining
cross-domain model packages. Inspect the package for the current model inventory instead of copying a file count into
documentation.

Not every domain follows the same internal structure. The pattern varies by complexity:
- **Controller/Service/Repository/Resolver**: vm, network, kernel, key, volume
- **Provisioner/Service/Repository/Resolver**: image
- **Controller/Service/Repository**: host
- **Repository + Resolver**: snapshot
- **Controller only**: console
- **Service only**: cache, ssh, update
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

*Example: `vm.NewController(vmItem, repo).Snapshot(ctx)` -- snapshots this specific VM. `network.NewService(repo, tracker, policyRepo).RemoveBridge(ctx, bridge)` -- removes a bridge, no single network entity needed.*

### Service (stateless, intra-domain)

A struct for stateless intra-domain operations. Handles infrastructure operations (bridges, TAPs, NAT, subprocesses, file/disk operations). Performs state detection (checking current system state as part of an operation -- "does this bridge exist?" to branch execution). Guards invariants that protect against system damage. Does NOT validate caller input. Does NOT manage state for a single entity -- Service operates on infrastructure, not on a bound instance.

Constructed with repos/options only: `network.NewService(repo Repository, tracker *firewall.FirewallTracker, policyRepo ServiceAccessPolicyRepository)`. Wired once at startup in `app.Initialize()`.

*Litmus test: if the operation would work the same way without a specific entity instance, it's Service. If it needs to communicate with a running entity's Firecracker API socket, it belongs in Controller. If it sequences multiple infrastructure steps (teardown NAT -> remove bridge -> delete DB record), it's intra-domain orchestration in Service.*

### Repository

A per-domain interface for database CRUD operations. Each domain defines its own `Repository` interface in its `repository.go` file. ALL SQL queries live in the sole concrete implementation (`sqlite.go` per domain), using `github.com/jmoiron/sqlx` for struct scanning. Every method takes `ctx context.Context` as its first parameter. SQL-level computation (COUNT, WHERE IN) is preferred over fetch-all-then-filter patterns. JSON-serialized fields (VM's SSH keys, volume IDs, CPU config) use intermediate scan structs with conversion methods.

Constructor pattern: `NewRepository(db *sqlx.DB) Repository`.

### Service subprocess pattern (internal/service/)

Long-running and privileged subprocess services (console relay, nocloud-net server, loopmount provisioner, Jailer launcher) live in `internal/service/{name}/`. These are compiled into the same `mvm` binary — no separate service binaries. The CLI layer has an `mvm run <service>` subcommand that serves as the entry point for each service. An additional embedded service (`agent/`) provides a cross-compiled guest agent binary that is compressed and embedded into the `mvm` binary at build time, then injected into the VM at runtime.

Each service follows a consistent three-function pattern:
- **`Config`** struct — holds all configuration for the service.
- **`Run(ctx, cfg)`** — runs the service in the foreground (blocking).
- **`Spawn(ctx, cfg, extraParams...)`** — launches the service as a background subprocess via `system.SpawnService()`. The context parameter is typically `nil` (background/nil) for daemon services (console relay, nocloud-net server) and a real context for synchronous services (loopmount provisioning). Extra parameters carry service-specific data: `console.Spawn()` passes a PTY file descriptor, `nocloudnet.Spawn()` passes the config only, `loopmount.Spawn()` passes a wire protocol input struct.

Services in `internal/service/`: `console/` (console relay PTY proxy), `nocloudnet/` (NoCloud HTTP metadata server), `loopmount/` (loop-mount provisioner wire protocol), `jailer/` (trusted release installation, per-VM jail setup, and privileged Jailer exec), `agent/` (embedded guest agent binary — cross-compiled, compressed, and injected into the VM at runtime via vsock).

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

Tables include `images`, `kernels`, `binaries`, `volumes`, `networks`, `network_leases`, `vm_instances`, `host_state`,
`host_state_changes`, `service_access_policies`, `iptables_rules`, `nftables_rules`, `ssh_keys`, `user_settings`,
`vm_vsock_config`, `snapshots`, and `db_migrations`. Read `internal/lib/db/migrations/` for the current schema.

The fresh v0.3.0 schema is version 6 and has no VM-wide `remote_exec` column. Schema-version 3, 4, or 5 databases that
still contain that removed column are legacy databases, even when every stored value is false. Both pending-migration
inspection and migration application reject them with a validation-class `database.migration.failed` error before
schema work, leaving their version and contents unchanged. v0.3.0 does not infer directional exec policies from that
legacy bit. After stopping the old VMs and preserving desired artifacts, remove the old mvm state directory selected
by `MVM_CACHE_DIR` (normally `~/.cache/mvmctl`) through the documented clean-install procedure and initialize fresh
v0.3.0 state; never delete live state as an incidental migration step.

The same fresh schema deliberately does not make `vm_vsock_config.guest_cid` globally unique. Each Firecracker process
serves its vsock device through an independent UDS endpoint and therefore has its own CID namespace, while a snapshot
clone must preserve the CID embedded in its vmstate. The non-unique `idx_vsock_config_cid` index remains. Removing the
legacy unique constraint is part of the v0.3.0 clean-install schema break; there is no migration or adoption path for an
older database constraint.

### Layer compliance enforcement

The Go compiler enforces type safety and rejects import cycles. The `internal/architecture` test adds machine
enforcement for production imports between sibling core domains and runs as its own CI step before formatting, vet,
and the full test suite. Code review continues to enforce the remaining project-specific dependency rules that are
stricter than Go's import rules. Key rules:
- Core domains never import other `internal/core/*` packages.
- CLI imports from `pkg/api/`, `pkg/api/inputs`, `pkg/api/results`, `pkg/errs`, `internal/cli/common/`, `internal/infra/`, `internal/lib/`, `internal/service/` -- enforced by code review.
- API imports `internal/core/*` + `internal/enricher/` + `internal/infra/` + `internal/infra/event` + `internal/lib/*` + `internal/assets` + `internal/service/*` + `pkg/errs` + `pkg/api/inputs` + `pkg/api/results`.
- `internal/infra/` and `internal/lib/` are LEAVES -- import NOTHING from core, api, or cli. Exception: `internal/lib/provisioner/loopmount/backend.go` imports `internal/service/loopmount` for wire-protocol types used to spawn the loopmount provisioner subprocess.
- `internal/service/` MAY import from `internal/infra/` / `internal/lib/` but NOT from `pkg/api/` or `internal/cli/`.

### Public API boundary

The `pkg/api` package IS the stable, curated public interface for all consumers -- CLI, future TUI/GUI, and external scripts. The `api.Operation` struct exposes all domain operations as methods. The `pkg/api/inputs` package exposes all Input types. The `internal/core` package is an implementation detail.

### Build and system-install contract (REQUIRED)

The release script produces the candidate artifact at `dist/mvm`. A development artifact may live in the worktree or
under `~/.local/bin`, but a user-owned path is **never** a sudo target.

The canonical host installation is `/usr/local/bin/mvm`, owned by `root:root` with mode `0755` under a root-owned,
non-writable directory chain. Install or replace it through the candidate artifact's exact early bootstrap route:

```bash
./scripts/build.sh release --version 0.3.0-rc.1 --output dist/mvm
sudo ./dist/mvm host install-system
sudo /usr/local/bin/mvm host init
```

`host install-system` executes before ordinary application initialization. It does not read `MVM_CACHE_DIR`, user
configuration, the user database, Cobra, or plugins, and it replaces its environment with fixed root-safe values before
installation. v0.3.0 is a clean installation: it does not convert, adopt, or preserve old user/runtime state. Remove an
old managed sudoers file and clean the old installation from an authenticated administrator session before running the
bootstrap command; the installer refuses unrecognized active syntax rather than guessing how sudo authenticated the
process. The exact fail-closed cleanup procedure remains a v0.3.0 release blocker until administrator reset/cleanup is
hardened; this architecture rule is not permission to improvise raw host cleanup.

Normal users run `/usr/local/bin/mvm` without sudo. The `mvm` group is the authorization role for the final constrained
passwordless privileged protocol; it never makes a development artifact trusted. During v0.3 development, the generated
policy still contains transitional raw-tool grants and `/usr/local/bin/mvm *` for callers not yet converted to typed
dispatch. That policy is not the final security boundary. The marker-only policy lands only after every supported
privileged caller uses the typed dispatcher. A system-installed executable cannot replace itself through
`mvm self-update`; an administrator installs the newly downloaded trusted artifact with `host install-system`.

The v0.3 privileged target splits state by authority and lifetime. Durable ownership and release records live beneath
`/var/lib/mvmctl`; root-only locks and launch handshakes use `/run/mvmctl/authority/<uid>`. API, vsock, and console
sockets plus display-only PID mirrors will use a separate pinned caller-owned VM directory beneath
`/run/mvmctl/runtime/<uid>/<vm-id>`. Persistent configuration, disks, logs, console logs, and optional metrics remain in
the caller's managed cache. Final privileged code must never mount the whole cache VM directory, trust a runtime leaf as
authority, or signal a PID read from a PID mirror. ADR-0016 defines the exact ownership, modes, launch ordering, and
cleanup contract; `tasks/todo.md` records implementation status.

The private Jailer runtime-directory foundation now creates the fixed root-owned mode-`0711`
`/run/mvmctl/runtime/<uid>` hierarchy and exclusively creates one caller-owned mode-`0700` `<vm-id>` directory beneath
it. It pins the VM directory's device/inode identity, rejects existing VM directories and unsafe parents without
repair, returns cleanup authority after partial VM-directory preparation, and removes only the exact empty directory
with retryable unlink/parent-sync reconciliation. The lease remains private and exposes no path or descriptor. Only a
durable `launching` lease can derive the recorded owner and VM identity to create it; partial preparation stays retained
for abort, and activation or abort followed by active cleanup transfers the same lease without reconstructing a path.
Cleanup removes and releases the exact empty directory before committing `cleaned`; a non-empty directory keeps the
record in retryable `cleaning`. Before removal it unlinks only the five compiled socket/PID basenames through the
retained directory descriptor. Missing leaves are already clean, symlinks are unlinked rather than followed, and a
directory, mount, or unexpected name remains untouched and blocks `cleaned`. Fresh cleanup after a controller restart
reopens only the exact existing root-controlled hierarchy and caller-owned VM directory without creating or repairing
any component. A missing hierarchy is already clean; unsafe state keeps the durable record and VM lock in retryable
`cleaning`. The directory is not yet mounted inside the VM namespace or used by public lifecycle operations;
socket/PID-leaf admission remains pending.

The final Jailer instance record is strict schema 3 with
`absent|cleaned -> launching -> active`, `launching|active -> cleaning -> cleaned`, and idempotent `cleaning`.
`launching` is durable pre-effect intent: owner UID/GID, VM ID, managed-cache identity, exact Firecracker/Jailer release
identity, host boot ID, launch/cleanup generations, the handshake-blocked child's PID/start locator, and an explicit
absent relay or handshake-blocked relay PID/start locator. It does not
claim final credentials, cgroup membership, final mount namespace, jail root, or executable identity. `active` records
the receiver-observed credentials, final mount namespace, jail root, and executable, requires empty supplementary
groups and private authority verification of exact derived cgroup membership, and binds the final copied Firecracker
to the release. The schema stores no cgroup path or caller-shaped cgroup value. A present active
relay records its caller credentials, empty groups, host mount namespace/root, and exact root-owned
`/usr/local/bin/mvm` executable metadata and digest; absence is explicit, and `console.pid` remains display-only.
Network namespace identity remains in the separate closed topology capability and is compared during activation rather
than stored as a raw path or descriptor.
`cleaned` is the same-UID tombstone and carries no launch-specific GID, cache, release, or process identity; mount
identity is valid for one mount lifetime and must not prevent a legitimate relaunch after reboot.

This split is required by Firecracker Jailer 1.16.1: Jailer copies Firecracker, joins netns/cgroup, then unshares and
pivots into a new mount namespace before dropping credentials and execing the copy. The trusted-store inode therefore
cannot equal `/proc/<pid>/exe`, and the final namespace/root cannot be known before effects. The receiver persists
`launching` before effects, invokes Jailer without `--daemonize` or `--new-pid-ns`, retains the original pidfd through
exec, verifies the final jail root and copy, seals both to `root:root 0755` beneath `root:root 0700` ancestors, hashes the
copy against the exact manifest, rechecks full identity, and only then commits `active`. All composite instance leases
must use pointer-shared mutex-protected close-once state and cache the first cleanup result.

The prepared release's consuming registration method derives release identity internally and transfers its pinned
admission, store, slot lock, and executable resources into the launch lease. That lease retains the VM, network, and
firewall leases; creates links down; applies and freshly verifies the complete atomic nftables generation; raises links
only after firewall success; and only afterward releases the blocked child to Jailer. A fixed jail destination must be
proved absent/fresh immediately before that release; only an exactly admitted, non-mounted stale fixed tree may be
removed after process absence. `launchLease.Abort` and `activeLease.BeginCleanup` transfer their resources directly
into cleanup without releasing and reacquiring the VM lock. Authority-level cleanup admission is recovery-only.

The private schema-3 graph now also carries a fixed-name cgroup-v2 authority for
`/sys/fs/cgroup/mvmctl/<vm-id>`. It descriptor-pins the cgroup root, compiled parent, and VM leaf and verifies exact
metadata, mount identity, and replacement stability. Preparation accepts only a freshly absent VM leaf, never adopts a
legacy leaf, and repeats the absence proof at the last handoff boundary. A successful blocked-child release records
Jailer-handoff provenance before the leaf can be bound. Binding and activation prove that the pidfd/start-tick-bound
Firecracker task is the leaf's sole member; a failed proof retains the same lease for retry. Cgroup authority transfers
from launch to active to cleanup without path reconstruction. Abort first terminates and exactly reaps the retained
task, then causally binds the leaf for cleanup. Completion requires the exact unreplaced leaf to be empty before
retryable descriptor-relative removal and commits durable `cleaned` only afterward. A terminal descriptor-close error
after proven removal converges rather than retrying a reused descriptor. This authority adds no cgroup path to the
schema and is not yet production host enforcement.

Fresh private cleanup admission now reopens the record's exact pidfd/start-tick-bound processes and observes the
Firecracker as either the committed active identity or the exact launching task before durably transferring authority
to `cleaning`. It then re-admits the fixed cgroup leaf only when that task is its sole member. A re-admission or final
membership error keeps the same cleanup lease authoritative with cgroup cleanup pending for retry. Across a host-boot
mismatch it opens and signals no process, requires the fixed leaf to be stably absent, and rejects a present or unstable
leaf. Recovery from `launching` retains its provisional leaf-absence proof through exact process termination and reap
before binding for cleanup. Only fresh-recovery cgroup admission callbacks run outside the `cleanupLease` mutex;
ordinary `Complete` still serializes cleanup effects under it. None of this adds a cgroup path to schema 3.

The original pidfd provides stable task identity and exit detection but does not notify `exec`. Activation uses a
bounded context-aware `/proc/<pid>/exe` observation loop, stopping immediately on pidfd-observed exit, before final
identity admission. After any active-record rename, even a later fsync/close error returns a non-nil active lease with
`record_replaced` and any `durability_uncertain` evidence so cleanup never uses stale launching authority. Recovery
re-admits a present console relay by its exact locator and final credential/namespace/root/executable identity; it never
signals an ambiguous or reused PID.

Root status observation has four closed outcomes: `running`, `stopped`, `crashed`, and `unavailable`. An absent/cleaned
record or boot mismatch is stopped; a same-boot missing/dead/mismatched active process is crashed; incomplete authority
is unavailable and fails the complete read. Interrupted `launching` records are reconciled by boot/PID/start/final-copy
identity: only the exact same task may be promoted or pidfd-terminated, and ambiguous state is neither killed nor
deleted. These are accepted target rules, not current runtime behavior. Main now implements the private strict schema-3
record/codec and the process-authority side of `launching -> active -> cleaning -> cleaned`. An already-created
Firecracker child and optional relay can be admitted while handshake-blocked through challenged EOF-fails-closed gates,
retained close-on-exec pidfds, exact PID/start locators, and a pinned receiver executable. Consuming release
registration transfers those resources with the pinned store, slot lock, manifest identity, executable authority, and
VM lock into durable `launching` authority.

The private activation path uses strict bounded `/proc` parsing and descriptor-pinned observations, a 10-second
executable-transition budget with a 25-millisecond minimum interval, and repeated boot, PID/start, pidfd-liveness,
credential, empty-group, cgroup, network-namespace, root, copied-executable, release-digest, and optional-relay checks.
Its cgroup check consumes only the retained fixed-name authority and exact sole-member proof. It binds and seals the
Jailer-created root and Firecracker copy before replacing `launching` with `active`. Private
`Abort`, `BeginCleanup`, and `Complete` transitions retain cleanup ownership, signal only through pidfds, wait and reap
before `cleaned`, accept both exit readability and the post-reap pidfd hangup as dead, and keep retryable failures in
`cleaning`. Fresh cleanup recovery is bounded to the recorded Firecracker and optional relay and signals nothing across
a boot change; on the same boot, active cleanup requires their complete recorded identities and executable digests
before returning cleanup authority. These process, destination, active, and cleanup leases use pointer-held close-once
state and cache their first cleanup result.

Fresh cleanup now reopens the exact existing per-VM runtime directory through the root-controlled hierarchy before
runtime cleanup. It creates or repairs nothing, treats an absent hierarchy as already clean, and preserves retryable
`cleaning` on unsafe state. No production child launcher, privileged VM action, or public VM lifecycle constructs or
consumes this private graph. Same-boot dead-process cgroup reconciliation, cgroup controller enablement and readiness
plus exact limit readback, replacement of the path-based cgroup prepare/remove calls in `entry.go`, production topology
and network/firewall lease transfer, stale Jailer-tree reconciliation, mount/output cleanup, public create/start/stop
and other lifecycle wiring, status observation and full reconciliation, and live L2, Tier 3, and system qualification
remain pending.

The transitional public relaunch path now has a separate, private normal-process `vmRelaunchState` increment. Start,
reboot, and snapshot restore load the per-VM vsock row explicitly: no row is a valid absence, while lookup failure,
mismatched VM identity, or an out-of-range CID aborts before respawn. A present row is cloned, its host UDS metadata is
normalized to the fixed managed `vsock.sock` leaf, and its CID, agent port, and token remain available to the normal
process. Only the validated CID enters `model.FirecrackerConfig`; the VM/Jailer path derives the fixed host and
`/run/mvm/vsock.sock` jail leaves. Snapshot restore preserves the CID stored in vmstate and sends the fixed jail-visible
vsock UDS override. VM create still uses its existing builder state rather than this common relaunch state. The private
host `/run/mvmctl/runtime` directory lease now transfers through the private launch/active/cleanup graph, but its
launch-time `/run/mvm/runtime` mount, authority-verified stale-socket cleanup, public transactional lifecycle,
host-reboot reconciliation, and L2 `mvm exec` relaunch qualification remain pending, so this increment is not evidence
that the reported restart failure is fully resolved.

SQLite binary rows are an untrusted display projection of the root release store. The implemented projection schema
now includes architecture and scopes list, prefix, type/version, latest, and default resolution to the runtime
architecture. VM migration pairs Firecracker only with a Jailer row of the same architecture. Private repository
methods atomically replace one complete `(architecture, version)` pair or remove that exact slot while leaving the same
version for another architecture unchanged. Public pull/remove now use the typed trusted-release client. Pull invokes
root before reading SQLite and projects only an authoritative installed/replaced/unchanged result; root failure leaves
SQLite untouched, while a projection failure retains the root outcome and requires repair by retry. Remove accepts
exact versions, sends only version plus runtime architecture to root, and treats local references as warning-only;
`--force` suppresses that advisory without changing the root request. Local list/default/prune never stat root-owned
paths. The old local downloader, source-build, filesystem removal, path/checksum Jailer installer, and
`mvm run jailer install/remove-release` actions are deleted. Release mutation now has only the typed trusted-release
authority path. Transitional VM launch still uses `ResolvePair` until the separate launch-authority cutover.

The cycle-free `internal/lib/authorityfs` leaf module now establishes the implemented portion of that split. It creates
or admits exact `root:root` mode-`0711` `/run/mvmctl` and exact `root:root` mode-`0700`
`/run/mvmctl/authority`. Jailer's global index lock, release-lock directory, per-UID authority directories, and per-VM
locks use its closed `Jailer`, `JailerUID`, and `Lock` capabilities, while persistent instance records remain beneath
`/var/lib/mvmctl/instances`. A separate closed `LoopMount` capability pins the authenticated UID authority and its
`loopmount` directory, then acquires canonical VM and image locks as empty one-link `root:root` mode-`0600` files that
are never unlinked during the boot. Existing managed objects are verified and never repaired; L1 tests cover exact
modes, symlinks, concurrent creation, creation-fault descriptor cleanup, record scans, lock placement, contention,
cancellation, and independent-resource concurrency.

The capability structs retain pointer-shared lifecycle state, so even a copied value performs checked release exactly
once, caches and repeats the first cleanup result, and never retries an unlock or close against a potentially reused
descriptor. Release preserves context values while ignoring cancellation; child capabilities and acquired locks remain
valid after their parent closes its own descriptors. The module exposes closed Jailer-, LoopMount-, and Network-specific
keys and methods, not a raw path, descriptor, or generic lock interface. The private LoopMount service adapter now binds
the transaction coordinator's resource-lock seam to the authenticated UID and canonical VM/image keys, retains partial
acquisition for cleanup, and releases the lock before its authority capability.

The private network registration foundation now serializes a global network-ID index and each canonical network. Its
strict schema-1 record persists only owner UID, network ID, current boot ID, and exact derived bridge
name/ifindex/host-namespace device and inode at `/var/lib/mvmctl/networks/<uid>/<network-id>.json`. Root-owned mode-`0700`
directories and one-link mode-`0600` records are admitted descriptor-relatively; a bounded exact codec and atomic
no-replace publication preserve both created-record and durability-uncertain evidence. Registration releases its
network, UID/store, index, and root authority in exact reverse order with pointer-shared cached close-once results. The
registration entry point remains private and unreferenced, does not receive the Jailer VM-lock lease, and does not
consume the separate namespace capability.

The Network authority family now also creates one exact root-owned
`/run/mvmctl/authority/<uid>/netns/<vm-id>` placeholder and retains its directory, leaf, and mount identities. A private
network effect can create a network namespace on a disposable locked OS thread, publish its exact nsfs mount onto that
placeholder through `open_tree`/`move_mount`, verify restoration of the host network namespace, and remove only the
retained mount and placeholder with explicit uncertain-unmount evidence. This is an inert private effect, not runtime
activation. A separate private link effect now completes the `TopologyEffects` boundary: it creates and verifies the
exact namespace-local bridge, host/namespace veth pair, and TAP down; attaches only identity-checked endpoints; and
deletes only exact retained identities. Every rtnetlink operation uses an explicit retained network-namespace
descriptor. TAP creation enters the retained VM namespace on a disposable locked thread, opens only `/dev/net/tun`,
uses `TUNSETIFF` and `TUNSETOWNER` with no group authority, and reverifies owner, no-PI, down state, and the managed
alias before final persistence. Neither `newService` nor the composed topology effect has a production caller, and
there is no Jailer VM-lock transfer, route/address or link-up stage, firewall application, persistent VM-topology
record, privileged action, public behavior, or live-kernel qualification. The `/run/mvmctl/runtime` tree remains
uncomposed; only the separate private runtime-directory foundation described above can create and pin it.

The private Jailer resource foundation now treats caller-owned `firecracker.json` as admission evidence only. It
requires complete semantic equality with root-built typed intent, closes the source, and retains a root-owned `0444`
sealed memfd. This is not active runtime protection yet: the typed privileged launch and descriptor-only mount consumer
remain pending, so current public launches still use the transitional configuration path.

For **release testing / RC QA / system tests**, always use `./scripts/build.sh release`; a bare `go build` lacks the
release version metadata, symbol stripping, and PIE settings and must not be used for release qualification. An
untagged RC build must pass the intended version explicitly. Otherwise `git describe` can inherit the previous release
tag and produce a candidate with the wrong identity.

### Asset mirror environment variable (OPTIONAL)

**The `MVM_ASSET_MIRROR` env var is optional.** When set, the downloader checks the mirror path first before fetching from the network. If the file exists in the mirror, it is copied locally (saving bandwidth). Successfully downloaded files are also auto-populated into the mirror for future use. Without it, downloads proceed normally from the original URLs.

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mvm <subcommand>
```

This variable directs the local asset cache for downloaded kernel images, root filesystems, and firmware blobs.
Checksum/version metadata may still be fetched from the fixed upstream origin. The system-test `asset-mirror` volume is
a point-in-time, read-only copy of this directory: after repairing or adding a host mirror object, rebuild that volume
before running QA. Although the downloader can detect a stale object and try its fixed upstream source, the release
runner rejects that fallback and any mirror auto-population as invalid qualification evidence. Builder pulls pass only
when each artifact reports an explicit local-mirror read; checksum/version metadata may still use upstream.

### CLI is the canonical interface

The `mvm` CLI is the ONLY supported interface for all mvmctl operations. Do NOT bypass it with raw commands. The CLI handles privilege escalation, database state tracking, and dynamic resolution of assets (IPs, keys, images, kernels).

Built with `github.com/spf13/cobra`. Root command in `internal/cli/root.go`. Subcommands in `internal/cli/{domain}.go` -- monolithic files per domain (one file contains all subcommands for that domain).

CLI aliases: Domain-level aliases for faster typing: `mvm net` for `mvm network`, `mvm img` for `mvm image`, `mvm vol` for `mvm volume`, `mvm bin` for `mvm binary`, and `mvm ss` for `mvm snapshot`. Environment workflow subcommands have shorter aliases: `mvm env up` for `mvm env apply` and `mvm env down` for `mvm env destroy`. Every `list` subcommand has both `ls` (Use) and `list` (Alias). Every `remove` subcommand has `rm` (Use), `remove`, `delete`, and `del` (Aliases).

### VM-to-VM execution transition

The legacy guest-originated `remote_vm` handler and protocol are removed. The cutover first made every legacy request
fail closed before source lookup, payload parsing, destination lookup, vsock dial, relay, or command logging; the
isolated source-session slice then removed the generic callback and shared guest connection entirely. VM-to-VM
execution remains unavailable while ordinary host-to-VM execution and all copy directions are preserved.

Directional exact-user exec-policy intent is now public through
`mvm policy exec create <source-vm> <destination-vm> <target-user>`, `mvm policy exec ls [--json]`, and
`mvm policy exec rm <policy...> [--force]`. Creation resolves the source and destination through the VM domain, rejects
the same VM and the root target user, and derives a deterministic identity from the exact source VM ID, destination VM
ID, and target user. It performs no network lookup: the grant is directional and independent of same-network,
cross-network, or absent IP-network placement. List and removal operate only on the exec-policy family.

The protocol-only `internal/lib/vmexecwire` foundation is implemented. It uses fixed `MVMEXEC1` magic, version 1, a
big-endian length, and at most 64 KiB per frame. Strict closed schemas distinguish a guest-source request from the exact
host-built destination request: only the destination request carries the canonical lowercase UUIDv4 agent token. Every
request and stream frame carries one 32-lowercase-hex request identity; target users must be non-root, commands are
limited to 32 KiB, combined stdout and stderr to 8 MiB, and reported execution duration to five minutes. The response
grammar allows `stdout` and `stderr` followed by exactly one terminal `result` or `error` and then transport EOF;
`cancel` is the only request-bound control frame.

The guest agent now recognizes only the exact `MVMEXEC1` prefix as the destination protocol on its existing vsock port;
all other prefixes remain on the ordinary host-exec/copy protocol. It strictly decodes the host-built destination
request, requires the configured canonical token in constant time before effects, resolves the exact target account to
a nonzero numeric UID plus numeric primary/supplementary groups and a clean absolute home, and executes `/bin/sh -c`
with that credential, home, working directory, and a fixed environment. Output and duration use the wire limits,
accepted vsock descriptors are close-on-exec/nonblocking with effective deadlines, cancellation kills the command's
process group, and terminal writes have a separate two-second bound. Errors and logs do not expose the command, token,
or process cause. This process-group boundary is not a per-command cgroup guarantee and does not claim cleanup of a
descendant that deliberately leaves the group.

The isolated source-session foundation is also implemented but remains dormant. A host-side `SourceSession` accepts
only an already authority-admitted connection, authenticates it with exact `MVMSRC01`/version-1 framing and the
configured agent token, and binds one random 32-lowercase-hex source-session ID to one request. Bootstrap and ready
bodies are limited to 512 bytes. The ordinary non-TTY exec path has a separate named method that carries only this
session ID; normal exec, shell, and copy do not open or inspect a source session. Unknown ordinary exec response types
are protocol failures. The legacy generic `OnHostFrame`, shared `activeConn`, `remote_vm`, and
`internal/vsockhandler` paths are removed.

After `ReadRequest` succeeds, `SourceSession.WaitCancel` permits exactly one control reader bound to that request's
ID. It accepts only the matching `cancel` frame. Malformed, non-cancel, wrong-ID, and context-cancelled partial control
input closes the source transport fail-closed; explicit `Close` and context cancellation unblock a partial read. The
cancel reader sets only the read deadline while response writers set only the write deadline, preserving full-duplex
source response output. This is a dormant source-side primitive, not destination cancellation or relay composition.

The closed host-side `DestinationSession` is also implemented but dormant. It consumes an already selected connection
and permits one validated `Open` with one host-built `DestinationRequest`. Its strict request-ID-bound response reader
enforces the wire output limits, permits one sequential reader, and withholds either `Result` or `Error` until physical
EOF. After open, at most one exact matching `Cancel` may be written concurrently with response reading. Read and write
operations use independent directional deadlines bounded by one fixed session deadline; the opening context or an
absolute lifetime no later than five minutes closes even an idle session. Operation-context cancellation and explicit
`Close` interrupt partial I/O. Protocol or transport failure closes the connection, and cleanup is close-once with the
first result cached. The type performs no dialing, socket-path selection, VM or policy lookup, root effect, audit, or
API relay work and does not create a positive VM-to-VM path.

Inside the source guest, the agent binds a pending session to the exact outer command process created by the matching
ordinary exec. The outer command may run as root or non-root, starts in a new session/process group, and receives no
token through its environment, arguments, or filesystem. The mode-`0666` local AF_UNIX broker authorizes by process
identity rather than path access: it acquires `SO_PEERCRED` and `SO_PEERPIDFD` together, validates and retains pidfds,
requires matching UID/GID, and twice proves a stable PID/parent/start-time chain to the live outer command before one
claim. The source-session ID is correlation, not authority. Admission is capped at eight concurrent attempts, local
request reads at two seconds, and parent traversal at 64 identities. Missing `SO_PEERPIDFD` or incomplete process
evidence fails closed.

The broker transfers the dedicated transport from disconnect monitoring to exactly one local request and relays the
closed `MVMEXEC1` stream. Terminal delivery requires physical EOF. Context cancellation closes blocked source-session
reads or writes, ordinary source-command cancellation kills its process group, the first relay disconnect closes both
transports, and agent shutdown releases all pending/bound sessions and pins. The local client form is
`mvm-agent remote --user <non-root> <destination> -- <command>`; it preserves binary output and exposes only generic
local errors.

The root-owned VM-exec capacity substrate is implemented and production-composed only behind the retained authority
sidecar. Strict schema-1 records live at
`/var/lib/mvmctl/allocations/exec/<32-lowercase-hex>.json`; they bind the non-root owner, exact source and destination
VM IDs and launch generations, host boot ID, and creation/expiration times from `CLOCK_BOOTTIME`. The managed
directories are exact `root:root` mode `0700`, records are one-link exact `root:root` mode `0600`, and the never-unlinked
global lock is `/run/mvmctl/authority/capacity.lock`. Holding that lock serializes the complete cross-process,
cross-user scan/prune, ceiling check, and no-replace publication. Corrupt or ambiguous state fails closed before
canonical pruning; safe crashed temporary files, prior-boot records, and expired records are removed with directory
sync. Creation, deletion, pruning, durability uncertainty, and cleanup-required outcomes remain explicit error
evidence.

Each reservation fixes its relay-stop deadline at creation plus `vmexecwire.MaxExecutionDuration`; setup and suspend
consume that `CLOCK_BOOTTIME` budget. Its record expires only after a separate four-second terminal/cleanup grace.

The active VM-exec pair authority is implemented and production-composed behind that same sidecar. It opens the
instance authority once, takes the
global index once, requires two distinct same-owner `active` records, opens each VM authority separately, and acquires
the two VM locks in canonical lexical ID order. After releasing the index, it retains the shared roots separately from
both active leases, reopens both recorded process sets into pidfds, and proves both complete active identities plus
final pidfd locator/liveness. It then reserves root-global capacity and rechecks the host boot ID, both complete
identities, and both final pidfd locator/liveness proofs before exposing an immutable owner/source/destination
launch-generation, reservation, boot, and relay-stop admission. Its close-once cleanup releases destination/source
peer pidfds, capacity, destination/source process capabilities, active leases in descending lexical order, and the pair
roots last without reacquiring a lock.

Its production constructor fixes a host-global ceiling of eight sessions. Each retained source and destination
connection can be bound to the corresponding admitted Firecracker through `SO_PEERCRED` and `SO_PEERPIDFD`, exact
PID/start identity, matching effective UID/GID, complete active-process identity, and retained-process proof. After
both expensive identity proofs, readiness performs a bounded final locator/liveness sweep of both peer pidfds and both
retained process sets, followed by one final host-boot and relay-budget check.

The absolute `CLOCK_BOOTTIME` stop is also production-composed. It arms a close-on-exec, nonblocking `timerfd` with
`TFD_TIMER_ABSTIME` and waits through bounded polling with cancellation. Exactly one concurrent waiter is allowed; a
simultaneous second waiter is rejected rather than sharing the in-flight wait, while calls after completion receive the
cached result. Release runs once while retaining cleanup-error evidence. The retained handler starts this stop from the
capacity admission's absolute deadline and owns connection shutdown plus authority cleanup when it completes.

The exact-one VM-exec descriptor transport and structural admission are used by the registered sidecar action. Its
single-use sender attaches one duplicate through one `SCM_RIGHTS` message on the first non-NUL frame byte, sends the
ordinary remainder without ancillary data through cancellation-bounded nonblocking I/O, and closes the duplicate
exactly once. The receiver uses `recvmsg(MSG_CMSG_CLOEXEC)` for every read, takes cleanup ownership of every installed
descriptor before validation, rejects missing, extra, unknown, late, or truncated control data, and retains only one
close-on-exec connected, non-listening `AF_UNIX` `SOCK_STREAM`. Structural admission deliberately proves no peer
process, Firecracker, VM, guest port, or policy identity. Its private phase-specific receiver transfers a connection
only after the matching closed frame is decoded and exposes only a borrowed connection capability to the next private
effect.

The retained sidecar wire, process transport, named Jailer client, and root handler lifecycle are production-composed,
and the central dispatcher registers exactly `jailer-vm-exec-session`. The fixed protocol admits only the ordered
`open_source`,
`source_retained`, `bind_destination`, `ready`, `release`, and `released` phases or one bounded terminal failure. The
process transport starts one copy of the existing system binary through the existing privileged invocation and an fd-0
AF_UNIX control socket, transfers one owned connection duplicate in each setup phase, and bounds cancellation, process
group termination, waiting, and cleanup. The Jailer client duplicates each caller-owned `net.UnixConn`, so the caller
retains its original source and destination connections.

The handler structurally retains the source before its acknowledgement, retains and binds the destination before ready,
and performs close-once cancellation-detached cleanup on explicit release, control EOF, setup failure, stop completion,
or response failure. It reports `released` only after explicit-release cleanup succeeds. The handler accepts no path,
command, token, target user, policy material, payload, or raw descriptor accessor. Its sole synchronous binder bridge
keeps each handler-owned descriptor opaque, live, and immutable under its owner lock while Jailer proves the peer; the
descriptor is never exposed through the public capability.

Production dispatch constructs the pair/capacity/peer/absolute-stop server and places it behind this retained handler.
The caller UID/GID passed to Jailer derives only from the authenticated control peer identity and never from request
fields. Authenticated identity, peer, environment, executable, or authorization failures before action dispatch emit
one bounded `MVMXSES1` failure without draining request input. Once dispatched, the handler remains the sole owner of
action-specific responses and destination/source connection cleanup; its server releases the stop first, then the
destination/source peer pidfds, capacity, destination/source process capabilities, descending lexical active leases,
and pair roots.

The typed `logging.VMExecAuditSink` foundation is implemented separately from the relay. It writes one complete
schema-1 JSON object per line to the dedicated user-owned `vm-exec-audit.jsonl` file in the mvmctl user cache
(`~/.cache/mvmctl` by default). Its closed projection contains only `vm_exec`, the request/source/destination
identities, non-root target user, UTC start time, bounded duration, one of the six fixed results, and the
result-consistent optional exit code; it contains no command, agent token, or process cause. The sink serializes record
and close transitions across copies, poisons itself after any incomplete or failed write, closes once, and surfaces
typed validation, I/O, and cleanup errors.

This file is user-owned observability, not tamper-proof root authority. Construction rejects an effective root caller
because the user-cache pathname is not retained descriptor-relatively. `Record` observes its supplied context and does
not detach cancellation itself. When the future relay reaches a terminal cancellation or timeout after its operation
context is cancelled, the relay must derive a fresh bounded context through `context.WithoutCancel`, call `Record` with
that context, and surface or join any audit failure with its relay result.

These components still do not form a positive VM-to-VM path. No API-owned relay listener/consumer opens a source or
destination session, invokes the registered retained-sidecar client, reads and forwards relay payload, resolves and
enforces the exact runtime directional exec policy, constructs the authorized destination request, or invokes the audit
sink. Consequently there are no production VM-exec audit records yet.
The legacy VM-wide trust flag is removed from the fresh schema, model, configuration defaults, VM create CLI/API,
environment specs, results, and repository persistence; it has no compatibility path into exec policies. Positive
Python L2 qualification remains pending, so VM-to-VM execution remains unavailable. No SQLite VM status,
guest-supplied source ID, socket path, or process-local semaphore may substitute for root runtime authority.

### Provisioner Backend (LoopMount vs GuestFS -- mutual exclusion)

Two independent rootfs provisioning backends. They are **mutually exclusive** -- a single VM or image operation uses exactly ONE backend, never a combination. The `guestfs_enabled` user setting is a **toggle selector**, not a preference.

- **LoopMount** (default, `guestfs_enabled=false`): Uses the compiled `mvm run provision` subcommand via `losetup` + `mount` + `chroot`. No system package dependencies beyond the `mvm` binary itself.
- **GuestFS** (opt-in, `guestfs_enabled=true`): Uses `libguestfs` via `internal/lib/provisioner/guestfs/`. Requires `libguestfs` system packages installed on the host.

Provisioner type resolved ONCE at startup in `api.NewOperation()` by reading `settings.guestfs_enabled`. All callers use `op.ProvisionerType` directly. Backend interface defined in `internal/lib/provisioner/backend.go`.

Task 13 now has an activated private LoopMount receiver path for VM provisioning and read-only image inspection in
`internal/service/loopmount/`. Its named
`ProvisionVM`, `InspectImage`, and `OptimizeImage` client methods use closed VM/image request, plan, and result types.
VM resize is grow-only relative to the admitted rootfs size. The protocol bounds rootfs and image sizes, guest
usernames, SSH key count and bytes, agent port, and canonical UUIDv4 token before exchange. A result becomes authority
only when the privileged process started and the transport observed actual response EOF; otherwise stable transport
error details report `process_started` and `outcome_unknown`, while an EOF-qualified remote `DomainError` retains its
code, class, entity, and bounded details.

The private rootfs-authority foundation accepts only an authenticated non-root caller and one canonical managed-cache
locator. It pins the cache through descriptor-relative no-symlink/no-magic-link traversal, permits mount crossing only
while locating the cache, requires the final caller-owned cache to use the closed local-filesystem set, and records its
device, inode, and typed mount identity. Descendants add `RESOLVE_NO_XDEV` and select only fixed
`vms/<vm-id>/rootfs.img` or `images/staging/<image-id>/rootfs.img` leaves. Admission enforces caller ownership, one-link
regular-file type, safe owner access and modes, exact bounded size, stable metadata, and action-specific read-only or
read-write access. The lease retains the image staging directory when replacement will eventually be required and
releases every descriptor in checked reverse order with cancellation-independent cleanup. Deterministic L1 races
replace each caller-owned ancestor and leaf immediately before and after open; admission either fails closed or retains
the original object rather than attacker content.

The private rootfs mount-source policy consumes only pathless receiver-derived block-identity and filesystem
probes. It accepts at most 16 partition probes. A raw source is valid only when there are no partitions and its probed
filesystem exactly matches the request; a partitioned source is valid only when exactly one partition matches.
Malformed probes, reused or duplicate block identities, duplicate partition numbers, raw-plus-partition ambiguity, and
missing or multiple matches fail closed. Receiver discovery now enumerates fixed `/sys/class/block` with bounded entry
and partition counts, proves stable membership and exact block identities through retained descriptors, probes only
through fixed `/usr/bin/blkid` with bounded output and exit policy, a five-second cap, and any earlier caller deadline,
then returns a pathless source capability bound to the exact probe. No caller-selected tool, path, or device name
crosses the seam.

The pure loop-device kernel contract admits only fixed `/dev/loop-control` character-device identity `10:237`.
It derives `/dev/loop<N>` only for an index from `0` through `2^20-1` and requires block major `7` with minor exactly
`N`. The generated `unix.LoopConfig` carries the exact admitted rootfs descriptor and only
`LO_FLAGS_AUTOCLEAR|LO_FLAGS_PARTSCAN`, plus `LO_FLAGS_READ_ONLY` for read-only access; every other field remains zero,
direct I/O is disabled, and no legacy setup fields are used. Invalid inputs fail closed through the private LoopMount
`DomainError` path. The pure constructor itself performs no I/O; the private attach/release capability consumes it.

That private capability opens only fixed `/dev/loop-control` and the kernel-derived `/dev/loop<N>` with
`O_RDWR|O_CLOEXEC|O_NOFOLLOW`, then uses `fstat` to recheck the exact control and block-device identities. It obtains
each candidate through `LOOP_CTL_GET_FREE` and retries only configure-time `EBUSY`, for at most 16 attempts. One atomic
`LOOP_CONFIGURE` binds the admitted rootfs descriptor and closed access mode. Configure failure closes the candidate
without `LOOP_CLR_FD`, because another process may have won the free-device race.

The resulting package-private lease exposes no path or descriptor accessor. Its checked, idempotent release uses
`LOOP_CLR_FD` and closes the loop descriptor under cancellation-independent cleanup; `AUTOCLEAR` remains crash safety.
If configuration succeeds but loop-control close fails, attachment returns both the configured lease and the error so
the caller can still release it.

The read-only image-inspection adapter locks its OS thread, enters a private mount
namespace, makes propagation private, mounts only the admitted source at fixed `/run/mvmctl/rootfs` with
`ro,nosuid,nodev,noexec`, and revalidates the exact source, mountpoint, filesystem, and backing-image identity. It reads
only bounded fixed `etc/os-release` or `usr/lib/os-release` leaves through no-symlink/no-cross-mount traversal, then
releases root, mount, source, partition, loop, mountpoint, and image authority in checked reverse order. `unchanged`
requires complete admitted/statx stability and successful cleanup; otherwise the closed result is `outcome_unknown`,
while the primary `DomainError` and bounded cleanup details are preserved. The short-lived receiver handler exits
rather than returning its unshared OS thread to the Go runtime.

The private `ProvisionVM` adapter consumes the admitted rootfs capability directly. It
grows the admitted ext-family filesystem through fixed `e2fsck`/`resize2fs` or Btrfs through fixed
`btrfs filesystem resize`, with a five-minute tool cap and any earlier caller deadline, and has ext4/Btrfs convergence
evidence from either the exact old or target backing size. Every guest read and write is descriptor-bound beneath the
admitted mounted root. Its closed content set is hostname/hosts, DNS,
existing-account SSH/sudo, fixed SSH service, embedded agent, fstab, a root-only persistent
`/etc/mvmctl/agent.token`, and exactly one admitted init-system enablement: systemd `sysinit.target` or OpenRC default
runlevel. Cleanup unwinds root, mount, source, partitions, loop, mountpoint, and backing authority in reverse and maps
mutation, durability uncertainty, and unknown outcomes without losing the primary error.

The private `Server` coordinates each admitted operation through two action-specific effect seams.
`rootfsEffects` contains exactly `ProvisionVM` and `InspectImage`, while the private `imageOptimizeEffects` contains
only `OptimizeImage`. Provisioning and inspection validate `rootfsEffects`; optimization validates
`imageOptimizeEffects`; every action separately validates the shared rootfs authority and resource-lock seam. After
validating the closed request and authenticated caller, the coordinator acquires a resource lease through the private
`rootfsResourceLocks` seam, admits the fixed rootfs, invokes the corresponding effect, and validates the request-bound
result. The lock seam has a private adapter over the implemented `authorityfs` LoopMount UID and canonical VM/image
lock capabilities. The production composition root constructs only the provisioning/inspection server, binds the exact
embedded-agent accessor, and registers only `loopmount-provision-vm` and `loopmount-inspect-image` in the early
privileged switch. The optimize seam has no production implementation, construction, receiver handler, or dispatcher
entry. Live-kernel qualification, public/backend cutover, and legacy-route removal remain pending.

The coordinator requires an action-specific effect cleanup capability and unwinds effect cleanup, rootfs authority,
then the resource lock, each with `context.WithoutCancel`. Effect outcomes use closed action-specific states to derive
the mandatory `rootfs_modified`, `image_replaced`, `durability_uncertain`, and `outcome_unknown` error evidence;
invalid or missing state cannot authorize success. Cleanup failures add bounded details without replacing the primary
`DomainError` identity.

VM grow replay admission accepts only the stable pre-grow size or the exact requested target size, rejects any third
size before an effect begins, and retains the actually admitted size on the rootfs lease handed to the narrow effect
seam. Image optimization remains blocked from privileged registration until durable shrink and replacement replay
authority exists. Provisioning and read-only inspection are reachable only through their exact internal privileged
actions; the ordinary LoopMount backend has not cut over to them. Live-kernel qualification, backend/public cutover,
and removal of `mvm run provision`, caller-shaped raw wire/sudo, and the legacy arbitrary cleanup/unmount surface remain
pending. Task 13 is therefore incomplete.

**Performance:** GuestFS is 3–5x slower than LoopMount for VM creation (9–14s vs 2–5s wall-clock, sequential). Under parallel load GuestFS degrades further due to QEMU lock contention. See `docs/adr/0003-loopmount-guestfs-mutual-exclusion.md` for full benchmark data.

**Why no fallback chain:** A fallback (try loop-mount, fall back to guestfs) was rejected because: (1) if a user enables GuestFS, they expect GuestFS behavior -- silent fallback to loop-mount violates least surprise; (2) each backend has different sudoers requirements -- mixing them in one session increases the privilege surface; (3) each backend has independent test suites -- a fallback chain requires testing all combinations; (4) an earlier version incorrectly described GuestFS as a "fallback," which caused regression bugs where a stale `guestfs_enabled=true` silently selected the slow backend.

### Firewall backend transition

The transitional implementation has two independent firewall backends -- **nftables** (default) and **iptables**
(legacy) -- selected by the `firewall_backend` setting. Exactly one is active per session. This describes existing code,
not the accepted v0.3.0 target.

- **nftables** (default): Uses `internal/lib/firewall/nftables.go` -- atomic `nft -f -` batch files.
- **iptables** (legacy): Uses `internal/lib/firewall/iptables.go` -- per-rule `iptables` calls.

Selection logic in `firewall.NewFirewallTracker(backend, xtcommentAvail, db)`. Both DB tables persist independently; only the active backend's table is queried.

**Why nftables is default:** nftables is the modern replacement for iptables and is the default firewall on RHEL 9+, Debian 11+, Ubuntu 22.04+, and Arch Linux. It provides atomic batch operations via `nft -f -` (all rules applied or none), cleaner rule management (no separate save/restore workflow), and better performance for large rule sets.

**UFW compatibility:** The nftables backend uses non-hook chains inside the system `ip filter` and `ip nat` tables, with jump rules inserted at position 0 of built-in chains (FORWARD, POSTROUTING, INPUT). This ensures MVM rules evaluate before UFW's. When UFW reloads (`ufw reload`), it flushes built-in chains and removes MVM's jump rules -- these are re-created lazily on the next `mvm network` or `mvm vm create` operation. The iptables backend has the same limitation.

The `FirewallTracker` also reads an `iptables_xtcomment` user setting that adds comment tags to iptables rules for easier identification.

The private v0.3.0 compiler foundation under `internal/service/firewall/` pins `github.com/google/nftables v0.3.0` and
accepts only fully resolved mvmctl-owned intent. It emits fixed `mvmctl_bridge` and `mvmctl_inet` tables and fixed hook
chains. Bridge prerouting uses raw priority for source MAC/IPv4/ARP anti-spoofing and unsupported-L3 guards, while
bridge forward compiles same-network allows and default deny. Inet forward/input/postrouting compile routed allows,
established replies, host-service access, default deny, and conditional egress/NAT; disabling masquerade emits neither
uplink egress nor NAT. Compilation rejects overlapping managed IPv4 prefixes and unusable network, host, or endpoint
addresses. The caller supplies no generation identity. The compiler derives SHA-256 from a versioned canonical closed
projection of normalized intent and the exact managed tables, chains, rules, expressions, order, and userdata schema.
The userdata generation slot uses a fixed placeholder while hashing to break self-reference; the derived digest and
rule identity are then bound into every rule. Explicit empty intent emits no reserved tables or rules.

The projection covers all fields of exactly the eight emitted `github.com/google/nftables v0.3.0` expression structs:
`expr.Meta`, `expr.Masq`, `expr.Bitwise`, `expr.Cmp`, `expr.Ct`, `expr.Payload`, `expr.Range`, and `expr.Verdict`.
Typed-nil or unknown expressions fail closed. Digest encoding streams through a fixed 64-KiB buffer directly into
SHA-256, and canonical validation, encoding, and finalization errors surface to the compiler caller. Any
nftables-module upgrade or change to the emitted expression set requires an explicit schema audit and digest-version
bump. Input ordering is deterministic.
On the isolated representative 1,000-VM benchmark, compilation was roughly 23–28 ms with about 10 MB and 179,000
allocations; the regression test enforces a 250,000-allocation ceiling.

The compiler remains inert. A separate implemented authority foundation now provides the never-unlinked global
`/run/mvmctl/authority/firewall.lock`, a strict committed/applying generation record and bounded codec, and a
descriptor-relative exclusive store at `/var/lib/mvmctl/firewall/generation.json`. Its private acquisition seam first
requires earlier network authority, then binds the admitted record to the current boot and pinned host network
namespace while retaining pointer-shared cached close-once cleanup authority. It fails when the record is missing
because only qualified kernel inspection may establish initial authority. An admitted lease can atomically replace the
record only after re-reading both its exact contents and retained file evidence; post-rename readback preserves exact
`record_replaced` and durability evidence and prevents another mutation after an ambiguous outcome.

A separate inert raw-netlink snapshot collector now issues only receiver-chosen read requests for the two exact managed
tables, five fixed chains, their rules, and the absence of sets, objects, and flowtables. It brackets collection with
ruleset-generation reads, uses a shared 32-MiB/20,000-message budget across fresh single-request transports, and feeds
the replies to the strict raw decoder, which rejects unknown expressions and structural drift. A retained namespace
authority now requires the earlier network and generation identities to agree, pins and rechecks the exact current host
network namespace, and supplies fresh strict `NETLINK_NETFILTER` transports through that descriptor. No production
application or recovery path obtains this factory or invokes the collector, and no real-kernel result is qualified.

Qualified initial inspection/adoption, actual empty-generation deletion/application, `nftables.Conn`/`Flush`, mutation
and recovery orchestration, privileged action, public policy cutover, legacy `nft` CLI removal, live-kernel
qualification, and L2 qualification remain pending. These foundations are not evidence of kernel application,
atomicity, enforcement, or release readiness; the transitional backends and public policy path remain active.

ADR-0017 defines the remaining clean-break replacement. Before any fixed-name table can be replaced or removed, root
must hold the never-unlinked global `/run/mvmctl/authority/firewall.lock` and bind the exact observed generation to the
strict `/var/lib/mvmctl/firewall/generation.json` authority record. The compiler now derives the canonical digest, but
that digest alone is neither kernel ownership nor deletion authority. An applied empty generation must mean both
reserved tables are absent; the implemented empty compiler output does not yet inspect, delete, or apply kernel state.
Missing, corrupt, mismatched, or ambiguous authority never permits deletion.

`github.com/google/nftables v0.3.0` exposes rule userdata but no table/chain ownership metadata, and its structured rule
reader can omit expression types it does not understand. The inert raw collector and retained host-netns factory close
the expression-decoder and namespace-selection seams, but root-record-bound live inspection within an accepted threat
model for another root or `CAP_NET_ADMIN` actor and real-kernel delete/recreate batch atomicity remain explicit
activation gates. The final adapter must resolve those gates, apply one complete generation directly, then remove
iptables, the backend selector, backend-specific repositories, and the production `nft` CLI path. Library and netlink
types stay private to that adapter; there is no final CLI fallback. Kernel nftables, bridge-family, connection-tracking,
NAT, and privilege capabilities remain host requirements. A disposable QA runner may use the `nft` CLI only as an
independent read-only oracle.

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
| `internal/service/jailer/entry.go` (Jailer exec) | Privileged chroot setup ends with `syscall.Exec` so Jailer inherits console stdin/stdout/stderr and becomes the managed Firecracker process |
| `internal/core/ssh/utils.go` (SSH connectivity probe) | Uses `exec.CommandContext` with a short-lived probe context for SSH connectivity detection |
| `internal/service/loopmount/provisioner.go` | Direct provisioning engine running losetup/mount/umount/chroot in chained operations with precise error recovery |
| `internal/service/agent/exec.go` (command execution) | Uses `exec.CommandContext` for `su` user switching and `sh -c` command execution inside the guest agent |
| `internal/service/agent/pty.go` (PTY session) | Uses `exec.CommandContext` for `su` user switching to establish PTY sessions |
| `internal/service/agent/destination_process.go` (VM-exec destination) | Uses `exec.CommandContext` only after exact nonzero numeric UID and numeric-group resolution; fixes environment/home and `/bin/sh -c`, creates a process group, and kills that group on cancellation |
| `internal/lib/archive/archive.go` (xz decompression) | Uses `exec.CommandContext` for `xz -d --stdout` pipe-based decompression |
| `internal/lib/system/runner.go`, `interactive_run.go`, `spawn.go` | Implementation of the subprocess abstraction layer (`DefaultRunner`, `RunInteractive`, `SpawnService`). These use raw `os/exec` because they ARE the abstraction boundary |

**Binary lookup carve-out:** Utility files across the codebase use `exec.LookPath()` (not `exec.Command`/`exec.CommandContext`) solely to check whether a system binary exists before calling it through `DefaultRunner.Run()`. This is NOT a subprocess execution — it's a filesystem existence check that happens to use the `os/exec` package. These files are not listed as exceptions above and do not violate the subprocess rule. Key locations: `internal/core/host/detector.go`, `internal/core/host/probe.go`, `internal/core/host/utils.go`, `internal/core/ssh/service.go`, `internal/core/key/utils.go`, `internal/core/image/service.go`, and others in `internal/lib/`, `internal/core/`, `internal/service/`.

Services running as subprocesses (`mvm run <service>`) use `system.SpawnService(ctx, cfg)` which resolves the executable, optionally prepends `sudo`, and manages process groups. The Jailer service uses the documented `syscall.Exec` exception after validating trusted executables and per-VM resource paths; other services do not execute arbitrary child commands except the documented provisioning engine.

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
| **API inputs** | `internal/core/{domain}`, `internal/infra`, `internal/lib/*` | `import "mvmctl/internal/lib/model"` |
| **Core domain** | `internal/infra`, `internal/lib/*`, `internal/assets`, `internal/service/*` (for subprocess spawning) — no other core domains | `import "mvmctl/internal/lib/model"` |
| **Infra/lib** | stdlib, `github.com/jmoiron/sqlx`, `pkg/errs`, `internal/assets`, `internal/infra`, other `internal/lib/*` sub-packages, external deps. Exception: `internal/lib/provisioner/loopmount/` imports `internal/service/loopmount` for wire-protocol types. | N/A -- leaf nodes that never import core, api, or cli |

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

The test architecture uses L0, L1, and L2 test levels plus T1, T2, and T3 system-test execution tiers. Read
`docs/system-test-architecture.md` for the architecture and `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` for
authoring guidance.

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

**Ground truth.** Every user-facing feature must have an L2 test. Real binary, real subprocess, real infrastructure inside
a disposable Firecracker VM with nested KVM. No mocking of any kind. The release candidate is installed through
`host install-system`, and T1/T2 tests invoke exact `/usr/local/bin/mvm`; a directly copied or PATH-selected developer
binary is not release evidence. Tests verify actual outcomes at the OS level: JSON, files, processes, cgroups,
namespaces, links, mounts, sockets, and nftables state as applicable.

The outer host controller and the artifact under test are deliberately separate. `MVM_BINARY` names an already
installed controller used only to create, inspect, copy to, and destroy disposable runner VMs.
`MVM_CANDIDATE_BINARY` names the release artifact (default `dist/mvm`) installed only inside those VMs. The runner
image tag comes from an isolated `MVM_CANDIDATE_BINARY --version` probe. Building or testing a candidate must never
replace `MVM_BINARY`.

Runner initialization requests exact Firecracker 1.16.0 with
`/usr/local/bin/mvm init --non-interactive --binary-version 1.16.0` when no local pair exists. This avoids a moving
`latest` dependency while preserving existing local/default selection on already-prepared images.

This controller/candidate isolation applies only to T1/T2 runner VMs. Tier 3 is host-direct and may mutate outer-host
VMs, kernels, images, networks, and volumes. It is allowed only on a clean disposable qualification host where the
candidate has been explicitly installed as exact `/usr/local/bin/mvm`; it is not an iterative developer-host test mode.
The runner rejects Tier 3 or `--all` before any work unless `--host-direct` explicitly acknowledges outer-host mutation.
Its stricter `--release-qualification` mode requires an unfiltered `--all`, `--host-direct`, a fresh explicit-version
release build, and exact root-owned `/usr/local/bin/mvm` content/version identity before resource mutation. This consent
and binary gate does not yet prove that every Tier 3 resource is newly owned or that every teardown succeeded: do not
invoke Tier 3 or `--all` on a host with pre-existing mvmctl state until the remaining Task 17 cleanup gates land.

Before any probe, build, or resource mutation, the runner also validates the system-test registry itself. Domain names
and test files must be unique, registrations must be non-empty canonical regular files under `tests/system/`, and the
registered set must equal the discovered `tests/system/**/test_*.py` set. A missing or duplicate domain therefore cannot
silently produce incomplete release evidence.

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

### Coverage matrix
`tests/system/COVERAGE_MATRIX.md` tracks command and flag coverage. The authoring guide explains how to classify a
scenario as L0, L1, or L2. Release qualification requires the matrix and executable registry to agree with the selected
system-test suite.

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
