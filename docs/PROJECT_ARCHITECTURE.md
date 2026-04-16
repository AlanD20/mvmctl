# Project Architecture

## Overview

Three-layer architecture with strict import boundaries: **CLI → API → Core**.

**Key Principle:** Domains are **business capabilities**, not CLI commands. A single CLI command (like `vm create`) often orchestrates multiple domains.

```
mvmctl/
├── api/              # Public interface (thin re-exports only)
├── core/             # All business logic (domains + orchestration + shared infrastructure)
└── cli/              # Frontend (Typer commands)
```

## Layer Responsibilities

| Layer | Purpose | Import Rules |
|-------|---------|--------------|
| **CLI** | Argument parsing, output formatting | `api/*` only |
| **API** | Public contract curation | `core/*` only |
| **Core** | Business logic, domain isolation | `core/_internal/` for infrastructure, `core/_orchestration/` for cross-domain |

## Core Structure

Domains represent business capabilities. They are isolated and only import from `core/_internal/`.

```
core/
├── vm/                    # VM lifecycle (start, stop, pause, config)
│   ├── _controller.py     # VMController - stateful VM operations
│   ├── _service.py        # VMService - stateless VM operations
│   ├── _repository.py     # VMRepository - database operations for VMs
│   └── __init__.py
├── network/               # Networking (bridge, tap, NAT, IP lease)
│   ├── _controller.py
│   ├── _service.py
│   ├── _repository.py     # NetworkRepository + LeaseRepository
│   └── __init__.py
├── image/                 # OS images (fetch, import, cache)
│   ├── _controller.py
│   ├── _service.py
│   ├── _repository.py     # ImageRepository
│   └── __init__.py
├── kernel/                # Kernel images (fetch, build)
│   ├── _controller.py
│   ├── _service.py
│   ├── _repository.py     # KernelRepository
│   └── __init__.py
├── key/                   # SSH keys (create, list)
│   ├── _controller.py
│   ├── _service.py
│   ├── _repository.py     # KeyRepository
│   └── __init__.py
├── binary/                # Firecracker binaries (fetch, versions)
│   ├── _controller.py
│   ├── _service.py
│   ├── _repository.py     # BinaryRepository
│   └── __init__.py
├── host/                  # Host-level operations (init, reset, prune)
│   ├── _controller.py
│   ├── _service.py
│   ├── _repository.py     # HostRepository
│   └── __init__.py
├── cache/                 # Cache management
├── config/                # Configuration management
├── console/               # Console relay management
├── _internal/             # Shared infrastructure (DB, resolvers, validators, iptables)
│   ├── _db.py
│   ├── _resolvers/
│   ├── _validators/
│   └── _iptables_tracker.py   # Generic iptables (used by network, firewall)
└── _orchestration/        # Cross-domain operations
    ├── __init__.py
    ├── vm_operations.py       # Imports: vm, network, image, kernel, binary
    ├── cloudinit_operations.py # Example: if cloudinit were its own domain
    ├── network_operations.py  # Imports: network
    ├── host_operations.py     # Imports: host, network
    ├── image_operations.py    # Imports: image
    ├── kernel_operations.py   # Imports: kernel
    ├── key_operations.py      # Imports: key
    └── binary_operations.py   # Imports: binary
```

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
├── Is it stateless operations on a resource? (list, search, find, create_single)
│   └── YES → core/{domain}/_service.py
│       Example: VMService with list_all(), search_by_name(), exists()
│
├── Is it database operations for a specific entity type? (get, list, upsert, delete)
│   └── YES → core/{domain}/_repository.py
│       Example: VMRepository with get_vm(), list_vms(), upsert_vm(), delete_vm()
│       Each domain owns its data persistence. Repositories are the ONLY files
│       in a domain that touch the database.
│
└── Is it infrastructure with no domain knowledge? (DB connection, iptables, validation)
    └── YES → core/_internal/
        Example: _db.py (connection manager), IPTablesTracker, VMResolver
```

### 2. Orchestration Files (Multiple Domains)

**Golden Rule:** If an implementation imports from multiple domains, it belongs in `_orchestration/`.

```
Does it import from multiple domains?
│
└── YES → core/_orchestration/{primary_domain}_operations.py
    Example: vm_operations.py imports vm, network, image, kernel
    (could also import cloudinit if cloudinit were its own domain)
    
    Why: VM creation (vm domain) requires network setup (network domain),
    image cloning (image domain), and kernel selection (kernel domain).
    This is orchestration, not vm domain logic.
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
- Atomic transactions for multi-row operations
- **NO business logic** — repositories only move data between domain objects and database

**Why This Pattern:**
1. **Domain owns its data** — Each domain controls how its entities are persisted
2. **No giant files** — `mvm_db.py` (1000+ lines) splits into focused domain repositories
3. **Testable** — Can mock repository at domain boundary
4. **Clear separation** — Business logic (Controller/Service) vs Data access (Repository)

**Example Repository Structure:**
```python
# core/vm/_repository.py
class VMRepository:
    """Database operations for VM instances."""
    
    def __init__(self, db: DatabaseConnection) -> None:
        self._db = db
    
    def get_vm(self, vm_id: str) -> Optional[VMInstance]:
        """Return VM by ID or None."""
        ...
    
    def list_vms(self) -> list[VMInstance]:
        """Return all VMs."""
        ...
    
    def upsert_vm(self, vm: VMInstance) -> None:
        """Insert or update VM record."""
        ...
    
    def delete_vm(self, vm_id: str) -> None:
        """Delete VM by ID."""
        ...
```

**Repository Usage in Domain:**
```python
# core/vm/_service.py
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core._internal._db import get_db_connection

class VMService:
    def list_vms(self) -> list[VMInstance]:
        db = get_db_connection()
        repo = VMRepository(db)
        return repo.list_vms()
```

## Import Boundaries (Enforced)

```python
# ✅ CLI - ONLY imports api
from mvmctl.api import vm, network

# ✅ API - ONLY re-exports from core
from mvmctl.core.vm import VMController, VMService
from mvmctl.core._orchestration import vm_operations

# ✅ Domain - ONLY imports _internal
from mvmctl.core._internal import MVMDatabase
from mvmctl.core._internal import IPTablesTracker  # OK: generic infrastructure

# ❌ FORBIDDEN - Domains never import other domains or orchestration
# In core/vm/_controller.py:
from mvmctl.core.network import NetworkController       # NEVER
from mvmctl.core._orchestration import create_vm        # NEVER
from mvmctl.core.image import ImageManager              # NEVER

# ✅ Orchestration - ONLY place that imports multiple domains + _internal
# In core/_orchestration/vm_operations.py:
from mvmctl.core.vm import VMController, VMBuilder
from mvmctl.core.network import NetworkController
from mvmctl.core.image import ImageManager
from mvmctl.core.kernel import KernelService
# from mvmctl.core.cloudinit import CloudInitController  # If cloudinit were a domain
from mvmctl.core._internal import MVMDatabase
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
| **Stateless resource ops** | `Service` | `core/{domain}/` | `VMService`, `ImageService` | CRUD operations, search, list |
| **Cross-domain workflow** | `_operations.py` | `core/_orchestration/` | `vm_operations.py`, `host_operations.py` | Functions importing multiple domains |
| **Shared infrastructure** | None | `core/_internal/` | `MVMDatabase`, `VMResolver`, `IPTablesTracker` | No domain knowledge, reusable utilities |
| **Domain-specific helpers** | `Manager` or descriptive | `core/{domain}/` | `NetworkIPLeaseManager` | Domain-specific but reusable within domain |

## Public API Example

API layer is thin curation - no business logic:

```python
# api/vm.py
from mvmctl.core._orchestration.vm_operations import create_vm, remove_vm
from mvmctl.core.vm import VMController, VMService

__all__ = [
    "VMController",   # Stateful: stop, start, pause, ssh (from domain)
    "VMService",      # Stateless: list, search, exists (from domain)
    "create_vm",      # Orchestrated creation (from _orchestration)
    "remove_vm",      # Orchestrated removal (from _orchestration)
]
```

CLI consumes only this public surface:

```python
# cli/vm.py
from mvmctl.api import vm

# Single domain operation - uses Controller directly
controller = vm.VMController("myvm")
controller.stop()

# Orchestrated operation - uses orchestration function
vm.create_vm(name="newvm", image="ubuntu-24.04")  # Orchestrates 6 domains
```

## Domain Growth Patterns

### Adding New Capabilities to a Domain

When a domain grows, add files following the naming convention:

```
core/vm/
├── _controller.py          # VMController (primary lifecycle)
├── _service.py             # VMService
├── _firecracker.py         # FirecrackerController (process management)
├── _console.py             # ConsoleController (relay management)
├── _snapshot.py            # SnapshotController (if large enough)
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

## Summary

- **Domains ≠ CLI commands** - Domains are business capabilities (vm, network, image, etc.)
- **CLI commands trigger orchestration** - `vm create` orchestrates 6 domains
- **Orchestration rule** - If code imports multiple domains → `core/_orchestration/`
- **Domain isolation** - Domains only import `core/_internal/`, never other domains
- **Infrastructure placement** - Generic → `_internal/`, domain-specific → `{domain}/`
- **Naming** - `Controller` (stateful), `Service` (stateless), `_operations.py` (cross-domain)
- **Input resolution** - `Request` (raw) → `validate()` → `resolve()` → `ResolvedRequest` (frozen)
