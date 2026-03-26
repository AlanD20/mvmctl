# mvmctl/cli/ — CLI Layer

**Scope:** Typer command definitions only — arg parsing, output formatting, NO business logic  
**Rule:** Call `api/` for everything; never import from `core/` directly

## STRUCTURE

```
src/mvmctl/cli/
├── vm.py          # VM subcommands: create, rm, ls, ps, ssh, logs, prune, snapshot, load
├── asset.py       # kernel/image/bin subcommands — THREE Typer apps in one file
├── configure.py   # Guided onboarding wizard: mvm configure
├── host.py        # Host subcommands: init, ls, clean, reset
├── network.py     # Network subcommands: create, rm, ls, inspect
├── key.py         # SSH key subcommands: add, create, ls, rm, inspect
├── config.py      # Config subcommands: get, set, show, validate, dump-vm
└── _helpers.py    # Internal: check_name_arg() guard for positional name args
```

## SUBCOMMAND WIRING

Root is `LazyMVMGroup` (custom `click.Group`), NOT `typer.Typer`. Sub-apps lazy-loaded:

```python
_COMMAND_SPECS: dict[str, _LazyCommandSpec] = {
    "vm":        _LazyCommandSpec("mvmctl.cli.vm",        "app",         "VM lifecycle management"),
    "host":      _LazyCommandSpec("mvmctl.cli.host",      "app",         "Host configuration"),
    "network":   _LazyCommandSpec("mvmctl.cli.network",   "app",         "Network management"),
    "key":       _LazyCommandSpec("mvmctl.cli.key",        "app",         "SSH key management"),
    "config":    _LazyCommandSpec("mvmctl.cli.config",     "app",         "Configuration commands"),
    "configure": _LazyCommandSpec("mvmctl.cli.configure",  "app",         "Guided setup wizard"),
    "kernel":    _LazyCommandSpec("mvmctl.cli.asset",      "kernel_app",  "Kernel management"),
    "image":     _LazyCommandSpec("mvmctl.cli.asset",      "image_app",   "Image management"),
    "bin":       _LazyCommandSpec("mvmctl.cli.asset",      "bin_app",     "Binary management"),
}
```

`LazyMVMGroup.get_command()` imports module on first access via `importlib.import_module()`. Typer apps converted via `typer.main.get_command()`.

Root-level commands (`clear`, `version`, `help`) are plain `click.Command` in `main.py`.

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

## NONE-DEFAULT + RUNTIME RESOLUTION

Typer option defaults must be `None`. Config-backed values resolved at runtime.

**Why:** Defaults live in `~/.config/mvmctl/config.json` or `assets/defaults.yaml`. Hardcoding in `typer.Option()` bypasses user config.

```python
# CORRECT — default None, resolve at runtime
vcpus: Optional[int] = typer.Option(None, "--vcpus", ...)
defaults = _get_vm_defaults()
effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count

# WRONG — hardcoded default bypasses config
vcpus: int = typer.Option(2, "--vcpus", ...)
```

**Pattern:**
1. Typer param: `typer.Option(None, ...)`
2. Runtime: `_defaults = _get_vm_defaults()`
3. Resolution: `value if value is not None else _defaults.field`

## MULTIPLE POSITIONAL ARGS

Typer `nargs=-1` cannot have a non-empty default. Use this pattern:

```python
# CORRECT — Optional[List[str]], convert to list
ids: Optional[List[str]] = typer.Argument(None, help="IDs to remove")
effective_ids = list(ids) if ids else []

# WRONG — empty list as default
ids: List[str] = typer.Argument([], help="IDs to remove")  # Typer fails
```

## ASSET.PY — THREE APPS IN ONE FILE

Single file exports three separate `typer.Typer()` instances:

| App | Attribute | Commands |
|-----|-----------|----------|
| `kernel_app` | `kernel_ls`, `kernel_fetch`, `kernel_set_default`, `kernel_rm` | `--type firecracker\|official` for fetch |
| `image_app` | `image_ls`, `image_fetch`, `image_set_default`, `image_rm`, `image_import` | Hash-based ID; `_find_meta_for_yaml_id()` for YAML lookup |
| `bin_app` | `bin_ls`, `bin_fetch`, `bin_set_default`, `bin_rm` | SHA256 verified against GitHub releases |

**Shared function:** `clear_assets()` — removes all cached `bin/`, `kernels/`, `images/` dirs.

**Callbacks:** Each app has empty callback with `invoke_without_command=True` to show help when called without subcommand.

## CONFIGURE.PY — ONBOARDING WIZARD

`mvm configure` runs 6-step guided setup:

| Step | Function | Description |
|------|----------|-------------|
| 1 | `_step_host()` | Privilege setup via `sudo mvm host init` |
| 2 | `_step_binary()` | Download Firecracker binary |
| 3 | `_step_kernel()` | Build kernel from source |
| 4 | `_step_image()` | Download root filesystem image |
| 5 | `_step_ssh_key()` | Generate or import SSH key |
| 6 | `_step_summary()` | Print readiness report |

**Flags:**
- `--non-interactive` — use defaults, skip all prompts
- `--skip-host` — bypass privilege setup step

**Entry point:**
```python
@app.callback(invoke_without_command=True)
def configure(
    non_interactive: bool = typer.Option(False, "--non-interactive", ...),
    skip_host: bool = typer.Option(False, "--skip-host", ...),
) -> None:
```

## VM RM — ID VS NAME RESOLUTION

`mvm vm rm` accepts either short ID or name:

```python
# By short ID (6 chars from full hash)
matches = manager.find_by_short_id(short_id)

# By name
matches = manager.get_by_name(name)

# Resolution logic:
# 1. Try as short ID first
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

- `asset.py` — imports `mvmctl.core.metadata` directly (bypasses `api/`)
- `configure.py` — imports `mvmctl.core.config_state` directly (bypasses `api/`)
