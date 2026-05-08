---
name: architect
version: 1.0.0
description: Guide architecture-aligned design decisions for mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "architecture", "design"]
  workflow: development
---

## What I do

I guide you in making architecture-aligned design decisions:

- **Boundary consciousness** — Every layer has a sacred purpose; respect the walls between them
- **Flow fidelity** — Data MUST travel the correct path; never skip layers
- **Import integrity** — Prevent circular dependencies before they breed
- **Configuration hierarchy** — Know which source of truth dominates
- **Responsibility clarity** — Each module answers to exactly one master
- **Database sovereignty** — ONLY the API layer may query the database

## When to use me

Use me when designing new features, planning refactors, or deciding where to place new code.

I am NOT for code review — use `@.agents/skills/code-review/` skill for that.

## Core Principles

### Principle 1: RESPECT THE LAYER WALLS

Each layer has a SACRED purpose. You MUST know which layer owns what:

| Layer | It IS | It IS NOT | Database Access |
|-------|-------|-----------|-----------------|
| **cli/** | The voice — speaks to the user | The brain — does not compute | **NO** — passes `None` or explicit values |
| **api/** | The gatekeeper — checks privileges + queries DB | The executor — does not perform | **YES** — ONLY layer that queries DB |
| **core/** | The workhorse — all business logic | The speaker — does not print | **NO** — receives explicit values |
| **models/** | The data — pure containers | The actor — has no side effects | **NO** — leaf node |
| **utils/** | The tools — shared helpers | The decider — has no domain knowledge | **NO** — leaf node |
| **db/** | The schema — SQL definitions | The querier — no business logic | **DEFINITION ONLY** — API does queries |

**MEMO**: "Ask not what the code does — ask which LAYER owns it."

### Principle 2: DATA MUST FLOW DOWNWARD

The path of least resistance is NOT the correct path. Data MUST travel:

```
User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

Skipping layers is architectural debt. If you see:
- `cli/` importing from `core/` → VIOLATION
- `core/` printing to console → VIOLATION
- `models/` making network calls → VIOLATION
- `cli/` querying the database → VIOLATION
- `core/` querying the database → VIOLATION

**MEMO**: "Downward only. Never climb upstream."

### Principle 3: DATABASE SOVEREIGNTY — ONLY API QUERIES THE DB

This is the MOST CRITICAL architectural rule:

**ONLY the API layer (`api/`) may query the database via the `Database` class (`core/_shared/_db.py`).**

| Layer | Database Query Policy | Example |
|-------|----------------------|---------|
| **CLI** | **ABSOLUTELY FORBIDDEN** | CLI passes `None` → API queries DB |
| **API** | **REQUIRED for DB-backed defaults** | API receives `None` → creates `Database()` → resolves via `{Domain}Request.resolve()` → passes explicit to Core |
| **Core** | **ABSOLUTELY FORBIDDEN** | Core receives explicit values from API |

**Why this matters:**
- CLI is a client layer — it shouldn't know about database implementation
- API owns the database boundary — it resolves all DB-backed defaults
- Core operates on explicit values — it has no database dependencies
- Testing is easier — mock API layer, not database in CLI/Core tests

**MEMO**: "The database is API's domain. All other layers are database-ignorant."

### Principle 4: IMPORT BOUNDARIES ARE FENCES, NOT SUGGESTIONS

Circular dependencies are the root of all architectural evil:

- `cli/` can ONLY import from `api/`
- `api/` can ONLY import from `core/`, `models/`, `utils/`
- `core/` can ONLY import from `models/`, `utils/`
- `models/` and `utils/` are LEAF NODES — they import nothing
- **NO layer imports from `cli/`** — one-way flow only

**Critical import rules:**
- CLI **NEVER** imports from `mvmctl.core` — uses `mvmctl.api` only
- Core **NEVER** imports from `db/` module — `Database` lives in `core/_shared/_db.py`, domain repos in `core/{domain}/_repository.py`
- **ONLY** API layer creates `Database` instances and orchestrates domain repositories

**MEMO**: "Import only from below. Never from beside. Never skip layers."

### Principle 5: TRUST THE CONFIGURATION HIERARCHY

When multiple sources claim to define a default, KNOW WHICH WINS:

```
1. (lowest) constants.py FALLBACK_* — the desperate last resort
2. SQLite database defaults — $MVM_CACHE_DIR/mvmdb.db (queried by API only)
3. State files — config.json for non-DB defaults
4. MVM_* environment variables — user override
5. (highest) CLI flags — explicit user intent
```

**MEMO**: "Higher authority always wins. The user knows best."

### Principle 6: EACH MODULE HAS ONE JOB

When placing code, ask: "What is this module's SOLE PURPOSE?"

**cli/ SOLE PURPOSE**: Parse args, format output, call api/
- Typer app with `no_args_is_help=True`, `rich_markup_mode=None`, `add_completion=False`
- Runtime defaults: `_defaults = _get_vm_defaults()` — NOT typer defaults
- **NO database queries** — pass `None` to API, let API resolve from DB
- NO business logic. NO print statements. NO subprocess.

**api/ SOLE PURPOSE**: Add privilege checks, query DB for defaults, delegate to core/, return results
- `HostPrivilegeHelper.check_privileges(binary, operation_description)` before ANY privileged operation
- **Query database** when CLI passes `None` for DB-backed defaults (image, kernel, binary, network)
- Pass explicit values to Core — never pass `None` for required parameters
- `__all__` exports only
- NO output formatting. NO business logic beyond DB queries.

**core/ SOLE PURPOSE**: Execute business logic, raise typed exceptions
- Return data OR raise MVMError subclasses
- Subprocess calls ONLY here (list form, NO shell=True)
- **NO database queries** — receive explicit values from API
- NO console output. NO privilege checks.

**db/ SOLE PURPOSE**: Define schema and manage migrations
- SQL migrations in `migrations/*.sql`
- Active `Database` class lives in `core/_shared/_db.py`
- Domain row dataclasses live in `models/` — not in `db/`
- **NO business logic** — API orchestrates, Repositories query

**models/ SOLE PURPOSE**: Contain data
- `@dataclass` ONLY
- `__post_init__` for validation
- NO subprocess, NO I/O, NO side effects

**utils/ SOLE PURPOSE**: Provide pure helpers
- No domain knowledge whatsoever
- Shared across all layers

**MEMO**: "One purpose. One reason to exist. If it needs two reasons, split it."

## Architecture Decision Protocol

### Before placing ANY code, answer:

1. **What is the user trying to do?** → cli/
2. **Does it need database access for defaults?** → api/ (queries DB, passes explicit to core/)
3. **Does it need privilege verification?** → api/ (adds check, then calls core/)
4. **What is the actual operation?** → core/
5. **Does it hold state or represent domain data?** → models/
6. **Is it a reusable pure function?** → utils/
7. **Does it define database schema?** → db/

### Database Query Decision Tree

```
Does the code need to query the database?
├── YES → Is it in api/ layer?
│   ├── YES → ✅ CORRECT — proceed with Database() + Domain resolution
│   └── NO (cli/ or core/) → ❌ VIOLATION — move to api/
└── NO → Does it receive values that might be None?
    ├── YES → Should those values come from DB?
    │   ├── YES → API must resolve before calling this code
    │   └── NO → Use config.json defaults in CLI
    └── NO → Pass explicit values
```

### Checklist (verify before committing):

- [ ] Which layer owns this logic? (cli/api/core/db/models/utils)
- [ ] Does data flow follow cli → api → core → models?
- [ ] cli/ imports ONLY from api/? (NEVER from core/ or db/)
- [ ] **NO database queries in cli/ or core/?**
- [ ] api/ adds `HostPrivilegeHelper.check_privileges()` before privileged ops?
- [ ] api/ queries DB when CLI passes `None` for DB-backed defaults?
- [ ] api/ passes explicit values to core/?
- [ ] **api/ every existing-resource operation uses `{Domain}Input` + `{Domain}Request.resolve()`?** (NO direct `Resolver.by_name()`, `Resolver.by_id()`, or `Repository.get()` calls in operation methods — the Input→Request→Resolved pipeline is the ONLY allowed resolution path)
- [ ] core/ raises typed exceptions, never prints?
- [ ] models/ is @dataclass ONLY, no side effects?
- [ ] NO hardcoded defaults (use FALLBACK_* in constants.py)?
- [ ] Env vars use `MVM_` prefix?
- [ ] Subprocess calls ONLY in core/?
- [ ] New exceptions extend MVMError hierarchy?

## Default Resolution Patterns

### Pattern 1: Config-Backed Defaults (config.json)

For defaults stored in `config.json` (e.g., vcpu_count, mem_mib):

```python
# cli/vm.py
@app.command()
def create(vcpus: Optional[int] = typer.Option(None, "--vcpus")):
    defaults = _get_vm_defaults()  # From config.json
    effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
    create_vm(vcpus=effective_vcpus)  # Pass explicit value
```

### Pattern 2: Database-Backed Defaults (mvmdb.db)

For defaults stored in SQLite (e.g., default image, kernel, binary):

```python
# cli/vm.py — CLI passes None
@app.command()
def create(image: Optional[str] = typer.Option(None, "--image")):
    create_vm(image=image)  # ✅ Passes None if user didn't specify

# api/vms.py — API creates Database + resolves via Input→Request pipeline
def create_vm(image: Optional[str] = None, ...) -> VMInstance:
    db = Database()
    inputs = VMCreateInput(image=image, ...)
    request = VMCreateRequest(inputs=inputs, db=db)
    resolved = request.resolve()  # Resolves DB-backed defaults
    
    HostPrivilegeHelper.check_privileges(...)
    return _core_create_vm(image=resolved.image, ...)  # Passes explicit value

# core/vm_lifecycle.py — Core receives explicit
def create_vm(image: str, ...) -> VMInstance:
    # image is guaranteed to be set
    ...
```

## Anti-Patterns (Forbidden)

| Forbidden Pattern | Why | Correct Approach |
|-------------------|-----|------------------|
| CLI querying database directly | CLI is client, not resolver | CLI passes `None`, API resolves via `{Domain}Request.resolve()` |
| Core importing `Database` from `core/_shared/_db.py` | Core should not depend on DB directly at runtime | API queries DB via repositories, passes explicit values |
| API function with default param (`def func(arg=DEFAULT)`) | Bypasses DB resolution | Use `Optional[T]` and resolve in function body |
| `typer.Option(DEFAULT_*, ...)` | Import-time evaluation | Use `typer.Option(None, ...)` + runtime resolution |
| Skipping API layer (cli → core) | Violates layer boundary | cli → api → core always |
| Core printing to console | Output belongs in CLI | Return data or raise exception |
| Models with side effects | Pure data only | Move logic to core/ |
| Bypassing the Input→Request→Resolved pipeline in API operations | Skips DB-backed default resolution, breaks ID-prefix resolution, bypasses validation | Use `{Domain}Input` + `{Domain}Request.resolve()` pipeline |
| Hardcoded paths | Breaks configurability | Use constants.py + env vars |

## (No Known Violations)

All CLI files now correctly import from `mvmctl.api` only. There are currently no tolerated import violations.

**MEMO**: "Keep it clean. Every violation is a debt that must be repaid."

## Entry Point Mental Model

```
main.py:LazyMVMGroup (click.Group)
├── _COMMAND_SPECS dict — deferred loading
├── get_command() — imports module only when called
└── Sub-apps via typer.main.get_command()
```

Think of it as a librarian who does not fetch books until you ask for them.

## Multi-AGENTS.md Awareness

This project has multiple AGENTS.md files defining architecture:

| AGENTS.md | Scope |
|-----------|-------|
| `/AGENTS.md` | Root orchestrator instructions, project overview |
| `src/mvmctl/cli/AGENTS.md` | CLI layer: no database queries, pass None to API |
| `src/mvmctl/api/AGENTS.md` | API layer: query DB when CLI passes None |
| `src/mvmctl/core/AGENTS.md` | Core layer: receive explicit values, no DB access |
| `src/mvmctl/db/AGENTS.md` | DB layer: schema only, API does queries |
| `src/mvmctl/models/AGENTS.md` | Models layer: dataclasses only |
| `src/mvmctl/utils/AGENTS.md` | Utils layer: pure helpers |

**When making architectural decisions, consult ALL relevant AGENTS.md files.**

## Quick Reference

| Question | Answer |
|----------|--------|
| New command? | cli/ calls api/ |
| Need DB default? | api/ creates Database() + resolves via {Domain}Request, passes explicit to core/ |
| Privileged op? | api/ checks privileges → calls core/ |
| Data container? | models/ @dataclass |
| Helper function? | utils/ (pure, no domain) |
| Database schema? | db/ migrations + models |
| Default value? | constants.py FALLBACK_* (last resort) |
| Config resolution? | `None` default → api/ queries DB → explicit to core/ |
| Subprocess? | core/ ONLY, list form, NO shell=True |
| Error handling? | Raise MVMError subclasses, never print |
