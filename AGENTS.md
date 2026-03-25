# mvmctl

**Scope:** Production-grade Python CLI for managing Firecracker microVMs  
**Stack:** Python 3.13, Typer, Rich, uv  
**Entry:** `mvm` console script (defined in `pyproject.toml`)

## STRUCTURE

```
mvmctl/
├── src/mvmctl/
│   ├── main.py          # Root Typer app; registers all sub-apps via add_typer()
│   ├── constants.py     # Single source of truth — CLI name, env prefix, all defaults
│   ├── exceptions.py    # Custom exception hierarchy (FCMError → domain subclasses)
│   ├── cli/             # Thin Typer command definitions (no business logic)
│   ├── api/             # Stable public Python API; adds privilege checks before core
│   ├── core/            # All business logic, subprocess, Firecracker interaction
│   ├── models/          # Pure dataclasses (VMInstance, VMConfig, ImageSpec, etc.)
│   ├── utils/           # Shared helpers: console, process, fs, http, audit, validation
│   └── assets/          # Bundled YAML configs (images.yaml, kernel.yaml, defaults.yaml)
├── tests/               # 38 unit + integration test files
└── pyproject.toml       # Build, ruff, mypy strict, pytest (80% branch coverage gate)
```

## WHERE TO LOOK

| Task | Location |
|------|----------|
| VM lifecycle | `core/vm_lifecycle.py` — `create_vm()`, `remove_vm()` |
| Image resolution | `core/vm_lifecycle.py` — `_resolve_image_path()` (hash + ext lookup) |
| Network setup | `core/network.py` (bridge/TAP/iptables), `core/network_manager.py` (named networks) |
| Host init | `core/host_setup.py` — `init_host()` |
| Privilege checks | `core/host_privilege.py` — `check_privileges()` |
| Asset metadata | `core/metadata.py` — single `metadata.json`, keyed by full 64-char hash |
| Active binary/version | `core/config_state.py` — `get_firecracker_config()`, `update_firecracker_config()` |
| Firecracker HTTP API | `core/firecracker.py` — `FirecrackerClient` |
| CLI commands | `cli/` — see `cli/AGENTS.md` |
| API layer | `api/` — see `api/AGENTS.md` |
| First-time setup | `cli/configure.py` — guided onboarding wizard (`mvm configure`) |
| Tests | `tests/unit/conftest.py` (fixtures), `tests/unit/test_*.py` |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

## DATA FLOW

```
User → mvm → main.py → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

- `main.py` registers sub-apps with `app.add_typer()`; `kernel`/`image`/`bin` are three separate Typer apps all defined in `cli/asset.py`, mounted at root with `rich_help_panel="Assets"`
- **CLI params default to `None`**; resolved at runtime via `_defaults = _get_vm_defaults()` pattern — never use Typer option defaults for config-backed values
- API layer is the privilege boundary: `check_privileges(binary_path)` called here, not in CLI

## ASSET ID SYSTEM

Every downloaded/imported asset (image, kernel, VM) gets a **full 64-char SHA256 hash** as its persistent ID:

- `sha256(file_content + ":" + timestamp)` → stored as JSON key in `metadata.json`
- CLI always displays only the **first 6 chars** of the hash
- Removal and lookup accept the 6-char prefix; `find_images_by_short_id()` / `find_kernels_by_short_id()` do the prefix search
- YAML images (e.g. `ubuntu-24.04`) keep their YAML filename on disk; their hash is only in `metadata.json`

## CONVENTIONS

### Architecture (Strict Layers)
- **cli/** — arg parsing + output formatting ONLY; call `api/`
- **api/** — privilege checks + delegation to `core/`; stable public API with `__all__`
- **core/** — subprocess, filesystem, business logic; returns data or raises typed exceptions
- **models/** — `@dataclass` only; no methods with side effects
- **utils/** — pure helpers with no domain knowledge

### Default Values Rule
- Fallback defaults → `constants.py` with `FALLBACK_` prefix: `FALLBACK_FC_CI_VERSION`, `FALLBACK_FIRECRACKER_BIN`, `FALLBACK_KERNEL_BUILD_JOBS`
- User-facing defaults → `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`)
- NEVER hardcode defaults in function parameters or as inline variables

### Configuration Priority (lowest → highest)
1. `constants.py` fallbacks
2. `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`)
3. `MVM_*` environment variables
4. CLI flags

### Privilege Model
- `sudo mvm host init` — one-time: creates `mvm` group, sudoers drop-in, bridges
- After init: NO sudo needed; `check_privileges()` validates group membership (not just root)
- After `sudo mvm host init`, created files are chowned back to invoking user

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Hardcode paths/names | `constants.py` or `MVM_*` env vars |
| Business logic in `cli/` | Move to `core/`, expose via `api/` |
| `print()` in `core/` | `from mvmctl.utils.console import print_info` — only in CLI |
| Bare `except:` | Catch specific types from `exceptions.py` |
| Inline default values | `FALLBACK_*` in `constants.py` |
| Skip failing tests | Fix the test; coverage drop = CI failure |
| `python -m mvmctl` | Not supported — no `__main__.py` |

## CODE QUALITY GATES

All enforced in CI (`ci.yml`):

```bash
uv run ruff check src/         # Must be clean
uv run ruff format --check src/ # Must be clean
uv run mypy src/               # Strict mode — no type: ignore allowed
uv run pytest tests/ -q        # 80% branch coverage minimum
```

Tests must NOT require root, KVM, or real network. Mock all subprocess calls.

## COMMANDS

```bash
uv sync --group dev            # Install all deps
uv run pytest tests/ -x -q    # Test (stop at first failure)
uv run ruff check src/ && uv run mypy src/  # Lint + types

# Build standalone binary
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# Output: dist/mvm
```

## NOTES

- **Cache:** `~/.cache/mvmctl/` (`MVM_CACHE_DIR`)
- **Config:** `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`) — JSON, not YAML
- **Metadata:** `$MVM_CACHE_DIR/metadata.json` — single file for all images, kernels, binaries
- **Network prefix:** bridge = `mvm-{network_name}` (e.g. `mvm-default`), TAP = `mvm-{net[:3]}-{vm[:3]}-{rand3}`
- **Env var prefix:** `MVM_` (e.g. `MVM_CACHE_DIR`, `MVM_KERNEL`)
- **known violation:** `core/kernel.py` uses `console.print` directly (should be in CLI layer)

## Related AGENTS.md

- `src/mvmctl/core/AGENTS.md` — Core module inventory, state management, subprocess conventions
- `src/mvmctl/cli/AGENTS.md` — CLI wiring, Typer patterns, command groups
- `src/mvmctl/api/AGENTS.md` — API layer pattern, privilege boundary
- `tests/AGENTS.md` — Test fixtures, mock conventions, coverage
