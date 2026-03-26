# legacy/ — Archived Bash Scripts

**Status:** Archived reference material — do NOT modify for production use.

This directory contains the original bash-based Firecracker VM setup scripts, preserved as implementation reference. The production Python CLI is in the project root (`src/mvmctl/`).

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
