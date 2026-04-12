# mvmctl

**Scope:** Production-grade Python CLI for managing microVMs
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Stack:** Python 3.13, Click (root), Typer (sub-apps), Rich, uv
**Entry:** `mvm` console script → `main.py:LazyMVMGroup` (NOT a Typer root app)

### Agent CLI Execution
To execute the `mvmctl` CLI with proper group privileges, use:
`sg mvm -c 'mvm ...'`

### CI Verification (MANDATORY)
**ALL code changes MUST pass CI checks before completion.**
Before finishing any implementation, you MUST verify:
1. **Ruff Linting** — `uv run ruff check src/` must be clean
2. **Ruff Formatting** — `uv run ruff format --check src/` must pass
3. **Type Checking** — `uv run mypy src/` must pass (strict mode)
4. **Tests** — `uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80` must pass

**If checks fail:**
- Fix linting/formatting issues with `uv run ruff check src/ --fix` and `uv run ruff format src/`
- Fix type errors with proper type annotations
- Fix failing tests — NEVER delete tests to make them pass

## STRUCTURE
```
mvmctl/
├── src/mvmctl/
│   ├── main.py          # LazyMVMGroup (click.Group)
│   ├── constants.py     # Single source of truth 
│   ├── exceptions.py    # Custom exception hierarchy
│   ├── cli/             # Thin Typer command definitions (no business logic)
│   ├── api/             # Stable public Python API boundary. 
│   ├── core/            # All business logic, subprocesses, and Firecracker interactions
│   ├── models/          # Pure @dataclass objects
│   ├── utils/           # Shared helpers
│   ├── assets/          # Bundled YAML configs
│   ├── services/        # Runtime subprocess services
│   └── db/              # SQLite schema, migrations, and ORM models
├── docs/                # Project documentation
├── tests/               # 69 test files
└── pyproject.toml       
```

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

| Layer | Resolves | How |
|-------|----------|-----|
| **CLI** | User input + constants-backed defaults | `DEFAULT_*` from `constants.py` if flag not provided. No DB queries ever. |
| **API** | DB-backed defaults | Query SQLite (`MVMDatabase`) when CLI passes `None`. |
| **Core** | Nothing — executes only | Receives ALL explicit, resolved values. No `None` for required params. No DB. |
| **Models** | Nothing | Pure `@dataclass` containers. No defaults for config-backed fields. |

## ORCHESTRATION ARCHITECTURE (The Burger Analogy)
**Key principle**: Core modules are **ISOLATED**. They do not call each other. The **API layer is the ONLY entity** that calls multiple core modules and sequences them together. 

## CONVENTIONS
- **cli/** — arg parsing + output formatting ONLY; runtime default resolution; call `api/`
- **api/** — privilege checks + delegation to `core/`; **NO default values in params**; **SOLE orchestrator** of core modules
- **core/** — subprocess, filesystem, business logic; **NO default values in params**; **ISOLATED** — no cross-core imports
- **models/** — `@dataclass` only; **NO default values for config-backed fields**
- **utils/** — pure helpers with no domain knowledge. **All external tool wrappers MUST be centralized in `utils/` — NEVER scattered in `core/`.**

### Default Values Rule (STRICT ENFORCEMENT - ZERO TOLERANCE)
**Default values belong ONLY in the CLI layer.** API, Core, and Models must receive explicit values.
- NO `typer.Option(DEFAULT_*, ...)`
- NO `typer.Option(get_assets_dir(), ...)`
- NO non-None default for config-backed values

**MANDATORY CORRECT PATTERN:**
```python
vcpus: Optional[int] = typer.Option(None, "--vcpus", help="Number of vCPUs")
defaults = _get_vm_defaults()
effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
```

## ASSET ID SYSTEM
Every downloaded/imported asset gets a **full 64-char SHA256 hash**. CLI displays first 6 chars. Removal and lookup accept the 6-char prefix.

## ANTI-PATTERNS
| Forbidden | Correct |
|-----------|---------|
| Hardcode paths/names | `constants.py` or `MVM_*` env vars |
| Business logic in `cli/` | Move to `core/`, expose via `api/` |
| `print()` in `core/` | `from mvmctl.utils.console import print_info` — only in CLI |
| Bare `except:` | Catch specific types from `exceptions.py` |
| Skip failing tests | Fix the test; coverage drop = CI failure |
| `as any` / `type: ignore` | Strict mypy — no suppressions allowed |

## TESTING
- **80% branch coverage** minimum
- Tests must NOT require root, KVM, or real network. Mock all subprocess calls.
- Use `@pytest.fixture(autouse=True)` for `_mock_sudo_cache`, `isolate_config_and_cache`, `_isolate_iptables_rules`, `_setup_database`.

## COMMANDS
```bash
uv sync --group dev
uv run pytest tests/ -x -q -n auto
uv run ruff check src/ && uv run mypy src/
```
