> **⚠️ ARCHIVED — Historical document from an earlier phase.**
> The project has evolved significantly. See [CONTEXT.md](../CONTEXT.md) for current domain language,
> [docs/PROJECT_ARCHITECTURE.md](../docs/PROJECT_ARCHITECTURE.md) for the current architecture,
> and [docs/API.md](../docs/API.md) for the current API reference.
> This file is kept for historical reference only.

# legacy/ — Archived Bash Scripts

**Status:** Archived reference material — do NOT modify for production use.

This directory contains the original bash-based Firecracker VM setup scripts, preserved as implementation reference. The production Python CLI is in the project root (`src/mvmctl/`).

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

## Contents

| Directory | Description |
|-----------|-------------|
| `single-vm/` | Single Firecracker microVM with TAP device and NAT (see `single-vm/AGENTS.md`) |
| `multi-vm/` | Multiple VMs with bridge networking (see `multi-vm/AGENTS.md`) |
| `assets/` | Shared kernel images, rootfs images, SSH keys, and binaries for bash scripts |

## Usage

These scripts require root and direct system access:

```bash
# Single VM
cd legacy/single-vm
sudo ./setup.sh
sudo ./create-vm.sh

# Multi VM
cd legacy/multi-vm
sudo ./setup.sh
sudo ./create-vm.sh vm1
```

## Relation to Python CLI

The Python CLI (`mvm`) replaces all functionality here. See the project root `AGENTS.md` and `CLAUDE.md` for the current architecture.

The bash scripts are kept for:
- Understanding original network/TAP setup patterns
- Reference for Firecracker configuration parameters
- Troubleshooting without the Python CLI installed
