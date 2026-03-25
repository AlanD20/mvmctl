# mvmctl/cli/ — CLI Layer

**Scope:** Typer command definitions only — arg parsing, output formatting, NO business logic  
**Rule:** Call `api/` for everything; never import from `core/` directly

## STRUCTURE

```
src/mvmctl/cli/
├── vm.py          # VM subcommands: create, rm, ls, ps, ssh, logs, prune, snapshot, load (655 lines)
├── asset.py       # kernel/image/bin subcommands — THREE Typer apps in one file (889 lines)
├── configure.py   # Guided onboarding wizard: mvm configure (392 lines)
├── host.py        # Host subcommands: init, ls, clean, reset (263 lines)
├── network.py     # Network subcommands: create, rm, ls, inspect (195 lines)
├── key.py         # SSH key subcommands: add, create, ls, rm, inspect (190 lines)
├── config.py      # Config subcommands: get, set, show, validate, dump-vm (115 lines)
└── _helpers.py    # Internal: check_name_arg() guard for positional name args
```

## SUBCOMMAND WIRING (main.py)

Root app is `LazyMVMGroup` (custom `click.Group`), NOT `typer.Typer`. Sub-apps are lazy-loaded:

```python
_COMMAND_SPECS: dict[str, _LazyCommandSpec] = {
    "vm": _LazyCommandSpec("mvmctl.cli.vm", "app", "VM lifecycle management"),
    "host": _LazyCommandSpec("mvmctl.cli.host", "app", "Host configuration"),
    ...
    "kernel": _LazyCommandSpec("mvmctl.cli.asset", "kernel_app", "Kernel management"),
    "image": _LazyCommandSpec("mvmctl.cli.asset", "image_app", "Image management"),
    "bin": _LazyCommandSpec("mvmctl.cli.asset", "bin_app", "Binary management"),
}
```

`LazyMVMGroup.get_command()` imports the module on first access via `importlib.import_module()`. Root-level commands (`clear`, `version`, `help`) are plain `click.Command` instances defined directly in `main.py`.

## KEY PATTERNS

### Typer App Configuration (MANDATORY for all sub-apps)
```python
app = typer.Typer(
    help="VM lifecycle management",
    no_args_is_help=True,
    rich_markup_mode=None,      # Plain Click help — no Rich panels
    add_completion=False,        # No --install-completion/--show-completion
)
```

### None-default + runtime resolution (MANDATORY for config-backed values)
```python
# CORRECT — default None, resolve at runtime
vcpus: Optional[int] = typer.Option(None, "--vcpus", ...)
effective_vcpus = vcpus if vcpus is not None else _defaults.vcpu_count

# WRONG — hardcoded default
vcpus: int = typer.Option(2, "--vcpus", ...)
```

### Multiple positional args (Typer nargs=-1 workaround)
```python
ids: Optional[List[str]] = typer.Argument(None, help="Short IDs to remove")
effective_ids = list(ids) if ids else []
```
Use `Optional[List[str]] = None` NOT `list[str] = []` — Typer `nargs=-1` cannot have a non-empty default.

### VM rm — ID vs name resolution
```python
# Short ID path:
matches = manager.find_by_short_id(short_id)  # → list[VMInstance]
# Name path:
matches = manager.get_by_name(name)           # → list[VMInstance], prompts if len > 1
```

## ASSET.PY — Three Apps in One File

`asset.py` exports `kernel_app`, `image_app`, `bin_app` — each a separate `typer.Typer()`:

| App | Commands | Notable |
|-----|----------|---------|
| `kernel_app` | ls, fetch, set-default, rm | `--type firecracker\|official` required for fetch |
| `image_app` | ls, fetch, set-default, rm, import | hash-based ID; `_find_meta_for_yaml_id()` for lookup |
| `bin_app` | ls, fetch, set-default, rm | SHA256 verified against GitHub sidecar |

`image_ls` display ID: first 6 chars of the full hash stored in `metadata.json` under `full_hash` key.

## CONFIGURE.PY — Onboarding Wizard

`mvm configure` runs a step-by-step wizard:
1. Check host (KVM, binaries)
2. Download Firecracker binary
3. Download/build kernel
4. Download image
5. Setup SSH keys
6. Initialize `config.json`

`--non-interactive` flag skips all prompts using current defaults.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| `import from mvmctl.core` | Import from `mvmctl.api` only |
| Hardcode defaults in `typer.Option(N, ...)` | `typer.Option(None, ...)` + runtime resolution |
| Business logic (subprocess, filesystem) | Raise to `api/`, never touch core directly |
| `list[str] = []` as `typer.Argument` default | `Optional[List[str]] = None` |
| `rich_markup_mode="rich"` or omitting it | Always `rich_markup_mode=None` |

## KNOWN VIOLATIONS

- `asset.py` — imports `mvmctl.core.metadata` directly (bypasses `api/`)
- `configure.py` — imports `mvmctl.core.config_state` directly (bypasses `api/`)
