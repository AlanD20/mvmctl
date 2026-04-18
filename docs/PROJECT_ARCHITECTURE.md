# Project Architecture

## Overview

Three-layer architecture with strict import boundaries: **CLI → API → Core**.

**Key Principle:** Domains are **business capabilities**, not CLI commands. A single CLI command (like `vm create`) often orchestrates multiple domains.

**Orchestration lives in `api/`, NOT in `core/`.** The API layer is the ONLY entity that imports multiple domains and sequences them together.

```
mvmctl/
├── api/              # Public interface + ORCHESTRATION (imports multiple domains)
├── core/             # All business logic (isolated domains + shared infrastructure)
└── cli/              # Frontend (Typer commands)
```

## Layer Responsibilities

| Layer | Purpose | Import Rules |
|-------|---------|--------------|
| **CLI** | Argument parsing, output formatting | `api/*` only |
| **API** | Public contract curation, DB resolution, **ORCHESTRATION** | `core/*` only. **ONLY layer that imports multiple domains.** |
| **Core** | Business logic, domain isolation | `core/_internal/` for infrastructure. **NO cross-domain imports.** |

## Core Structure

Domains represent business capabilities. They are isolated and only import from `core/_internal/`.

```
core/
├── vm/                    # VM lifecycle (start, stop, pause, config)
│   ├── _controller.py     # VMController - stateful VM operations
│   ├── _firecracker.py    # FirecrackerController - process management
│   ├── _guestfs.py        # GuestfsProvisioner - rootfs provisioning
│   ├── _resolver.py       # VMResolver - resolve VM by name/id/ip/mac
│   ├── _repository.py     # VMRepository - database operations (queries, counts)
│   └── __init__.py
├── network/               # Networking (bridge, tap, NAT, IP lease)
│   ├── _controller.py     # NetworkController
│   ├── _service.py          # NetworkService
│   ├── _lease_service.py    # LeaseService - IP lease management
│   ├── _resolver.py         # NetworkResolver
│   ├── _repository.py       # NetworkRepository + LeaseRepository
│   └── __init__.py
├── image/                 # OS images (fetch, import, cache)
│   ├── _controller.py     # ImageController
│   ├── _resolver.py         # ImageResolver
│   ├── _repository.py       # ImageRepository
│   └── __init__.py
├── kernel/                # Kernel images (fetch, build)
│   ├── _controller.py
│   ├── _resolver.py         # KernelResolver
│   ├── _repository.py       # KernelRepository
│   └── __init__.py
├── key/                   # SSH keys (create, list)
│   ├── _controller.py     # KeyController
│   ├── _resolver.py         # KeyResolver
│   ├── _repository.py       # KeyRepository
│   └── __init__.py
├── binary/                # Firecracker binaries (fetch, versions)
│   ├── _controller.py
│   ├── _resolver.py         # BinaryResolver
│   ├── _repository.py       # BinaryRepository
│   └── __init__.py
├── host/                  # Host-level operations (init, reset, prune)
│   ├── _controller.py
│   ├── _repository.py       # HostRepository
│   └── __init__.py
├── cache/                 # Cache management
├── config/                # Configuration management
├── console/               # Console relay management
│   └── _controller.py     # ConsoleController
├── cloudinit/             # Cloud-init provisioning (separate domain)
│   ├── _manager.py          # CloudInitManager
│   ├── _provisioner.py      # CloudInitProvisioner
│   └── __init__.py
└── _internal/             # Shared infrastructure
    ├── _db.py               # Database class (connection manager)
    ├── _iptables_tracker.py # Generic iptables
    └── _asset_manager.py    # Asset management
```

### API Orchestration Layer

Cross-domain orchestration lives in `api/` as `*_operations.py` files:

```
api/
├── vm_operations.py       # VM creation, removal, cleanup (orchestrates vm + network + image + kernel)
├── network_operations.py  # Network orchestration
├── image_operations.py    # Image orchestration
├── kernel_operations.py   # Kernel orchestration
├── key_operations.py      # Key orchestration
├── host_operations.py     # Host orchestration
├── binary_operations.py   # Binary orchestration
└── inputs/                # Request → ResolvedRequest pattern (grows with project)
    ├── __init__.py
    ├── _vm_create_request.py      # VMCreateRequest + ResolvedVMCreateRequest
    ├── _vm_request.py             # VMRequest + ResolvedVMRequest
    └── ...                        # More input request types as project grows
```

### API Input Layer

## Domain ≠ CLI Command

**Domains are business capabilities.** CLI commands often orchestrate multiple domains.

| CLI Command | Domains Involved | Why |
|-------------|-----------------|-----|
| `mvm vm create` | vm + network + image + kernel + binary (+ cloudinit if separate domain) | Creates VM requires network, image, kernel, binary, and optionally cloud-init as separate domain |
| `mvm vm stop` | vm | Single domain operation |
| `mvm network create` | network | Single domain operation |
| `mvm host init` | host + network | Host setup requires network initialization |

## File Placement Rules

### 1. Domain Files (Single Domain Only)

```
Where does domain code go?
│
├── Does it manage a specific entity instance? (bound to self._entity)
│   └── YES → core/{domain}/_controller.py
│       Example: VMController with stop(), pause(), ssh() methods operating on self._vm
│
├── Is it stateless operations on a resource? (create, setup, teardown)
│   └── YES → core/{domain}/_service.py
│       Example: NetworkService with setup_bridge(), teardown_nat()
│
├── Is it database operations for a specific entity type? (get, list, upsert, delete, count)
│   └── YES → core/{domain}/_repository.py
│       Example: VMRepository with get(), list_all(), list_by_status(), count(), upsert(), delete()
│       - All queries (list, count, filter) belong here — NO separate Inventory class
│       - Use SQL-level computation (COUNT, WHERE IN) not Python filtering
│       - Repositories are the ONLY files in a domain that touch the database
│
└── Is it infrastructure with no domain knowledge? (DB connection, iptables, validation)
    └── YES → core/_internal/
        Example: _db.py (connection manager), IPTablesTracker, VMResolver
```

### 2. Orchestration Files (Multiple Domains)

**Golden Rule:** If an implementation imports from multiple domains, it belongs in `api/` as `{domain}_operations.py`.

```
Does it import from multiple domains?
│
└── YES → api/{primary_domain}_operations.py
    Example: vm_operations.py imports vm, network, image, kernel
    (could also import cloudinit if cloudinit were its own domain)
    
    Why: VM creation (vm domain) requires network setup (network domain),
    image cloning (image domain), and kernel selection (kernel domain).
    This is orchestration, which lives in the API layer, not in core.
```

### 3. Infrastructure Placement Decision

```
Infrastructure tool placement:
│
├── Is it generic and could be used by any domain? (DB, process management)
│   └── YES → core/_internal/
│
├── Is it specific to one domain's concerns? (IP lease for networks)
│   └── YES → core/{domain}/ (e.g., core/network/_lease_manager.py)
│
└── Is it shared by multiple domains but has domain logic? (iptables rules)
    └── DECISION:
        - Generic iptables → core/_internal/_iptables_tracker.py
        - Network-specific rule generation → core/network/
```

## Repository Pattern (Domain Data Ownership)

Each domain owns its data persistence through a `_repository.py` file. This is the **Repository Pattern**:

```
core/
├── vm/
│   ├── _controller.py      # Business logic: stop(), start(), ssh()
│   ├── _service.py         # Business logic: list_vms(), find_vm()
│   ├── _repository.py      # Data access: get_vm(), upsert_vm(), delete_vm()
│   └── __init__.py
└── ...
```

**Repository Responsibilities:**
- Database CRUD operations for domain entities
- Query methods (get_by_id, get_by_name, list_all, find_by_prefix)
- Aggregate queries (count, count_by_*) — use SQL COUNT, not fetch-all + len()
- Flexible filtering — Methods accept single value or list: `value | list[value]` with SQL `WHERE IN`
- Atomic transactions for multi-row operations
- **NO business logic** — repositories only move data between domain objects and database

**Repository Pattern Rules:**
1. **SQL-level computation** — Use `SELECT COUNT(*)`, `WHERE column IN (...)` instead of fetching all rows and filtering in Python
2. **No separate Inventory/Query classes** — All queries belong in Repository
3. **Flexible query parameters** — Methods accept both single value and list for filtering: `status: Status | list[Status]`
4. **Domain owns its data** — Each domain controls how its entities are persisted

**Why This Pattern:**
1. **Performance** — SQL COUNT is O(1) vs fetching all rows
2. **No giant files** — `mvm_db.py` (1000+ lines) splits into focused domain repositories
3. **Single query location** — All data access in one file per domain
4. **Testable** — Can mock repository at domain boundary
5. **Clear separation** — Business logic (Controller/Service) vs Data access (Repository)

**Example Repository Structure (Generic Pattern):**
```python
# core/{domain}/_repository.py
from mvmctl.models import SomeStatusEnum

class SomeRepository:
    """Database operations for domain entities."""
    
    def __init__(self, db: DatabaseConnection) -> None:
        self._db = db
    
    def get(self, id: str) -> SomeEntity | None:
        """Return entity by ID or None."""
        ...
    
    def list_all(self) -> list[SomeEntity]:
        """Return all entities."""
        ...
    
    def list_by_status(self, status: SomeStatusEnum | list[SomeStatusEnum]) -> list[SomeEntity]:
        """Return entities filtered by status(es). SQL WHERE IN clause."""
        ...
    
    def count(self) -> int:
        """Total count using SQL COUNT."""
        ...
    
    def count_by_status(self, status: SomeStatusEnum | list[SomeStatusEnum]) -> int:
        """Count by status using SQL COUNT + WHERE IN."""
        ...
    
    def upsert(self, entity: SomeEntity) -> None:
        """Insert or update entity record."""
        ...
    
    def delete(self, id: str) -> None:
        """Delete entity by ID."""
        ...
```

**Repository Pattern in Action:**
```python
# Generic pattern for any domain - SQL-level computation (CORRECT)
def count_by_status(self, status: SomeStatus | list[SomeStatus]) -> int:
    statuses = [status] if isinstance(status, SomeStatus) else status
    status_values = [s.value for s in statuses]
    placeholders = ",".join(["?"] * len(status_values))
    
    # Use SQL COUNT instead of fetching all rows
    query = f"SELECT COUNT(*) FROM {table_name} WHERE status IN ({placeholders})"
    result = conn.execute(query, status_values).fetchone()
    return result[0] if result else 0

# ❌ WRONG: Fetch all, then filter in Python
def count_by_status_wrong(self, statuses: list[str]) -> int:
    all_entities = self.list_all()  # Fetches ALL rows!
    return len([e for e in all_entities if e.status in statuses])
```

## Import Boundaries (Enforced)

```python
# ✅ CLI - ONLY imports api
from mvmctl.api import vm, network

# ✅ API - re-exports from core + orchestration lives here
from mvmctl.core.vm import VMController, VMService
from mvmctl.api.vm_operations import create_vm, remove_vm  # Orchestration in API

# ✅ Domain - ONLY imports _internal
from mvmctl.core._internal._db import Database
from mvmctl.core._internal._iptables_tracker import IPTablesTracker

# ✅ Domain resolvers - located in each domain
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.core.image._resolver import ImageResolver

# ❌ FORBIDDEN - Domains never import other domains or orchestration
# In core/vm/_controller.py:
from mvmctl.core.network import NetworkController       # NEVER
from mvmctl.api.vm_operations import create_vm           # NEVER
from mvmctl.core.image import ImageManager              # NEVER

# ✅ API orchestration - ONLY place that imports multiple domains + _internal
# In api/vm_operations.py:
from mvmctl.core.vm import VMController
from mvmctl.core.network import NetworkController
from mvmctl.core.image import ImageController
from mvmctl.core.kernel import KernelResolver
from mvmctl.core._internal._db import Database
```

## Dependency Direction

```
_orchestration/  →  vm/  →  _internal/
              ↘  network/  ↗
              ↘  image/    ↗
              ↘  kernel/   ↗
              ↘  ...       ↗
```

**Rules:**
1. `_orchestration/` sits at the top - it calls domains
2. Domains sit in the middle - they only use `_internal/`
3. `_internal/` sits at the bottom - pure utilities, no domain knowledge
4. **No cycles:** Domains never import orchestration or other domains

## Naming Convention

| Pattern | Suffix | Location | Example | Purpose |
|---------|--------|----------|---------|---------|
| **Stateful entity manager** | `Controller` | `core/{domain}/` | `VMController`, `NetworkController` | Bound to specific instance (self._vm), lifecycle operations |
| **Stateless operations** | `Service` | `core/{domain}/` | `NetworkService`, `LeaseService` | Setup/teardown operations, stateless business logic |
| **Database operations** | `Repository` | `core/{domain}/_repository.py` | `VMRepository`, `ImageRepository` | ALL data access: get, list, count, upsert, delete. Use SQL-level ops. |
| **Entity resolution** | `Resolver` | `core/{domain}/_resolver.py` | `VMResolver`, `ImageResolver` | Resolve IDs/names to domain objects |
| **Cross-domain workflow** | `_operations.py` | `core/_orchestration/` | `vm_operations.py` | Functions importing multiple domains |
| **Shared infrastructure** | None | `core/_internal/` | `Database`, `IPTablesTracker` | No domain knowledge, reusable utilities |
| **Domain-specific helpers** | Descriptive | `core/{domain}/` | `GuestfsProvisioner` | Domain-specific but reusable within domain |

## Public API Example

API layer is thin curation - no business logic:

```python
# api/vm.py
from mvmctl.core._orchestration.vm_operations import create_vm, remove_vm
from mvmctl.core.vm import VMController, VMRepository
from mvmctl.api.input.vm_create_request import VMCreateRequest

__all__ = [
    "VMController",     # Stateful: stop, start, pause (from domain)
    "VMRepository",     # Data access: list, count, get (from domain)
    "VMCreateRequest",  # Input resolution (from api/input)
    "create_vm",        # Orchestrated creation (from _orchestration)
    "remove_vm",        # Orchestrated removal (from _orchestration)
]
```

CLI consumes only this public surface:

```python
# cli/vm.py
from mvmctl.api import vm
from mvmctl.api.input.vm_create_request import VMCreateRequest

# Single domain operation - uses Controller directly
controller = vm.VMController("myvm")
controller.stop()

# Orchestrated operation - uses Request pattern
request = VMCreateRequest(name="newvm", image="ubuntu-24.04")
vm.create_vm(request)  # Orchestrates 6 domains
```

## Domain Growth Patterns

### Adding New Capabilities to a Domain

When a domain grows, add files following the naming convention:

```
core/vm/
├── _controller.py          # VMController (primary lifecycle)
├── _firecracker.py         # FirecrackerController (process management)
├── _guestfs.py             # GuestfsProvisioner (rootfs operations)
├── _resolver.py            # VMResolver (resolve by name/id/ip/mac)
├── _repository.py          # VMRepository (database operations - all queries)
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
2. **Used by multiple orchestrations** (e.g., used by vm_operations and host_operations)
3. **Independent lifecycle** (can be tested in isolation)

## Input Resolution Pattern (Request → ResolvedRequest)

For complex orchestrated operations, use a two-phase input resolution pattern to avoid passing 20+ individual arguments through layers.

### Pattern Structure

```
api/input/
├── __init__.py
├── vm_create_request.py    # VMCreateRequest + ResolvedVMCreateRequest
├── vm_remove_request.py    # VMRemoveRequest + ResolvedVMRemoveRequest
└── ...
```

### Request Types

**CREATE Operations** (complex, many parameters):
```python
@dataclass
class VMCreateRequest:
    """Holds raw CLI arguments for VM creation."""
    name: str
    image: str           # Raw: "ubuntu-24.04"
    kernel: str          # Raw: "v6.1"
    vcpus: int | None
    memory_mib: int | None
    network: str | None
    binary: str | None
    # ... many more fields
    
    def validate(self) -> None:
        """Validate without DB lookups."""
        if not self.name:
            raise ValueError("VM name is required")
        # ... other validation
    
    def resolve(self) -> ResolvedVMCreateRequest:
        """Resolve all string references to actual objects."""
        # Uses domain controllers to resolve
        return ResolvedVMCreateRequest(
            name=self.name,
            image=ImageController().get_by_os_slug(self.image),
            kernel=KernelController().get_by_version(self.kernel),
            network=NetworkController().resolve(self.network),
            # ... all fields resolved
        )

@dataclass(frozen=True)
class ResolvedVMCreateRequest:
    """Fully resolved and validated - ready for orchestration."""
    name: str
    image: Image           # Resolved object
    kernel: Kernel         # Resolved object
    network: Network       # Resolved object
    # ... all resolved, immutable
```

**OTHER Operations** (simple, reference existing VM):
```python
@dataclass
class VMRequest:
    """For remove, start, stop, pause, resume operations.
    
    Uses VMInstance properties to identify existing VM.
    """
    vm_id: str | None
    name: str | None
    # Minimal fields - just need to identify the VM
    
    def resolve(self) -> ResolvedVMRequest:
        """Resolve to existing VMInstance."""
        return ResolvedVMRequest(
            vm=VMRepository().get(self.vm_id) or VMRepository().get_by_name(self.name)
        )

@dataclass(frozen=True)
class ResolvedVMRequest:
    """Resolved to actual VMInstance."""
    vm: VMInstance
```

### Flow

```
CLI: Create Request(...) → API: request.validate() → request.resolve() → Orchestration

# Example:
cli/vm.py:
    request = VMCreateRequest(name="myvm", image="ubuntu", ...)
    api.vm.create_vm(request)

api/vms.py:
    def create_vm(request: VMCreateRequest) -> VMInstance:
        request.validate()              # Fast validation
        resolved = request.resolve()      # Heavy DB lookups
        return orchestration.create_vm(resolved)

core/_orchestration/vm_operations.py:
    def create_vm(resolved: ResolvedVMCreateRequest) -> VMInstance:
        # Use resolved.image, resolved.kernel directly
        network_ctrl = NetworkController(resolved.network.id)
        image_ctrl = ImageController(resolved.image)
        # ... coordinate domains with resolved objects
```

### Why This Pattern

1. **Single object passed through layers** - Not 20+ individual args
2. **Validation in one place** - `request.validate()`
3. **Resolution in one place** - `request.resolve()`
4. **Orchestration gets clean objects** - No lookups needed
5. **Immutable resolved requests** - `frozen=True` prevents mutation
6. **Separation of concerns** - Validation (fast) vs Resolution (slow with DB)

### Key Distinctions

| Operation Type | Request Class | Resolved Class | Fields |
|----------------|---------------|----------------|--------|
| **CREATE** | `VMCreateRequest` | `ResolvedVMCreateRequest` | Many (all creation params) |
| **EXISTING** | `VMRequest` | `ResolvedVMRequest` | Minimal (just VM identifier) |

## Relation Enrichment System

Resolvers can optionally enrich resolved entities with related entities (e.g., VM → Kernel, Network → Leases). This prevents N+1 query problems by using batch loading and is configured once at the resolver constructor level.

### Design Principles

1. **Configuration at construction** — `include` is a constructor parameter, not a method parameter. All methods on the resolver auto-enrich.
2. **Batch loading** — FK values are collected, deduplicated, and resolved in a single query per relation. O(relations) queries regardless of entity count.
3. **Single model** — No flat vs rich model split. The same model has `Optional[Relation]` fields that are `None` when not requested.
4. **Declarative registry** — Each resolver declares its relations via a `RELATIONS` class attribute dict.
5. **Nested support** — Dot notation (`network.leases`) resolves parents before children automatically.
6. **Centralized engine** — `RelationEnricher` in `core/_internal/` handles all enrichment logic.

### How It Works

```python
# Resolver declares its relations
class VMResolver:
    RELATIONS: dict[str, tuple[str, type, str]] = {
        # relation_name: (fk_field, resolver_class, method_name)
        "kernel": ("kernel_id", KernelResolver, "resolve"),
        "image": ("image_id", ImageResolver, "resolve"),
        "binary": ("binary_id", BinaryResolver, "resolve"),
        "network": ("network_id", NetworkResolver, "resolve"),
        "network.leases": ("network", NetworkLeaseResolver, "list_by_network_id"),
    }

    def __init__(
        self,
        repo: VMRepository | None = None,
        *,
        include: list[str] | None = None,
    ) -> None:
        self._repo = repo or VMRepository()
        self._include = include

    def _enrich(self, vms: list[VMInstance]) -> list[VMInstance]:
        if self._include:
            RelationEnricher().enrich(
                vms, self._include, self.RELATIONS, self._repo._db
            )
        return vms

    # ALL methods call self._enrich() before returning
    def by_id(self, vm_id: str) -> VMInstance:
        vm = self._repo.find_by_prefix(vm_id)[0]
        return self._enrich([vm])[0]

    def resolve_many(self, identifiers: list[str]) -> list[VMInstance]:
        vms = self._repo.get_many(identifiers)
        return self._enrich(vms)
```

### Complete Relation Graph

```
VMResolver
├── "kernel"        → KernelResolver.resolve(kernel_id)
├── "image"         → ImageResolver.resolve(image_id)
├── "binary"        → BinaryResolver.resolve(binary_id)
├── "network"       → NetworkResolver.resolve(network_id)
└── "network.leases" → NetworkLeaseResolver.list_by_network_id(network.id)

NetworkResolver
├── "leases"        → NetworkLeaseResolver.list_by_network_id(network.id)
└── "iptables_rules" → IPTablesRuleResolver.list_by_network_id(network.id)

ImageResolver       → (no relations)
KernelResolver      → (no relations)
BinaryResolver      → (no relations)
KeyResolver         → (no relations)
NetworkLeaseResolver → (no relations)
IPTablesRuleResolver → (no relations)
```

### Model Design — Optional Relation Fields

Models have `Optional` fields for relations that are `None` by default and populated only when requested:

```python
# models/vm.py
@dataclass
class VMInstance:
    id: str
    name: str
    kernel_id: str
    kernel: "Kernel | None" = None       # Populated when include=["kernel"]
    image_id: str
    image: "Image | None" = None         # Populated when include=["image"]
    # ... all other flat fields ...

# models/network.py
@dataclass
class NetworkItem:
    id: str
    name: str
    leases: list["NetworkLease"] | None = None           # include=["leases"]
    iptables_rules: list["IPTablesRule"] | None = None   # include=["iptables_rules"]
```

### Batch Loading — How N+1 is Prevented

When `resolve_many` is called with `include`, the `RelationEnricher`:

1. **Collects unique FK values** — deduplicates across all entities
2. **Single batch query** — uses `WHERE id IN (...)` to resolve all at once
3. **Maps back** — assigns resolved objects to the correct entity

```python
# Example: 1000 VMs with include=["kernel", "network"]
# Queries executed: 3 total (not 3000)
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

```python
# include=["network.leases"]
# 1. Resolve all networks (batch)
# 2. For each resolved network, resolve its leases (batch by network_id)
# 3. vm.network.leases is now populated
```

### Validation

Unknown relation paths raise `ValueError` with available options:

```python
resolver = VMResolver(include=["foo"])
resolver.resolve("my-vm")
# ValueError: Unknown relation 'foo'. Available: binary, image, kernel, network, network.leases
```

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
vm = resolver.by_name("my-vm")       # same
vm = resolver.by_ip("10.0.0.5")      # same
vm = resolver.resolve("my-vm")       # same
vms = resolver.resolve_many([...])   # batch-enriched, deduplicated

# Nested relations
resolver = NetworkResolver(include=["leases", "iptables_rules"])
network = resolver.by_id("net-123")
print(network.leases)         # List[NetworkLease]
print(network.iptables_rules) # List[IPTablesRule]

# VM with nested network relations
resolver = VMResolver(include=["network.leases"])
vm = resolver.by_id("abc123")
print(vm.network.leases)  # Works — network resolved first, then leases
```

### Adding New Relations

To add a new relation to a resolver:

1. **Add to `RELATIONS` dict:**
```python
RELATIONS: dict[str, tuple[str, type, str]] = {
    "new_relation": ("foreign_key_field", NewResolver, "resolve"),
}
```

2. **Add `Optional` field to the model:**
```python
new_relation: "NewModel | None" = None
```

3. **Create the resolver** (if it doesn't exist) with `_enrich()` pattern.

No changes to `RelationEnricher` or any other code needed.

## Summary

- **Domains ≠ CLI commands** - Domains are business capabilities (vm, network, image, etc.)
- **CLI commands trigger orchestration** - `vm create` orchestrates 6 domains
- **Orchestration rule** - If code imports multiple domains → `core/_orchestration/`
- **Domain isolation** - Domains only import `core/_internal/`, never other domains
- **Infrastructure placement** - Generic → `_internal/`, domain-specific → `{domain}/`
- **Repository consolidation** - All queries in `_repository.py` (no separate Inventory)
- **SQL-level computation** - Use `COUNT(*)`, `WHERE IN` instead of fetch-all + Python filtering
- **Flexible queries** - Accept `single_value | list[single_value]` for filtering parameters
- **Naming** - `Controller` (stateful), `Service` (stateless), `Repository` (DB), `Resolver` (lookup)
- **Input resolution** - `Request` (raw) → `validate()` → `resolve()` → `ResolvedRequest` (frozen)
- **Relation enrichment** - `include` on resolver constructor, `RELATIONS` dict declares relations, `RelationEnricher` batch-loads with deduplication
- **File naming** - `_controller.py`, `_service.py`, `_repository.py`, `_resolver.py`, `_operations.py`
