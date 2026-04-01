# Subagent Instructions
 
## Agent Role: ORCHESTRATOR ONLY
 
You are the **orchestrating agent**. You **NEVER** read files or edit code yourself. ALL work is done via subagents.
 
---
 
### ⚠️ ABSOLUTE RULES
 
1. **NEVER read files yourself** — spawn a subagent to do it
2. **NEVER edit/create code yourself** — spawn a subagent to do it
3. **ALWAYS use default subagent** — NEVER use `agentName: "Plan"` (omit `agentName` entirely)

### User Confirmation Required

**NEVER implement changes immediately without user confirmation.**

Before making any code changes:
1. Present your proposed approach to the user
2. Explain what you intend to do and why
3. Wait for explicit user approval
4. Only proceed with implementation after receiving confirmation

This applies to all edits, fixes, features, and refactoring. No exceptions.

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

# tests/unit/ — Unit Test Suite

**Scope:** 54 test files covering all CLI, API, core, utils, models, and services modules  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.  
**Parent:** See `tests/AGENTS.md` for fixtures, mocking patterns, and CliRunner conventions — not repeated here

## FILE → SOURCE MAPPING

### CLI Layer (7 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_cli_vm.py` | `cli/vm.py` | CliRunner + mocker.patch; short-ID vs name resolution |
| `test_cli_asset.py` | `cli/asset.py` | kernel/image/bin commands; patches `core.metadata` directly (known layer violation) |
| `test_cli_host.py` | `cli/host.py` | host init/clean/reset; mocked subprocess |
| `test_cli_network.py` | `cli/network.py` | network create/rm/ls; patches `api.network.*` |
| `test_cli_key.py` | `cli/key.py` | key add/create/rm; patches `api.keys.*` |
| `test_cli_config.py` | `cli/config.py` | config get/set/show; patches `api.config.*` |
| `test_cli_configure.py` | `cli/configure.py` | wizard steps; mocked binary/kernel/image flows |

### API Layer (3 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_api_vms.py` | `api/vms.py` | Verifies `cleanup_vms` is the only vm op with privilege check |
| `test_api_network.py` | `api/network.py` | Verifies privilege check on create/remove; not on list/inspect |
| `test_api_assets.py` | `api/assets.py` | Verifies direct pass-through to core (no privilege wrap) |

### Core Layer (22 files)

| Test file | Source module | Lines |
|-----------|--------------|-------|
| `test_image.py` | `core/image.py` | ~2032 — image resolution, download, import, conversion, remove |
| `test_host.py` | `core/host_setup.py` + `core/host.py` | ~1849 — init, clean, reset, iptables, sysctl |
| `test_network.py` | `core/network.py` | ~1233 — bridge, TAP, NAT, iptables chains |
| `test_vm_manager.py` | `core/vm_manager.py` | ~950 — hash-keyed CRUD, name vs short-ID lookup |
| `test_kernel.py` | `core/kernel.py` | ~800 — legacy (complete coverage) |
| `test_kernel_new.py` | `core/kernel.py` | — new feature tests; do NOT delete `test_kernel.py` |
| `test_firecracker.py` | `core/firecracker.py` | ~700 — socket, HTTP API, client lifecycle |
| `test_firecracker_client.py` | `core/firecracker.py` | — FirecrackerClient unit tests |
| `test_vm_lifecycle.py` | `core/vm_lifecycle.py` | — create/remove orchestration |
| `test_vm_lifecycle_helpers.py` | `core/vm_lifecycle.py` | — `_resolve_image_path`, `generate_vm_id` |
| `test_network_manager.py` | `core/network_manager.py` | — named networks, IP leases |
| `test_metadata.py` | `core/metadata.py` | — MetadataCache, locking, short-ID lookup |
| `test_config_gen.py` | `core/config_gen.py` | — ConfigGenerator, template rendering |
| `test_config.py` | `core/config.py` | — YAML loading, MVMConfig dataclass |
| `test_config_state.py` | `core/config_state.py` | — config.json persistence, default accessors |
| `test_binary_manager.py` | `core/binary_manager.py` | — fetch, set-default, version management |
| `test_cloud_init.py` | `core/cloud_init.py` | — ISO creation, user-data injection |
| `test_host_privileges.py` | `core/host_privilege.py` | — group membership, sudoers check |
| `test_key_manager.py` | `core/key_manager.py` | — import, generate, list, remove |
| `test_logs.py` | `core/logs.py` | — log path resolution, follow mode |
| `test_ssh.py` | `core/ssh.py` | — key resolution, command building |
| `test_user_config.py` | `core/user_config.py` | — config get/set helpers |

### Utils Layer (6 files)

| Test file | Source module | Notes |
|-----------|--------------|-------|
| `test_audit.py` | `utils/audit.py` | Tests private `_audit_logger` and `_get_audit_log_path` directly |
| `test_constants.py` | `constants.py` | Verifies `FALLBACK_*` / `DEFAULT_*` completeness |
| `test_fs.py` | `utils/fs.py` | Path resolution, SUDO_USER bridging |
| `test_http.py` | `utils/http.py` | Resumable download, SHA256, missing checksum handling |
| `test_process.py` | `utils/process.py` | `run_cmd` / `stream_cmd` — only consumer in test suite |
| `test_validation.py` | `utils/validation.py` | Name regex, boot arg rejection, IP validation |

### Root (3 files)

| Test file | Source |
|-----------|--------|
| `test_main.py` | `main.py` — LazyMVMGroup loading, `_reconcile_networks`, root commands |
| `test_vm_config_file.py` | `models/vm_config_file.py` — `--import-config` / `--output-config` |
| `test_security.py` | Cross-cutting: checksum handling, privilege escalation boundaries |

## NOTES

- **Two kernel test files coexist**: `test_kernel.py` (full legacy coverage) + `test_kernel_new.py` (new features). Do not merge or delete either.
- **VMManager mocking**: Always mock both `get_by_name()` and `find_by_id_prefix()` together — `vm rm` tries ID prefix first, then falls back to name.
- **`test_security.py`**: Not tied to a single source file — validates security properties across modules.
- **`conftest.py`** (132 lines) — provides VM fixtures (`sample_vm`, `running_vm`, `stopped_vm`, `error_vm`), network fixtures, key fixtures, and subprocess mock fixtures; autouse isolation via parent `tests/conftest.py`.
