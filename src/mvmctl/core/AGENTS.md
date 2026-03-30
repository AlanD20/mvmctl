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

### CI Verification (MANDATORY)

**ALL code changes MUST pass CI checks before completion.**

Before finishing any implementation, you MUST verify:

1. **Ruff Linting** — `uv run ruff check src/` must be clean
2. **Ruff Formatting** — `uv run ruff format --check src/` must pass  
3. **Type Checking** — `uv run mypy src/` must pass (strict mode)
4. **Tests** — `uv run pytest tests/ -q --cov=src/mvmctl --cov-fail-under=80` must pass

**If checks fail:**
- Fix linting/formatting issues with `uv run ruff check src/ --fix` and `uv run ruff format src/`
- Fix type errors with proper type annotations
- Fix failing tests — NEVER delete tests to make them pass

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

# mvmctl/core/ — Business Logic Layer

**Scope:** All subprocess calls, privilege checks, VM lifecycle, network, image, kernel  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Return data or raise typed exceptions — NEVER format output here

## STRUCTURE

```
src/mvmctl/core/
├── vm_lifecycle.py      # VM create/start/stop/remove (556 lines)
├── vm_manager.py        # VM registry; state.json keyed by full 64-char hash (306 lines)
├── network.py           # Low-level: bridge, TAP, NAT, iptables (815 lines)
├── network_manager.py   # Named networks with IP lease tracking (499 lines)
├── host.py              # Host orchestration: clean/prune/reset (145 lines)
├── host_setup.py        # Host init: KVM, sysctl, binary checks (334 lines)
├── host_privilege.py    # Group/sudoers management; check_privileges() (296 lines)
├── host_state.py        # Host state snapshots for rollback (204 lines)
├── image.py             # Image download, QCOW2→raw conversion, partition extract (666 lines)
├── kernel.py            # Kernel fetch (FC CI S3) + build-from-source pipeline (805 lines)
├── binary_manager.py    # Firecracker/jailer version management (283 lines)
├── metadata.py          # Unified metadata.json for images/kernels/binaries + default markers (508 lines)
├── config_state.py      # config.json persistence + metadata-backed default accessors (223 lines)
├── config_gen.py        # Generates Firecracker boot JSON (202 lines)
├── firecracker.py       # HTTP API client for live VM control (265 lines)
├── ssh.py               # SSH command building + key resolution (211 lines)
├── key_manager.py       # SSH key import/create/registry (321 lines)
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
| Firecracker binary | `binary_manager.py` | `fetch_binary()`, `set_active_version()` |
| Asset metadata | `metadata.py` | `find_images_by_short_id()`, `update_kernel_entry()` |
| Active version/binary | `config_state.py` | `get_firecracker_config()`, `update_firecracker_config()` |
| Firecracker HTTP API | `firecracker.py` | `FirecrackerClient` |
| Config dataclass | `config.py` | `MVMConfig`, `load_config()` |

## STATE SCHEMAS

**VM state** (`$MVM_CACHE_DIR/vms/state.json`):
```json
{ "vms": { "<full-64-char-sha256>": { "id": "...", "name": "myvm", "pid": 1234, ... } } }
```
- Key = full 64-char hash generated by `generate_vm_id(name)` at creation
- `VMManager.get(name)` searches by name; `find_by_short_id(prefix)` searches by hash prefix
- Migration: old name-keyed state auto-migrates on first load

**Asset metadata** (`$MVM_CACHE_DIR/metadata.json`):
```json
{
  "images":  { "<full-hash>": { "yaml_id": "ubuntu-24.04", "filename": "...", "is_default": 0|1, ... } },
  "kernels": { "<full-hash>": { "filename": "vmlinux", "version": "6.1", "is_default": 0|1, ... } },
  "binaries": {
    "firecracker": { "binary_name": "firecracker", "binary_path": ".../firecracker-v1.15.0", "full_version": "v1.15.0", "ci_version": "v1.15", "package_version": "1.15.0", "default_binary_path": ".../firecracker", "is_default": 0|1, ... },
    "jailer":      { "binary_name": "jailer", "binary_path": ".../jailer-v1.15.0", "full_version": "v1.15.0", "ci_version": "v1.15", "package_version": "1.15.0", "default_binary_path": ".../jailer", "is_default": 0|1, ... }
  }
}
```
- Use `find_images_by_short_id(cache_dir, "abc123")` for 6-char prefix lookup
- Images downloaded via `mvm image fetch` store `yaml_id` to link back to images.yaml
- Exactly one entry per section should carry `is_default: 1` when a default is set

**Config** (`$MVM_CONFIG_DIR/config.json`):
```json
{
  "assets": { "kernels_dir": "...", "images_dir": "...", "bin_dir": "...", ... }
}
```
- Image/kernel/binary defaults are metadata-backed and not stored under `config.json.defaults`

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

### vm_lifecycle.py (556 lines)
- `_resolve_image_path(image)` — checks all extensions + metadata short-hash lookup
- `generate_vm_id(name)` — `sha256(name:timestamp).hexdigest()`
- `create_vm()` — full orchestration: image→rootfs copy, cloud-init, config, network, process, register
- TAP naming: `mvm-{net[:3]}-{vm[:3]}-{rand3}` (15-char Linux IFNAMSIZ limit)

### network_manager.py (499 lines)
- `NetworkConfig` + `NetworkLease` dataclasses; persisted as JSON under `$MVM_CACHE_DIR/networks/`
- Bridge = `mvm-{network_name}` (e.g. `mvm-default`)
- `ensure_default_network()` — idempotent; called at VM create and host init

### kernel.py (805 lines)
- `fetch_kernel_sha256(version)` — fetches `.sha256` sidecar before download
- `build_kernel_pipeline()` — auto-fetches sha256, downloads tarball, patches config, builds, returns `KernelPipelineResult`
- `download_firecracker_kernel()` — downloads prebuilt from Firecracker CI S3
- `human_readable_time(iso)` — "5 minutes ago" format; imported by CLI asset.py
- `parse_kernel_filename(name)` → `ParsedKernelFilename(base_name, version, arch)`
- Implements config fragments merging and `--clean-build` cache bypassing logic.

### image.py (666 lines)
- `fetch_image(spec, out, force)` — download + sha256 verify + optional QCOW2 convert
- `import_image(spec, output_dir)` — local file conversion to ext4/btrfs
- `_detect_and_rename_fs(path)` — uses `blkid` to detect FS, renames `.img` → `.ext4` etc.

### metadata.py (508 lines)
- `find_images_by_short_id(cache_dir, short_id)` → `list[tuple[str, dict]]` (full_key, meta)
- `find_kernels_by_short_id(cache_dir, short_id)` → same
- `update_kernel_entry()`, `update_image_entry()` — upsert by full key
- `MetadataCache` class with LRU cache and TTL for read performance
