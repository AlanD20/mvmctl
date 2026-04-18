# mvmctl/cli/ — CLI Layer (The Chef)

**Scope:** Typer command definitions only — arg parsing, output formatting, NO business logic
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Call `api/` for everything; never import from `core/` directly

**CLI's Role (The Chef):**
1. Parse user input from command-line flags and arguments
2. Apply `DEFAULT_*` constants for constants-backed defaults (vcpus, mem, etc.)
3. Pass `None` for DB-backed defaults (image, kernel, binary, network) — let API resolve
4. Delegate entirely to the API layer
5. Format API responses for display to the user

**CLI MUST NOT:**
- Query the database (even via API wrappers)
- Contain business logic
- Import from `core/` directly
- Format output in complex ways (keep it simple)

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

| Layer | Resolves | How |
|-------|----------|-----|
| **CLI** | User input + constants-backed defaults | `DEFAULT_*` from `constants.py` if flag not provided. **No DB queries ever.** |
| **API** | DB-backed defaults | Query SQLite (`MVMDatabase`) when CLI passes `None`. `is_default=1` rows are canonical. |
| **Core** | Nothing — executes only | Receives ALL explicit, resolved values. No `None` for required params. No DB. |
| **Models** | Nothing | Pure `@dataclass` containers. No defaults for config-backed fields. |

**Constants-backed** (CLI resolves via `DEFAULT_*` from `constants.py`):
`vcpu_count`, `mem`, `ssh_user`, `boot_args`, `lsm_flags`, `disk_size`, `enable_api_socket`, `enable_pci`, `enable_console`, `cloud_init_mode`

**DB-backed** (pass `None` to API — API resolves via `MVMDatabase`):
image path, kernel path, firecracker binary path, network config

**CLI MUST:**
- Resolve constants-backed defaults in the command function body: `effective_x = x if x is not None else DEFAULT_X`
- Pass `None` for DB-backed params: image, kernel, binary, network — API resolves these
- NEVER import `MVMDatabase`, `get_default_image()`, `get_default_kernel()`, `get_default_network()`, or any DB-querying symbol

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_imports.py`.

## STRUCTURE

```
src/mvmctl/cli/
├── vm.py          # VM subcommands: create, rm, ls, ps, ssh, logs, prune, snapshot, load (1038 lines)
├── image.py       # Image subcommands: ls, fetch, set-default, rm, import (810 lines)
├── host.py        # Host subcommands: init, ls, clean, reset (328 lines)
├── key.py         # SSH key subcommands: add, create, ls, rm, inspect (333 lines)
├── network.py     # Network subcommands: create, rm, ls, inspect (324 lines)
├── init.py        # Guided onboarding wizard: mvm init (273 lines)
├── kernel.py      # Kernel subcommands: ls, fetch, set-default, rm (253 lines)
├── cache.py       # Cache management: init, prune (232 lines)
├── console.py     # VM console access via PTY-over-vsock (179 lines)
├── bin.py         # Binary subcommands: ls, fetch, set-default, rm (165 lines)
├── config.py      # Config subcommands: get, set, show, validate, dump-vm (116 lines)
├── _helpers.py    # Internal: check_name_arg() guard for positional name args (146 lines)
├── ssh.py         # VM SSH helper commands (87 lines)
├── logs.py        # VM log viewing: --follow, --lines, --type (53 lines)
└── __init__.py    # CLI package exports (15 lines)
```

## SUBCOMMAND WIRING

Root is `LazyMVMGroup` (custom `click.Group`), NOT `typer.Typer`. Sub-apps lazy-loaded:

```python
_COMMAND_SPECS: dict[str, _LazyCommandSpec] = {
    "vm":      _LazyCommandSpec("mvmctl.cli.vm",      "app",        "VM lifecycle management"),
    "console": _LazyCommandSpec("mvmctl.cli.console", "app",        "VM console access"),
    "host":    _LazyCommandSpec("mvmctl.cli.host",    "app",        "Host configuration"),
    "network": _LazyCommandSpec("mvmctl.cli.network", "app",        "Network management"),
    "key":     _LazyCommandSpec("mvmctl.cli.key",     "app",        "SSH key management"),
    "config":  _LazyCommandSpec("mvmctl.cli.config",  "app",        "Configuration commands"),
    "init":    _LazyCommandSpec("mvmctl.cli.init",    "app",        "Initialize mvm"),
    "kernel":  _LazyCommandSpec("mvmctl.cli.kernel",   "app",        "Kernel management"),
    "image":   _LazyCommandSpec("mvmctl.cli.image",    "app",        "Image management"),
    "bin":     _LazyCommandSpec("mvmctl.cli.bin",      "app",        "Binary management"),
    "cache":   _LazyCommandSpec("mvmctl.cli.cache",   "app",        "Cache management"),
    "logs":    _LazyCommandSpec("mvmctl.cli.logs",    "app",        "VM log management"),
    "ssh":     _LazyCommandSpec("mvmctl.cli.ssh",     "app",        "VM SSH access"),
}
```

`LazyMVMGroup.get_command()` imports module on first access via `importlib.import_module()`. Typer apps converted via `typer.main.get_command()`.

Root-level commands (`version`, `help`) are plain `click.Command` in `main.py`.

## TYPER APP CONFIGURATION (MANDATORY)

Every sub-app MUST use these exact settings:

```python
app = typer.Typer(
    help="...",
    no_args_is_help=True,       # Show help when no args given
    rich_markup_mode=None,      # Plain Click help — no Rich panels
    add_completion=False,       # No --install-completion/--show-completion
)
```

Missing `rich_markup_mode=None` causes Rich markup in help output.

## NONE-DEFAULT + RUNTIME RESOLUTION (STRICT ENFORCEMENT)

Typer option defaults **MUST BE `None`**. NO EXCEPTIONS for config-backed values.

### Why This Rule Exists

Defaults come from SQLite state (`$MVM_CACHE_DIR/mvmdb.db`) or `_defaults.py` at **runtime**, not at import time. Hardcoding in `typer.Option()`:
1. Bypasses user config changes
2. Ignores environment variable overrides  
3. Breaks the configuration priority chain
4. Makes CLI help text show stale/incorrect values

### Default Value Ownership (ARCHITECTURAL RULE)

**CRITICAL: CLI layer MUST NOT query the database directly.** 

The CLI is a thin client that:
1. Parses command-line flags and arguments
2. Passes values to the API layer (use `None` when user didn't specify)
3. Formats API responses for display

**Database queries for defaults belong EXCLUSIVELY in the API layer.** When CLI passes `None` for a value that lives in the database (e.g., default image, kernel, binary), the API layer must query the database and resolve the default.

| Layer | Database Query Policy | Reason |
|-------|----------------------|--------|
| **CLI** | **NO database queries** — only passes `None` or explicit values to API | CLI is a client; DB access is an implementation detail |
| **API** | **MUST query database** when CLI passes `None` for DB-backed defaults | API owns the database boundary; resolves defaults before calling Core |
| **Core** | **NO database queries** — receives explicit values from API | Core operates on explicit inputs only |

### Example: Correct vs Incorrect Default Resolution

**INCORRECT — CLI queries database:**
```python
# CLI layer (WRONG - don't do this)
def _resolve_default_image() -> str | None:
    from mvmctl.api.metadata import get_default_image_entry
    default_entry = get_default_image_entry()  # ❌ CLI querying DB via API
    if default_entry:
        return default_entry[0]
    return None

@app.command()
def create(
    image: Optional[str] = typer.Option(None, "--image", ...),
) -> None:
    effective_image = image or _resolve_default_image()  # ❌ CLI resolving DB default
    create_vm(image=effective_image, ...)  # Passes resolved value
```

**CORRECT — API queries database:**
```python
# CLI layer (CORRECT - just pass None)
@app.command()
def create(
    image: Optional[str] = typer.Option(None, "--image", ...),
) -> None:
    create_vm(image=image, ...)  # ✅ Passes None if user didn't specify

# API layer (CORRECT - resolves from DB)
def create_vm(image: Optional[str] = None, ...) -> VMInstance:
    if image is None:
        image = _resolve_default_image_from_db()  # ✅ API queries DB
    return _core_create_vm(image=image, ...)
```

### Absolute Rules (VIOLATION = IMMEDIATE REJECTION)

| Forbidden | Consequence |
|-----------|-------------|
| `typer.Option(DEFAULT_*, ...)` using any constant | Hardcoded default bypasses runtime config |
| `typer.Option(get_assets_dir(), ...)` | Function evaluated at import time, ignores env vars |
| `typer.Option([], ...)` for list types | Breaks Typer internals; use `None` with runtime list() conversion |
| `typer.Option(True/False, ...)` for config-backed booleans | Must use `None` for tri-state (user/config/default resolution) |
| **CLI functions that call `get_default_*_entry()` or query database** | Violates layer boundary — CLI should NOT know about database |
| Default values in `api/` function parameters | API receives explicit values from CLI; must resolve DB defaults internally |
| Default values in `core/` function parameters | Core receives explicit values from API; never use `def func(arg=DEFAULT_VALUE)` |

### Correct Pattern (MANDATORY)

```python
# Step 1: All typer params use None default
vcpus: Optional[int] = typer.Option(None, "--vcpus", ...)
lines: int = typer.Option(None, "--lines", ...)  # int, not Optional[int], but still None default

# Step 2: Pass directly to API (let API resolve DB defaults)
defaults = _get_vm_defaults()  # Only for config.json defaults (NOT DB defaults)
effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
create_vm(vcpus=effective_vcpus, image=image)  # image is None if not specified
```

### Verification Checklist

Before submitting any CLI change, verify:
- [ ] No `typer.Option(DEFAULT_*` patterns exist
- [ ] No `typer.Option(get_*_dir()` patterns exist  
- [ ] No `typer.Option([])` patterns exist
- [ ] **No CLI functions call `get_default_image_entry()`, `get_default_kernel_entry()`, `get_default_binary_entry()`, or any database query functions**
- [ ] All config-backed values resolve at runtime, not import time
- [ ] Help text does not show hardcoded defaults for config-backed options

### Enforcement

CI checks will reject PRs containing:
- Named constants in `typer.Option()` defaults
- Function calls as `typer.Option()` defaults
- Non-None defaults for values that should be config-backed
- **CLI code that queries the database for defaults** (even via API wrappers)

**NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**

## MULTIPLE POSITIONAL ARGS

Typer `nargs=-1` cannot have a non-empty default. Use this pattern:

```python
# CORRECT — Optional[List[str]], convert to list
ids: Optional[List[str]] = typer.Argument(None, help="IDs to remove")
effective_ids = list(ids) if ids else []

# WRONG — empty list as default
ids: List[str] = typer.Argument([], help="IDs to remove")  # Typer fails
```

## IMAGE, KERNEL, BIN — SEPARATE STANDALONE APPS

Asset management is split into three separate modules, each exporting its own `typer.Typer()` app:

| Module | App | Commands |
|--------|-----|----------|
| `cli/kernel.py` | `app` | `ls`, `fetch`, `set-default`, `rm` |
| `cli/image.py` | `app` | `ls`, `fetch`, `set-default`, `rm`, `import` |
| `cli/bin.py` | `app` | `ls`, `fetch`, `set-default`, `rm` |

**Common Features:**
- All use hash-based IDs (64-char SHA256, 6-char prefix in CLI)
- All support `--type` and `--name` filtering
- All verify downloads against checksums

**Shared function:** `clear_assets()` — removes all cached `bin/`, `kernels/`, `images/` dirs.

**Callbacks:** Each app has empty callback with `invoke_without_command=True` to show help when called without subcommand.

## INIT.PY — ONBOARDING WIZARD

`mvm init` runs 8-step guided setup:

| Step | Function | Description |
|------|----------|-------------|
| 1 | `_step_host()` | Privilege setup via `sudo mvm host init` |
| 2 | `_step_cache_init()` | Initialize cache directory structure |
| 3 | `_step_local_state()` | Initialize local state/SQLite DB |
| 4 | `_step_binary()` | Download Firecracker binary |
| 5 | `_step_kernel()` | Build or fetch kernel |
| 6 | `_step_image()` | Download root filesystem image |
| 7 | `_step_ssh_key()` | Generate or import SSH key |
| 8 | `_step_summary()` | Print readiness report |

**Flags:**
- `--non-interactive` — use defaults, skip all prompts
- `--skip-host` — bypass privilege setup step

## VM RM — ID VS NAME RESOLUTION

`mvm vm rm` accepts either ID prefix or name:

```python
# By ID prefix
matches = manager.find_by_id_prefix(prefix)

# By name
matches = manager.get_by_name(name)

# Resolution logic:
# 1. Try as ID prefix first
# 2. If ambiguous (len > 1) or not found, try as name
# 3. If multiple name matches, prompt user
```

## VM CREATE — TYPICAL PATTERN

```python
def _get_vm_defaults() -> "VMDefaultsConfig":
    from mvmctl.api.config import load_config
    from mvmctl.utils.fs import get_assets_dir
    return load_config(get_assets_dir()).vm_defaults

@app.command()
def create(
    name: str = typer.Option(..., "--name", "-n", ...),
    image: Optional[str] = typer.Option(None, "--image", ...),
    kernel: Optional[str] = typer.Option(None, "--kernel", ...),
    vcpus: Optional[int] = typer.Option(None, "--vcpus", ...),
    mem: Optional[int] = typer.Option(None, "--mem", ...),
    # ...
) -> None:
    defaults = _get_vm_defaults()

    effective_image = image or _resolve_default_image()
    effective_kernel = kernel or _resolve_default_kernel()
    effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
    effective_mem = mem if mem is not None else defaults.mem_mib
    # ...
```

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| `import from mvmctl.core` | Import from `mvmctl.api` only |
| `typer.Option(2, ...)` for config values | `typer.Option(None, ...)` + runtime resolution |
| Business logic in CLI | Raise to `api/`, never touch core directly |
| `list[str] = []` as Argument default | `Optional[List[str]] = None` |
| `rich_markup_mode="rich"` or omitting it | Always `rich_markup_mode=None` |
| `add_completion=True` or omitting it | Always `add_completion=False` |

## KNOWN VIOLATIONS

- `bin.py`, `image.py`, `kernel.py` — import `mvmctl.core.metadata` directly (bypasses `api/`)
- `init.py` — imports `mvmctl.core.config_state` directly (bypasses `api/`)
