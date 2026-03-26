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
    - Creates spec/analysis doc in docs/SubAgent docs/
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
Create a spec/analysis doc at: docs/SubAgent docs/[NAME].md
Return: summary of findings and the spec file path.
```
 
**Implementation Subagent:**
```
Read the spec at: docs/SubAgent docs/[NAME].md
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

# mvmctl/api/ — Public API Layer

**Scope:** Stable Python API boundary between CLI and core  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Role:** Add privilege checks; delegate to `core/`; export with `__all__`

## STRUCTURE

```
src/mvmctl/api/
├── vms.py       # VM operations: create, remove, list, get, ssh, logs, cleanup
├── assets.py    # Image/kernel/binary operations (356 lines)
├── host.py      # Host init/reset/status/clean + default_cache_dir()
├── network.py   # Network create/remove/list/inspect
├── keys.py      # SSH key add/create/list/remove
├── config.py    # Config get/set/dump
└── vm_config.py # VM config file load/merge/save
```

## DELEGATION PATTERN

```python
# api/network.py — privilege-checked example
from mvmctl.core.network_manager import create_network as _core_create_network
from mvmctl.core.host_privilege import check_privileges

def create_network(name: str, ...) -> NetworkConfig:
    check_privileges("/usr/sbin/ip")       # ← privilege check HERE, not in CLI
    return _core_create_network(name, ...)

__all__ = ["create_network", "remove_network", ...]
```

Key behaviors:
- Only ops that touch network/host call `check_privileges()` — not all API functions do
- They re-export core functions that need no privilege wrapper unchanged
- Return core's return value directly; never reformat output
- `api/vms.py`: only `cleanup_vms` calls `check_privileges`; `create_vm`, `remove_vm` do NOT
- `api/vm_config.py` has no `__all__` and is not re-exported from `api/__init__.py`

## API → CORE MAPPING

| API function | Core module | Notes |
|---|---|---|
| `vms.create_vm()` | `vm_lifecycle.create_vm()` | direct (no privilege check) |
| `vms.list_vms()` | `vm_manager.VMManager.list_all()` | filters by `include_stopped` |
| `vms.remove_vm()` | `vm_lifecycle.remove_vm()` | direct |
| `vms.ssh_vm()` | `ssh.connect_to_vm()` | direct |
| `assets.fetch_image()` | `image.fetch_image()` | direct pass-through |
| `assets.fetch_binary()` | `binary_manager.fetch_binary()` | direct |
| `assets.build_kernel_pipeline()` | `kernel.build_kernel_pipeline()` | direct |
| `vms.cleanup_vms()` | `vm_manager.VMManager` + `vm_lifecycle` | ONLY vm op with privilege check |
| `network.create_network()` | `network_manager.create_network()` | adds privilege check |
| `network.remove_network()` | `network_manager.remove_network()` | adds privilege check |
| `network.ensure_default_network()` | `network_manager.ensure_default_network()` | direct |
| `host.init_host()` | `host_setup.init_host()` | adds privilege check |
| `vm_config.load_vm_config_file()` | `models/vm_config_file.py` | deserialization only |
| `vm_config.merge_cli_overrides()` | `models/vm_config_file.py` | merges CLI flags into config |

## VM CONFIG FILE (vm_config.py)

`--output-config` and `--import-config` flags in `mvm vm create` are handled here:

```python
base = load_vm_config_file(Path("myvm.json"))
merged = merge_cli_overrides(base, name="override-name", vcpus=4)
save_vm_config_file(config, Path("out.json"))
```

The config file JSON includes a `firecracker_config` key with the Firecracker boot JSON embedded.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Format or print output | Return data; let CLI format |
| Business logic beyond privilege + delegation | Move to `core/` |
| Skip `__all__` | Always declare public surface |
| Import from `cli/` | One-way dependency: `cli` → `api` → `core` |
