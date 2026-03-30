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

# mvmctl/services/ — Runtime Services

**Scope:** Subprocess-based runtime services for VM console access and cloud-init datasource serving
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Services run as standalone subprocesses; managers handle lifecycle in core/

## STRUCTURE

```
src/mvmctl/services/
├── __init__.py              # Package marker only
├── console_relay/           # PTY-to-socket relay for VM serial console
│   ├── __init__.py
│   ├── manager.py          # ~200 lines — ConsoleManager lifecycle
│   └── process.py          # ~150 lines — Standalone PTY relay subprocess
└── nocloud_server/          # HTTP server for cloud-init nocloud-net datasource
    ├── __init__.py
    ├── manager.py          # ~200 lines — NocloudServerManager lifecycle
    └── process.py          ~130 lines — Standalone HTTP server subprocess
```

## ARCHITECTURE

Services follow a manager+process pattern:

```
core/vm_lifecycle.py
        │
        ▼ calls
┌─────────────────┐     spawns    ┌──────────────────┐
│  Manager class  │ ─────────────►│  process.py      │
│  (in services/)   │   subprocess  │  (standalone)    │
└─────────────────┘               └──────────────────┘
        │
        ▼ manages
   VM resource
```

**Key distinction:**
- **Manager** (manager.py): Imported by core/; handles start/stop/restart; manages PID files; monitors health
- **Process** (process.py): Has `main()` entry point; runs standalone with `if __name__ == "__main__"`; minimal dependencies

## CONSOLE RELAY

**Purpose:** Bridge between Firecracker's vsock serial console and host PTY

**Manager:** `console_relay/manager.py:ConsoleManager`
- `start(vm_name, vsock_port)` → spawns `process.py`, writes PID file
- `stop(vm_name)` → reads PID, sends SIGTERM, cleans up
- `is_running(vm_name)` → checks PID file + process exists

**Process:** `console_relay/process.py`
- `main()` entry point with argparse
- Creates PTY master/slave pair
- Connects to Firecracker vsock at `vsock_port`
- Bidirectional relay: PTY ↔ vsock

**CLI access:** `mvm vm console --name <vm>` (from `cli/console.py`)

## NOCLOUD-NET SERVER

**Purpose:** HTTP server serving cloud-init meta-data/user-data/network-config to VMs

**Manager:** `nocloud_server/manager.py:NocloudServerManager`
- `start(vm_name, port, config_dir)` → spawns `process.py`, writes PID file
- `stop(vm_name)` → reads PID, sends SIGTERM, cleans up
- Port allocation: 8000-9000 range with collision detection

**Process:** `nocloud_server/process.py`
- `main()` entry point with argparse
- HTTP server bound to bridge gateway IP (not 0.0.0.0)
- Serves from `config_dir` containing:
  - `meta-data` — instance ID, hostname, public keys
  - `user-data` — cloud-init configuration
  - `network-config` — network interface configuration (v2 YAML)

**CLI enable:** `mvm vm create --name <vm> --nocloud-net` (default behavior)

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Import from `core/` in process.py | Process.py has no upward deps; only stdlib + minimal utils |
| Run manager methods in process.py | Manager runs in parent (core/), process runs standalone |
| Share state via globals | Use PID files + signal handling for coordination |
| Bind HTTP server to 0.0.0.0 | Bind to bridge gateway IP only (firewall-isolated) |

## COMMANDS

```bash
# Console relay tests
uv run pytest tests/unit/services/console_relay/ -v

# Nocloud server tests
uv run pytest tests/unit/services/nocloud_server/ -v

# Manual process execution (for debugging)
python -m mvmctl.services.console_relay.process --vm-name myvm --vsock-port 1024
python -m mvmctl.services.nocloud_server.process --port 8080 --config-dir /tmp/nocloud
```

## NOTES

- Both services write PID files to `$MVM_STATE_DIR/services/<vm_name>/`
- Port allocation for nocloud-net: tries 8000-9000, skips in-use ports
- Firewall rules (iptables) are managed by `core/network.py`, not services/
- Services auto-exit when parent process (Firecracker VM) terminates
