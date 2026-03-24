# firecracker-manager

**Scope:** Production-grade Python CLI for managing Firecracker microVMs  
**Stack:** Python 3.13, Typer, Rich, uv (package manager)  
**Entry:** `fcm` command (defined in `pyproject.toml`)

## STRUCTURE

```
firecracker-manager/
├── src/fcm/              # Main package
│   ├── main.py          # CLI entry point (Typer app)
│   ├── constants.py     # Project identity (drives CLI name, env vars, cache dirs)
│   ├── exceptions.py    # Custom exception hierarchy
│   ├── cli/             # Typer command definitions (thin wrappers)
│   ├── api/             # Stable public Python API
│   ├── core/            # Business logic, Firecracker interaction
│   ├── models/          # Dataclasses (VMInstance, VMConfig, ImageSpec)
│   ├── utils/           # Shared helpers (console, process, fs, http, audit)
│   └── assets/          # Bundled YAML configs (defaults.yaml, images.yaml)
├── tests/               # Test suite (unit + integration)
└── pyproject.toml       # Build, lint, test, type-check config
```

## WHERE TO LOOK

| Task | Location |
|------|----------|
| CLI commands | `src/fcm/cli/` (vm.py, host.py, network.py, asset.py, etc.) |
| VM lifecycle | `src/fcm/core/vm_lifecycle.py` |
| Network setup | `src/fcm/core/network.py`, `src/fcm/core/network_manager.py` |
| Host initialization | `src/fcm/core/host*.py` (host.py, host_setup.py, host_state.py, host_privilege.py) |
| Firecracker API client | `src/fcm/core/firecracker.py` |
| Tests | `tests/unit/conftest.py` for fixtures; `tests/unit/test_*.py` |
| Build config | `pyproject.toml` (ruff, mypy, pytest, hatchling) |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

## CODE MAP

**Entry Point Flow:**
```
User → fcm (console script) → main.py → cli/*.py → api/*.py → core/*.py
```

**Key Modules:**
- `main.py` — Creates Typer app, registers subcommands via `app.add_typer()`
- `constants.py` — Single source of truth: PROJECT_NAME, CLI_NAME, CACHE_DIR, etc.
- `core/vm_lifecycle.py` — VM create/start/stop/remove logic (405 lines)
- `core/network.py` — Bridge, TAP, NAT, iptables operations (409 lines)
- `core/host*.py` — Host init, privilege management, state snapshots

## CONVENTIONS

### Architecture (Layered)
```
User → cli/ → api/ → core/ → models/ + utils/
```
- **cli/** — Thin Typer commands only; NO business logic
- **api/** — Stable public Python API; delegates to core
- **core/** — All business logic, subprocess calls, privilege checks
- **models/** — Pure dataclasses for type safety
- **utils/** — Shared helpers (no business logic)

### Naming & Organization
- CLI commands: `src/fcm/cli/{group}.py` (vm.py, host.py, etc.)
- Each CLI module exports a `typer.Typer()` app instance
- Core modules named by domain: `vm_lifecycle.py`, `network.py`, `image.py`
- All paths resolved via `constants.py` or config; NEVER hardcoded

### Configuration Priority (lowest → highest)
1. `constants.py` defaults
2. `~/.config/fcm/config.yaml`
3. `FCM_*` environment variables
4. CLI flags

### Privilege Model
- `sudo fcm host init` — One-time setup (creates group, sudoers drop-in)
- After init: NO sudo needed for regular `fcm` commands
- Privileged binaries listed in `PRIVILEGED_BINARIES` constant

## ANTI-PATTERNS

### NEVER Do These
- **Hardcode paths/names** — Always use `constants.py` or config
- **Business logic in CLI** — CLI layer ONLY does arg parsing and output formatting
- **Bare `except:` clauses** — Catch specific exceptions from `exceptions.py`
- **Modify bash scripts** — `single-vm/`, `multi-vm/`, `assets/` are read-only reference
- **Skip tests** — A requirement is not complete until tests pass
- **Break cross-phase compatibility** — Run full test suite after every change

### Code Quality Gates (CI Enforced)
- **ruff lint:** Must pass with no errors (`uv run ruff check src/`)
- **ruff format:** Must pass (`uv run ruff format src/`)
- **mypy strict:** Must pass (`uv run mypy src/`)
- **pytest coverage:** ≥79% branch coverage required
- **Tests must NOT require root/KVM:** Mock all subprocess calls

### Testing Requirements
- Mock subprocess calls (`ip`, `iptables`, `sysctl`, `firecracker`) — tests must pass without root
- Use `tmp_path` fixture for filesystem operations (NOT `tempfile`)
- Coverage gate: 79% minimum (enforced in CI)
- Unit tests: `tests/unit/test_{module}.py`
- Fixtures: `tests/unit/conftest.py`

## COMMANDS

```bash
# Development setup
cd firecracker-manager
uv sync --group dev

# Run tests
uv run pytest tests/ -v
uv run pytest tests/ --cov=src/fcm --cov-fail-under=79

# Lint/format/type-check
uv run ruff check src/
uv run ruff format src/
uv run mypy src/

# Build binary
uv run pyinstaller --onefile --name fcm src/fcm/main.py
# Output: dist/fcm
```

## NOTES

- **Cache directory:** `~/.cache/firecracker-manager/` (override with `FCM_CACHE_DIR`)
- **Config directory:** `~/.config/firecracker-manager/` (override with `FCM_CONFIG_DIR`)
- **Host state snapshots:** Saved in cache dir for `fcm host reset` rollback
- **Network device prefix:** `fcm-` (derived from project name)
- **Env var prefix:** `FCM_` (derived from CLI name)
- **No `__main__.py`:** Package does NOT support `python -m fcm` (only `fcm` or `python src/fcm/main.py`)
- remove the default config values in the entire CLI codebase. do not hard code config values in any function parameters or as variables! Default config must only come from user config if it's user facing and if it's backend facing, they must come from constants.py file. If major refactoring is required, do it so long as tests are going to pass and nothing breaks by validating your work.
  - Fallback default values must be defined in constants.py file with FALLBACK_ prefix to the variable!

## Project-Specific Files

| File | Purpose |
|------|---------|
| `pyproject.toml` | Project metadata, deps, tool configs (ruff, mypy, pytest) |
| `uv.lock` | uv lockfile for reproducible installs |
| `.python-version` | Python 3.13 version pin |
| `src/fcm/assets/*.yaml` | Bundled configs (defaults.yaml, images.yaml, kernel.yaml) |

## Related AGENTS.md

- `../single-vm/AGENTS.md` — Legacy single-VM bash setup
- `../multi-vm/AGENTS.md` — Legacy multi-VM bash setup
- `src/fcm/core/AGENTS.md` — Core module details
- `tests/AGENTS.md` — Testing patterns and fixtures
