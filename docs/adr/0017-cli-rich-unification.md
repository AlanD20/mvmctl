# 0017 — CLI Rich unification: MVMCli class, unified inspect, generic tree rendering

**Status:** accepted

The project previously used a mix of output patterns across the CLI layer: a `_PlainConsole` shim in `utils/_io.py` with plain `print()` calls, hand-rolled ASCII tree builders in five CLI files, `typer.echo()` for JSON output, `rich.console.Console().status()` spinners for some long operations, and Rich Progress in the `cp` command. Each domain had its own inspect rendering functions (`_print_vm_details`, `_print_network_details_tree`, etc.) and its own table marker logic (`get_combined_marker()`).

## What changed

### 1. MVMCli class (`utils/cli.py`)

A single `MVMCli` class now owns all CLI display. It holds two `rich.console.Console` instances (stdout, stderr) and exposes:

| Method | Output | Style |
|--------|--------|-------|
| `error(msg, is_unexpected=False)` | stderr | red `✗ Error:` or yellow `⚠ Unexpected Error:` |
| `success(msg)` | stdout | green `✓` |
| `warning(msg)` | stderr | yellow `!` |
| `info(msg)` | stdout | dim, 2-space indent |
| `section_header(title)` | stdout | bold |
| `inspect_header(title, subtitle)` | stdout | bold + underline |
| `key_value(key, value, ...)` | stdout | cyan key, white value |
| `table(columns, rows, title)` | stdout | Rich Table, `box.SIMPLE` |
| `print_dict_tree(data, title)` | stdout | Rich Tree from nested dict |

Static format helpers: `check_name_arg()`, `format_timestamp(iso, style)` (relative or full), `format_size(bytes)`, `format_id(id)` (shorten), `format_marker(is_default)` (`*` or ``), `format_name(name, is_missing)` (Rich markup for missing).

A module-level singleton `mvm_cli` is exported. The `handle_errors` decorator calls `mvm_cli.error()` instead of its own `_print_error` function.

### 2. `utils/_io.py` reduced to logging

Removed: `console`, `_PlainConsole`, `_strip_markup`, all `print_*` display functions, `get_state_marker`, `get_combined_marker`. Kept only `setup_logging`, `get_logger`, `log_exception`.

### 3. API inspect methods return grouped dicts

Every `inspect()` method across all 6 domains (vm, network, image, kernel, key, volume) now returns a single canonical grouped dict. No `tree` parameter, no `is_json` parameter, no model objects. The CLI decides how to render it.

Example (VM):
```python
{
    "vm": {"name": ..., "id": ..., "status": ..., "pid": ...},
    "resources": {"vcpus": ..., "mem": ..., "disk": ...},
    "networking": {"ipv4": ..., "mac": ..., "network_name": ..., "tap_device": ...},
    "assets": {"image_name": ..., "kernel_version": ..., "binary_name": ...},
    "filesystem": {"vm_dir": ..., "rootfs_path": ..., ...},
    "console": {"relay_running": ..., "relay_pid": ..., ...},
    "volumes": [...],
}
```

### 4. Inspect command unified

Three ways to view the same grouped dict:
- **Default** — `mvm_cli.print_dict_tree(data, title)` renders as Rich Tree
- **`--json`** — `json.dumps(data, indent=2)`

No `--tree` flag. The tree IS the default human view.

All per-domain tree builders (`_print_vm_inspect_tree`, `_print_network_details_tree`, `_print_image_details_tree`, `_print_kernel_details_tree`, `_print_key_details_tree`) and per-domain section renderers (`_print_vm_details`, `_print_image_details`, etc.) are deleted.

### 5. Marker column in listing tables

`_get_combined_marker()` was removed from `utils/common.py` (zero callers remaining), replaced by `MVMCli.format_marker()`. Tables now have a narrow column at position 0 with an empty header. Content is `mvm_cli.format_marker(is_default)` — `*` or empty. Missing resources show in red via `mvm_cli.format_name(name, is_missing)` which returns `[red]{name}[/]` markup.

### 6. Consistent timestamp formatting

- **Listing tables**: relative time ("2m ago", "3h ago", "1d ago") via `mvm_cli.format_timestamp(iso, "relative")`
- **Inspect (tree view)**: full datetime (ISO string shown as-is, already readable)
- **`--json`**: raw ISO string from API (unchanged)

## Why

1. **Single visual language**: Every command speaks the same Rich dialect. No more plain print in one file and Rich in another.
2. **Eliminates duplication**: Five tree builders, six section renderers, 100+ `print_*` calls — all replaced by one `print_dict_tree` method and one `mvm_cli` singleton.
3. **API contract is cleaner**: Inspect returns one canonical shape. Frontend decides rendering. No `tree` parameter leaking API concerns.
4. **Missing/default markers are readable**: Instead of `*X myvm` prepend-hacks, a dedicated column for `*` and red coloring for missing resources. Users can scan.
5. **Progress toward release**: A consistent CLI is a prerequisite for production readiness.

## Trade-offs

- **Rich becomes a hard dependency**: The `_PlainConsole` fallback is gone. If Rich ever has a compatibility issue, all CLI output breaks.
- **`print_dict_tree` is generic**: Key prettification (snake_case → Title Case with acronym fixes) is automatic, not curated. Some keys display imperfectly (e.g. "Mem Mib", "Vcpus"). Users who need canonical output use `--json`.
- **Inspect API return type changed**: Callers accessing attributes on the old model objects will break. All known callers were updated in this change.
- **No `--tree` flag**: Users familiar with the old `--tree` flag will not find it. The tree is now the default, which is a breaking UX change.

## Implementation

Total: 23 files modified (12 CLI + 6 API + 4 utility + 1 main). Executed as a single sequential pass across ~2,500 lines of changed code. Ruff check ✅, mypy ✅ (3 pre-existing errors only).

## Future considerations

- Spinner-based `rich.console.Console().status()` calls remain in `vm create`, `image pull/import/warm`, `kernel pull`, `bin ls --remote`, `cache init`, and `init` wizard. These could migrate to `MVMCli` in the future if a common spinner abstraction emerges.

> **Implementation Note:** The `_prettify_key()` helper in `utils/cli.py` handles snake_case→Title Case conversion with acronym normalization (e.g., `nocloud_net_port`→`Nocloud Net Port` is normalized to keep acronyms uppercase). This is used by `print_dict_tree()` and `key_value()` methods.
