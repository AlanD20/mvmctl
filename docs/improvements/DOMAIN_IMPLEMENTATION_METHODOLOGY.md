# Domain Implementation Methodology

## Overview

This document defines the standard process for implementing domain controllers and services in mvmctl. The methodology ensures:
- **No ambiguity** in AI execution
- **Traceable** archive → plan → implementation flow
- **Consistent** patterns across all domains
- **User approval** at each critical phase

---

## Core Principle: One Domain at a Time

Never mix domain implementations. Each domain (network, image, kernel, binary, etc.) follows this complete lifecycle before moving to the next.

---

## Architecture Rules (MANDATORY)

### Rule 1: Core Returns DB Models Only

**Core domain classes (Controller, Service) MUST return `*Item` dataclasses (DB models), NOT custom Config/Input classes.**

```
❌ WRONG:  Controller.get() → NetworkConfig
✅ CORRECT: Controller.get() → NetworkItem
```

The `*Item` classes (e.g., `NetworkItem`, `VMInstanceItem`) are the single source of truth for domain data. They live in `models/` and map directly to DB records. Any custom data shapes (Config, Input, Request) belong in the API layer.

### Rule 2: API Layer Data Flow (Input → Request → Resolved → Operation)

The API layer has a precise data flow pattern for handling user input. This pattern applies to ALL domains (VM, network, image, kernel, etc.). The word "VM" below is a placeholder for any domain resource.

#### Two Input Categories

**Category A: Existing Resource Actions** (remove, ssh, console, get, list, inspect)

When the user wants to act on an EXISTING resource, the flow is:

```
CLI → VMInput → VMOperation.rm(input) → VMRequest(input, db).resolve()
                                              ↓
                                    ResolvedVMInput (frozen, validated)
                                              ↓
                                    Operation acts on resolved data
```

- **`VMInput`** — Raw identifiers from CLI (name, id, IP, MAC). Used to identify WHICH existing resource to act on. This is a thin dataclass with list fields for identifiers plus optional flags (like `force`).
- **`VMRequest`** — Takes `VMInput` + `db`. Has `resolve()` method that resolves identifiers to actual DB records. Calls `ensure_validate()` internally after resolution.
- **`ResolvedVMInput`** — Frozen dataclass containing fully resolved DB records (`vms: list[VMInstanceItem]`, `force: bool`). These records are guaranteed to exist in the DB, making them safe to operate on.
- **`VMOperation`** — Static methods like `rm()`, `ssh()`, `console()` take `VMInput` as first argument. They create a `VMRequest`, call `resolve()`, and use `ResolvedVMInput` to perform the action.

**Category B: Resource Creation** (create)

When the user wants to CREATE a new resource, the flow is:

```
CLI → VMCreateInput → VMOperation.create(input) → VMCreateRequest(input, db).resolve()
                                                        ↓
                                              ResolvedVMCreateInput (frozen, validated)
                                                        ↓
                                              Operation creates the resource
```

- **`VMCreateInput`** — Raw creation parameters from CLI (name, vcpu_count, mem_size_mib, etc.). Optional fields are `None` — defaults are resolved by the Request.
- **`VMCreateRequest`** — Takes `VMCreateInput` + `db`. Has `resolve()` method that resolves DB-backed defaults (default image, default kernel, default network, etc.) and calls `ensure_validate()` internally.
- **`ResolvedVMCreateInput`** — Frozen dataclass with ALL values resolved and validated. No `None` values for required fields. This is the sanitized input that `VMOperation.create()` uses to create the resource.

#### Key Principles

1. **`resolve()` always calls `ensure_validate()`** — Validation happens AFTER resolution, not before. You validate the resolved result, not the raw input.
2. **`ResolvedVM*` classes are frozen** — They are immutable once created. This prevents accidental mutation during orchestration.
3. **`VMOperation` methods are `@staticmethod`** — They take Input classes as arguments and create Request/Resolved internally.
4. **Input classes have `None` for optional fields** — The CLI layer passes what the user provides. The Request layer resolves `None` to DB-backed defaults.
5. **Resolved classes have NO `None` for required fields** — All values are explicit and validated.

#### File Organization

```
api/inputs/
├── _vm_input.py              # VMInput, VMRequest, ResolvedVMInput
├── _vm_create_input.py       # VMCreateInput, VMCreateRequest, ResolvedVMCreateInput
├── _network_input.py         # NetworkInput, NetworkRequest, ResolvedNetworkInput
├── _network_create_input.py  # NetworkCreateInput, NetworkCreateRequest, ResolvedNetworkCreateRequest
└── ...

api/
├── vm_operations.py          # VMOperation (create, remove, list, get, etc.)
├── network_operations.py    # NetworkOperation (create, remove, list, get, etc.)
└── ...
```

#### Reference Implementation

See `src/mvmctl/api/inputs/_vm_input.py` for existing resource actions and `src/mvmctl/api/inputs/_vm_create_input.py` for creation flows.

### Rule 3: Controller Is Stateful, Returns Item Only

```python
class NetworkController:
    def __init__(self, entity: str | NetworkItem, repo: NetworkRepository) -> None:
        # Resolve entity, store as self._network

    def get(self) -> NetworkItem:        # Returns DB model
    def set_default(self) -> None:        # Updates DB
    def get_leases(self) -> list[NetworkLeaseItem]:  # Returns DB models
```

Controller does NOT have `create()`, `remove()`, `list()`, or `inspect()`. Those are orchestration methods that belong in `*Operation` at the API layer.

### Rule 4: Service Is Stateless Infrastructure

```python
class NetworkService:
    # Infrastructure methods (already exist, keep as-is)
    def ensure_bridge(self, bridge, subnet) -> None: ...
    def remove_bridge(self, bridge) -> None: ...
    def ensure_nat(self, bridge, nat_gateways, *, subnet) -> None: ...
    def remove_nat(self, bridge, nat_gateways, *, subnet) -> None: ...
    def ensure_tap(self, tap, bridge) -> None: ...
    def remove_tap(self, tap, bridge) -> None: ...
    def initialize(self) -> None: ...               # iptables chains
    def bridge_exists(self, bridge) -> bool: ...
    def get_physical_interfaces(self) -> list[str]: ...
    # ... etc
```

Service handles infrastructure (bridges, TAPs, NAT, iptables). It does NOT handle CRUD orchestration.

### Rule 5: Operation Class Is API-Layer Orchestration

```python
# In api/network_operations.py
class NetworkOperation:
    # Category A: Existing resource actions — take NetworkInput
    @staticmethod
    def remove(inputs: NetworkInput) -> None: ...
    
    @staticmethod
    def get(inputs: NetworkInput) -> NetworkItem: ...
    
    @staticmethod
    def list(inputs: NetworkInput) -> list[NetworkItem]: ...
    
    @staticmethod
    def inspect(inputs: NetworkInput) -> NetworkItem: ...
    
    # Category B: Resource creation — takes NetworkCreateInput
    @staticmethod
    def create(inputs: NetworkCreateInput) -> NetworkItem: ...
    
    # Other orchestration methods
    @staticmethod
    def ensure_default() -> NetworkItem: ...
    
    @staticmethod
    def reconcile() -> list[NetworkItem]: ...
    
    @staticmethod
    def restore() -> list[str]: ...
```

Operation methods are `@staticmethod` — they take Input classes as arguments, create Request/Resolved internally, and orchestrate across multiple core modules.

### Rule 6: Validation Goes in Request Classes, Not Service

```python
# In api/inputs/_network_input.py
class NetworkCreateRequest:
    def resolve(self) -> ResolvedNetworkCreateRequest: ...
    def ensure_validate(self) -> None:
        # _validate_subnet_no_overlap()
        # _validate_bridge_not_conflicting()
        # validate_entity_name()
        # validate_subnet()
```

Validation that requires DB queries (like checking for subnet overlap) belongs in the Request resolver, NOT in Service static methods.

### Rule 7: Single Data Model Per Domain

**Avoid creating multiple data classes for the same domain.** Use `*Item` as the canonical model. If runtime state is needed (like `bridge_exists`), add it as an optional field on the `*Item` class or use the resolver enrichment pattern.

```
❌ WRONG:  NetworkConfig + NetworkItem + NetworkInspectInfo (3 classes)
✅ CORRECT: NetworkItem (1 class, with optional relation fields for enrichment)
```

If `NetworkItem` needs additional runtime data (like `bridge_exists` or enriched VM info), add optional fields:

```python
@dataclass
class NetworkItem:
    # DB fields...
    bridge_active: bool
    
    # Enriched relations (loaded by resolver when needed)
    leases: list[NetworkLeaseItem] | None = None
    iptables_rules: list[IPTablesRuleItem] | None = None
```

### Rule 8: LeaseService Takes Repository as Required Parameter

```python
# WRONG:
class LeaseService:
    def __init__(self, entity, db=None) -> None:
        self._db = db or Database()

# CORRECT:
class LeaseService:
    def __init__(self, entity: str | NetworkItem, repo: LeaseRepository) -> None:
        self._lease_repo = repo
```

No `db=None` fallbacks. The caller must provide the repository. This follows the same pattern as Controller taking `repo` as required.

### Rule 9: No `list[dict]` — Use Proper Models

**Never use `list[dict[str, Any]]` when a proper `*Item` dataclass exists.** If you need to represent VM lease data, use `NetworkLeaseItem`. If you need VM status data, use `VMInstanceItem` or create a proper dataclass.

```
❌ WRONG:  vms: list[dict[str, Any]]  # vm_id, ipv4, status, pid
✅ CORRECT: vms: list[NetworkLeaseItem]  # proper DB model with typed fields
```

---

## The Five-Phase Workflow

### Phase 1: Archive Consolidation

**Objective:** Gather all existing domain code from `archive/` into numbered `_archive-*.py` files.

**Process:**
1. Call `@code-consolidator` agent with domain-specific prompt
2. Agent discovers archive location and extracts all domain-related code
3. Code dumped into `src/mvmctl/core/{domain}/_archive-*.py` files
4. Existing working files (`_controller.py`, `_repository.py`, `_service.py`, `_resolver.py`) are **preserved untouched**

**Critical Rules:**
- ❌ DO NOT modify existing files in the domain directory
- ❌ DO NOT attempt implementation during this phase
- ❌ DO NOT make assumptions about what code does — dump first, analyze later
- ✅ Include ALL related subdomains (e.g., network includes leases, iptables, bridging)
- ✅ Archive files are raw dumps — do not cut mid-function to hit line limits
- ✅ If a function spans ~1.5k lines, complete it fully in that file

**Code-Consolidator Prompt Template:**
```
Consolidate all [DOMAIN] domain operations from the archive into
src/mvmctl/core/[domain]/_archive-01.py, _archive-02.py, etc.

Include:
- CRUD operations: create, remove, get, list
- Supporting helpers and utilities
- Subdomain code (leases, iptables, bridging, etc.)
- Any related configuration or validation logic

Each file should be ~1k lines but DO NOT cut mid-function. Complete the
function and then continue to the next file. The agent will discover the
archive location automatically. Preserve all function signatures and logic
exactly as-is.
```

---

### Phase 2: Operation Identification

**Objective:** Catalog all operations discovered in the archived code.

**Process:**
1. Orchestrator reads all `_archive-*.py` files
2. Identify all operations and categorize them:
   - **CRUD operations:** create, remove, get, list
   - **Supporting operations:** validation, formatting, state transitions
   - **Subdomain operations:** lease management, iptables rules, etc.
3. Cross-reference with `_excluded.py` to identify what's already implemented

**Output Format:**
```
## [Domain] Operations Catalog

### CRUD Operations
| Operation | Location | Description |
|-----------|----------|-------------|
| create_network | _archive-01.py:45-120 | Creates bridge and allocates subnet |
| remove_network | _archive-01.py:200-280 | Tears down bridge and releases IPs |
| get_network | _archive-02.py:50-90 | Retrieves network by ID or name |
| list_networks | _archive-02.py:150-220 | Lists all networks with filtering |

### Supporting Operations
| Operation | Location | Description |
|-----------|----------|-------------|
| validate_network_config | _archive-01.py:300-340 | Validates CIDR and gateway |
| allocate_ip | _archive-02.py:400-450 | Allocates IP from subnet pool |

### Already Implemented (excluded from migration)
| Operation | Implemented By | Method |
|-----------|---------------|--------|
| setup_bridge | NetworkService | ensure_bridge() |
| teardown_bridge | NetworkService | remove_bridge() |
```

**Critical Rules:**
- ❌ DO NOT plan implementation during this phase
- ❌ DO NOT skip any operation — catalog everything
- ✅ Be exhaustive — missing an operation now means it gets lost later
- ✅ Cross-reference with `_excluded.py` to avoid duplicating work

---

### Phase 3: Implementation Planning

**Objective:** Create a detailed, executable plan that mirrors reference patterns.

**Reference Pattern:** Always mirror `VMController` + `VMService` + `VMOperation` from the VM domain.

**Architecture Layer Map:**

```
┌─────────────────────────────────────────────────────────────┐
│ API Layer (api/)                                            │
│                                                             │
│  api/inputs/_network_create_input.py                        │
│    NetworkCreateInput      — raw CLI input                  │
│    NetworkCreateRequest    — resolves DB defaults            │
│    ResolvedNetworkCreateInput — frozen, all values set       │
│                                                             │
│  api/network_operations.py                                  │
│    NetworkOperation        — orchestration (create, remove,  │
│                              list, get, inspect, etc.)       │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│ Core Layer (core/)                                          │
│                                                             │
│  core/network/_controller.py                                │
│    NetworkController       — stateful, single entity         │
│    (entity: str | NetworkItem, repo: NetworkRepository)      │
│    Returns: NetworkItem                                     │
│                                                             │
│  core/network/_service.py                                   │
│    NetworkService          — stateless infrastructure        │
│    (ensure_bridge, ensure_nat, ensure_tap, etc.)            │
│                                                             │
│  core/network/_repository.py                                │
│    NetworkRepository       — DB operations                  │
│                                                             │
│  core/network/_resolver.py                                  │
│    NetworkResolver         — entity resolution              │
│                                                             │
│  core/network/_lease_service.py                             │
│    LeaseService            — IP lease lifecycle              │
│    (entity: str | NetworkItem, repo: LeaseRepository)        │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│ Models Layer (models/)                                      │
│                                                             │
│  models/network.py                                          │
│    NetworkItem             — DB record (single source)      │
│    NetworkLeaseItem        — lease record                   │
│    IPTablesRuleItem        — iptables record                │
│    (NO NetworkConfig, NO NetworkInspectInfo here)           │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

**Planning Steps:**

1. **Map Operations to Layers**
   ```
   API Layer (api/network_operations.py):
   - NetworkOperation.create()  → uses NetworkCreateRequest + NetworkService
   - NetworkOperation.remove()  → uses NetworkController + NetworkService
   - NetworkOperation.list()    → uses NetworkRepository
   - NetworkOperation.get()     → uses NetworkController
   - NetworkOperation.inspect() → uses NetworkController + LeaseService
   - NetworkOperation.ensure_default() → creates default network
   - NetworkOperation.reconcile() → compares DB vs actual state
   - NetworkOperation.restore() → restores networks after reboot

   Core Layer (core/network/_controller.py):
   - NetworkController.get()         → returns NetworkItem
   - NetworkController.set_default()  → updates DB
   - NetworkController.get_leases()   → returns list[NetworkLeaseItem]

   Core Layer (core/network/_service.py):
   - NetworkService.ensure_bridge()   → infrastructure
   - NetworkService.ensure_nat()      → infrastructure
   - NetworkService.ensure_tap()      → infrastructure
   - (all existing methods preserved)
   ```

2. **Define Input/Request Classes**
   ```
   api/inputs/_network_input.py:
   
   NetworkCreateInput:
     name: str
     subnet: str
     ipv4_gateway: str | None
     nat: bool
     nat_gateways: list[str] | None
   
    NetworkCreateRequest:
      def __init__(self, inputs: NetworkCreateInput, db: Database) -> None:
      def resolve(self) -> ResolvedNetworkCreateInput:
      def ensure_validate(self) -> None:
        - validate_entity_name()
        - validate_subnet()
        - _validate_subnet_no_overlap()
        - _validate_bridge_not_conflicting()
    
    ResolvedNetworkCreateInput:
      name: str
      subnet: str
      ipv4_gateway: str
      bridge: str
      nat_enabled: bool
      nat_gateways: list[str]
      network_id: str
      created_at: str
   ```

3. **Identify What Stays in Core vs Moves to API**
   - Validation (subnet overlap, bridge conflict) → API layer (Request class)
   - CRUD orchestration (create, remove) → API layer (Operation class)
   - Infrastructure (bridge, NAT, TAP) → Core (Service class)
   - Entity lifecycle (get, set_default) → Core (Controller class)

4. **Plan Import Dependencies**
   - What does Operation import from Core?
   - What does Controller import from Repository?
   - What does Service import from utils?

**Critical Rules:**
- ❌ DO NOT spawn implementation agent during this phase
- ❌ DO NOT skip reference pattern analysis
- ❌ DO NOT make decisions without user approval
- ✅ Plan must reference VMController/VMService/VMOperation as the pattern to follow
- ✅ Plan must be detailed enough to execute without further clarification
- ✅ Core classes return `*Item` models only — no Config/Input classes in core

---

### Phase 4: User Approval

**Objective:** Ensure plan is correct before implementation begins.

**User must explicitly approve:**
- Class structure (Controller/Service/Operation split)
- Operation migration map
- Input/Request class design
- File structure after implementation
- Any risky decisions or trade-offs

**User may request:**
- Changes to class responsibilities
- Different operation mappings
- Additional research before approval
- Reference to other patterns

**Only proceed to Phase 5 after explicit user approval** (must say "yes, proceed" or equivalent).

---

### Phase 5: Implementation

**Objective:** Execute the approved plan using `@refactor-engineer`.

**Process:**
1. Spawn `@refactor-engineer` with complete context:
   - Approved plan document
   - Reference patterns (VMController/VMService/VMOperation)
   - Source files (archived code)
   - Target files (new implementation)
   - Constraints and rules

2. Implementation follows plan exactly

3. Verification:
   - Ruff linting passes
   - Ruff formatting passes
   - Type checking passes
   - ❌ **NO TESTS RUN** — During active migration, all tests are false positives and broken. Do not run the test suite.

**Critical Rules:**
- ❌ DO NOT deviate from approved plan without user approval
- ❌ DO NOT skip verification steps
- ❌ DO NOT run tests — they are broken during migration
- ✅ Follow VMController/VMService/VMOperation patterns exactly
- ✅ Preserve existing working files (Repository, Resolver, etc.)
- ✅ Core classes return `*Item` models only
- ✅ Validation goes in Request classes, not Service
- ✅ Orchestration goes in Operation classes, not Controller

---

## Reference: VMController + VMService + VMOperation Pattern (Source of Truth)

**IMPORTANT:** These examples are guidance for the first step — they are NOT strict boundaries. If a better approach exists, we will use it. Resource limitations and optimization are top priority. Over-engineering is unacceptable.

### VMController (Stateful) — ACTUAL IMPLEMENTATION

From `src/mvmctl/core/vm/_controller.py` lines 39-58:

```python
class VMController:
    """Stateful VM lifecycle manager.

    Resolves VM entity in __init__ and operates on cached VM instance.
    """

    def __init__(
        self,
        entity: str | VMInstanceItem,
        repo: VMRepository,
    ) -> None:
        from mvmctl.core.vm._resolver import VMResolver

        self._repo = repo

        if isinstance(entity, VMInstanceItem):
            self._vm = entity
        else:
            self._resolver = VMResolver(self._repo)
            self._vm = self._resolver.resolve(entity)
```

**Key Pattern Points:**
- Constructor accepts `entity: str | VMInstanceItem` and `repo: VMRepository`
- If entity is already the model object, use it directly (no resolver needed)
- If entity is a string (name/id), create Resolver internally and resolve
- `_resolver` is only created when needed (lazy initialization pattern)
- `self._vm` holds the resolved entity for all operations to use
- **Returns `VMInstanceItem`** — the DB model, not a Config class

### VMOperation (API Layer Orchestration) — ACTUAL IMPLEMENTATION

From `src/mvmctl/api/vm_operations.py`:

**Category A: Existing resource actions (rm, ssh, console, etc.)**
```python
class VMOperation:
    @staticmethod
    def remove(inputs: VMInput) -> None:
        # 1. Resolve identifiers to DB records
        db = Database()
        resolver = VMRequest(inputs=inputs, db=db)
        resolved = resolver.resolve()  # Returns ResolvedVMInput
        # 2. Act on resolved DB records
        VMService(db).stop_many(resolved.vms, force=resolved.force)
        # 3. Cleanup and persist
```

**Category B: Resource creation**
```python
class VMOperation:
    @staticmethod
    def create(inputs: VMCreateInput) -> None:
        # 1. Resolve DB-backed defaults
        db = Database()
        resolver = VMCreateRequest(vm_id=ctx.vm_id, vm_dir=ctx.vm_dir, inputs=inputs, db=db)
        resolved = resolver.resolve()  # Returns ResolvedVMCreateInput
        # 2. Act on resolved and validated data
        ctx.execute()
        # 3. Persist to DB
        vm_repo.upsert(vm_instance)
```

**Key Pattern Points:**
- `@staticmethod` methods — no instance state
- Category A methods take `VMInput` (identifiers for existing resources)
- Category B methods take `VMCreateInput` (creation parameters)
- Both create a `*Request` internally, call `resolve()`, and use `Resolved*` to act
- `resolve()` always calls `ensure_validate()` internally
- Returns DB models (`*Item` classes)

### VMCreateInput + VMCreateRequest (API Layer — Resource Creation) — ACTUAL IMPLEMENTATION

From `src/mvmctl/api/inputs/_vm_create_input.py`:

```python
@dataclass
class VMCreateInput:
    """Input model for VM creation — replaces 31 function parameters."""
    name: str
    vcpu_count: int
    mem_size_mib: int
    ssh_keys: list[str]
    # Optional fields (DB-backed at API layer)
    user: str | None
    enable_pci: bool | None
    # ... etc

@dataclass(frozen=True)
class ResolvedVMCreateInput:
    """Immutable resolved inputs - output of VMCreateRequest."""
    name: str
    vm_id: str
    vm_dir: Path
    vcpu_count: int
    mem_size_mib: int
    user: str
    network: NetworkItem
    image: ImageItem
    kernel: KernelItem
    binary: BinaryItem
    # ... all values resolved, no None for required fields

class VMCreateRequest:
    """Resolve all DB-backed defaults using a single DB instance."""
    def __init__(self, *, vm_id, vm_dir, inputs: VMCreateInput, db) -> None:
        # Initialize resolvers

    def resolve(self) -> ResolvedVMCreateInput:
        # Resolve all DB-backed defaults (image, kernel, network, binary, keys)
        # Build ResolvedVMCreateInput
        self.ensure_validate()  # Always called by resolve()
        return self._result

    def ensure_validate(self) -> None:
        # Validate resolved values (ranges, file existence, etc.)
```

**Key Pattern Points:**
- `VMCreateInput` — raw user input, `None` for optional fields
- `VMCreateRequest` — resolves DB-backed defaults (default image, kernel, network, etc.)
- `resolve()` always calls `ensure_validate()` — validation happens AFTER resolution
- `ResolvedVMCreateInput` — frozen dataclass, ALL values explicit, no `None` for required fields
- Used for: `create` only

### VMService (Stateless Bulk Operations) — Pattern Reference

```python
class VMService:
    """Stateless VM operations coordinator.

    Handles bulk operations and delegates single-VM operations to Controller.
    """

    # FIXME: take repo instead of db
    def __init__(self, db: Database) -> None:
        self._db = db
        self._repo = VMRepository(self._db)
        self._executor = ParallelExecutor()

    def stop(self, vm: VMInstanceItem, force: bool = False) -> None:
        controller = VMController(entity=vm, repo=self._repo)
        controller.stop(force=force)

    def stop_many(
        self, vms: list[VMInstanceItem], force: bool = False, ...
    ) -> BulkResult[VMInstanceItem]:
        raw = self._executor.execute(
            items=vms,
            func=lambda vm: self.stop(vm, force=force),
            ...
        )
        return BulkResult(...)

    # Same pattern for: start/start_many, pause/pause_many,
    # resume/resume_many, reboot/reboot_many
```

### Repository Pattern — ACTUAL IMPLEMENTATION

```python
class VMRepository:
    """Database operations for VM instances."""

    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    def get(self, vm_id: str) -> VMInstanceItem | None:
        """Return a VM by its full 64-char ID, or None if not found."""

    def get_by_name(self, name: str) -> VMInstanceItem | None:
        """Return a VM by name, or None if not found."""

    def find_by_prefix(self, prefix: str) -> list[VMInstanceItem]:
        """Return all VMs whose ID starts with prefix."""

    def list_all(self) -> list[VMInstanceItem]:
        """Return all VMs."""

    def count(self) -> int:
        """Total count using SQL COUNT."""

    def count_by_status(self, status: VMStatus | list[VMStatus]) -> int:
        """Count by status using SQL COUNT + WHERE IN."""

    def upsert(self, vm: VMInstanceItem) -> None:
        """Insert or update VM record."""

    def delete(self, vm_id: str) -> None:
        """Delete VM by ID."""
```

---

## Common Mistakes to Avoid

| Mistake | Why It's Wrong | Correction |
|---------|----------------|------------|
| Starting implementation before archive dump is complete | Missing context leads to gaps | Always dump first |
| Skipping operation cataloging | Operations get lost or mis-mapped | Be exhaustive |
| Not referencing existing patterns | Inconsistent architecture | Always mirror VM pattern |
| Proceeding without user approval | Plan may have flaws | Get explicit approval |
| Modifying existing files during dump | Working code gets corrupted | Preserve all existing files |
| Mixing domains in one implementation | Confusion and cross-contamination | One domain at a time |
| Cutting functions mid-way to hit line limits | Broken code in archive files | Complete functions fully, then split |
| Over-engineering | Waste of resources | Simple, pragmatic solutions only |
| Returning Config/Input classes from Core | Violates layer boundary | Core returns `*Item` DB models only |
| Putting validation in Service | Validation needs DB queries, belongs in API | Put in `*Request.ensure_validate()` |
| Putting CRUD orchestration in Controller | Controller is stateful, single-entity | Put in `*Operation` at API layer |
| Creating `list[dict]` instead of `*Item` | Loses type safety | Use proper `*Item` dataclasses |
| Making repo parameter optional in Service/LeaseService | Hides dependency, makes testing harder | Require repo as explicit parameter |
| Creating multiple data classes for same domain | Confusion and duplication | Use single `*Item` model with optional enrichment fields |

---

## Decision Threshold

**When uncertain or below 95% certainty:**
- STOP
- Ask for clarification or more context
- Do NOT proceed until certainty is ≥95%

**When 95%+ certainty is reached:**
- Proceed to next step confidently

---

## Document Version

- **Created:** 2026-04-19
- **Updated:** 2026-04-30 — Fixed VMService pattern (actual bulk operations coordinator), Repository pattern types (VMInstanceItem, list_all, count_by_status), and standardized Resolved naming convention (Resolved*Input)
- **Purpose:** Generic domain implementation methodology for mvmctl
- **Reference Domain:** network (first application)