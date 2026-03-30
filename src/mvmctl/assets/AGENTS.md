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

### Agent CLI Execution
 
To execute the `mvmctl` CLI with proper group privileges, use:
`sg mvm -c 'mvm ...'`

---

# mvmctl/assets/ — Bundled Configuration

**Scope:** Static YAML/JSON assets bundled with the package; read at runtime, never mutated  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.  
**Rule:** Never read directly — access via `core/config.py:load_config()` or `utils/fs.get_assets_dir()`

## STRUCTURE

```
src/mvmctl/assets/
├── defaults.yaml             # Master config: all runtime defaults (136 lines)
├── images.yaml               # Image catalog: 5 entries with URLs + convert specs
├── kernels.yaml              # Kernel catalog: build-from-source + prebuilt entries
└── firecracker.template.json # Firecracker boot JSON template with {placeholder} vars
```

## HOW ACCESSED

```python
# YAML loading goes through core/config.py
from mvmctl.api.config import load_config
from mvmctl.utils.fs import get_assets_dir
config = load_config(get_assets_dir())  # Returns MVMConfig dataclass

# Image/kernel catalog lists
from mvmctl.core.config import load_images_from_yaml, load_kernels_from_yaml
images = load_images_from_yaml(get_assets_dir())   # list[ImageSpec]
kernels = load_kernels_from_yaml(get_assets_dir())  # list[KernelSpec]

# Path to assets dir (inside installed package — NOT under cache)
from mvmctl.utils.fs import get_assets_dir
assets = get_assets_dir()  # Path(__file__).parent.parent / "assets"
```

## FILE SCHEMAS

### `defaults.yaml` — Master Runtime Defaults

Top-level sections map to `MVMConfig` dataclass fields:

| Section | Key fields |
|---------|-----------|
| `firecracker` | `binary`, `socket_dir`, `run_dir`, `log_dir`, `versions.full/ci` |
| `vm_defaults` | `vcpu_count` (2), `mem_size_mib` (2048), `ssh_user` (root), `boot_args`, `lsm_flags` |
| `network.defaults` | `name` (default), `cidr` (172.35.0.0/24), `gateway` (172.35.0.1) |
| `vm.cloud_init` | `seed_path`, `kernel_cmdline_ds`, `final_message` — injected into cloud-init ISO |
| `vm.network_guest` | `mac_prefix` (02:FC), `iface` (eth0) |
| `vm.limits` | `max_vms` (50) — hard cap on simultaneous VMs |
| `image` | `convert_to` (ext4), `supported_extensions`, `import_format_map` |
| `host.sbin_paths` | `ip`, `iptables`, `iptables_restore`, `iptables_save`, `sysctl` — used in privilege checks |
| `host.required_binaries` | `["ip", "iptables", "qemu-img"]` — checked at host init |
| `kernel.defaults` | `version` (6.19.9), `arch` (x86_64), `build_jobs` (1) |
| `fallbacks` | Last-resort values loaded by `constants.py` via `FALLBACK_*` |
| `urls` | All download URL templates for Firecracker releases, CI kernels, kernel.org |

### `images.yaml` — Image Catalog

Each entry → `ImageSpec` dataclass. `id` becomes the CLI argument to `mvm image fetch`:

```yaml
- id: ubuntu-24.04          # mvm image fetch ubuntu-24.04
  format: tar-rootfs         # tar-rootfs | qcow2 | squashfs
  convert_to: ext4           # ext4 | btrfs
  size_mib: 2048             # Resize target after conversion
  sha256: null               # null = fetch from sha256_url sidecar
  sha256_url: https://...    # URL to SHA256SUMS file
```

**Adding an image:** Append YAML entry with unique `id`. No code changes needed.

### `kernels.yaml` — Kernel Catalog

Two entries covering both acquisition strategies:

| Key | Type | Source | Notes |
|-----|------|--------|-------|
| `kernel-official` | `official` | kernel.org tarball | Build-from-source pipeline (`core/kernel.py:build_kernel_pipeline()`) |
| `kernel-firecracker` | `firecracker` | Firecracker CI S3 | Prebuilt vmlinux — no compilation |

Each entry carries `enabled_configs`, `disabled_configs`, `set_val_configs`, `required_settings` — applied during build patching.

### `firecracker.template.json` — Boot Config Template

Python `str.format()`-style (via `utils/template.py:render_template`). Rendered by `core/config_gen.py:ConfigGenerator`:

```json
{
  "boot-source": { "kernel_image_path": "{kernel_image_path}", "boot_args": "{boot_args}" },
  "drives": {drives},
  "network-interfaces": {network_interfaces},
  "machine-config": { "vcpu_count": {vcpu_count}, "mem_size_mib": {mem_size_mib} },
  "logger": {logger},
  "metrics": {metrics}
}
```

`{drives}`, `{network_interfaces}`, `{logger}`, `{metrics}` are pre-serialized JSON strings substituted as raw values (not quoted strings).

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Parse `assets/*.yaml` directly with `yaml.safe_load` | Use `load_config()` or `load_images_from_yaml()` |
| Hardcode URLs from `defaults.yaml` in Python | Read via `MVMConfig.urls.*` |
| Edit defaults to change per-VM behavior | CLI flags or `mvm config set` |
| Add secrets or tokens to any YAML file | Environment variables only |
