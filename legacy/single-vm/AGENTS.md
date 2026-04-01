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

# Firecracker Single-VM Setup

**Scope:** Single Firecracker microVM with TAP device and NAT
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.

## STRUCTURE

```
single-vm/
├── config.env           # VM resources (vCPU, memory, disk)
├── cloud-init/
│   ├── user-data       # cloud-init config
│   └── 99-nocloud.cfg  # Data source config
├── setup.sh            # Prepare env/ directory
└── create-vm.sh        # Start Firecracker VM
```

## WHERE TO LOOK

| Task | Script |
|------|--------|
| Change VM resources | `config.env` |
| Customize cloud-init | `cloud-init/user-data` |
| Setup VM (run once) | `sudo ./setup.sh` |
| Start/stop VM | `sudo ./create-vm.sh` / `sudo ./delete-vm.sh` |
| View boot logs | `./logs-vm.sh` |
| View Firecracker logs | `./logs-vm.sh os` |

## NETWORK

```
Host                    Guest
eth0 ──NAT──► fc-tap0 ──► eth0 (10.10.0.2)
      (MASQUERADE)      gw: 10.10.0.1
```

- **Guest IP**: 10.10.0.2
- **Host TAP**: fc-tap0 (10.10.0.1)
- **MAC**: 02:FC:00:00:00:01

## CONVENTIONS

- Generated files in `env/` (rootfs.ext4, firecracker.json, vm.id_rsa)
- Uses `../assets/` for kernel and base image
- Rootfs is copied, not shared (isolated VM)

## ANTI-PATTERNS

- **Don't edit `env/firecracker.json` directly**: It's regenerated
- **Don't run from multi-vm/**: Paths won't resolve
- **Don't forget to delete-vm.sh first**: setup.sh fails if env/ exists

## COMMANDS

```bash
# Full workflow
cd single-vm
sudo ./setup.sh           # Create env/, embed cloud-init
sudo ./create-vm.sh       # Start VM
tail -f env/firecracker.console.log
./ssh.sh                  # SSH as root
sudo ./delete-vm.sh       # Cleanup
```

## NOTES

- **Socket API**: Set `ENABLE_SOCKET=true` in config.env for graceful shutdown
- **Disk size**: Configured in config.env, applied during setup.sh
- **Boot args**: IP configuration hardcoded in create-vm.sh
