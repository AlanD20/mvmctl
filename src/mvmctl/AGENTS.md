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
| `services/` | Long-running subprocesses (console relay, etc.) |
| `db/` | SQLite schema, migrations, and ORM models |

## WHERE TO LOOK

- `main.py` — Entry point. Implements `LazyMVMGroup` (Click) to lazy-load Typer sub-apps.
- `constants.py` — Single source of truth. Defines `DEFAULT_*` and `FALLBACK_*` values.
- `exceptions.py` — Typed exception hierarchy. All custom errors inherit from `MVMError`.
- `__init__.py` — Package metadata. Contains only `__version__`.

## CONVENTIONS

### 4-Layer Architecture
All code must follow the strict flow: **CLI → API → Core → Models**.
- **CLI** resolves runtime defaults and calls the API.
- **API** performs privilege checks and sequences Core calls.
- **Core** executes isolated tasks without importing from other core modules.
- **Models** store pure state with no side effects.

### Import Structure
- Use absolute imports: `from mvmctl.core import vm_lifecycle`.
- Avoid circular dependencies by keeping Core modules isolated.
- The API layer is the only place where multiple Core modules should be imported and sequenced together.

### Default Resolution
Defaults must be resolved at runtime in the CLI layer. Never hardcode `DEFAULT_*` values in `typer.Option` or function signatures in API/Core layers.
