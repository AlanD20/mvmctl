# Project Architecture

## Overview

Three-layer architecture with strict import boundaries: **CLI → API → Core**.

```
src/mvmctl/
├── api/              # Public interface + ORCHESTRATION (imports multiple domains)
├── core/             # All business logic (isolated domains + shared infrastructure)
├── cli/              # Frontend (Click/Typer commands)
├── services/         # Long-running subprocess services
├── db/               # SQLite schema, migrations, ORM models
├── assets/           # Bundled YAML configs and templates
├── models/           # Pure @dataclass objects
├── utils/            # Shared helpers
└── py.typed          # PEP 561 marker — declares the package supports strict typing
```

**Key Principle:** Domains are **business capabilities**, not CLI commands. A single CLI command (like `mvm vm create`) often orchestrates multiple domains.

**Orchestration lives in `api/`, NOT in `core/`.** The API layer is the ONLY entity that imports multiple domains and sequences them together.

## Table of Contents

- [Overview](#overview)
- [Layer Responsibilities](#layer-responsibilities)
- [File Structure](#file-structure)
- [Core Structure — Domain Files](#core-structure--domain-files)
  - [Standard Domain Pattern (4 Files)](#standard-domain-pattern-4-files)
  - [Controller (Stateful)](#controller-stateful)
  - [Service (Stateless / Bulk)](#service-stateless--bulk)
  - [Repository](#repository)
  - [Resolver](#resolver)
- [API Layer — Orchestration](#api-layer--orchestration)
  - [Operation Classes](#operation-classes)
  - [Public API Surface](#public-api-surface)
- [API Data Flow — Input → Request → Resolved](#api-data-flow--input--request--resolved)
  - [The Three Types](#the-three-types)
  - [VM Creation Flow (Complex — Many Parameters)](#vm-creation-flow-complex--many-parameters)
  - [VM Operations Flow (Simple — References Existing VM)](#vm-operations-flow-simple--references-existing-vm)
  - [Default Resolution (API Layer)](#default-resolution-api-layer)
- [Domain ≠ CLI Command](#domain--cli-command)
- [File Placement Rules](#file-placement-rules)
  - [2. Orchestration (Multiple Domains)](#2-orchestration-multiple-domains)
  - [3. Infrastructure Placement](#3-infrastructure-placement)
- [Import Boundaries (Enforced)](#import-boundaries-enforced)
- [Dependency Direction](#dependency-direction)
- [Naming Conventions](#naming-conventions)
  - [Model Naming — `*Item` Suffix](#model-naming--item-suffix)
- [Relation Enrichment System](#relation-enrichment-system)
  - [Design Principles](#design-principles)
  - [How It Works](#how-it-works)
  - [Complete Relation Graph](#complete-relation-graph)
  - [Model Design — Optional Relation Fields](#model-design--optional-relation-fields)
  - [Batch Loading — How N+1 is Prevented](#batch-loading--how-n1-is-prevented)
  - [Nested Relations — Parent Auto-Resolution](#nested-relations--parent-auto-resolution)
  - [Lazy Resolver Registry](#lazy-resolver-registry)
  - [Usage](#usage)
  - [Adding New Relations](#adding-new-relations)
- [Repository Pattern — SQL-Level Computation](#repository-pattern--sql-level-computation)
- [Domain Growth Patterns](#domain-growth-patterns)
  - [Adding New Capabilities to a Domain](#adding-new-capabilities-to-a-domain)
  - [Extracting Subsystems](#extracting-subsystems)
  - [When to Create a New Domain](#when-to-create-a-new-domain)
- [Summary](#summary)

## Layer Responsibilities

| Layer | Purpose | Import Rules |
|-------|---------|--------------|
| **CLI** | Argument parsing, output formatting | `mvmctl.api` only |
| **API** | Public contract curation, DB resolution, **ORCHESTRATION** | `core/*` only. **ONLY layer that imports multiple domains.** |
| **Core** | Business logic, domain isolation | `core/_shared/` for infrastructure. **NO cross-domain imports.** |

## File Structure

```
src/mvmctl/
├── api/                                    # Public Python API surface
│   ├── __init__.py                         # Re-exports all Operation + Input classes
│   ├── vm_operations.py                    # VMOperation — VM lifecycle orchestration
│   ├── network_operations.py               # NetworkOperation
│   ├── volume_operations.py                # VolumeOperation — persistent storage management
│   ├── host_operations.py                  # HostOperation
│   ├── image_operations.py                 # ImageOperation
│   ├── kernel_operations.py                # KernelOperation
│   ├── key_operations.py                   # KeyOperation
│   ├── binary_operations.py                # BinaryOperation
│   ├── cache_operations.py                 # CacheOperation
│   ├── config_operations.py                # ConfigOperation
│   ├── console_operations.py               # ConsoleOperation
│   ├── cp_operations.py                    # CPOperation — file copy via tar-over-SSH
│   ├── init_operations.py                  # InitOperation
│   ├── logs_operations.py                  # LogOperation
│   ├── ssh_operations.py                   # SSHOperation
│   └── inputs/                             # Input/Request/Resolved classes
│       ├── __init__.py
│       ├── _vm_create_input.py             # VMCreateInput → VMCreateRequest → ResolvedVMCreateInput
│       ├── _vm_input.py                    # VMInput → VMRequest → ResolvedVMInput
│       ├── _vm_import_input.py
│       ├── _vm_export_config.py
│       ├── _network_create_input.py
│       ├── _network_input.py
│       ├── _image_input.py
│       ├── _image_acquire_input.py
│       ├── _kernel_import_input.py       # KernelImportInput → KernelImportRequest → ResolvedKernelImportInput
│       ├── _kernel_input.py
│       ├── _kernel_pull_input.py
│       ├── _key_input.py
│       ├── _key_create_input.py
│       ├── _binary_input.py
│       ├── _binary_pull_input.py
│       ├── _ssh_input.py
│       ├── _config_input.py
│       ├── _console_input.py
│       ├── _logs_input.py
│       ├── _volume_input.py               # VolumeInput → VolumeRequest → ResolvedVolumeInput
│       └── _volume_create_input.py        # VolumeCreateInput → VolumeCreateRequest → ResolvedVolumeCreateInput
│
├── core/                                    # Isolated domain logic
│   ├── vm/                                  # VM lifecycle
│   │   ├── _controller.py                   # VMController (stateful — per-VM operations)
│   │   ├── _service.py                      # VMService (stateless — bulk operations)
│   │   ├── _repository.py                   # VMRepository (database operations)
│   │   ├── _resolver.py                     # VMResolver (entity resolution)
│   │   ├── _provisioner.py                  # VMProvisioner (rootfs provisioning via backend selection)
│   │   └── _firecracker.py                  # FirecrackerSpawner, FirecrackerClient
│   ├── network/                             # Networking (bridge, tap, NAT, IP lease)
│   │   ├── _controller.py                   # NetworkController
│   │   ├── _service.py                      # NetworkService
│   │   ├── _lease_service.py                # LeaseService
│   │   ├── _lease_resolver.py               # NetworkLeaseResolver
│   │   ├── _repository.py                   # NetworkRepository + LeaseRepository
│   │   └── _resolver.py                     # NetworkResolver
│   ├── image/                               # OS images
│   │   ├── _controller.py                   # ImageController
│   │   ├── _provisioner.py                  # ImageProvisioner (image optimization via backends)
│   │   ├── _service.py                      # ImageService
│   │   ├── _repository.py                   # ImageRepository
│   │   ├── _resolver.py                     # ImageResolver
│   │   └── _version_resolver.py             # HttpDirVersionResolver wrapper (image version listings)
│   ├── kernel/                              # Kernel images
│   │   ├── _controller.py                   # KernelController
│   │   ├── _service.py                      # KernelService
│   │   ├── _repository.py                   # KernelRepository
│   │   └── _resolver.py                     # KernelResolver
│   ├── binary/                              # Firecracker binaries
│   │   ├── _controller.py                   # BinaryController
│   │   ├── _service.py                      # BinaryService
│   │   ├── _repository.py                   # BinaryRepository
│   │   └── _resolver.py                     # BinaryResolver
│   ├── key/                                 # SSH keys
│   │   ├── _controller.py                   # KeyController
│   │   ├── _service.py                      # KeyService
│   │   ├── _repository.py                   # KeyRepository
│   │   └── _resolver.py                     # KeyResolver
│   ├── host/                                # Host-level operations (init, reset, prune)
│   │   ├── _controller.py                   # HostController
│   │   ├── _service.py                      # HostService
│   │   ├── _repository.py                   # HostRepository
│   │   ├── _detector.py                     # HostDetector (host capability detection)
│   │   ├── _helper.py                       # HostPrivilegeHelper
│   │   └── _probe.py                        # HostProbe (pre-flight checks for host readiness)
│   ├── cache/                               # Cache management
│   │   └── _service.py
│   ├── config/                              # Configuration management
│   │   ├── _constraints.py                  # ConstraintRegistry (cross-key validation)
│   │   ├── _repository.py                   # SettingsRepository (DB CRUD)
│   │   └── _service.py                      # SettingsService (validation + type coercion)
│   ├── console/                             # Console relay
│   │   ├── __init__.py
│   │   └── _controller.py                   # ConsoleController (stateful — console relay management)
│   ├── logs/                                # Log management
│   │   ├── __init__.py
│   │   ├── _controller.py                   # LogController (stateful — bound to VM entity)
│   │   └── _service.py                      # LogService (stateless log file operations)
│   ├── cloudinit/                           # Cloud-init provisioning
│   │   ├── __init__.py
│   │   ├── _manager.py                      # CloudInitManager (orchestration state tracker)
│   │   └── _provisioner.py                  # CloudInitProvisioner
│   ├── volume/                              # Persistent storage volumes
│   │   ├── _controller.py                   # VolumeController (stateful — per-volume attach/detach)
│   │   ├── _service.py                      # VolumeService (disk creation, removal, resize, inspect)
│   │   ├── _repository.py                   # VolumeRepository (database operations)
│   │   └── _resolver.py                     # VolumeResolver (entity resolution)
│   ├── ssh/                                 # SSH operations
│   │   ├── __init__.py
│   │   ├── _cp.py                           # CP-related SSH operations (file copy)
│   │   └── _service.py                      # SSHService (stateful — stores connection params as instance state)
│   └── _shared/                           # Shared infrastructure
│       ├── __init__.py
│       ├── _db.py                           # Database (connection manager)
│       ├── _enrichment.py                   # RelationEnricher (batch relation loading)
│       ├── _http_dir_version_resolver.py    # HTTP directory version listing (image + kernel)
│       ├── _version_resolver.py             # Shared version resolution (semver parsing, spec resolution)
│       ├── _resolver_registry.py            # Lazy resolver registry (prevents circular imports)
│       ├── _asset_manager.py                # Generic asset management
│       ├── _parallel.py                     # ParallelExecutor
│       ├── _guestfs/                        # Guestfs filesystem provisioning utilities
│       │   ├── __init__.py
│       │   ├── _base.py                     # OptimizedGuestfs — low-level guestfs wrapper
│       │   ├── _kernel_detector.py
│       │   ├── _provisioner.py              # GuestfsProvisioner — all provisioning operations
│       │   └── _service.py                  # GuestfsService — appliance management
│       ├── _loopmount/                      # Loop-mount provisioning (mvm-provision binary interface)
│       │   ├── __init__.py
│       │   ├── _manager.py
│       │   └── _provisioner.py
│       ├── _provisioner/                    # Provisioner backend abstraction (factory + backends)
│       │   ├── __init__.py
│       │   ├── _backend.py
│       │   └── _content.py
│       ├── _firewall_tracker.py             # Unified firewall tracker — delegates to iptables/nftables backend
│       ├── _iptables_tracker/               # Generic iptables rule tracking
│       │   ├── __init__.py
│       │   ├── _repository.py
│       │   ├── _resolver.py
│       │   └── _tracker.py
│       └── _nftables_tracker/               # nftables rule tracking (default firewall backend)
│           ├── __init__.py
│           ├── _repository.py
│           ├── _resolver.py
│           └── _tracker.py
│
├── cli/                                     # Thin Click/Typer command definitions
│   ├── vm.py
│   ├── network.py
│   ├── image.py
│   ├── kernel.py
│   ├── key.py
│   ├── host.py
│   ├── bin.py
│   ├── cache.py
│   ├── config.py
│   ├── console.py
│   ├── cp.py
│   ├── init.py
│   ├── logs.py
│   ├── ssh.py
│   ├── volume.py
│   └── _completion.py                       # Shell completion helpers (11 functions)
│
│   **CLI Aliases:** Several commands have short aliases defined in `_COMMAND_SPECS`:
│   `net`/`network`, `img`/`image`, `vol`/`volume`.
│
├── models/                                  # Pure @dataclass objects
│   ├── __init__.py                          # Re-exports all model types
│   ├── binary.py                            # BinaryItem
│   ├── bulk.py                              # BulkResult, BulkResultItem
│   ├── cache.py                             # PruneAllResult, CleanResult
│   ├── cloudinit.py                         # CloudInitMode, CloudInitStatus
│   ├── firecracker.py                       # FirecrackerConfig, DriveConfig
│   ├── host.py                              # HostStateItem, HostStateChangeItem
│   ├── image.py                             # ImageItem, ImageSpec
│   ├── kernel.py                            # KernelItem, KernelSpec, KernelPullResult
│   ├── key.py                               # SSHKeyItem
│   ├── network.py                           # NetworkItem, NetworkLeaseItem, FirewallRule, FirewallChain, FirewallPort, FirewallProtocol, FirewallRuleType, FirewallTable, FirewallTarget, FirewallWildcard, FirewallBackendType, FirewallRuleResult
│   ├── provisioner.py                       # ProvisionerType
│   ├── result.py                            # OperationResult, BatchResult, ProgressEvent, NeedsInteraction, OperationStatus
│   ├── vm.py                                # VMInstanceItem, VMInspectInfo, ConsoleInfo, ConsoleState, VMStatus
│   └── volume.py                            # VolumeItem, VolumeStatus
│
├── services/                                # Long-running subprocess services
│   ├── console_relay/                       # Console relay service
│   │   ├── __init__.py
│   │   ├── _defaults.py
│   │   ├── exceptions.py
│   │   ├── client.py
│   │   ├── manager.py
│   │   ├── process.py
│   │   └── README.md
│   ├── loopmount/                           # Loop-mount binary (mvm-provision entry point)
│   │   ├── __init__.py
│   │   └── process.py                       # Standalone mvm-provision binary entry point
│   └── nocloud_server/                      # NoCloud server service
│       ├── __init__.py
│       ├── _defaults.py
│       ├── exceptions.py
│       ├── manager.py
│       └── process.py
│
├── db/                                      # SQLite schema, migrations, ORM models
│   ├── __init__.py
│   └── migrations/
│       ├── __init__.py
│       └── 001_initial_schema.sql
│
├── assets/                                  # Bundled YAML configs and templates
│   ├── __init__.py
│   ├── cloud-init.template.yaml
│   ├── firecracker.template.json
│   ├── images.yaml
│   └── kernels.yaml
│
├── utils/                                   # Shared helpers (pure, no domain knowledge)
│   ├── __init__.py
│   ├── _disk.py
│   ├── _io.py
│   ├── _lazy_import.py
│   ├── _system.py
│   ├── _validators.py
│   ├── auditlog.py
│   ├── cli.py
│   ├── common.py
│   ├── crypto.py
│   ├── fs.py
│   ├── http.py
│   ├── network.py
│   ├── operation_utils.py
│   ├── progress.py
│   ├── template.py
│   ├── timinglog.py
│   ├── version.py
│   └── yaml.py
│
└── py.typed                                 # PEP 561 marker — declares the package supports strict typing
```

**Key context sources:** Read `CONTEXT.md` first for domain language, conventions, and architecture rules. Architecture Decision Records in `docs/adr/` document hard-to-reverse decisions with real trade-offs.

Agent instructions live in `.opencode/agent/*.md`.

## Core Structure — Domain Files

### Standard Domain Pattern (4 Files)

Each domain follows a consistent pattern with four files representing different concerns:

> **Note:** 7 of the 14 domains break this pattern:
> - **host** — has no resolver (uses helper instead)
> - **cache** — service-only (no controller, repository, or resolver)
> - **config** — has `_constraints.py` instead of a controller
> - **console** — controller-only (no service, repository, or resolver)
> - **logs** — has controller + service (no repository or resolver)
> - **cloudinit** — uses `_manager.py` + `_provisioner.py` instead of the 4-file pattern
> - **ssh** — service + cp (no controller, repository, or resolver; file copy operations via `_cp.py`)
>
> Note: guestfs is not a domain; it lives in `core/_shared/_guestfs/` as shared infrastructure.

```
core/{domain}/
├── _controller.py     # Stateful — bound to a specific entity instance
├── _service.py         # Stateless — setup, teardown, bulk operations
├── _repository.py      # Database CRUD (queries, counts, upsert, delete)
└── _resolver.py        # Entity resolution (name/ID/IP → entity)
```

| Concern | Class | Purpose | Example |
|---------|-------|---------|---------|
| **Stateful entity manager** | `Controller` | Bound to specific instance (`self._entity`), lifecycle operations | `VMController(entity=vm, repo).stop()` |
| **Stateless operations** | `Service` | Setup/teardown, bulk operations | `VMService(db).stop_many(vms)` |
| **Database operations** | `Repository` | ALL data access — get, list, count, upsert, delete. SQL-level ops. | `VMRepository(db).list_by_status(status)` |
| **Entity resolution** | `Resolver` | Resolve identifiers to domain objects | `VMResolver(repo).resolve_many(["vm1", "vm2"])` |

**Note:** Some domains have additional files (e.g., `vm/_firecracker.py`, `vm/_provisioner.py`, `network/_lease_service.py`, `host/_helper.py`) for domain-specific subsystems. The 4-file pattern is the default minimum.

### Controller (Stateful)

A Controller is instantiated with a specific entity and operates against it:

```python
# core/vm/_controller.py
class VMController:
    def __init__(self, entity: str | VMInstanceItem, repo: VMRepository) -> None:
        # Resolves entity in __init__, caches on self._vm

    def stop(self, force: bool = False) -> None: ...
    def start(self) -> None: ...
    def pause(self) -> None: ...
    def resume(self) -> None: ...
    def reboot(self, force: bool = False) -> None: ...
    def snapshot(self, mem_out: Path, state_out: Path) -> None: ...
    def load_snapshot(self, mem_in: Path, state_in: Path, resume_after: bool = False) -> None: ...
```

**Note:** While `VMController` still provides `start()`, `stop()`, and `pause()` for individual VM operations, bulk lifecycle operations (e.g., stopping multiple VMs) are handled by `VMService` in the API layer. The `VMService.stop_many()` creates per-VM `VMController` instances internally via `ParallelExecutor`.

### Service (Stateless / Bulk)

A Service handles stateless operations — often coordinating multiple Controller instances:

```python
# core/vm/_service.py
class VMService:
    def __init__(self, repo: VMRepository) -> None:
        self._repo = repo

    def stop(self, vm: VMInstanceItem, force: bool = False) -> None:
        controller = VMController(entity=vm, repo=self._repo)
        controller.stop(force=force)

    def stop_many(self, vms: list[VMInstanceItem], force: bool = False, ...) -> BulkResult:
        # Uses ParallelExecutor to run stop() across VMs
```

### Repository

The Repository owns ALL database access for its domain. Single-file, no separate Inventory/Query classes:

```python
# core/vm/_repository.py
class VMRepository:
    def __init__(self, db: Database | None = None) -> None: ...

    def get(self, id: str) -> VMInstanceItem | None: ...
    def get_by_name(self, name: str) -> VMInstanceItem | None: ...
    def list_all(self) -> list[VMInstanceItem]: ...
    def list_by_status(self, status: VMStatus | list[VMStatus]) -> list[VMInstanceItem]: ...
    def count(self) -> int: ...
    def count_by_status(self, status: VMStatus | list[VMStatus]) -> int: ...
    def upsert(self, entity: VMInstanceItem) -> None: ...
    def delete(self, id: str) -> None: ...
```

**Repository Responsibilities:**
- Database CRUD operations for domain entities
- Query methods (get_by_id, get_by_name, list_all, find_by_prefix)
- Aggregate queries (count, count_by_*) — use SQL `COUNT`, not fetch-all + `len()`
- Flexible filtering — Methods accept single value or list: `value | list[value]` with SQL `WHERE IN`
- Atomic transactions for multi-row operations
- **NO business logic** — repositories only move data between domain objects and database

**Repository Pattern Rules:**
1. **SQL-level computation** — Use `SELECT COUNT(*)`, `WHERE column IN (...)` instead of fetching all rows and filtering in Python
2. **No separate Inventory/Query classes** — All queries belong in Repository
3. **Flexible query parameters** — Methods accept both single value and list for filtering: `status: Status | list[Status]`
4. **Domain owns its data** — Each domain controls how its entities are persisted

### Resolver

Resolvers handle entity lookup by identifier (name, ID, IP, MAC):

```python
# core/vm/_resolver.py
class VMResolver:
    RELATIONS: dict[str, RelationSpec] = { ... }

    def __init__(self, repo: VMRepository, *, include: list[str] | None = None) -> None: ...
    def by_id(self, vm_id: str) -> VMInstanceItem: ...
    def resolve(self, identifier: str) -> VMInstanceItem: ...
    def resolve_many(self, identifiers: list[str]) -> VMResolveResult: ...
    def get_default(self) -> VMInstanceItem | None: ...
```

## API Layer — Orchestration

Cross-domain orchestration lives in `api/*_operations.py`. Each operation class groups actions
for a domain (e.g., VMOperation has create, remove, start, stop, etc.).

### Operation Classes

```python
# api/vm_operations.py
class VMOperation:
    @staticmethod
    def create(inputs: VMCreateInput) -> OperationResult[list[VMInstanceItem]] | NeedsInteraction:
        """Creates a VM — orchestrates vm, network, image, kernel, binary, cloudinit."""

    @staticmethod
    def remove(inputs: VMInput) -> BatchResult[VMInstanceItem]:
        """Removes one or more VMs."""

    @staticmethod
    def list_all(status: VMStatus | list[VMStatus] | None = None) -> list[VMInstanceItem]: ...

    @staticmethod
    def start(inputs: VMInput) -> BatchResult[VMInstanceItem]: ...

    @staticmethod
    def stop(inputs: VMInput) -> BatchResult[VMInstanceItem]: ...

    @staticmethod
    def reboot(inputs: VMInput) -> BatchResult[VMInstanceItem]: ...

    @staticmethod
    def snapshot(inputs: VMInput, mem_out: Path, state_out: Path) -> OperationResult[VMInstanceItem]: ...
```

### Public API Surface

The `api/__init__.py` re-exports ALL public Operation and Input types. CLI code imports
exclusively from this surface:

Exports include:
- **Operation classes:** `BinaryOperation`, `CacheOperation`, `ConfigOperation`, `ConsoleOperation`, `CPOperation`, `HostOperation`, `ImageOperation`, `InitOperation`, `KernelOperation`, `KeyOperation`, `LogOperation`, `NetworkOperation`, `SSHOperation`, `VMOperation`, `VolumeOperation`
- **Console types:** `ConsoleConnectionInfo`
- **Init types:** `InitResult`, `InitStepResult`
- **Input classes:** `BinaryPullInput`, `BinaryInput`, `ConsoleInput`, `ConsoleRequest`, `ImagePullInput`, `ImageImportInput`, `ImageInput`, `KernelImportInput`, `KernelImportRequest`, `ResolvedKernelImportInput`, `KernelPullInput`, `KernelInput`, `KeyCreateInput`, `KeyInput`, `LogInput`, `NetworkCreateInput`, `NetworkInput`, `SSHInput`, `VolumeCreateInput`, `VolumeInput`, `VMCreateInput`, `VMImportInput`, `VMImportRequest`, `VMInput`
- **VM export/import config models:** `VMExportComputeConfig`, `VMExportImageConfig`, `VMExportKernelConfig`, `VMExportBinaryConfig`, `VMExportNetworkConfig`, `VMExportBootConfig`, `VMExportFirecrackerConfig`, `VMExportCloudInitConfig`, `VMExportConfig`

```python
# ✅ CORRECT — CLI imports from mvmctl.api
from mvmctl.api import VMOperation, VMCreateInput, VMInput

request = VMCreateInput(name="my-vm", ssh_keys=["key1"])
VMOperation.create(request)
```

```python
# ❌ WRONG — deep imports into api or core submodules
from mvmctl.api.vm_operations import VMOperation            # ❌
from mvmctl.core.vm._controller import VMController          # ❌
```

## API Data Flow — Input → Request → Resolved

Complex operations use a three-stage pipeline to move from raw CLI parameters to fully
resolved, validated values:

```
CLI → *Input → *Operation.*() → *Request(input, db).resolve() → Resolved*Input (frozen) → Operation acts
```

### The Three Types

| Stage | Class | Properties |
|-------|-------|------------|
| **`*Input`** | Raw CLI parameters | Thin `@dataclass`, `None` for optional fields |
| **`*Request`** | DB-backed resolver | Takes `*Input + db`. Has `resolve()` → calls `ensure_validate()` internally |
| **`Resolved*Input`** | Frozen output | `@dataclass(frozen=True)`. ALL values resolved and validated |

### VM Creation Flow (Complex — Many Parameters)

```python
# 1. CLI creates Input (raw values, None for optionals)
@dataclass
class VMCreateInput:
    name: str
    ssh_keys: list[str]
    vcpu_count: int | None = None
    mem_size_mib: int | None = None
    image: str | None = None
    kernel_id: str | None = None
    network_name: str | None = None
    # ...

# 2. VMOperation.create() creates VMCreateRequest, which resolves
class VMCreateRequest:
    def __init__(self, *, vm_id: str, vm_dir: Path, inputs: VMCreateInput, db: Database | None = None):
        self._inputs = inputs
        self._db = db or Database()
        # Sub-resolvers created from DB
        self._network_resolver = NetworkResolver(NetworkRepository(self._db))
        # ...

    def resolve(self) -> ResolvedVMCreateInput:
        """Resolve all inputs to explicit values, then validate."""
        image = self._resolve_image()
        kernel = self._resolve_kernel()
        network = self._resolve_network()
        # ... resolve all fields, applying defaults from DB
        self._result = ResolvedVMCreateInput(...)
        self.ensure_validate()        # ✅ Validation inside resolve()
        return self._result

# 3. ResolvedVMCreateInput is frozen — all values resolved
@dataclass(frozen=True)
class ResolvedVMCreateInput:
    name: str
    vm_id: str
    vcpu_count: int
    mem_size_mib: int
    network: NetworkItem       # ✅ Resolved from DB
    image: ImageItem           # ✅ Resolved from DB
    kernel: KernelItem         # ✅ Resolved from DB
    binary: BinaryItem         # ✅ Resolved from DB
    # ... all fields resolved, no Nones for required params
```

Note that `VMCreateRequest.__init__` takes `vm_id` and `vm_dir` (pre-computed by `VMCreateContext`) in addition to the standard `inputs` and `db` parameters.

### VM Operations Flow (Simple — References Existing VM)

```python
# 1. CLI creates VMInput (filter criteria)
@dataclass
class VMInput:
    identifiers: list[str] = field(default_factory=list)
    force: bool | None = None

# 2. VMRequest resolves against DB
class VMRequest:
    def __init__(self, *, inputs: VMInput, db: Database | None = None):
        self._inputs = inputs
        self._vm_resolver = VMResolver(VMRepository(db), include=["image", "kernel", "network.leases", "volumes"])

    def resolve(self) -> ResolvedVMInput:
        result = self._vm_resolver.resolve_many(self._inputs.identifiers)
        self._result = ResolvedVMInput(vms=result.items, force=...)
        self.ensure_validate()
        return self._result

# 3. ResolvedVMInput — frozen, contains VM instance(s)
@dataclass(frozen=True)
class ResolvedVMInput:
    vms: list[VMInstanceItem]
    force: bool
```

### Default Resolution (API Layer)

Defaults are resolved at the API layer (Input → Request pipeline), with the CLI passing `None` for optionals:

```python
# cli/vm.py  ✅ CORRECT
@vm_app.command()
def create(
    name: str,
    vcpus: Optional[int] = typer.Option(None, "--vcpus"),
    memory: Optional[int] = typer.Option(None, "--memory"),
):
    inputs = VMCreateInput(
        name=name,
        vcpu_count=vcpus,           # ✅ None means "use DB default"
        mem_size_mib=memory,        # ✅ None means "use DB default"
        ssh_keys=resolve_ssh_keys(),
    )
    VMOperation.create(inputs)
```

**MANDATORY CORRECT PATTERN:**
```python
# cli/ — NO DEFAULT_* in typer.Option
vcpus: Optional[int] = typer.Option(None, "--vcpus", help="Number of vCPUs")
```

## Domain ≠ CLI Command

**Domains are business capabilities.** CLI commands often orchestrate multiple domains.

| CLI Command | Domains Involved | Why |
|-------------|-----------------|-----|
| `mvm vm create` | vm + network + image + kernel + binary + cloudinit + volume | Creates VM requires network, image, kernel, binary, cloud-init, and optionally volumes |
| `mvm vm stop` | vm | Single domain operation |
| `mvm network create` | network | Single domain operation |
| `mvm host init` | host + network | Host setup requires network initialization |
| `mvm volume create` | volume | Single domain operation |
| `mvm vm attach-volume` | vm + volume | Attaching a volume requires vm and volume domains |
| `mvm cp` | ssh | File copy between host and VM uses SSH/tar — resolves VM identifiers via the ssh domain |

## File Placement Rules

### 1. Single-Domain Logic

```
Does it manage a specific entity instance? (bound to self._entity)
│
├── YES → core/{domain}/_controller.py
│       Example: VMController with stop(), pause(), start() methods operating on self._vm
│
├── Is it stateless operations on a resource? (create, setup, teardown, bulk)
│   └── YES → core/{domain}/_service.py
│       Example: NetworkService with setup_bridge(), teardown_nat()
│       Example: VMService with stop_many(), start_many()
│
├── Is it database operations for a specific entity type? (get, list, upsert, delete, count)
│   └── YES → core/{domain}/_repository.py
│       - All queries (list, count, filter) belong here — NO separate Inventory class
│       - Use SQL-level computation (COUNT, WHERE IN) not Python filtering
│       - Repositories are the ONLY files in a domain that touch the database
│
├── Is it entity resolution by identifier? (name/ID/IP/MAC → entity)
│   └── YES → core/{domain}/_resolver.py
│       Example: VMResolver resolves name, ID, IP, MAC to VMInstanceItem
│
└── Is it infrastructure with no domain knowledge? (DB, iptables, validation)
    └── YES → core/_shared/
        Example: _db.py (connection manager), _iptables_tracker/, _enrichment.py
```

### 2. Orchestration (Multiple Domains)

**Golden Rule:** If an implementation imports from multiple domains, it belongs in `api/` as `{domain}_operations.py`.

```
Does it import from multiple domains?
│
└── YES → api/{primary_domain}_operations.py
    Example: vm_operations.py imports vm, network, image, kernel, binary, cloudinit, console

    Why: VM creation (vm domain) requires network setup (network domain),
    image cloning (image domain), kernel selection (kernel domain),
    binary selection (binary domain), and cloud-init provisioning (cloudinit domain).
    This is orchestration, which lives in the API layer, not in core.
```

### 3. Infrastructure Placement

```
Infrastructure tool placement:
│
├── Is it generic and could be used by any domain? (DB, process management, enrichment)
│   └── YES → core/_shared/
│
├── Is it specific to one domain's concerns? (IP lease for networks)
│   └── YES → core/{domain}/ (e.g., core/network/_lease_service.py)
│
├── Is it a filesystem provisioning backend? (guestfs, loopmount, provisioner abstraction)
│   └── YES → core/_shared/_guestfs/, core/_shared/_loopmount/, core/_shared/_provisioner/
│
└── Is it shared by multiple domains but has domain logic? (iptables rules)
    └── DECISION:
        - Generic iptables → core/_shared/_iptables_tracker/
        - Network-specific rule generation → core/network/
```

## Import Boundaries (Enforced)

```python
# ✅ CLI — ONLY imports from mvmctl.api
from mvmctl.api import VMOperation, VMCreateInput, VMInput

# ✅ API — orchestrates multiple core domains
from mvmctl.core.vm import VMController, VMRepository
from mvmctl.core.network import NetworkService, NetworkRepository
from mvmctl.core.volume import VolumeController, VolumeRepository
from mvmctl.core._shared._db import Database

# ✅ Domain — ONLY imports _shared (never other domains)
from mvmctl.core._shared import Database
from mvmctl.core._shared._iptables_tracker import IPTablesTracker

# ✅ Domain resolvers — located in each domain
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.core.volume._resolver import VolumeResolver

# ❌ FORBIDDEN — Domains never import other domains or orchestration
# In core/vm/_controller.py:
from mvmctl.core.network import NetworkController       # NEVER
from mvmctl.api.vm_operations import VMOperation         # NEVER
from mvmctl.core.image import ImageController            # NEVER
```

## Dependency Direction

```
     api/
    /    \
   vm    network    image    kernel    binary    key    host    volume ...
    \    /
   core/_shared/
```

**Rules:**
1. `api/` sits at the top — orchestrates domains
2. Domains sit in the middle — they only use `_shared/`
3. `_shared/` sits at the bottom — pure infrastructure, no domain knowledge
4. **No cycles:** Domains never import orchestration or other domains

## Naming Conventions

| Pattern | Suffix | Location | Example |
|---------|--------|----------|---------|
| Stateful entity manager | `Controller` | `core/{domain}/` | `VMController`, `NetworkController` |
| Stateless operations | `Service` | `core/{domain}/` | `NetworkService`, `VMService` |
| Database operations | `Repository` | `core/{domain}/_repository.py` | `VMRepository` |
| Entity resolution | `Resolver` | `core/{domain}/_resolver.py` | `VMResolver` |
| Cross-domain orchestration | `*Operation` | `api/*_operations.py` | `VMOperation` |
| Raw CLI input | `*Input` | `api/inputs/_*_input.py` | `VMCreateInput` |
| DB-backed resolver | `*Request` | `api/inputs/_*_input.py` | `VMCreateRequest` |
| Resolved frozen output | `Resolved*Input` | `api/inputs/_*_input.py` | `ResolvedVMCreateInput` |
| Shared infrastructure | None | `core/_shared/` | `Database`, `IPTablesTracker` |
| Domain-specific helpers | Descriptive | `core/{domain}/` | `VMProvisioner` |

### Model Naming — `*Item` Suffix

All data models use the `*Item` suffix:

```python
# models/vm.py
@dataclass
class VMInstanceItem: ...

# models/network.py
@dataclass
class NetworkItem: ...
@dataclass
class NetworkLeaseItem: ...
@dataclass
class FirewallRule: ...

# models/image.py
@dataclass
class ImageItem: ...

# models/kernel.py
@dataclass
class KernelItem: ...

# models/binary.py
@dataclass
class BinaryItem: ...

# models/key.py
@dataclass
class SSHKeyItem: ...

# models/volume.py
@dataclass
class VolumeItem: ...

# models/host.py
@dataclass
class HostStateItem: ...
@dataclass
class HostStateChangeItem: ...
```

## Relation Enrichment System

Resolvers can optionally enrich resolved entities with related entities (e.g., VM → Kernel, Network → Leases). This prevents N+1 query problems by using batch loading.

### Design Principles

1. **Configuration at construction** — `include` is a constructor parameter, not a method parameter. All methods on the resolver auto-enrich.
2. **Batch loading** — FK values are collected, deduplicated, and resolved in a single query per relation. O(relations) queries regardless of entity count.
3. **Single model** — No flat vs rich model split. The same model has `Optional[Relation]` fields that are `None` when not requested.
4. **Declarative registry** — Each resolver declares its relations via a `RELATIONS` class attribute dict using `RelationSpec` objects.
5. **Lazy registration** — Resolvers are registered in `_resolver_registry.py` via `register()` to prevent circular imports. The `RelationEnricher` uses string names to look up resolvers.
6. **Nested support** — Dot notation (`network.leases`) resolves parents before children automatically.
7. **Centralized engine** — `RelationEnricher` in `core/_shared/_enrichment.py` handles all enrichment logic.

### How It Works

```python
# Resolver declares its relations
class VMResolver:
    RELATIONS: dict[str, RelationSpec] = {
        "kernel": RelationSpec(
            fk_field="kernel_id", resolver="kernel",
            method="resolve", relation_name="kernel",
        ),
        "image": RelationSpec(
            fk_field="image_id", resolver="image",
            method="resolve", relation_name="image",
        ),
        "binary": RelationSpec(
            fk_field="binary_id", resolver="binary",
            method="resolve", relation_name="binary",
        ),
        "network": RelationSpec(
            fk_field="network_id", resolver="network",
            method="resolve", relation_name="network",
        ),
        "network.leases": RelationSpec(
            fk_field="network", resolver="network_lease",
            method="list_by_network_id", relation_name="leases",
        ),
        "volumes": RelationSpec(
            fk_field="volume_ids", resolver="volume",
            method="by_id", relation_name="volumes",
            batch_method="resolve_by_vm_volume_ids",
        ),
    }

    def __init__(
        self,
        repo: VMRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo or VMRepository()
        self._include = include

    def _enrich(self, vms: list[VMInstanceItem]) -> list[VMInstanceItem]:
        if self._include:
            RelationEnricher().enrich(
                vms, self._include, self.RELATIONS, self._repo._db
            )
        return vms

    # ALL methods call self._enrich() before returning
    def by_id(self, vm_id: str) -> VMInstanceItem:
        matches = self._repo.find_by_prefix(vm_id)
        if len(matches) == 0:
            raise VMNotFoundError(f"VM not found: {vm_id}")
        if len(matches) > 1:
            names = ", ".join(vm.name for vm in matches)
            raise VMNotFoundError(f"ID {vm_id} matches multiple VMs: {names}")
        return self._enrich(matches)[0]

    def resolve_many(self, identifiers: list[str]) -> VMResolveResult:
        vms = self._repo.get_many(identifiers)
        return VMResolveResult(
            items=self._enrich(vms.items),
            errors=vms.errors,
            exit_code=vms.exit_code,
        )
```

### Complete Relation Graph

```
VMResolver
├── "kernel"          → KernelResolver.resolve(kernel_id)
├── "image"           → ImageResolver.resolve(image_id)
├── "binary"          → BinaryResolver.resolve(binary_id)
├── "network"         → NetworkResolver.resolve(network_id)
├── "network.leases"  → NetworkLeaseResolver.list_by_network_id(network.id)
└── "volumes"         → VolumeResolver.by_id  # per-VM; batch via resolve_by_vm_volume_ids

NetworkResolver
├── "leases"          → NetworkLeaseResolver.list_by_network_id(network.id)
├── "iptables_rules"  → IPTablesRuleResolver.list_by_network_id(network.id)
└── "vms"             → VMResolver.find_by_network_id(network.id)  # batch: by_network_id_batch

ImageResolver         → (no relations)
KernelResolver        → (no relations)
BinaryResolver        → (no relations)
KeyResolver           → (no relations)
VolumeResolver        → (no relations)
NetworkLeaseResolver  → (no relations)
IPTablesRuleResolver  → (no relations)
```

### Model Design — Optional Relation Fields

Models have `Optional` fields for relations that are `None` by default and populated only when requested:

```python
# models/vm.py
@dataclass
class VMInstanceItem:
    id: str
    name: str
    kernel_id: str
    # ... all other flat fields ...

    # Resolved relations (Optional, None when not requested)
    kernel: KernelItem | None = None
    image: ImageItem | None = None
    binary: BinaryItem | None = None
    network: NetworkItem | None = None
    volumes: list[VolumeItem] = field(default_factory=list)

# models/network.py
@dataclass
class NetworkItem:
    id: str
    name: str
    # ... flat fields ...

    # Resolved relations
    leases: list[NetworkLeaseItem] | None = None
    iptables_rules: list[FirewallRule] | None = None
    vms: list[VMInstanceItem] | None = None
```

### Batch Loading — How N+1 is Prevented

When `resolve_many` is called with `include`, the `RelationEnricher`:

1. **Collects unique FK values** — deduplicates across all entities
2. **Single batch query** — uses `WHERE id IN (...)` to resolve all at once
3. **Maps back** — assigns resolved objects to the correct entity

```python
# Example: 1000 VMs with include=["kernel", "network"]
# Queries executed: 3 total (not 2000)
#   1. SELECT * FROM vm_instances WHERE id IN (...)
#   2. SELECT * FROM kernels WHERE id IN (...)  -- batch, unique kernel_ids
#   3. SELECT * FROM networks WHERE id IN (...) -- batch, unique network_ids
```

### Nested Relations — Parent Auto-Resolution

When a nested path like `network.leases` is requested, the enricher:
1. Sorts paths by depth (`network` before `network.leases`)
2. Resolves `network` first (parent)
3. Collects `network.id` from resolved parents
4. Batch-resolves `leases` for each unique network ID
5. Assigns `leases` list to each resolved `Network` object

### Lazy Resolver Registry

To prevent circular imports, resolvers are registered by string name in `core/_shared/_resolver_registry.py`:

```python
# Registration (in each resolver's module auto-run at module level)
from mvmctl.core._shared._resolver_registry import register

register("vm", lambda: VMResolver)
```

```python
# Lookup (in RelationEnricher)
from mvmctl.core._shared._resolver_registry import get

resolver_class = get("kernel")   # Returns KernelResolver class
```

#### Auto-Discovery via `_RESOLVER_MODULE_PATHS`

The registry includes an **auto-discovery** mechanism that avoids requiring explicit module imports before calling `get()`. A `_RESOLVER_MODULE_PATHS` dict maps known resolver name strings to their module paths:

```python
_RESOLVER_MODULE_PATHS: dict[str, str] = {
    "binary": "mvmctl.core.binary._resolver",
    "image": "mvmctl.core.image._resolver",
    "kernel": "mvmctl.core.kernel._resolver",
    "key": "mvmctl.core.key._resolver",
    "network": "mvmctl.core.network._resolver",
    "network_lease": "mvmctl.core.network._lease_resolver",
    "vm": "mvmctl.core.vm._resolver",
    "volume": "mvmctl.core.volume._resolver",
    "iptables_rule": "mvmctl.core._shared._iptables_tracker._resolver",
}
```

When `get(name)` is called for an unregistered name, the registry:

1. Looks up the name in `_RESOLVER_MODULE_PATHS`
2. Calls `importlib.import_module(module_path)` to trigger the module-level `register()` side-effect
3. Retries the factory lookup — if still not found, raises `KeyError`

This means callers never need to manually import resolver modules. They simply call `get("vm")` and the auto-discovery handles the rest.

### Usage

```python
# Flat resolution (existing behavior)
resolver = VMResolver(repo=repo)
vm = resolver.by_id("abc123")
print(vm.kernel_id)  # Works
print(vm.kernel)     # None

# With relations — configured once, all methods auto-enrich
resolver = VMResolver(repo=repo, include=["kernel", "image", "network"])
vm = resolver.by_id("abc123")        # kernel, image, network populated
vms = resolver.resolve_many([...])   # batch-enriched, deduplicated

# Nested relations
resolver = NetworkResolver(include=["leases", "iptables_rules"])
network = resolver.by_id("net-123")
print(network.leases)         # List[NetworkLeaseItem]
```

### Adding New Relations

To add a new relation to a resolver:

1. **Add to `RELATIONS` dict:**
```python
RELATIONS: dict[str, RelationSpec] = {
    "new_relation": RelationSpec(
        fk_field="foreign_key_field", resolver="new_resolver",
        method="resolve", relation_name="new_relation",
    ),
}
```

2. **Add `Optional` field to the model:**
```python
new_relation: "NewModel | None" = None
```

3. **Create the resolver** (if it doesn't exist) with `_enrich()` pattern.
4. **Register** the resolver in `_resolver_registry.py` if not already.

## Repository Pattern — SQL-Level Computation

```python
# ✅ CORRECT — SQL COUNT instead of fetch-all
def count_by_status(self, status: SomeStatus | list[SomeStatus]) -> int:
    statuses = [status] if isinstance(status, SomeStatus) else status
    status_values = [s.value for s in statuses]
    placeholders = ",".join(["?"] * len(status_values))
    query = f"SELECT COUNT(*) FROM {table_name} WHERE status IN ({placeholders})"
    result = conn.execute(query, status_values).fetchone()
    return result[0] if result else 0

# ❌ WRONG — Fetch all, then filter in Python
def count_by_status_wrong(self, statuses: list[str]) -> int:
    all_entities = self.list_all()  # Fetches ALL rows!
    return len([e for e in all_entities if e.status in statuses])
```

## Domain Growth Patterns

### Adding New Capabilities to a Domain

When a domain grows, add files following the naming convention:

```
core/vm/
├── _controller.py          # VMController (primary lifecycle)
├── _service.py             # VMService (bulk operations)
├── _firecracker.py         # FirecrackerSpawner (process management)
├── _provisioner.py         # VMProvisioner (rootfs provisioning)
├── _resolver.py            # VMResolver (resolve by name/id/ip/mac)
├── _repository.py          # VMRepository (database operations)
└── __init__.py
```

### Extracting Subsystems

If a subsystem becomes large, nest it:

```
core/vm/
├── _controller.py
├── _service.py
├── firecracker/            # Subsystem folder
│   ├── _controller.py      # FirecrackerController
│   ├── _config.py          # Internal helpers
│   └── __init__.py
└── __init__.py
```

### When to Create a New Domain

Create a new domain folder when:
1. **Business logic is complex** (e.g., cloud-init with 4 provisioning modes)
2. **Used by multiple orchestrations** (e.g., used by `vm_operations` and `host_operations`)
3. **Independent lifecycle** (can be tested in isolation)

## Summary

- **Domains ≠ CLI commands** — Domains are business capabilities (vm, network, volume, image, kernel, key, binary, host, config, console, logs, cache, cloudinit, ssh)
- **3-layer architecture** — CLI → API → Core, strict import boundaries
- **Orchestration lives in `api/`** — `api/*_operations.py` is the ONLY place that imports multiple domain modules
- **Domain isolation** — Domains only import `core/_shared/`, never other domains
- **Input → Request → Resolved pattern** — `*Input` (raw) → `*Request(input, db).resolve()` → `Resolved*Input` (frozen)
- **`resolve()` validates internally** — calls `ensure_validate()` after constructing the resolved result
- **Defaults resolved at API layer** — `None` in Input means "use DB default" (resolved by Request)
- **Naming** — `Controller` (stateful), `Service` (stateless), `Repository` (DB), `Resolver` (lookup)
- **Model naming** — `*Item` suffix: `VMInstanceItem`, `NetworkItem`, `ImageItem`, etc.
- **API surface** — `api/__init__.py` re-exports all Operation and Input types
- **Repository consolidation** — All queries in `_repository.py` (no separate Inventory)
- **SQL-level computation** — Use `COUNT(*)`, `WHERE IN` instead of fetch-all + Python filtering
- **Flexible queries** — Accept `single_value | list[single_value]` for filtering parameters
- **Relation enrichment** — `include` on resolver constructor, `RELATIONS` dict with `RelationSpec`, `RelationEnricher` batch-loads with deduplication
- **Infrastructure placement** — Generic → `_shared/`, domain-specific → `{domain}/`, filesystem provisioning → `_shared/_guestfs/`, `_shared/_loopmount/`, `_shared/_provisioner/`
- **File naming** — `_controller.py`, `_service.py`, `_repository.py`, `_resolver.py`
