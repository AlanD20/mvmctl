# mvmctl

MicroVM Manager -- a speed-first CLI for managing Firecracker microVMs. Provides fast VM lifecycle management, networking, image provisioning, and console/SSH access.

## Language

### Domain
A business capability with isolated logic. Each domain (vm, network, image, kernel, binary, key, host, config, cache, volume, console, logs, cloudinit, ssh) lives in `core/{domain}/` and consists of Controller, Service, Repository, and Resolver files. Domains do NOT import other domains.
_Avoid_: Module, component, service (overloaded terms)

### Intra-domain orchestration
Work that sequences multiple operations within a single domain (e.g., teardown NAT -> remove bridge -> delete DB record). Lives in core/ Service classes.
_Avoid_: Orchestration in Controller

### Cross-domain orchestration
Work that coordinates across multiple domains (e.g., VM creation orchestrates vm + network + image + kernel + cloudinit). Lives exclusively in api/ *Operation classes.
_Avoid_: Cross-domain imports in core/

### Controller (stateful, per-entity)
A class bound to a single entity instance. Manages lifecycle state transitions for that entity (start, stop, pause, resume, snapshot). The litmus test: if the operation doesn't need a specific entity instance to exist, it doesn't belong in Controller. Controller communicates with the running entity's Firecracker API socket -- but that's a consequence of the entity-orientation, not the definition. Does NOT validate input. Does NOT orchestrate across domains. Does NOT handle CRUD creation or removal.

*Example: `VMController(pm.item).snapshot()` -- snapshots this specific VM. `NetworkService(repo).remove_bridge(bridge)` -- removes a bridge, no single network entity needed.*
_Avoid_: Controller.remove(), Controller.create() -- these are CRUD operations, not state transitions

### Service (stateless, intra-domain)
A class for stateless intra-domain operations. Handles infrastructure operations (bridges, TAPs, NAT, subprocesses, file/disk operations). Performs state detection (checking current system state as part of an operation -- "does this bridge exist?" to branch execution). Guards invariants that protect against system damage. Does NOT validate caller input. Does NOT manage state for a single entity -- Service operates on infrastructure, not on a bound instance.

*Litmus test: if the operation would work the same way without a specific entity instance, it's Service. If it needs to communicate with a running entity's Firecracker API socket, it belongs in Controller. If it sequences multiple infrastructure steps (teardown NAT -> remove bridge -> delete DB record), it's intra-domain orchestration in Service.*
_Avoid_: Validation gatekeeping in Service

### Repository
A class for database CRUD operations. ALL SQL queries live here -- single file, no separate Inventory/Query classes. Uses SQL-level computation (COUNT, WHERE IN), never fetch-all-then-filter.
_Avoid_: Business logic in Repository

### Manager+Process pattern (runtime services)
Long-running subprocesses (console relay, nocloud-net server, mvm-provision) follow a two-class split:
- **Manager class** (e.g., `ConsoleRelayManager`): imported by core/. Handles start/stop/restart, PID files, health monitoring, orphan cleanup.
- **Process class** (e.g., `process.py`): standalone `main()` entry point compiled via Nuitka. Has NO upward imports into `mvmctl` -- only stdlib + minimal shared defaults. Communicates with the parent via stdin/stdout JSON protocol or Unix sockets.

The manager+process boundary enforces reliability: if the manager crashes, the subprocess continues running. If the subprocess crashes, the manager detects it via PID file + health check. Process binaries are compiled as a single multidist `mvm-services` binary with `sys.argv[0]` dispatch.

### `_graceful_read` decorator
Wraps Repository read methods to return a safe fallback when the database is unavailable (e.g., during unit tests, or after a clean with partial state). The decorator's two parameters follow Python's mutable-default rules:

| Return type | Parameter | Example |
|---|---|---|
| Single optional item | `default=None` | `def get(id) -> Item \| None` |
| Collection | `factory=list` (or `factory=dict`) | `def list_all() -> list[Item]` |
| Scalar (count) | `default=0` | `def count() -> int` |

Never use `default=[]` or `default={}` -- those are mutable defaults shared across calls. Use `factory=list` / `factory=dict` instead.

### Resolver
A class for entity resolution by identifier (name, ID, IP, MAC to domain object). Uses `RELATIONS` dict + `RelationEnricher` for batch relation loading of cross-entity data (e.g., loading an ImageItem with its referencing VMs).
_Avoid_: DB queries or business logic in Resolver

### Enrichment pattern (RelationSpec + RelationEnricher)
Cross-entity data (e.g., which VMs reference this image?) is loaded via the enrichment system, not via ad-hoc queries in Service classes. Every domain that needs cross-entity data declares `RelationSpec` entries in its Resolver's `RELATIONS` dict. The `enrich()` method on the Resolver calls `RelationEnricher().enrich(items, include, RELATIONS)` which dispatches batch queries to the related domain's Resolver.

**RelationSpec fields:**

| Field | Purpose | Example |
|-------|---------|---------|
| `fk_field` | The field on THIS domain's Item that contains the FK value | `"id"` (ImageItem.id is referenced by VM's image_id) |
| `resolver` | Registered resolver name for the RELATED domain | `"vm"` (looked up via the resolver registry) |
| `method` | Single-item lookup method on the related resolver | `"by_image_id"` |
| `relation_name` | The field name set on THIS domain's Item after enrichment | `"vms"` -> `image.vms` |
| `is_reverse` | True if the related entity points TO this one (FK lives on related entity) | `True` (VM has `image_id`, not Image has `vm_id`) |
| `batch_method` | Batch lookup method on the related resolver -- must exist for any batch enrichment | `"by_image_id_batch"` |

**Batch method contract on the related Resolver:**

```python
def by_image_id_batch(self, image_ids: list[str]) -> dict[str, list[VMInstanceItem]]:
    """Batch-resolve VMs by image IDs. Returns dict mapping each image_id to its VMs."""
    vms = self._repo.get_by_image_ids(image_ids)
    results: dict[str, list[VMInstanceItem]] = {img_id: [] for img_id in image_ids}
    for vm in vms:
        if vm.image_id in results:   # <-- uses the FK field on the related entity
            results[vm.image_id].append(vm)
    return results
```

**Template for adding enrichment to a new domain:**

In the Resolver file:
```python
RELATIONS: dict[str, RelationSpec] = {
    "vm": RelationSpec(
        fk_field="id",
        resolver="vm",
        method="find_by_<entity>_id",
        relation_name="vms",
        is_reverse=True,
        batch_method="by_<entity>_id_batch",
    ),
}

def enrich(self, items: list[ItemType]) -> list[ItemType]:
    """Enrich items with relations if include is set."""
    if self._include and items:
        RelationEnricher().enrich(items, self._include, self.RELATIONS)
    return items

def by_id(self, entity_id: str) -> ItemType:
    ...
    return self.enrich([result])[0]

def resolve_many(self, ...) -> ResolveResult:
    ...
    items = self.enrich(items)
    return ResolveResult(items=items, ...)
```

**Two-step registry wiring (must both exist):**

1. At the bottom of the Resolver file:
```python
from mvmctl.core._shared import register  # noqa: E402
register("<domain_name>", lambda: ResolverClass)
```

2. In `core/_shared/_resolver_registry.py`, add an entry to the `_RESOLVER_MODULE_PATHS` dict:
```python
"vm": "mvmctl.core.vm._resolver",
```

Both are required. Missing either causes a `KeyError` at enrichment time.

**Rule:** Enrichment is done in the API layer for VM reference checks (caller validates). The Service receives pre-enriched items and reads `item.vms or []`. Services never import a VM Repository directly -- that's a cross-domain violation.

### Validation (caller's responsibility)
Checks that input is structurally valid: format, existence, cross-field constraints. Belongs in API layer (*Input or *Request classes). Does NOT belong in Service or Controller. The caller (API layer) is responsible for passing clean, validated data down.
_Avoid_: Defensive validation in Service/Controller

### State detection (operation's responsibility)
Checks that are inherently part of executing an operation -- detecting whether the system is in state A or B to decide the execution path (e.g., "does this bridge exist?" to branch between create vs reconcile). Belongs in Service/Controller as part of the operation's logic.

### Invariant guard
A check in Service that protects against system damage (e.g., "are TAPs still attached?" before removing NAT rules). The one exception to "Service does not validate" -- these guard against partial-failure states, not invalid input.

### Speed-first principle
Every architectural decision is weighed against its runtime cost. Avoid redundant subprocess calls, unnecessary dataclass allocations, and deep call chains. A 10ms subprocess check that duplicates what the operation already detects is a bug.
_Avoid_: "Clean architecture" for its own sake

### Operation class
A collection of static methods in `api/{domain}_operations.py`. The ONLY place where multiple domains are imported and sequenced. Handles cross-domain orchestration, cross-domain data passing (e.g., querying VMRepository for a network's reference check, then passing results to NetworkService).
_Avoid_: Core domain files executing cross-domain logic

### Direct repository calls in the API layer
The API layer may call a Repository directly (e.g., `repo.count_by_status()`, `repo.find_by_network_id()`) without going through the Input/Request/Resolved pipeline when:
- The data is for enrichment or internal orchestration, not user-facing input processing.
- The call is inside an Operation method as part of a multi-step workflow (e.g., checking VM references before removing a network).

The rule: **user-facing input must go through Input/Request/Resolved.** Internal cross-domain data lookups can call Repositories directly from the Operation method, but the result must be passed to Core Service classes (not queried from within Core). Violation: any `from mvmctl.core.{domain_a} import {Repository}` inside `core/{domain_b}/`.

### Input/Request/Resolved triple (public-facing domains only)
Every domain with public-facing input must follow this three-class pattern in `api/inputs/`:

1. **`*Input`** -- Raw CLI or external input. Thin dataclass with typed fields. Optional fields are `None` -- no DB-backed defaults, no constants-backed defaults. The CLI layer resolves constants before creating this; the API layer resolves DB-backed defaults from `None` in the Request.
2. **`*Request(inputs, db)`** -- Accepts the Input and a `Database` instance. The `resolve()` method looks up DB-backed records for any `None` identifiers, resolves FK references, then calls `ensure_validate()` as its LAST step before returning a `Resolved*` object. The resolve-then-validate order guarantees validation operates on fully resolved data.
3. **`Resolved*`** -- Frozen (`@dataclass(frozen=True)`) dataclass. Every field is explicit and validated. No `None` for required fields. The list-of-entities field is named after the entity (e.g., `images`, `networks`, `kernels`) -- never `items`.

Mandatory for any domain that has public CLI commands or API endpoints. No shortcuts or skip-the-Request exceptions.

*Example: `VMInput` (raw name/id/IP/MAC) -> `VMRequest(db).resolve()` (resolves to DB records, validates) -> `ResolvedVMInput` (frozen, all fields explicit, `vms: list[VMInstanceItem]`).*

### SQLite schema overview
12 tables in `migrations/001_initial_schema.sql`: `images`, `kernels`, `binaries`, `volumes`, `networks`, `network_leases`, `vm_instances`, `host_state`, `host_state_changes`, `iptables_rules`, `ssh_keys`, `user_settings`. Each asset table has `is_default INTEGER` for default tracking. Foreign keys link VMs to assets and networks. Key constraints: `networks(name)` UNIQUE, `vm_instances(name)` UNIQUE, `ssh_keys(name)` UNIQUE, `volumes(name)` UNIQUE, `network_leases(network_id, ipv4)` UNIQUE composite. Foreign keys enabled via `PRAGMA foreign_keys = ON`.

**Portable reference fields** (used for export/import across environments -- never internal SHA256 IDs):
- Images: `(os_slug, arch)` -- unique identifier across environments
- Kernels: `(version, arch, type)` -- unique identifier across environments
- Binaries: `(name, version)` -- unique identifier across environments
- Networks: `name` -- unique identifier (subnet/gateway are hints for auto-recreation)

### Layer compliance enforcement
Architecture rules are enforced in CI via `tests/layer_compliance/`. Uses `ast.parse()` (not runtime imports) to scan source code:
- **`test_imports.py`**: CLI may only import from `api/`, `models/`, `exceptions`, `constants`, `utils/` -- NOT from `core/` directly.
- **`test_constants.py`**: No hardcoded paths, large integers, or list/dict literals outside `constants.py`.
- **`test_privilege.py`**: Specific API functions must call `check_privileges()` before delegating to core.
- **`test_startup_time.py`**: <200ms cold-start enforcement via subprocess spawn.

### Public API boundary
The `mvmctl.api` package IS the stable, curated public interface for all consumers -- CLI, future TUI/GUI, and external scripts. It lazily re-exports all Operation classes and Input types via `__init__.py`. External code should `from mvmctl.api import VMOperation, VMCreateInput` and nothing else. The `mvmctl.core` package is an implementation detail. The CLI layer is just one frontend -- it has no special access privileges.
_Avoid_: External consumers importing from `mvmctl.core` or `mvmctl.cli`

## Relationships

- An **Operation** class orchestrates across **Domains**
- A **Service** performs **intra-domain orchestration** using **Controller** and **Repository**
- A **Controller** manages state transitions for exactly one entity
- A **Repository** provides data access for its **Domain** only
- **Validation** runs in the API layer before data reaches **Service** or **Controller**
- **State detection** runs inside **Service** or **Controller** as part of execution
- **Invariant guards** may appear in **Service** when the guard prevents system damage

### Exception hierarchy
A 3-level hierarchy: `MVMError` (root) -> `{Domain}Error` (domain category) -> `{Domain}{Specific}Error` (specific issue). Every exception carries an optional `code` string for fine-grained programmatic branching in the API layer. The `code` enables auto-detection/auto-handling without parsing message text.

The `MVMError` base class has an optional `code: str | None` parameter:
```python
class MVMError(Exception):
    def __init__(self, message: str = "", *, code: str | None = None) -> None:
        self.code = code
        super().__init__(message)
```

### API result types
The API layer returns three types that the CLI/TUI/GUI consumes:
- **`OperationResult[T]`** -- Single operation result with `status` (success/error/warning), `code` (machine-readable), `message` (user-facing), `item` (payload), and optional `exception`.
- **`BatchResult[T]`** -- Collection of `OperationResult` items from bulk operations.
- **`NeedsInteraction`** -- Returned when the operation requires user action (e.g., sudo password prompt). The frontend checks for this type before treating the result as complete.

```
MVMError                                     # Root -- carries optional code string
├── MVMRuntimeError                          # Runtime assertion failure
├── ImageAcquireError                        # Image fetch/import failure (direct child)
├── VMError                                  # VM domain
│   ├── VMCreateError                        # VM creation failure (mid-rollback)
│   ├── VMStateError                         # Invalid state transition
│   ├── VMRequestError                       # Request resolution/validation failure
│   ├── VMBuilderError                       # VM builder failure (mid-rollback)
│   └── VMNotFoundError                      # VM not found in state
├── IPTablesTrackerError                     # IPTables action failure (direct MVMError child)
├── NetworkError                             # Network setup/teardown failure
├── ImageError                               # Image download/conversion failure
│   ├── ImageCompressionError                # Compression failure
│   ├── ImageDecompressionError              # Decompression failure
│   ├── ImageCorruptError                    # File appears corrupted
│   ├── ImageEmptyError                      # File is empty
│   ├── ImageValidationError                 # Format validation failure
│   └── ChecksumMismatchError                # SHA256 checksum mismatch
├── KernelError                              # Kernel build/config failure
├── FirecrackerError                         # Firecracker domain
│   ├── FirecrackerClientError               # Process/API failure
│   │   └── SocketNotFoundError              # Unix socket not found
│   ├── FirecrackerSpawnError                # Spawn failure
│   └── FirecrackerConfigError               # Config generation failure
├── ConfigError                              # Configuration loading failure
├── DatabaseError                            # Database operation failure
│   └── MigrationError                       # Migration version/filename failure
├── HostError                                # Host configuration failure
│   └── PrivilegeError                       # Insufficient privileges
├── ConsoleError                             # Console/PTY operation failure
├── LogsError                                # Log read/tail failure
├── ProcessError                             # Subprocess execution failure
├── AssetNotFoundError                       # Asset not found locally/remotely
├── BundledAssetError                        # Bundled package asset failure
│   └── BundledAssetNotFoundError            # Bundled file not found
├── BinaryError                              # Binary management failure
│   └── BinaryAlreadyExistsError             # Version already exists
├── SSHError                                 # SSH connection/config failure
├── MVMKeyError                              # SSH key management failure
│   ├── KeyExportError                       # SSH key export failure
│   ├── KeyDependencyError                   # ssh-keygen missing
│   └── KeyFileError                         # Key file read/write failure
├── CloudInitError                           # Cloud-init provisioning failure
│   ├── CloudInitProvisionError              # Invalid custom user data
│   ├── CloudInitModeError                   # Mode resolution failure
│   ├── CloudInitOffModeError                # OFF mode guestfs failure
│   ├── CloudInitIsoModeError                # ISO creation failure
│   ├── CloudInitNetModeError                # Nocloud-net server failure
│   └── CloudInitInjectModeError             # Rootfs injection failure
├── GuestfsError                             # libguestfs errors
│   ├── GuestfsNotAvailableError             # Python bindings not found
│   ├── GuestfsLaunchError                   # Appliance launch failure
│   ├── GuestfsMountError                    # Rootfs mount failure
│   ├── GuestfsWriteError                    # File write failure
│   └── GuestfsApplianceError                # Fixed appliance build failure
├── LoopMountError                           # Loop-mount provisioning errors
│   ├── LoopMountBinaryNotFoundError         # Binary not found
│   └── LoopMountTimeoutError                # Timeout
├── RootPartitionDetectionError              # Root partition detection failure
├── TieDetectedError                         # Multiple partition tie
├── DownloadError                            # Download failure
├── HttpDownloadError                        # HTTP download failure (direct child)
└── ... (ImageNotFoundError, BinaryNotFoundError, KernelNotFoundError,
         NetworkNotFoundError, KeyNotFoundError, VolumeNotFoundError,
         VolumeCreateError are direct children of MVMError
         for legacy compat)
```

Error message format (user-facing, three parts in one line):
```
What happened. Why it happened. Possible fix.
```

Error codes format: Dot-separated with domain prefix. Hierarchical and self-documenting.
```
network.subnet.overlap        # NetworkError
vm.create.binary_not_found    # VMCreateError (child of VMError; hyphen for multi-word)
host.init.sudoers_failed      # PrivilegeError (child of HostError)
```

### Error handling pattern
- **Service/Controller**: Raise typed exceptions with `logger.error()` before each raise. Use `code` parameter for programmatic distinction.
- **API layer**: Catch typed exceptions, branch on `isinstance()` or `e.code` for auto-handling, convert to `OperationResult`/`BatchResult` for CLI display.
- **Repository**: Let DB exceptions propagate. `_graceful_read` decorator handles DB availability at the boundary.
- No bare `except:`, no `except Exception` that swallows typed errors.

### Logging pattern
- **Log before raise**: Every `raise` in Service/Controller has a preceding `logger.error()` or `logger.warning()` with operational context (parameters, state, failure reason).
- **Log message**: Operator-facing -- includes module context, parameter values, and the root cause.
- **Exception message**: User-facing -- "what happened. why. possible fix." short summary.
- **API layer**: `logger.info()` for success, `logger.warning()` for recoverable issues.

### Coding style
- **Method length**: No hard limit. 50+ lines is fine if logic is linear and clear.
- **Private helpers**: Only for reused logic or genuinely complex operations (not for single-use trivial extraction).
- **Early returns**: Prefer early returns over nested if/else branching.
- **Explicit typing**: All function signatures must have explicit types. No `Any`, no implicit `Optional`. Use `from __future__ import annotations`.
- **`from __future__ import annotations`**: Required as the first import (after file docstring) in EVERY `.py` file under `src/mvmctl/`. Enables PEP 563 postponed evaluation so forward references work without string quotes.
- **No quoted annotations**: With `from __future__ import annotations`, write types directly: `def get(x: str) -> VMInstanceItem | None` -- no quotes around annotations.
- **Centralized infrastructure**: Subprocess calls, file I/O, and system interactions must go through shared utility functions (e.g., `NetworkUtils._run_batch()`, `require_mvm_group_membership()`). Every tool invocation must have one canonical path.
- **Docstrings**: Public classes get 1-3 lines. Public methods get docstrings only when behavior is non-obvious. Private methods get no docstrings -- name explains it. Inline comments only for WHY, never for WHAT. The code should be self-explanatory; comments justify counter-intuitive choices.

### Lazy imports
ALL `__init__.py` files use PEP 562 `__getattr__` + `resolve_lazy()` from `mvmctl.utils._lazy_import`. Eager imports at package level are forbidden. When any module does `from mvmctl.core.vm import VMController`, only `_controller.py` is loaded -- not the entire domain.

Pattern:
```python
from __future__ import annotations
from mvmctl.utils._lazy_import import resolve_lazy

__all__ = ["ExportedClass1", ...]

_LAZY_MAP: dict[str, tuple[str, str]] = {
    "ExportedClass1": ("path._submodule", "ExportedClass1"),
}

def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)

def __dir__() -> list[str]:
    return __all__
```

### Subprocess invocation
ONE canonical path for all subprocess calls: `run_cmd()` / `stream_cmd()` in `mvmctl.utils._system`. No raw `subprocess.run()` except in these utility functions.
```python
# Correct -- everything routes through the centralized runner
from mvmctl.utils._system import run_cmd
result = run_cmd(["ip", "link", "set", tap, "down"], privileged=True)

# Forbidden -- raw subprocess.run scattered across modules
subprocess.run(["iptables", ...], check=True)
```

The centralized runner provides: consistent logging (`logger.debug` of the command), privilege escalation via `sudo` prepend (when `privileged=True`), timeout enforcement, and uniform error formatting.

### Import conventions
| Layer | Imports from | Example |
|-------|-------------|---------|
| **CLI** | `mvmctl.api` (public surface) | `from mvmctl.api import VMOperation, VMCreateInput` |
| **API** | `mvmctl.api.inputs` (public input surface) | `from mvmctl.api.inputs import VMCreateInput` |
| **API** | `mvmctl.core.{domain}` (public domain surface) | `from mvmctl.core.vm import VMController` |
| **API** | `mvmctl.core._shared` (public infrastructure) | `from mvmctl.core._shared import Database` |
| **API** | `mvmctl.utils.*` (shared helpers) | `from mvmctl.utils._system import run_cmd` |
| **Core** | `mvmctl.core._shared` only | `from mvmctl.core._shared._db import Database` |
| **Core** | Own sibling modules | `from mvmctl.core.vm._firecracker import FirecrackerClient` |
| **Utils** | Nothing from core/api/cli | N/A -- leaf nodes |

**Convention (with exceptions):** Prefer importing from the `__init__.py` re-export (`from mvmctl.core.vm import VMController`) rather than the private module (`from mvmctl.core.vm._controller import VMController`). However, API-layer code (`api/`) may import directly from private core modules when the number of imports from a single domain makes the public surface verbose (e.g., `from mvmctl.core.vm._repository import VMRepository`). CLI layer should always go through `mvmctl.api`.

## Flagged ambiguities

- "Orchestration" was used to mean both intra-domain and cross-domain sequencing -- resolved: intra-domain orchestration lives in Service (core/), cross-domain orchestration lives in Operation (api/). These are different concepts.
- "Validation" was overloaded to include format checks, existence checks, system state checks, and invariant guards -- resolved: format and existence checks are **validation** (caller's job), system state detection is part of the operation (Service's job), and invariant protection against damage is a narrow exception.
- "Validation in Service" was proposed as a norm -- resolved: rejected. Caller validates, receiver trusts. The Service's role is execution, not gatekeeping.
- "Controller" was being used for CRUD operations (remove, create) -- resolved: Controller is state management only. CRUD orchestration belongs in Service or Operation.
- "Multiple VMM backends" was considered (Firecracker + Cloud Hypervisor + QEMU) -- resolved: Firecracker-only for v0.1. The entire VM lifecycle is Firecracker-shaped (HTTP API over UDS, JSON config schema, vsock console relay). Adding backends would require 3-5x effort per supported VMM and is deferred until a concrete second VMM is needed with a committed timeline. The existing `api/` layer boundary is the right future seam for VMM abstraction.
- "System test assertion depth" was ambiguous (returncode? stdout? JSON?) -- resolved: **Option C** verification. Every system test must verify actual system state at the deepest practical level: JSON field assertions, filesystem checks (`os.path.exists`, `Path.readlink`, `Path.stat`), process checks (`/proc/$PID`), iptables checks (`sudo iptables -L`), and/or direct SQLite queries on `~/.cache/mvmctl/mvmdb.db`. Returncode-only assertions are explicitly forbidden in system tests. A test that does not verify business outcomes is incomplete.
- "Which tests can be shallow" was ambiguous -- resolved: **none**. All returncode-only tests must be upgraded to Option C before release. If a test only checks `result.returncode == 0` without verifying system state, it is incomplete and must be fixed.
- "What level of CLI coverage is required" was ambiguous -- resolved: **gap matrix must be zero**. Every CLI subcommand and every flag on every command must have a system test covering both happy path and error edge cases. Any untested command or flag is a blocking release risk.
- "Who owns tests/" was ambiguous -- resolved: **QA engineer agent** is the sole owner and operator of `tests/`. The engineer agent is strictly forbidden from touching any file under `tests/` at any cost (read, write, edit, create, delete, rename, patch). The only legitimate interaction with tests/ from the engineer agent is running `uv run pytest` when explicitly asked.
- "Where does context live" -- resolved: **Single root AGENTS.md** is the only AGENTS.md in the project. Per-folder AGENTS.md files are deleted -- they caused agents to skip the root file and miss CONTEXT.md + ADRs. All domain language lives in `CONTEXT.md`. All hard-to-reverse architectural decisions live in `docs/adr/`. The root AGENTS.md is a short pointer to these two files.
- "What about expensive/infrequent tests" -- resolved: two **exclusive markers** added. `pytest.mark.kernel_build` for kernel build-from-source tests (10+ min, needs gcc/make). `pytest.mark.host_reset` for host clean/reset with sudo (modifies real system state). Both excluded from default `pytest tests/system/` runs. Invoke explicitly with `-m kernel_build` or `-m host_reset`.
- "What about parallel test safety" -- resolved: every test that modifies shared state (defaults, cache, assets, binaries, kernels) must be marked `pytest.mark.serial` to prevent xdist race conditions.
- "How do system tests handle sudo operations" -- resolved: `sudo` is allowed for `mvm init`, `mvm host init`, `mvm host clean`, and `mvm host reset`. Use the built binary at `~/.local/bin/mvm` for sudo operations. The QA engineer agent has explicit sudo permission for these four command patterns.
- "What is the release gate" -- resolved: the release gate is **system tests passing against `dist/mvm`**. Before reporting release ready, the QA engineer must: (1) build `dist/mvm` via `scripts/build_services.py --fast`, (2) copy to `~/.local/bin/mvm`, (3) run all system tests against the binary, (4) report pass/fail status.

## Test types (three-layer test pyramid)

Three test types exist to cover different guarantees with appropriate trade-offs between speed, isolation, and fidelity:

### Unit test
Mocks everything (subprocess, DB, filesystem, iptables, sudo cache, config paths). Fastest execution (~ms per test). Tests one class or function in isolation. Runs in `tests/unit/`. Uses autouse fixtures from root `conftest.py` that patch all external dependencies. Coverage goal: 80%+ branch coverage across all of `src/mvmctl/`.

### Integration test
Mocks subprocess calls only (via `SmartSubprocessMock` in integration conftest). Runs against a real SQLite database, real filesystem paths, real class orchestration. Tests that multiple classes work together correctly. Slower than unit (~100ms per test). Runs in `tests/integration/`. The `SmartSubprocessMock` handles cp, dd, ip, iptables, genisoimage, losetup, mount, umount, and other system commands with realistic return values -- it creates fake files, tracks created bridges/TAPs, and simulates command output. Adding a new command handler requires adding a `if cmd == "mycommand":` branch to the mock.

### System test
Black-box CLI subprocess tests (no mocking, no imports from `mvmctl`). Operates against the real system -- real kernel, images, binaries, bridges, SQLite DB at `~/.cache/mvmctl/mvmdb.db`. Verifies actual business outcomes at the OS level: JSON state, filesystem state, process state, iptables state. Runs in `tests/system/`. The primary release gate -- a domain is NOT production-ready until its system tests pass on real hardware.

**Execution strategy -- MUST run per-file, never as a single batch:**
System tests are expensive and stateful. Running `pytest tests/system/` as a single invocation is UNDEFINED behavior -- the `prepare_system_env` session fixture pulls large assets (images, kernels, binaries) and the tests leave real system state (bridges, iptables rules, VMs) that pollutes subsequent test files. Instead, run each file individually:

```bash
pytest tests/system/test_network.py -n 0
pytest tests/system/test_vm_lifecycle.py -n 0
# etc.
```

Running individual test classes (not full files) within a file is also safe, provided the class is self-contained with its own fixture setup/teardown. Cross-file test ordering dependencies MUST NOT be assumed -- each file must be independently runnable.

**Sudo requirement:**
System tests that create VMs, build kernels, or modify host state require passwordless sudo via mvm group membership (set up by `mvm host init`). The mvm application handles privilege escalation internally through `run_cmd()` (with `privileged=True`) -- tests NEVER call `sudo` directly. The conftest's `_verify_system_test_iptables` fixture was removed because it bypassed this pattern. Tests that don't need privileged operations (bin, config, cache, keys, logs, network, ssh, etc.) run fine without passwordless sudo.

### Autouse fixture isolation rule (all test types)
Root `conftest.py` defines autouse fixtures that isolate each test from shared state: `_mock_sudo_cache`, `_isolate_iptables_rules`, `_setup_database`, `isolate_config_and_cache`, `_mock_privilege_checks`, `_block_real_sudo_invocations`. Every one of these fixtures includes `if request.node.get_closest_marker("system"): return` to skip isolation for system tests (which intentionally run against real state). This guard is repeated per fixture -- centralizing it is not worth the indirection. System tests opt out of isolation because they run against real infrastructure.

### Mandatory cleanup pattern (all test types)
Every test that creates a resource MUST clean it up in a `finally` block or via a generator fixture with cleanup in the final yield. The pattern:

```python
# Generator fixture pattern (preferred for reuse):
@pytest.fixture
def created_vm(...):
    vm = _create_vm(...)
    yield vm
    _remove_vm(vm, check=False)

# try/finally pattern (for inline usage):
try:
    result = mvm("create", ...)
    yield  # or assert
finally:
    mvm("remove", ..., check=False)
```

Without cleanup, subsequent tests may find stale state, and parallel (xdist) runs will race on shared resources. This is mandatory for ALL test types -- unit, integration, and system.

## System Tests

### Option C verification
The thoroughness standard for system test assertions. Every system test verifies
system state at the deepest practical level: JSON field assertions from `* ls --json`,
file existence/symlink checks via `os.path`, process presence via `/proc/$PID`,
iptables rule presence via `sudo iptables -L`, and/or direct SQLite queries on
`~/.cache/mvmctl/mvmdb.db`. A test that only checks `returncode == 0` is incomplete.
Named "Option C" to distinguish from minimal returncode-only (A) and moderate JSON-only (B).

### Gap matrix
A cross-reference of every CLI subcommand and flag against its system test coverage.
Built by reading every file in `src/mvmctl/cli/` and every file in `tests/system/`.
All gaps must be filled -- any untested command or flag is a blocking release risk.

### Edge case categories (8 categories)
For every CLI flag, check all eight: happy path (with state verify), missing required
args, invalid values, boundary values, JSON output format, confirmation prompts,
non-existent resources, duplicate creation.

### Marker
A `pytest.mark.*` annotation on a test class or function. System test markers include:
`system` (always), `domain_<name>` (file-level filter), `slow` (>30s), `serial`
(modifies shared state -- prevent xdist races), `requires_kvm` (needs /dev/kvm),
`requires_network` (needs real bridges), `kernel_build` (build from source, excluded
from default run), `host_reset` (host clean/reset with sudo, excluded from default run).

### Serial test
A test marked `pytest.mark.serial` because it modifies shared system state (default
image, default network, cached binaries, kernel defaults). Serial tests must not run
in parallel with each other to prevent race conditions on shared resources. Every test
that changes defaults, removes assets, or modifies global state must be serial.

### Non-destructive test
A test that does not modify persistent state -- it reads JSON, inspects resources,
lists records. Non-destructive tests run FIRST in every file, before any destructive
test that might remove or alter the resources they depend on.

### Destructive test
A test that modifies persistent state -- removes a resource, changes a default, prunes
cache, cleans host. Destructive tests MUST be defined at the END of their file, after
all non-destructive tests. Every destructive test must restore any removed state
(re-pull image, recreate network) in a `finally` block.

### Kernel build marker (`pytest.mark.kernel_build`)
A pytest marker that designates tests requiring kernel compilation from source
(`kernel pull --type official` with build flags like `--jobs`, `--keep-build-dir`,
`--clean-build`, `--config`). These tests require a full build toolchain (gcc, make,
bc, bison, flex) and take 10+ minutes. EXCLUDED from default system test runs.
Invoke explicitly: `pytest -m kernel_build`.

### Host reset marker (`pytest.mark.host_reset`)
A pytest marker that designates tests executing `host clean` or `host reset` with
sudo, which modifies real system state (bridges, iptables, sysctl, group memberships).
EXCLUDED from default system test runs. Invoke explicitly: `pytest -m host_reset`.

### Tautological test
A test that verifies something trivially true by construction, proving nothing about
the system. Examples: asserting CREATE output contains the name you just passed,
checking `--help` contains "Usage:", asserting `returncode == 0` without verifying
the downstream effect. Forbidden in system tests.
