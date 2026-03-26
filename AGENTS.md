# Subagent Instructions
 
## Agent Role: ORCHESTRATOR ONLY
 
You are the **orchestrating agent**. You **NEVER** read files or edit code yourself. ALL work is done via subagents.
 
---
 
### ⚠️ ABSOLUTE RULES
 
1. **NEVER read files yourself** — spawn a subagent to do it
2. **NEVER edit/create code yourself** — spawn a subagent to do it
3. **ALWAYS use default subagent** — NEVER use `agentName: "Plan"` (omit `agentName` entirely)
 
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

# mvmctl

**Scope:** Production-grade Python CLI for managing Firecracker microVMs
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Stack:** Python 3.13, Click (root), Typer (sub-apps), Rich, uv
**Entry:** `mvm` console script → `main.py:LazyMVMGroup` (NOT a Typer root app)
**Generated:** 2026-03-26T13:00Z  
**Commit:** 7d72dbc  
**Branch:** main

## STRUCTURE

```
mvmctl/
├── src/mvmctl/
│   ├── main.py          # LazyMVMGroup (click.Group) — lazy-loads sub-apps from _COMMAND_SPECS
│   ├── constants.py     # Single source of truth — CLI name, env prefix, all defaults
│   ├── exceptions.py    # Custom exception hierarchy (MVMError → domain subclasses)
│   ├── cli/             # Thin Typer command definitions (no business logic)
│   ├── api/             # Stable public Python API; adds privilege checks before core
│   ├── core/            # All business logic, subprocess, Firecracker interaction
│   ├── models/          # Pure dataclasses (VMInstance, VMConfig, ImageSpec, etc.)
│   ├── utils/           # Shared helpers: console, process, fs, http, audit, validation
│   └── assets/          # Bundled YAML configs (images.yaml, kernels.yaml, defaults.yaml)
├── tests/               # 48 test_*.py (41 unit, 4 integration, 3 layer_compliance); see tests/AGENTS.md
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
| `_BOOTSTRAP_NAME` | Final[str] | Internal package name "mvmctl" |
| `env_var(suffix)` | function | Returns `MVM_*` env var name |
| `DEFAULT_*` | Final[*] | User-facing defaults from `defaults.yaml` |
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
| Asset metadata | `core/metadata.py` — single `metadata.json`, keyed by full 64-char hash |
| Active binary/version | `core/config_state.py` — `get_firecracker_config()`, `update_firecracker_config()` |
| Firecracker HTTP API | `core/firecracker.py` — `FirecrackerClient` |
| SSH operations | `core/ssh.py` — `connect_to_vm()`, `build_ssh_command()`, `find_ssh_keys()` |
| Key management | `core/key_manager.py` — `add_key()`, `create_key()`, `list_keys()`, `remove_key()` |
| Cloud-init | `core/cloud_init.py` — `write_cloud_init()`, `inject_cloud_init()` |
| Logs | `core/logs.py` — `show_logs()`, `follow_log()`, `get_log_path()` |
| Config generation | `core/config_gen.py` — `ConfigGenerator` class |
| CLI commands | `cli/` — see `cli/AGENTS.md` |
| API layer | `api/` — see `api/AGENTS.md` |
| First-time setup | `cli/configure.py` — guided onboarding wizard (`mvm configure`) |
| Tests | `tests/AGENTS.md` (fixtures, mocks, layout) |
| CI/CD | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |

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
- `kernel`, `image`, `bin` — three separate Typer apps in `cli/asset.py`
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
- User-facing asset defaults (image/kernel/binary) → `$MVM_CACHE_DIR/metadata.json` via `is_default` flags
- NEVER hardcode defaults in function parameters or as inline variables

### Configuration Priority (lowest → highest)
1. `constants.py` fallbacks
2. State files (`~/.config/mvmctl/config.json` for general config + `$MVM_CACHE_DIR/metadata.json` for asset defaults)
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
| `cli/asset.py` | Imports `core/metadata` directly | Asset management needs direct metadata access for bulk operations |
| `cli/configure.py` | Imports `core/config_state` directly | Onboarding wizard needs raw state initialization |
| `core/host_privilege.py` | `check_privileges_interactive()` prints to console | UX: provides actionable guidance on privilege errors |

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
uv run pytest tests/ -q        # 80% branch coverage minimum
```

Tests must NOT require root, KVM, or real network. Mock all subprocess calls.

## TESTING

See `tests/AGENTS.md` for complete testing documentation.

### Key Fixtures
```python
# Root conftest (autouse for all tests)
@pytest.fixture(autouse=True)
def _mock_sudo_cache():  # Prevents real sudo calls

# Unit conftest (autouse for unit tests)
@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path, monkeypatch):  # Isolates ~/.config and ~/.cache

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
uv run pytest tests/ -x -q    # Test (stop at first failure)
uv run ruff check src/ && uv run mypy src/  # Lint + types

# Build standalone binary
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# Output: dist/mvm
```

## NOTES

- **Cache:** `~/.cache/mvmctl/` (`MVM_CACHE_DIR`)
- **Config:** `~/.config/mvmctl/config.json` (`MVM_CONFIG_DIR`) — JSON, not YAML (general runtime config)
- **Metadata:** `$MVM_CACHE_DIR/metadata.json` — single file for all images, kernels, binaries, and their `is_default` flags
- **Network prefix:** bridge = `mvm-{network_name}` (e.g. `mvm-default`), TAP = `mvm-{net[:3]}-{vm[:3]}-{rand3}`
- **Env var prefix:** `MVM_` (e.g. `MVM_CACHE_DIR`, `MVM_KERNEL`)
- **reconcile_networks():** called on every subcommand invocation in `main.py`; errors are swallowed (not user-visible)

## Related AGENTS.md

### Project
- `src/mvmctl/core/AGENTS.md` — Core module inventory, state management, subprocess conventions
- `src/mvmctl/cli/AGENTS.md` — CLI wiring, Typer patterns, command groups
- `src/mvmctl/api/AGENTS.md` — API layer pattern, privilege boundary
- `src/mvmctl/models/AGENTS.md` — Domain dataclasses (VMInstance, VMConfig, ImageSpec, etc.)
- `src/mvmctl/utils/AGENTS.md` — Shared helpers (console, fs, http, process, audit, validation)
- `tests/AGENTS.md` — Test fixtures, mock conventions, coverage

### Legacy
- `legacy/single-vm/AGENTS.md` — Archived bash single-VM reference
- `legacy/multi-vm/AGENTS.md` — Archived bash multi-VM reference
