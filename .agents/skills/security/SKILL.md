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

I ensure security compliance for code changes:

- **Subprocess safety** — Enforce list form, NO shell=True, proper error handling
- **Privilege model** — Validate one-time setup and runtime checks
- **File system security** — Secure paths, permissions, and sensitive files
- **Network security** — Isolate VMs with iptables, bridge binding, TAP permissions
- **Audit logging** — Log sensitive operations to audit.log

## When to use me

Use me when:
- Code uses subprocess calls
- Code performs privileged operations (network/host)
- Code touches file system or network
- Reviewing security-sensitive changes

I am NOT for general code quality — use `@.agents/skills/code-review/` skill for that.

## Subprocess Security

**ALWAYS**:
- Use list form: `["ip", "link", "add", name, "type", "bridge"]`
- Capture stderr and include in exceptions
- Raise typed `ProcessError` from utils/process.py
- Call subprocess ONLY in core/ (never cli/ or models/)

**NEVER**:
- Use `shell=True` in subprocess calls
- Use shell string form: `"ip link add ..."`
- Ignore CalledProcessError silently

**Example**:
```python
try:
    subprocess.run(["ip", "link", "add", name, "type", "bridge"], 
                  capture_output=True, text=True, check=True)
except subprocess.CalledProcessError as e:
    raise NetworkError(f"Bridge creation failed: {e.stderr}") from e
```

## Privilege Model

### One-Time Setup
```bash
sudo mvm host init  # Creates mvm group, sudoers drop-in
```
After init: NO sudo needed for normal commands.

### Runtime Checks
```python
from mvmctl.core.host_privilege import check_privileges
check_privileges("/usr/sbin/ip")  # Validates mvm group membership
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

- **iptables chain**: `MVM-NOCLOUD-INPUT` for cloud-init HTTP server
- **Rule comments**: `# mvm-nocloud:<vm_name>:<port>` for auditability
- **Source-based**: Only VM's IP can reach its nocloud server
- **Bridge naming**: `mvm-{network_name}` (e.g., `mvm-default`)
- **TAP naming**: `mvm-{net[:3]}-{vm[:3]}-{rand3}` (15-char limit)
- **Gateway binding**: HTTP servers bind to bridge gateway IP (not 0.0.0.0)

## Audit Logging

**Purpose**: Append-only operation log for security auditing.

**Location**: `$MVM_CACHE_DIR/audit.log`

**Called From**: cli/ layer only (NOT from api/ or core/)

**Usage**:
```python
from mvmctl.utils.audit import log_audit
log_audit("vm_create", {"name": vm_name, "image": image_id})
```

## Security Checklist

- [ ] Subprocess uses list form (NO shell=True)
- [ ] Subprocess calls only in core/ (never cli/ or models/)
- [ ] Privilege checks via `check_privileges()` in api/ layer
- [ ] File paths use `get_cache_dir()` / `get_config_dir()` helpers
- [ ] Network rules in `MVM-NOCLOUD-INPUT` chain only
- [ ] Per-VM firewall rules with source IP restriction
- [ ] HTTP servers bind to bridge gateway (not 0.0.0.0)
- [ ] Audit logging for sensitive operations
- [ ] SUDO_USER handled correctly (resolve to invoking user)
- [ ] Files created during init chowned back to invoking user

## Quick Reference

| Aspect | Pattern | Location |
|--------|---------|----------|
| Subprocess | List form, NO shell=True | core/ only |
| Privilege checks | `check_privileges(binary_path)` | api/ layer |
| File paths | `get_cache_dir()` / `get_config_dir()` | utils/fs.py |
| Network rules | `MVM-NOCLOUD-INPUT` chain | core/network.py |
| Audit logging | `log_audit(action, details)` | cli/ layer |
| SUDO_USER | Resolve to invoking user | utils/fs.py |
