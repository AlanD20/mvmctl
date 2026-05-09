---
name: security
version: 1.0.0
description: Ensure security compliance and prevent dangerous actions in mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "security", "subprocess", "privilege"]
  workflow: security
---

## What I do

I ensure security compliance:

- **Trust is a liability** — Input is guilty until proven innocent
- **Least privilege is law** — Ask only for what you need, and no more
- **Defense in depth** — One wall is a target; multiple walls are a maze
- **Audit everything that matters** — If you did not log it, it did not happen
- **Subprocess is a loaded weapon** — List form or death

## When to use me

Use me when:
- Code uses subprocess calls
- Code performs privileged operations (network/host)
- Code touches file system or network
- Reviewing security-sensitive changes

I am NOT for general code quality — use `@.agents/skills/code-review/` skill for that.

## Core Principles

### Principle 1: TRUST IS A LIABILITY

Every input is guilty until proven innocent:

- User input? Validate it.
- File paths? Sanitize it.
- Environment variables? Verify it.
- Network data? Assume it is hostile.

The safest code assumes everything is trying to exploit it.

**MEMO**: "Trust but verify is for diplomats. In code, distrust everything."

### Principle 2: LEAST PRIVILEGE IS LAW

Request ONLY what you need:

- Subprocess calls need to run a command — ONLY that command, NO shell
- File access needs to read a config — ONLY that file, in ONLY that directory
- Network access needs to reach one IP — ONLY that IP, on ONLY that port

Escalation is earned, not assumed.

**MEMO**: "Ask for the key to one room, not the master key to the building."

### Principle 3: DEFENSE IN DEPTH

One wall is a target. Multiple walls are a maze:

- VM network isolation via iptables
- Bridge binding with specific IPs
- Source-based firewall rules
- HTTP servers bind to bridge gateway, not 0.0.0.0

Never rely on a single security measure. If one fails, another must hold.

**MEMO**: "One castle wall is an invitation. Three moats are a deterrent."

### Principle 4: AUDIT EVERYTHING THAT MATTERS

If you did not log it, it did not happen:

- Sensitive operations MUST be logged to `$MVM_CACHE_DIR/audit.log`
- Log WHO did WHAT, WHEN, and with WHAT result
- The log is append-only — never truncate it

Audit logs are your forensic trail. They are useless if incomplete.

**MEMO**: "If it matters and you did not log it, it never happened."

### Principle 5: SUBPROCESS IS A LOADED WEAPON

The list form is MANDATORY:

```python
# SAFE
["ip", "link", "add", name, "type", "bridge"]

# DANGEROUS - NEVER
"ip link add " + name + " type bridge"  # shell injection vulnerable
"ip link add \(name\) type bridge"     # shell=True
```

**ALWAYS**:
- Use list form: `["ip", "link", "add", name, "type", "bridge"]`
- Capture stderr and include in exceptions
- Raise typed `ProcessError` from mvmctl/exceptions.py
- Call subprocess ONLY in core/ (never cli/ or models/)

**MEMO**: "The list form is safety. The string form is a loaded weapon."

## Subprocess Security

### The Doctrine

| Aspect | Safe | Forbidden |
|--------|------|-----------|
| Command form | List: `["ip", "link", "add", ...]` | String: `"ip link add ..."` |
| Shell | NEVER | Always dangerous |
| Error capture | Must include stderr | Silently ignored |
| Exception type | `ProcessError` from mvmctl/exceptions.py | Bare `Exception` |
| Location | core/ ONLY | cli/, api/, models/ — NEVER |

### Example (The Correct Way)
```python
try:
    subprocess.run(
        ["ip", "link", "add", name, "type", "bridge"],
        capture_output=True,
        text=True,
        check=True
    )
except subprocess.CalledProcessError as e:
    raise NetworkError(f"Bridge creation failed: {e.stderr}") from e
```

## Privilege Model

### One-Time Setup (The Init)
```bash
sudo mvm host init  # Creates mvm group, sudoers drop-in
```
After init: NO sudo needed for normal commands.

### Runtime Checks (The Gate)
```python
from mvmctl.core.host._helper import HostPrivilegeHelper
HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "describe operation")  # Validates mvm group membership
```

**Where to Check**:
- api/ layer: Before calling core/ for privileged ops (network/host)
- core/: Internal validation for host operations

## File System Security

| Directory | Env Var | Purpose |
|-----------|---------|---------|
| `~/.cache/mvmctl/` | `MVM_CACHE_DIR` | Binaries, kernels, images, VMs |
| `~/.config/mvmctl/` | `MVM_CONFIG_DIR` | config.json |
| `~/.local/share/mvmctl/` | `MVM_STATE_DIR` | Service PID files |

**Sensitive Files**:
- **metadata.json**: Asset registry with `is_default` flags
- **config.json**: User configuration
- **audit.log**: Append-only operation log at `$MVM_CACHE_DIR/audit.log`
- **PID files**: `$MVM_STATE_DIR/services/<vm_name>/`

## Network Security

- **iptables chain**: `MVM-NOCLOUDNET-INPUT` for cloud-init HTTP server
- **Rule comments**: `# mvm-nocloud:<vm_name>:<port>` for auditability
- **Source-based**: Only VM's IP can reach its nocloud server
- **Bridge naming**: `mvm-{network_name}` (e.g., `mvm-default`)
- **TAP naming**: `mvm-{net[:3]}-{vm[:3]}-{rand3}` (15-char limit)
- **Gateway binding**: HTTP servers bind to bridge gateway IP (not 0.0.0.0)

## Audit Logging

**Purpose**: Append-only operation log for security auditing.

**Location**: `$MVM_CACHE_DIR/audit.log`

**Called From**: api/ layer only (resolved operations), NOT from core/

**Usage**:
```python
from mvmctl.utils.auditlog import AuditLog
AuditLog.log("vm.create", changes={"name": vm_name}, context="image=abc123")
```

## Security Checklist

- [ ] Subprocess uses list form (NO shell=True)
- [ ] Subprocess calls only in core/ (never cli/ or models/)
- [ ] Privilege checks via `HostPrivilegeHelper.check_privileges()` in api/ layer
- [ ] File paths use `get_cache_dir()` / `get_config_dir()` helpers
- [ ] Network rules in `MVM-NOCLOUDNET-INPUT` chain only
- [ ] Per-VM firewall rules with source IP restriction
- [ ] HTTP servers bind to bridge gateway (not 0.0.0.0)
- [ ] Audit logging for sensitive operations
- [ ] SUDO_USER handled correctly (resolve to invoking user)
- [ ] Files created during init chowned back to invoking user

## Quick Reference

| Aspect | Pattern | Location |
|--------|---------|----------|
| Subprocess | List form, NO shell=True | core/ only |
| Privilege checks | `HostPrivilegeHelper.check_privileges(binary, operation_description)` | api/ layer |
| File paths | `get_cache_dir()` / `get_config_dir()` | utils/fs.py |
| Network rules | `MVM-NOCLOUDNET-INPUT` chain | core/network/_service.py |
| Audit logging | `AuditLog.log(operation, changes, context)` | api/ layer |
| SUDO_USER | Resolve to invoking user | utils/fs.py |

