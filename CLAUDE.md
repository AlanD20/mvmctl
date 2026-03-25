# mvmctl — Claude Code Context

**Project:** Production-grade Python CLI for managing Firecracker microVMs  
**Stack:** Python 3.13, Typer, Rich, uv  
**CLI Entry:** `mvm` console script (defined in `pyproject.toml`)

> **Legacy bash scripts** are preserved in `legacy/` for reference.

## Quick Start

```bash
uv sync --group dev            # Install all deps
uv run pytest tests/ -x -q    # Run tests (stop at first failure)
uv run ruff check src/ && uv run mypy src/  # Lint + type check

# Build standalone binary
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# Output: dist/mvm
```

## Project Structure

```
src/mvmctl/
├── main.py          # Root Typer app; registers all sub-apps via add_typer()
├── constants.py     # Single source of truth — CLI name, env prefix, all defaults
├── exceptions.py    # Custom exception hierarchy (FCMError → domain subclasses)
├── cli/             # Thin Typer command definitions (no business logic)
├── api/             # Stable public Python API; adds privilege checks before core
├── core/            # All business logic, subprocess, Firecracker interaction
├── models/          # Pure dataclasses (VMInstance, VMConfig, ImageSpec, etc.)
├── utils/           # Shared helpers: console, process, fs, http, audit, validation
└── assets/          # Bundled YAML configs (images.yaml, kernel.yaml, defaults.yaml)
tests/               # Unit + integration test files
assets/              # Project-level YAML asset configs
docs/                # API and release docs
legacy/              # Archived bash scripts (single-vm, multi-vm, assets)
pyproject.toml       # Build, ruff, mypy strict, pytest (80% branch coverage gate)
```

## Data Flow

```
User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

## Key Files

| Task | Location |
|------|----------|
| VM lifecycle | `src/mvmctl/core/vm_lifecycle.py` |
| Network setup | `src/mvmctl/core/network.py`, `core/network_manager.py` |
| Host init | `src/mvmctl/core/host_setup.py` |
| Privilege checks | `src/mvmctl/core/host_privilege.py` |
| Asset metadata | `src/mvmctl/core/metadata.py` |
| Firecracker HTTP API | `src/mvmctl/core/firecracker.py` |
| CLI commands | `src/mvmctl/cli/` |
| Tests | `tests/unit/` |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

## Configuration

- **Cache:** `~/.cache/mvmctl/` (`MVM_CACHE_DIR`)
- **Config:** `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`) — JSON, not YAML
- **Env prefix:** `MVM_` (e.g. `MVM_CACHE_DIR`, `MVM_KERNEL`)
- **Priority:** constants.py fallbacks → config.json → MVM_* env vars → CLI flags

## Architecture Constraints

- **cli/** — arg parsing + output formatting ONLY; call `api/`
- **api/** — privilege checks + delegation to `core/`; stable public API with `__all__`
- **core/** — subprocess, filesystem, business logic; raises typed exceptions
- **models/** — `@dataclass` only; no methods with side effects
- **utils/** — pure helpers with no domain knowledge

## Code Quality Gates (CI-enforced)

```bash
uv run ruff check src/          # Must be clean
uv run ruff format --check src/ # Must be clean
uv run mypy src/                # Strict mode — no type: ignore allowed
uv run pytest tests/ -q         # 80% branch coverage minimum
```

Tests must NOT require root, KVM, or real network. Mock all subprocess calls.

## Related Files

- `AGENTS.md` — Full architecture reference for AI agents
- `src/mvmctl/core/AGENTS.md` — Core module inventory
- `src/mvmctl/cli/AGENTS.md` — CLI wiring, Typer patterns
- `src/mvmctl/api/AGENTS.md` — API layer pattern
- `tests/AGENTS.md` — Test fixtures, mock conventions
