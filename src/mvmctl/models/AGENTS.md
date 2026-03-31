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

# mvmctl/models/ — Domain Dataclasses

**Scope:** Pure data containers; no subprocess, no I/O, no side effects (except `VMConfig.__post_init__` validation)
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** `@dataclass` only; no methods with business logic

## STRUCTURE

```
src/mvmctl/models/
├── __init__.py       # Exports: VMState, VMConfig, VMInstance, ImageSpec
│                     # NOT exported: ImageImportSpec, VMCreateConfigFile
├── vm.py             # VMState (StrEnum), VMConfig, VMInstance
├── image.py          # ImageSpec, ImageImportSpec
└── vm_config_file.py # VMCreateConfigFile — JSON config file schema
```

## MODELS

### VMState (StrEnum) — `vm.py`
Values: `RUNNING`, `STOPPED`, `ERROR`

### VMConfig — `vm.py`
Fields: `name`, `vcpu_count`, `mem_size_mib`, `kernel_path`, `rootfs_path`, `guest_ip`, `guest_mac`, `gateway`, `subnet_mask`, `tap_device`, `boot_args`, `enable_api_socket`, `enable_pci`, `lsm_flags`

**`__post_init__` validation:** vCPU 1–32; mem 128–65536 MiB — the only behavioral logic on a model.

### VMInstance — `vm.py`
Fields: `name`, `id` (16-char hex), `pid`, `socket_path`, `ip`, `mac`, `network_name`, `tap_device`, `created_at`, `status` (VMState), `config` (VMConfig)

### ImageSpec — `image.py`
Fields: `id`, `name`, `source` (URL), `format`, `convert_to`, `minimum_rootfs_size`, `sha256`, `sha256_url`
Used for YAML-defined images in `images.yaml`.

### ImageImportSpec — `image.py`
Fields: `id`, `name`, `source_path` (local), `format`, `convert_to`, `minimum_rootfs_size`
**Not in `models/__init__.__all__`** — import directly from `mvmctl.models.image`.

### VMCreateConfigFile — `vm_config_file.py`
Fields: all `mvm vm create` options + `firecracker_config` (embedded Firecracker boot JSON)
Methods: `from_dict()`, `from_json_file()`, `to_json_file()`, `to_dict()`
**Not in `models/__init__.__all__`** — imported by `api/vm_config.py` directly.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Business logic in model methods | Move to `core/`; models hold data only |
| subprocess or I/O in any model | Raise to `core/` layer |
| Add fields with `default_factory` side effects | Pure defaults only |
| Import from `core/`, `api/`, or `cli/` | Models are leaf nodes — no upward deps |
