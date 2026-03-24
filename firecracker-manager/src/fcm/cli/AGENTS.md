# fcm/cli/ — CLI Layer

**Scope:** Typer command definitions only — arg parsing, output formatting, NO business logic  
**Rule:** Call `api/` for everything; never import from `core/` directly

## STRUCTURE

```
src/fcm/cli/
├── vm.py          # VM subcommands: create, rm, ls, ps, ssh, logs, prune, snapshot (650 lines)
├── asset.py       # kernel/image/bin subcommands — THREE Typer apps in one file (874 lines)
├── configure.py   # Guided onboarding wizard: fcm configure (388 lines)
├── host.py        # Host subcommands: init, reset, status, clean (250 lines)
├── network.py     # Network subcommands: create, rm, ls, inspect, status (195 lines)
├── key.py         # SSH key subcommands: add, create, ls, rm, inspect (190 lines)
├── config.py      # Config subcommands: get, set, show, validate, dump-vm (115 lines)
└── _helpers.py    # Internal: check_name_arg() guard for positional name args
```

## SUBCOMMAND WIRING (main.py)

```python
app.add_typer(vm.app,           name="vm",      rich_help_panel="VM Management")
app.add_typer(host.app,         name="host",    rich_help_panel="Host Management")
app.add_typer(network.app,      name="network", rich_help_panel="Networking")
app.add_typer(key.app,          name="key",     rich_help_panel="Keys")
app.add_typer(configure.app,    name="configure", ...)
app.add_typer(config_cli.app,   name="config",  ...)
# Assets — three apps from one file, all under "Assets" panel:
app.add_typer(asset.kernel_app, name="kernel",  rich_help_panel="Assets")
app.add_typer(asset.image_app,  name="image",   rich_help_panel="Assets")
app.add_typer(asset.bin_app,    name="bin",     rich_help_panel="Assets")
```

## KEY PATTERNS

### No-args shows help
```python
app = typer.Typer(no_args_is_help=True)

@app.callback(invoke_without_command=True)
def callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()
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

`fcm configure` runs a step-by-step wizard:
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
| `import from fcm.core` | Import from `fcm.api` only |
| Hardcode defaults in `typer.Option(N, ...)` | `typer.Option(None, ...)` + runtime resolution |
| Business logic (subprocess, filesystem) | Raise to `api/`, never touch core directly |
| `list[str] = []` as `typer.Argument` default | `Optional[List[str]] = None` |
