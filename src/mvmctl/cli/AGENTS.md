# mvmctl/cli/ — CLI Layer

**Scope:** Typer command definitions only — arg parsing, output formatting, NO business logic
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Call `api/` for everything; never import from `core/` directly

## STRUCTURE

```
src/mvmctl/cli/
├── vm.py          # VM subcommands: create, rm, ls, ps, ssh, logs, prune, snapshot, load (1020 lines)
├── bin.py         # kernel/image/bin subcommands — THREE Typer apps in one file (1548 lines)
├── init.py        # Guided onboarding wizard: mvm init (437 lines)
├── host.py        # Host subcommands: init, ls, clean, reset
├── network.py     # Network subcommands: create, rm, ls, inspect
├── key.py         # SSH key subcommands: add, create, ls, rm, inspect
├── config.py      # Config subcommands: get, set, show, validate, dump-vm
├── console.py     # VM console access via PTY-over-vsock
├── cache.py       # Cache management: init, prune
├── ssh.py         # VM SSH helper commands
├── logs.py        # VM log viewing: --follow, --lines, --type
└── _helpers.py    # Internal: check_name_arg() guard for positional name args
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
    "kernel":  _LazyCommandSpec("mvmctl.cli.bin",     "kernel_app", "Kernel management"),
    "image":   _LazyCommandSpec("mvmctl.cli.bin",     "image_app",  "Image management"),
    "bin":     _LazyCommandSpec("mvmctl.cli.bin",     "bin_app",    "Binary management"),
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

**ONLY the CLI layer may resolve default values at runtime.** API and Core layers **MUST NOT** have default values in function parameters — they operate on explicit values only.

| Layer | Default Policy | Reason |
|-------|----------------|--------|
| **CLI** | Runtime resolution via `_get_vm_defaults()` pattern | User-facing interface needs configurable defaults |
| **API** | **NO defaults in params** — only privilege checks + delegation | Public boundary should receive explicit values |
| **Core** | **NO defaults in params** — business logic operates on what it's given | Business logic should not make assumptions about inputs |

### Absolute Rules (VIOLATION = IMMEDIATE REJECTION)

| Forbidden | Consequence |
|-----------|-------------|
| `typer.Option(DEFAULT_*, ...)` using any constant | Hardcoded default bypasses runtime config |
| `typer.Option(get_assets_dir(), ...)` | Function evaluated at import time, ignores env vars |
| `typer.Option([], ...)` for list types | Breaks Typer internals; use `None` with runtime list() conversion |
| `typer.Option(True/False, ...)` for config-backed booleans | Must use `None` for tri-state (user/config/default resolution) |
| Default values in `api/` function parameters | Violates layer boundary — API receives explicit values from CLI |
| Default values in `core/` function parameters | Violates layer boundary — Core operates on explicit inputs only |

### Correct Pattern (MANDATORY)

```python
# Step 1: All typer params use None default
vcpus: Optional[int] = typer.Option(None, "--vcpus", ...)
lines: int = typer.Option(None, "--lines", ...)  # int, not Optional[int], but still None default

# Step 2: Runtime resolution inside function
defaults = _get_vm_defaults()
effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
effective_lines = lines if lines is not None else DEFAULT_VM_LOG_LINES
```

### Verification Checklist

Before submitting any CLI change, verify:
- [ ] No `typer.Option(DEFAULT_*` patterns exist
- [ ] No `typer.Option(get_*_dir()` patterns exist  
- [ ] No `typer.Option([])` patterns exist
- [ ] All config-backed values resolve at runtime, not import time
- [ ] Help text does not show hardcoded defaults for config-backed options

### Enforcement

CI checks will reject PRs containing:
- Named constants in `typer.Option()` defaults
- Function calls as `typer.Option()` defaults
- Non-None defaults for values that should be config-backed

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

## BIN.PY — THREE APPS IN ONE FILE

Single file (`cli/bin.py`) exports three separate `typer.Typer()` instances:

| App | Attribute | Commands |
|-----|-----------|----------|
| `kernel_app` | `kernel_ls`, `kernel_fetch`, `kernel_set_default`, `kernel_rm` | `--type firecracker\|official`, `--name`, `--clean-build` for fetch |
| `image_app` | `image_ls`, `image_fetch`, `image_set_default`, `image_rm`, `image_import` | Hash-based ID; `_find_meta_for_internal_id()` for YAML lookup |
| `bin_app` | `bin_ls`, `bin_fetch`, `bin_set_default`, `bin_rm` | SHA256 verified against GitHub releases |

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

- `bin.py` — imports `mvmctl.core.metadata` directly (bypasses `api/`)
- `init.py` — imports `mvmctl.core.config_state` directly (bypasses `api/`)
