# Firecracker Manager (fcm)

A Python CLI for managing Firecracker microVMs, migrating from the existing bash-based tooling.

## Quick Start

```bash
# Install dependencies
uv sync

# Run the CLI
uv run fcm --help
uv run fcm vm list
uv run fcm vm create --name dev-01 --rootfs ubuntu-24.04
```

## Commands

- `fcm vm create` - Create a new VM
- `fcm vm delete` - Stop and remove a VM
- `fcm vm list` - List all VMs
- `fcm vm ssh` - SSH into a VM
- `fcm vm logs` - View VM logs
- `fcm image fetch` - Download and prepare images
- `fcm kernel build` - Build custom kernel

## Configuration

Edit `assets/defaults.yaml` for default settings.
