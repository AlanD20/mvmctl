# mvmctl/core/ — Business Logic Layer

**Scope:** All subprocess calls, privilege checks, VM lifecycle, network, image, kernel
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Return data or raise typed exceptions — NEVER format output here

## STRUCTURE

```
src/mvmctl/core/
├── vm_lifecycle.py      # VM create/start/stop/remove (2053 lines)
├── vm_manager.py        # VM registry; state.json keyed by full 64-char hash (306 lines)
├── network.py           # Low-level: bridge, TAP, NAT, iptables (1293 lines)
├── network_manager.py   # Named networks with IP lease tracking (908 lines)
├── host.py              # Host orchestration: clean/prune/reset (145 lines)
├── host_setup.py        # Host init: KVM, sysctl, binary checks (403 lines)
├── host_privilege.py    # Group/sudoers management; check_privileges() (296 lines)
├── host_state.py        # Host state snapshots for rollback (204 lines)
├── image.py             # Image download, QCOW2→raw conversion, partition extract (1622 lines)
├── kernel.py            # Kernel fetch (FC CI S3) + build-from-source pipeline (1271 lines)
├── binary_manager.py    # Firecracker/jailer version management (443 lines)
├── metadata.py          # SQLite-backed metadata helpers for images/kernels/binaries (637 lines)
├── config_state.py      # config.json persistence + SQLite-backed default accessors (349 lines)
├── config_gen.py        # Generates Firecracker boot JSON (202 lines)
├── firecracker.py       # HTTP API client for live VM control (265 lines)
├── ssh.py               # SSH command building + key resolution (211 lines)
├── key_manager.py       # SSH key import/create/registry (557 lines)
├── cloud_init.py        # cloud-init ISO creation (178 lines)
├── logs.py              # VM log retrieval (149 lines)
├── config.py            # YAML config loading (204 lines)
└── user_config.py       # User-specific config get/set (83 lines)
```

## WHERE TO LOOK

| Task | Module | Key entry point |
|------|--------|-----------------|
| Create VM | `vm_lifecycle.py` | `create_vm()` |
| Resolve image by ID/hash | `vm_lifecycle.py` | `_resolve_image_path()` |
| Remove VM | `vm_lifecycle.py` | `remove_vm()` |
| VM registry (CRUD) | `vm_manager.py` | `VMManager` class |
| Bridge/TAP/NAT | `network.py` | `setup_bridge()`, `create_tap()`, `setup_nat()` |
| iptables chains | `network.py` | `setup_mvm_chains()`, `teardown_mvm_chains()` |
| Named networks | `network_manager.py` | `create_network()`, `ensure_default_network()` |
| Host init | `host_setup.py` | `init_host()` |
| Privilege check | `host_privilege.py` | `check_privileges(binary_path)` |
| Image download/convert | `image.py` | `fetch_image()`, `import_image()` |
| Kernel fetch/build | `kernel.py` | `download_firecracker_kernel()`, `build_kernel_pipeline()` |
| Firecracker binary | `binary_manager.py` | `fetch_binary()`, `set_active_version()`, `get_binary_path()` |
| Binary default lookup | `mvm_db.py` | `db.get_default_binary("firecracker")` — SQLite is canonical; do NOT read `firecracker` symlink for state |
| Asset metadata | `metadata.py` | `find_images_by_id_prefix()`, `update_kernel_entry()` |
| Active version/binary | `binary_manager.py` + `mvm_db.py` | `get_binary_path("firecracker")` for path; `db.get_default_binary("firecracker")` for direct SQLite query |
| Firecracker HTTP API | `firecracker.py` | `FirecrackerClient` |
| Config dataclass | `config.py` | `MVMConfig`, `load_config()` |

## STATE QUERY PREFERENCE

**SQLite is the canonical source of truth for all binary/kernel/image defaults and state.**

When determining which binary/kernel/image is "active" or "default":
1. Query `MVMDatabase` first (e.g. `db.get_default_binary("firecracker")`)
2. Verify the returned path still exists on disk (stale-entry guard)
3. Do NOT read filesystem symlinks (`firecracker` → `firecracker-v1.15.0`) to derive state

The `firecracker` symlink in `bin/` is a **side-effect** of `set_active_version()` for shell/script compatibility — it is NOT the source of truth. The symlink may be absent or stale; SQLite `is_default=1` is always authoritative.

Pattern for active binary lookup:
```python
db = MVMDatabase()
default = db.get_default_binary("firecracker")
if default and Path(default.path).exists():
    return default.path
```

When to use filesystem scanning (`list_local_versions`):
- Only when discovering binaries not yet registered in SQLite (e.g. manual drops into `bin/`)
- Always pass results back through `update_binary_entry()` + `set_default_binary_entry()` to register them
- Or when an explicit `bin_dir` override is provided (test isolation / non-standard installs)

## STATE SCHEMAS

**VM state** (`$MVM_CACHE_DIR/vms/state.json`):
```json
{ "vms": { "<full-16-char-sha256>": { "id": "...", "name": "myvm", "pid": 1234, ... } } }
```
- Key = full 16-char hash generated by `generate_vm_id(name)` at creation
- `VMManager.get(name)` searches by name; `find_by_id_prefix(prefix)` searches by hash prefix
- Migration: old name-keyed state auto-migrates on first load

**Asset metadata** (SQLite `$MVM_CACHE_DIR/mvmdb.db` — canonical; `metadata.json` is a legacy compatibility shim):
```json
{
  "images":  { "<full-hash>": { "internal_id": "ubuntu-24.04", "filename": "...", "is_default": 0|1, ... } },
  "kernels": { "<full-hash>": { "filename": "vmlinux", "version": "6.1", "is_default": 0|1, ... } },
  "binaries": {
    "firecracker": { "binary_name": "firecracker", "binary_path": ".../firecracker-v1.15.0", "full_version": "v1.15.0", "ci_version": "v1.15", "package_version": "1.15.0", "default_binary_path": ".../firecracker", "is_default": 0|1, ... },
    "jailer":      { "binary_name": "jailer", "binary_path": ".../jailer-v1.15.0", "full_version": "v1.15.0", "ci_version": "v1.15", "package_version": "1.15.0", "default_binary_path": ".../jailer", "is_default": 0|1, ... }
  }
}
```
- Use `find_images_by_id_prefix(cache_dir, "abc123")` for prefix lookup
- Images downloaded via `mvm image fetch` store `internal_id` to link back to images.yaml
- Exactly one entry per section should carry `is_default: 1` when a default is set

**Config** (`$MVM_CONFIG_DIR/config.json`):
```json
{
  "assets": { "kernels_dir": "...", "images_dir": "...", "bin_dir": "...", ... }
}
```
- Image/kernel/binary defaults are SQLite-backed (not stored under `config.json.defaults`)

**Network state** (`$MVM_CACHE_DIR/networks/{name}/config.json` + `leases.json`):
- `NetworkConfig` dataclass persisted per network
- `NetworkLease` list tracks IP → VM mappings

## CONVENTIONS

### Subprocess Handling
```python
try:
    subprocess.run(["ip", "link", "add", ...], capture_output=True, text=True, check=True)
except subprocess.CalledProcessError as e:
    raise NetworkError(f"Bridge creation failed: {e.stderr}") from e
except FileNotFoundError:
    raise NetworkError("'ip' binary not found — install iproute2")
```
- Always list form (not shell string)
- Capture stderr; include in exception message
- Raise typed exception from `mvmctl.exceptions`

### Privilege Checks
```python
from mvmctl.core.host_privilege import check_privileges
check_privileges("/usr/sbin/ip")  # validates mvm group membership
```
Called in `api/` layer before entering core, or explicitly in core for ops needing root.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| `print()` or `console.print()` | Raise exception or return data; let CLI format |
| Hardcoded `"/usr/sbin/ip"` | `PRIVILEGED_BINARIES` from constants |
| `except Exception: pass` | Catch specific type, re-raise as MVMError subclass |
| Large functions (>100 lines) | Extract helpers; early returns to reduce nesting |
| `subprocess.run(..., shell=True)` | Always use list form |
| Read `bin/firecracker` symlink for state | Query `db.get_default_binary("firecracker")` — symlink is a side-effect, not source of truth |

## KNOWN VIOLATIONS

- `host_privilege.py:check_privileges_interactive()` — interactive messaging in core layer is an intentional exception for privilege setup UX. This function handles first-time user onboarding with interactive prompts and status messages. The core layer otherwise strictly returns data or raises exceptions.

## CORE LAYER OUTPUT RULE

The core layer **must not** produce console output. All output formatting belongs in the CLI layer (`cli/`).

**Correct pattern:**
```python
# core/kernel.py — return data
def build_kernel(...) -> KernelBuildResult:
    warnings = []
    if some_condition:
        warnings.append("Build may take 10-30 minutes")
    return KernelBuildResult(success=True, warnings=warnings, ...)

# cli/asset.py — format and display
def kernel_fetch(...):
    result = build_kernel(...)
    for warning in result.warnings:
        print_warning(warning)
```

**Exception:** `check_privileges_interactive()` in `host_privilege.py` is allowed to print because it's part of the first-time setup wizard (`mvm host init`) where immediate user feedback is essential for privilege configuration.

## KEY MODULES

### vm_lifecycle.py (2053 lines)
- `_resolve_image_path(image)` — checks all extensions + metadata ID prefix lookup
- `generate_vm_id(name)` — `sha256(name:timestamp).hexdigest()[:16]`
- `create_vm()` — full orchestration: image→rootfs copy, cloud-init, config, network, process, register
- TAP naming: `mvm-{net[:3]}-{vm[:3]}-{rand3}` (15-char Linux IFNAMSIZ limit)

### network_manager.py (908 lines)
- `NetworkConfig` + `NetworkLease` dataclasses; persisted as JSON under `$MVM_CACHE_DIR/networks/`
- Bridge = `mvm-{network_name}` (e.g. `mvm-default`)
- `ensure_default_network()` — idempotent; called at VM create and host init

### kernel.py (1271 lines)
- `fetch_kernel_sha256(version)` — fetches `.sha256` sidecar before download
- `build_kernel_pipeline()` — auto-fetches sha256, downloads tarball, patches config, builds, returns `KernelPipelineResult`
- `download_firecracker_kernel()` — downloads prebuilt from Firecracker CI S3
- `human_readable_time(iso)` — "5 minutes ago" format; imported by CLI asset.py
- `parse_kernel_filename(name)` → `ParsedKernelFilename(base_name, version, arch)`
- Implements config fragments merging and `--clean-build` cache bypassing logic.

### image.py (1622 lines)
- `fetch_image(spec, out, force)` — download + sha256 verify + optional QCOW2 convert
- `import_image(spec, output_dir)` — local file conversion to ext4/btrfs
- `_detect_and_rename_fs(path)` — uses `blkid` to detect FS, renames `.img` → `.ext4` etc.

### metadata.py (637 lines)
- `find_images_by_id_prefix(cache_dir, prefix)` → `list[tuple[str, dict]]` (full_key, meta)
- `find_kernels_by_id_prefix(cache_dir, prefix)` → same
- `update_kernel_entry()`, `update_image_entry()` — upsert by full key
- `MetadataCache` class with LRU cache and TTL for read performance
