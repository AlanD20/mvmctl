# mvmctl Package Root

Main Python package for the `mvmctl` microVM CLI. This root orchestrates the lazy loading of commands and provides the foundational constants and exception hierarchy used across all layers.

## STRUCTURE

| Directory | Purpose |
|-----------|---------|
| `cli/` | Thin Typer command definitions and CLI-only logic |
| `api/` | Stable public API boundary with privilege checks |
| `core/` | Isolated business logic and Firecracker interactions |
| `models/` | Pure `@dataclass` objects for domain data |
| `utils/` | Shared tool wrappers and filesystem helpers |
| `assets/` | Bundled YAML configs (images, kernels) |
| `services/` | Long-running subprocesses (console relay, nocloud server, loopmount provisioner, mvm-provision) |
| `db/` | SQLite schema, migrations, and ORM models |

## WHERE TO LOOK

- `main.py` — Entry point. Implements `LazyMVMGroup` (Click) to lazy-load Typer sub-apps.
- `constants.py` — Single source of truth. Defines `OVERRIDABLE_DEFAULTS` dict and standalone constants.
- `exceptions.py` — Typed exception hierarchy. All custom errors inherit from `MVMError`.
- `__init__.py` — Package metadata. Contains only `__version__`.

## CONVENTIONS

### 3-Layer Architecture (CLI → API → Core)
All code must follow the strict flow: **CLI → API → Core**.
- **CLI** (`cli/`) — resolves runtime defaults, calls the API via `from mvmctl.api import *`.
- **API** (`api/`) — public Python API surface. Performs privilege checks, resolves DB-backed defaults, and sequences multiple Core domains. ALL orchestration lives here. `api/__init__.py` re-exports all public types.
- **Core** (`core/`) — isolated domain logic. NEVER imports from other core domains. Returns `*Item` models only.
- **Models** (`models/`) — pure `@dataclass` data containers with `*Item` suffix (e.g., `VMInstanceItem`, `NetworkItem`). No business logic.

### API Public Surface
All public types are re-exported from `api/__init__.py`:
```python
from mvmctl.api import VMOperation, VMCreateInput
VMOperation.create(VMCreateInput(name="my-vm", ...))
```

### CLIs Import Pattern
CLI imports from the public `api/` package:
```python
from mvmctl.api import VMOperation, VMInput
from mvmctl.api import HostOperation
```

### Import Rules
- Use absolute imports: `from mvmctl.core.vm import VMController`.
- Avoid circular dependencies by keeping Core modules isolated.
- The API layer is the only place where multiple Core modules should be imported and sequenced together.
- Core domains never import from other core domains.
- Core modules return `*Item` models only (no Config/Input classes in core).

### Default Resolution
Defaults must be resolved at runtime in the CLI layer. Never hardcode `DEFAULT_*` values in `typer.Option` or function signatures in API/Core layers.
