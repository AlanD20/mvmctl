# Subagent Instructions

## Agent Role: ORCHESTRATOR ONLY

You are the **orchestrating agent**. You **NEVER** read files or edit code yourself. ALL work is done via subagents.

---

### ⚠️ ABSOLUTE RULES

1. **NEVER read files yourself** — spawn a subagent to do it
2. **NEVER edit/create code yourself** — spawn a subagent to do it
3. **ALWAYS use default subagent** — NEVER use `agentName: "Plan"` (omit `agentName` entirely)

### User Confirmation Required

**NEVER implement changes immediately without user confirmation.**

Before making any code changes:
1. Present your proposed approach to the user
2. Explain what you intend to do and why
3. Wait for explicit user approval
4. Only proceed with implementation after receiving confirmation

This applies to all edits, fixes, features, and refactoring. No exceptions.

---

### Mandatory Workflow (NO EXCEPTIONS)

```
User Request
    ↓
SUBAGENT #1: Research & Spec
    - Reads files, analyzes codebase
    - Creates spec/analysis doc in docs/analyses/
    - Returns summary to you
    ↓
YOU: Receive results, spawn next subagent
    ↓
SUBAGENT #2: Implementation (FRESH context)
    - Receives the spec file path
    - Implements/codes based on spec
    - Returns completion summary
```

---

### runSubagent Tool Usage

```
runSubagent(
  description: "3-5 word summary",  // REQUIRED
  prompt: "Detailed instructions"   // REQUIRED
)
```

**NEVER include `agentName`** — always use default subagent (has full read/write capability).

**If you get errors:**
- "disabled by user" → You may have included `agentName`. Remove it.
- "missing required property" → Include BOTH `description` and `prompt`

---

### Subagent Prompt Templates

**Research Subagent:**
```
Research [topic]. Analyze relevant files in the codebase.
Create a spec/analysis doc at: docs/analyses/[NAME].md
Return: summary of findings and the spec file path.
```

**Implementation Subagent:**
```
Read the spec at: docs/analyses/[NAME].md
Implement according to the spec.
Return: summary of changes made.
```

---

### What YOU Do (Orchestrator)

✅ Receive user requests
✅ Spawn subagents with clear prompts
✅ Pass spec paths between subagents
✅ Run terminal commands

### What YOU DON'T Do

❌ Read files (use subagent)
❌ Edit/create code (use subagent)
❌ Use `agentName: "Plan"` (always omit it)
❌ "Quick look" at files before delegating

---

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

**Pre-existing failures:** If there are pre-existing test failures unrelated to your changes, document them but ensure your changes don't introduce NEW failures.

---

### Commit Authorship (MANDATORY)

**DO NOT add `Co-authored-by` trailers unless the co-author actually contributed to that specific change.**

- Only add co-authors when they **directly contributed code, review, or significant input** to that specific commit
- Do NOT add co-authors as a blanket practice on every commit
- Do NOT add co-authors just because they are part of the project or team
- When in doubt, **omit the co-author trailer entirely**

**Correct:**
```
feat: add new VM snapshot feature

Co-authored-by: Alice <alice@example.com>  # Alice wrote part of this feature
```

**Incorrect:**
```
style: fix formatting

Co-authored-by: Adam <adam@example.com>  # WRONG - no contribution to this change
```

---

### Agent CLI Execution

To execute the `mvmctl` CLI with proper group privileges, use:
`sg mvm -c 'mvm ...'`

---

# mvmctl

**Scope:** Production-grade Python CLI for managing microVMs
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Stack:** Python 3.13, Click (root), Typer (sub-apps), Rich, uv
**Entry:** `mvm` console script → `main.py:LazyMVMGroup` (NOT a Typer root app)
**Generated:** 2026-04-10
**Commit:** 5cd5126
**Branch:** main
**Files:** 110 Python source, 109 test files

## STRUCTURE

```
mvmctl/
├── src/mvmctl/
│   ├── main.py          # LazyMVMGroup (click.Group) — lazy-loads sub-apps from _COMMAND_SPECS
│   ├── constants.py     # Single source of truth — CLI name, env prefix, all defaults; uses importlib.resources
│   ├── exceptions.py    # Custom exception hierarchy (MVMError → domain subclasses)
│   ├── cli/             # Thin Typer command definitions (no business logic)
│   ├── api/             # Stable public Python API boundary. Performs privilege checks before delegating to core/
│   ├── core/            # All business logic, subprocesses, and Firecracker interactions
│   ├── models/          # Pure @dataclass objects containing domain data (VMInstance, VMConfig, etc.)
│   ├── utils/           # Shared helpers (console, process, fs, http, audit, validation, guestfs, template, time)
│   ├── assets/          # Bundled YAML configs (images.yaml, kernels.yaml) + _defaults.py
│   ├── services/        # Runtime subprocess services (console_relay, nocloud_server)
│   └── db/              # SQLite schema, migrations, and ORM models (mvmdb.db)
├── docs/                # Project documentation
│   ├── DEPENDENCIES.md  # Detailed map of all binary and system dependencies per command
│   └── RELEASE.md       # Release process and build instructions (Nuitka/PyInstaller)
├── tests/               # 69 test files (unit, integration, system, layer_compliance); see tests/AGENTS.md
└── pyproject.toml       # Build, ruff, mypy strict, pytest (80% branch coverage gate)
```

## CODE MAP

### main.py
| Symbol | Type | Purpose |
|--------|------|---------|
| `LazyMVMGroup` | class | Custom `click.Group` — lazy-loads sub-apps from `_COMMAND_SPECS` |
| `_COMMAND_SPECS` | dict[str, _LazyCommandSpec] | Maps command names to module + attribute tuples |
| `_LazyCommandSpec` | dataclass | Holds `module`, `attribute`, `help_text` for lazy loading |
| `_COMMAND_ORDER` | list[str] | Ordered list of all commands |
| `_STATIC_COMMAND_HELP` | dict[str, str] | Static help text for all commands |
| `app` | click.Group | Root CLI group — handles global flags, logging, network reconciliation |
| `_warn_if_running_as_root()` | function | Warns if running as root (suppressed when `MVM_ESCALATED` set) |
| `_reconcile_networks()` | function | Reconciles networks on every subcommand (errors swallowed) |

### constants.py
| Symbol | Type | Purpose |
|--------|------|---------|
| `PROJECT_NAME` | Final[str] | Resolved from package metadata |
| `CLI_NAME` | Final[str] | Resolved from entry points, defaults to "mvm" |
| `_load_defaults()`| function | Loads `_defaults.py` Python dict |
| `_BOOTSTRAP_NAME` | Final[str] | Internal package name "mvmctl" |
| `env_var(suffix)` | function | Returns `MVM_*` env var name |
| `DEFAULT_*` | Final[*] | User-facing defaults from `_defaults.py` |
| `FALLBACK_*` | Final[*] | Last-resort runtime values |
| `CONST_*` | Final[*] | Hardcoded numeric constants (buffer sizes, timeouts, permissions) |

### exceptions.py
```
MVMError (base)
├── VMNotFoundError
├── VMAlreadyExistsError
├── NetworkError
├── ImageError
│   └── ChecksumMismatchError
├── KernelError
├── FirecrackerError
│   └── SocketNotFoundError
├── ConfigError
├── HostError
│   └── PrivilegeError
├── ProcessError
├── AssetNotFoundError
├── BinaryError
└── MVMKeyError
```

## WHERE TO LOOK

| Task | Location |
|------|----------|
| VM lifecycle | `core/vm_lifecycle.py` — `create_vm()`, `remove_vm()` |
| Image resolution | `core/vm_lifecycle.py` — `_resolve_image_path()` (hash + ext lookup) |
| Network setup | `core/network.py` (bridge/TAP/iptables), `core/network_manager.py` (named networks) |
| Host init | `core/host_setup.py` — `init_host()` |
| Privilege checks | `core/host_privilege.py` — `check_privileges()` |
| Asset metadata | `core/metadata.py` — SQLite-backed metadata helpers for images/kernels/binaries |
| Active binary/version | `core/binary_manager.py` + `core/mvm_db.py` | `get_binary_path("firecracker")` for path lookup; `db.get_default_binary("firecracker")` for direct SQLite query; do NOT use filesystem symlinks for state |
| SQLite schema/migrations | `db/migrations/` — `001_initial_schema.sql`, `runner.py` |
| Firecracker HTTP API | `core/firecracker.py` — `FirecrackerClient` |
| SSH operations | `core/ssh.py` — `connect_to_vm()`, `build_ssh_command()`, `find_ssh_keys()` |
| Key management | `core/key_manager.py` — `add_key()`, `create_key()`, `list_keys()`, `remove_key()` |
| Cloud-init | `core/cloud_init.py` — `write_cloud_init()`, `inject_cloud_init()` |
| Logs | `core/logs.py` — `show_logs()`, `follow_log()`, `get_log_path()` |
| Config generation | `core/config_gen.py` — `ConfigGenerator` class |
| CLI commands | `cli/` — see `cli/AGENTS.md` |
| API layer | `api/` — see `api/AGENTS.md` |
| First-time setup | `cli/init.py` — guided onboarding wizard (`mvm init`) |
| Tests | `tests/AGENTS.md` (fixtures, mocks, layout) |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

| Layer | Resolves | How |
|-------|----------|-----|
| **CLI** | User input + constants-backed defaults | `DEFAULT_*` from `constants.py` if flag not provided. No DB queries ever. |
| **API** | DB-backed defaults | Query SQLite (`MVMDatabase`) when CLI passes `None`. `is_default=1` rows are canonical. Also does privilege checks. |
| **Core** | Nothing — executes only | Receives ALL explicit, resolved values. No `None` for required params. No DB. |
| **Models** | Nothing | Pure `@dataclass` containers. No defaults for config-backed fields. |

**Constants-backed** (CLI resolves via `DEFAULT_*` from `constants.py`):
`vcpu_count`, `mem`, `ssh_user`, `boot_args`, `lsm_flags`, `disk_size`, `enable_api_socket`, `enable_pci`, `enable_console`, `cloud_init_mode`

**DB-backed** (pass `None` to API — API resolves via `MVMDatabase`):
image path, kernel path, firecracker binary path, network config

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_imports.py`.

## DATA FLOW

```
User → mvm → main.py:LazyMVMGroup → cli/*.py → api/*.py → core/*.py → models/ + utils/
```

### Entry Point
1. `mvm` console script invokes `mvmctl.main:app` (Click group)
2. `main.py` creates `LazyMVMGroup` — NOT a Typer root app
3. Sub-apps lazy-loaded via `importlib.import_module()` on first access

### Command Loading
1. `get_command(ctx, cmd_name)` looks up `_COMMAND_SPECS[cmd_name]`
2. Module imported, attribute retrieved (click.Command or Typer app)
3. Typer apps converted via `typer.main.get_command()`

### Sub-app Structure
- `kernel`, `image`, `bin` — three separate Typer apps in `cli/bin.py`
- All use `rich_markup_mode=None, add_completion=False` — plain Click help

### Default Resolution
- CLI params default to `None`
- Resolved at runtime via `_defaults = _get_vm_defaults()` pattern
- NEVER use Typer option defaults for config-backed values

### API Boundary
- `api/` layer adds privilege checks: `check_privileges(binary_path)`
- Called here, NOT in CLI or core

## ASSET ID SYSTEM

Every downloaded/imported asset (image, kernel, VM) gets a **full 64-char SHA256 hash** as its persistent ID:

- `sha256(file_content + ":" + timestamp)` → stored in SQLite (`mvmdb.db`)
- CLI always displays only the **first 6 chars** of the hash
- Removal and lookup accept the 6-char prefix; `find_images_by_short_id()` / `find_kernels_by_short_id()` do the prefix search
- YAML images (e.g. `ubuntu-24.04`) keep their YAML filename on disk; their hash is only in SQLite

## ORCHESTRATION ARCHITECTURE (The Burger Analogy)

Think of the system as a burger:

```
user input → CLI (validate, apply constants defaults)
               ↓
           API Layer (the "bun" — orchestrates everything)
           ├── calls core/network.py (setup network)
           ├── calls core/vm_lifecycle.py (start VM)
           ├── calls core/metadata.py (store metadata)
           ├── calls core/cloud_init.py (write cloud-init)
           └── returns result to CLI
               ↑
           Core Modules (isolated "ingredients")
           Each module does ONE thing, receives explicit inputs,
           does NOT import from other core/ modules.
```

**Key principle**: Core modules are **ISOLATED**. They do not call each other. The **API layer is the ONLY entity** that calls multiple core modules and sequences them together. This prevents circular dependencies and keeps each core module testable in isolation.

**Analogy**:
- **Chef** = CLI (takes the order, validates it)
- **Tomato, Burger, Onion** = core modules (each does its job independently)
- **The Bun** = API layer (holds all ingredients together, defines the complete product)

## CONVENTIONS

### Architecture (Strict Layers)
- **cli/** — arg parsing + output formatting ONLY; runtime default resolution; call `api/`
- **api/** — privilege checks + delegation to `core/`; **NO default values in params**; stable public API with `__all__`; **SOLE orchestrator** of core modules
- **core/** — subprocess, filesystem, business logic; **NO default values in params**; returns data or raises typed exceptions; **ISOLATED** — no cross-core imports
- **models/** — `@dataclass` only; **NO default values for config-backed fields**; no methods with side effects
- **utils/** — pure helpers with no domain knowledge

### Default Value Layer Rule (STRICT ENFORCEMENT)

**Default values belong ONLY in the CLI layer.** API, Core, and Models must receive explicit values.

| Layer | Default Policy | Implementation |
|-------|----------------|----------------|
| **CLI** | Runtime resolution | `typer.Option(None, ...)` + `_get_vm_defaults()` pattern |
| **API** | **NO defaults** | Function params must receive explicit values from CLI |
| **Core** | **NO defaults** | Business logic operates on explicit inputs only |
| **Models** | **NO defaults** | Dataclasses store exactly what they're given |

This ensures:
1. Single source of truth for defaults (CLI runtime resolution)
2. No hidden behavior in API/Core/Models that bypasses user config
3. Clear data flow: CLI resolves → API passes through → Core executes → Models store
4. Testability: API, Core, and Model tests use explicit values, not implicit defaults

### Centralized Tool Wrappers (CRITICAL)

**All external tool wrappers MUST be centralized in `utils/` — NEVER scattered in `core/` or other layers.**

This ensures:
1. **Single source of truth** for tool interactions (libguestfs, qemu-img, etc.)
2. **Consistent error handling** across the codebase
3. **Easier testing** with mocked utilities
4. **Better maintainability** when tools change

**Examples:**
- `utils/guestfs.py` — ALL libguestfs operations (OptimizedGuestfs, check_libguestfs, extract_partition_with_guestfs)
- `utils/http.py` — ALL HTTP operations (download, retry logic)
- `utils/process.py` — ALL subprocess wrappers

**Pattern:**
```python
# CORRECT: Tool logic in utils/
# utils/guestfs.py:
def extract_partition_with_guestfs(...) -> Path | None:
    # All guestfs logic here
    ...

# core/image.py imports and uses:
from mvmctl.utils.guestfs import extract_partition_with_guestfs
result = extract_partition_with_guestfs(...)

# WRONG: Tool logic scattered in core/
# core/image.py:
def _extract_partition_with_guestfs(...):  # DON'T DO THIS
    # Guestfs logic mixed with business logic
    ...
```

**Rule:** If a function uses an external tool (libguestfs, qemu-img, tar, etc.), it belongs in `utils/`. The `core/` layer should import and use these utilities, not implement them.

### Default Values Rule (STRICT ENFORCEMENT - ZERO TOLERANCE)

**ABSOLUTE RULES (VIOLATION = IMMEDIATE CI REJECTION):**

| Forbidden Pattern | Why It's Banned | Consequence |
|-------------------|-----------------|-------------|
| `typer.Option(DEFAULT_*, ...)` | Hardcoded constants bypass runtime config resolution | Help shows stale values; ignores user config |
| `typer.Option(get_assets_dir(), ...)` | Function evaluated at **import time**, not runtime | Breaks when `MVM_CONFIG_DIR` env var is set after import |
| `typer.Option([], ...)` for lists | Breaks Typer internals; mutable default | Use `None` with `list(values) if values else []` |
| `typer.Option(True/False, ...)` for config-backed booleans | Must support tri-state (CLI/config/default) | Use `None` and resolve at runtime |
| Any non-None default for config-backed values | Bypasses SQLite/_defaults.py configuration | User settings are silently ignored |

**MANDATORY CORRECT PATTERN:**
```python
# Step 1: typer param with None default
vcpus: Optional[int] = typer.Option(None, "--vcpus", help="Number of vCPUs")

# Step 2: Runtime resolution inside function
defaults = _get_vm_defaults()
effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
```

**VERIFICATION CHECKLIST (Before submitting any CLI PR):**
- [ ] No `typer.Option(DEFAULT_*` patterns exist in changed files
- [ ] No `typer.Option(get_*_dir()` patterns exist
- [ ] No `typer.Option([])` or `typer.Argument([])` patterns exist
- [ ] No `typer.Option(True/False)` for values that should be config-backed
- [ ] All config-backed values resolve at **runtime** inside function body
- [ ] Help text does not show hardcoded numbers/strings for config-backed options

**ENFORCEMENT:**
- `layer_compliance/test_constants.py` validates no hardcoded defaults
- CI will reject PRs with violations
- **NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**

- Fallback defaults → `constants.py` with `FALLBACK_` prefix: `FALLBACK_FC_CI_VERSION`, `FALLBACK_FIRECRACKER_BIN`, `FALLBACK_KERNEL_BUILD_JOBS`
- User-facing asset defaults (image/kernel/binary) → SQLite (`$MVM_CACHE_DIR/mvmdb.db`) via `is_default` column; `metadata.json` is a compatibility shim and is NOT canonical
- NEVER hardcode defaults in function parameters or as inline variables

### Configuration Priority (lowest → highest)
1. `constants.py` fallbacks
2. State files (`~/.config/mvmctl/config.json` for general config + `$MVM_CACHE_DIR/mvmdb.db` SQLite for asset defaults)
3. `MVM_*` environment variables
4. CLI flags

### Privilege Model
- `sudo mvm host init` — one-time: creates `mvm` group, sudoers drop-in, bridges
- After init: NO sudo needed; `check_privileges()` validates group membership (not just root)
- After `sudo mvm host init`, created files are chowned back to invoking user

## KNOWN EXCEPTIONS

These are intentional deviations from the layer architecture:

| File | Deviation | Reason |
|------|-----------|--------|
| `cli/bin.py` | Imports `core/metadata` directly | Asset management needs direct metadata access for bulk operations |
| `cli/init.py` | Imports `core/config_state` directly | Onboarding wizard needs raw state initialization |
| `core/host_privilege.py` | `check_privileges_interactive()` prints to console | UX: provides actionable guidance on privilege errors |
| `cli/__init__.py` | Stale `__all__` (lists `"asset"` which doesn't exist; missing `bin`, `console`, `cache`, `logs`, `ssh`) | Not enforced — only root `main.py` uses `_COMMAND_SPECS` for loading |

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
| `as any` / `type: ignore` | Strict mypy — no suppressions allowed |

## CODE QUALITY GATES

All enforced in CI (`ci.yml`):

```bash
uv run ruff check src/         # Must be clean (line-length=100, py313, import sorting)
uv run ruff format --check src/ # Must be clean (double quotes, space indent)
uv run mypy src/               # Strict mode — no type: ignore allowed
uv run pytest tests/ -q -n auto       # 80% branch coverage minimum
```

Tests must NOT require root, KVM, or real network. Mock all subprocess calls.

## TESTING

See `tests/AGENTS.md` for complete testing documentation.

### Key Fixtures
```python
# Root conftest (autouse for all tests)
@pytest.fixture(autouse=True)
def _mock_sudo_cache():  # Prevents real sudo calls

@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path, monkeypatch):  # Isolates ~/.config and ~/.cache

@pytest.fixture(autouse=True)
def _isolate_iptables_rules():  # Clears iptables before each test

@pytest.fixture(autouse=True)
def _setup_database():  # Sets up test database

# VM fixtures: vm_manager, sample_vm, stopped_vm, running_vm, error_vm
# Network fixtures: sample_network_config
# Key fixtures: mock_keys_dir, sample_key_info
# Subprocess mocks: mock_subprocess_run_success, mock_subprocess_run_failure
```

### Mocking Patterns
```python
# pytest-mock (preferred)
mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])

# unittest.mock.patch (for subprocess)
@patch("mvmctl.core.host_setup.subprocess.run")
def test_bar(mock_run): ...

# CLI testing (always CliRunner)
runner = CliRunner()
result = runner.invoke(app, ["rm", "--name", "myvm", "--force"])
```

### Coverage Gate
- **80% branch coverage** minimum
- Tests must NOT require root, KVM, or real network

## COMMANDS

```bash
uv sync --group dev            # Install all deps
uv run pytest tests/ -x -q  -n auto   # Test (stop on first failure)
uv run ruff check src/ && uv run mypy src/  # Lint + types

# Build standalone binary (Nuitka - Recommended for performance)
uv run --group build python -m nuitka --onefile --output-dir=dist --output-filename=mvm --include-package=mvmctl --include-data-dir=src/mvmctl/assets=mvmctl/assets --lto=yes --enable-plugin=anti-bloat src/mvmctl/main.py

# Build standalone binary (PyInstaller - Fast build)
uv run --group build pyinstaller --onefile --name mvm --collect-all mvmctl src/mvmctl/main.py
```

## NOTES

- **Cache:** `~/.cache/mvmctl/` (`MVM_CACHE_DIR`)
- **Config:** `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`) — JSON, not YAML (general runtime config)
- **State:** `$MVM_CACHE_DIR/mvmdb.db` — SQLite is canonical for all `is_default` and state queries; `metadata.json` is a legacy compatibility shim
- **Network prefix:** bridge = `mvm-{network_name}` (e.g. `mvm-default`), TAP = `mvm-{net[:3]}-{vm[:3]}-{rand3}`
- **Env var prefix:** `MVM_` (e.g. `MVM_CACHE_DIR`, `MVM_KERNEL`)
- **reconcile_networks():** called on every subcommand invocation in `main.py`; errors are swallowed (not user-visible)
- **Test path isolation:** `tests/helpers/paths.py:make_test_paths(tmp_path)` is the single source of truth for canonical test paths

## Related AGENTS.md

### Project
- `src/mvmctl/core/AGENTS.md` — Core module inventory, state management, subprocess conventions
- `src/mvmctl/cli/AGENTS.md` — CLI wiring, Typer patterns, command groups
- `src/mvmctl/api/AGENTS.md` — API layer pattern, privilege boundary
- `src/mvmctl/models/AGENTS.md` — Domain dataclasses (VMInstance, VMConfig, ImageSpec, etc.)
- `src/mvmctl/utils/AGENTS.md` — Shared helpers (console, fs, http, process, audit, validation)
- `src/mvmctl/assets/AGENTS.md` — Bundled YAML configs and templates
- `src/mvmctl/services/AGENTS.md` — Runtime subprocess services (console_relay, nocloud_server)
- `src/mvmctl/db/AGENTS.md` — SQLite schema, migrations, and ORM models
- `tests/AGENTS.md` — Test fixtures, mock conventions, coverage

### Legacy
- `legacy/single-vm/AGENTS.md` — Archived bash single-VM reference
- `legacy/multi-vm/AGENTS.md` — Archived bash multi-VM reference
