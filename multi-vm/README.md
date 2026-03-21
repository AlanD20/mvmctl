# Firecracker Multi-VM Setup

A scalable multi-virtual machine setup using AWS Firecracker with Ubuntu and bridge networking.

## Overview

This setup creates multiple microVMs with:
- **Bridge networking** for VM-to-VM communication
- **NAT** for internet access
- **Auto IP assignment** from configurable pool
- **Cloud-init provisioning** (from base image)

**Default Resources Per VM:**
- 0.5 vCPUs (configurable, minimum 1 as integer)
- 0.5 GB RAM (configurable)
- 2 GB Disk (from base image)

## Prerequisites

Before using this setup, ensure you have:

1. **KVM support**: `/dev/kvm` must exist
2. **Required tools**: `qemu-img`, `e2fsck`, `resize2fs`, `ip`, `iptables`, `screen`, `truncate`
3. **Assets downloaded**: Run `../assets/download-assets.sh` first
4. **Root privileges**: All operations require sudo

## Quick Start

```bash
cd multi-vm

# 1. Download assets first (from parent directory)
cd ../assets && sudo ./download-assets.sh
cd ../multi-vm

# 2. Setup bridge and base rootfs
sudo ./setup.sh

# 3. Create VMs
sudo ./create-vm.sh vm1                    # 0.5 vCPU, 0.5GB, auto IP
sudo ./create-vm.sh vm2 1 2              # 1 vCPU, 2GB, auto IP
sudo ./create-vm.sh vm3 2 4 10.20.0.50   # 2 vCPU, 4GB, static IP

# 4. View VMs
ls -la env/

# 5. Stop a specific VM
sudo ./stop-vm.sh vm1

# 6. Cleanup everything when done
sudo ./cleanup.sh
```

## File Structure

| File | Description |
|------|-------------|
| `config.env` | VM and network configuration |
| `setup.sh` | Prepare bridge and base rootfs |
| `create-vm.sh` | Create a new VM |
| `stop-vm.sh` | Stop and remove a specific VM |
| `cleanup.sh` | Stop all VMs, remove bridge, flush NAT |
| `env/` | Runtime directory (created by setup.sh) |
| `env/base-rootfs.ext4` | Base rootfs copied for each VM |

## Configuration

### VM Resources

Edit `config.env`:

```bash
# Default disk size (from IMAGE_SIZE)
DISK_SIZE="${IMAGE_SIZE:-2G}"

# Per-VM settings (passed to create-vm.sh)
# ./create-vm.sh <name> [vcpu] [memory_mib]

# Firecracker API socket mode
# Set to "true" to enable API socket, "false" for --no-api mode
ENABLE_SOCKET="${ENABLE_SOCKET:-false}"
```

### Network Configuration

Network is defined in `config.env`:

| Setting | Value | Description |
|---------|-------|-------------|
| Bridge | br0 | Bridge interface name |
| Bridge IP | 10.20.0.1/24 | Host bridge IP |
| Guest Range | 10.20.0.2 - 10.20.0.254 | Auto-assigned IPs |
| Tap Prefix | fc | Tap device prefix |

```
Network Topology:

    Host
┌─────────────────────────────────┐
│ eth0 (internet)                │
│         │                       │
│         ▼                       │
│    [NAT MASQUERADE]             │
│         │                       │
│         ▼                       │
│    br0 (10.20.0.1/24)           │
│    │     │     │                │
│    │     │     │                │
│ fc-vm1 fc-vm2 fc-vm3            │
│  -0      -0     -0              │
│   │      │      │               │
└───┼──────┼──────┼───────────────┘
    │      │      │
┌───┴───┐┌─┴────┐┌┴──────┐
│ VM 1  ││ VM 2 ││ VM 3  │
│10.20.0││10.20.││10.20. │
│   .2  ││  0.3 ││  0.4  │
└───────┘└──────┘└───────┘
```

VMs can communicate with each other via the bridge and access the internet via NAT.

## Usage Examples

### Create VM with Default Resources

```bash
sudo ./create-vm.sh testvm
```

### Create VM with Custom Resources

```bash
# 1 vCPU, 1GB RAM, auto IP
sudo ./create-vm.sh mediumvm 1 1

# 2 vCPUs, 4GB RAM, auto IP
sudo ./create-vm.sh largevm 2 4
```

### Create VM with Static IP

```bash
# 2 vCPUs, 4GB, specific IP
sudo ./create-vm.sh staticvm 2 4 10.20.0.100
```

### View Running VMs

```bash
# List VM directories
ls -la env/

# View VM config
cat env/vm1/config.json

# Check VM process
cat env/vm1/firecracker.pid
```

### Stop Individual VM

```bash
sudo ./stop-vm.sh vm1
```

### Access VM Console

```bash
# Attach to serial console
sudo screen -r fc-vm1

# Detach: Ctrl+A, then D
```

## How It Works

### 1. Setup Phase (`setup.sh`)

1. **Check dependencies**: Verifies required tools and KVM
2. **Check assets**: Ensures kernel and base image exist
3. **Create base rootfs**: Converts qcow2 → raw, resizes to DISK_SIZE
4. **Create bridge**: Sets up br0 with IP
5. **Configure NAT**: Adds iptables rules for internet access

### 2. Create VM Phase (`create-vm.sh`)

1. **Parse arguments**: name, vcpu, memory, optional IP
2. **Validate**: Check KVM, bridge, base rootfs exist
3. **Calculate resources**: Convert to Firecracker format
4. **Assign IP**: Auto-assign from pool or use provided IP
5. **Create directory**: `env/<name>/`
6. **Copy rootfs**: From base-rootfs.ext4
7. **Generate config**: config.json with boot args, network, resources
8. **Create tap**: fc-<name>-0, attach to bridge
9. **Start VM**: Firecracker in screen session

### 3. Stop VM Phase (`stop-vm.sh`)

1. **Read PID**: From firecracker.pid or <name>.pid
2. **Stop process**: SIGTERM, then SIGKILL if needed
3. **Remove tap**: Delete fc-<name>-0 interface
4. **Cleanup**: Remove VM directory

### 4. Cleanup Phase (`cleanup.sh`)

1. **Stop all VMs**: Kill all Firecracker processes
2. **Remove taps**: Delete all fc-* interfaces
3. **Remove bridge**: Delete br0
4. **Flush iptables**: Remove NAT rules

## Important Notes

1. **Run with sudo**: All scripts require root for networking
2. **Setup once**: Run `setup.sh` before creating VMs
3. **IP pool**: Auto-assigns from 10.20.0.2 - 10.20.0.254
4. **Bridge persists**: Bridge stays up after VM shutdown
5. **vCPU minimum**: Firecracker requires integer, minimum 1
6. **Memory units**: Passed in GB, converted to MiB

## Troubleshooting

### VM Won't Start

```bash
# Check KVM
ls -la /dev/kvm

# Check assets
ls -la ../assets/kernels/
ls -la ../assets/images/
ls -la ../assets/bin/firecracker

# Check base rootfs
ls -la env/base-rootfs.ext4
```

### No Network

```bash
# Recreate setup
sudo ./setup.sh

# Check bridge
ip link show br0
ip addr show br0

# Check NAT rules
sudo iptables -t nat -L -n -v
```

### IP Already in Use

```bash
# Check existing configs
grep -r "10.20.0." env/*/config.json
```

### Cleanup Fails

```bash
# Manual cleanup
sudo pkill -f firecracker
sudo rm -rf env/
sudo ip link del fc-*-0 2>/dev/null || true
sudo ip link del br0 2>/dev/null || true
sudo iptables -t nat -F
```

## Dependencies

Required commands (checked by `setup.sh`):
- `qemu-img` - Image conversion
- `e2fsck` - Filesystem check
- `resize2fs` - Filesystem resize
- `ip` - Network configuration
- `iptables` - Firewall/NAT
- `screen` - Terminal multiplexing
- `truncate` - File resizing

Required assets (from `../assets/`):
- `kernels/${KERNEL_NAME}` - Kernel image
- `images/${OS}-${VERSION}-server-cloudimg-${ARCH}.img` - Base image
- `bin/firecracker` - Firecracker binary

## See Also

- [Parent README](../README.md) - Full project documentation
- [Single-VM Setup](../single-vm/) - Single VM with NAT
- [Firecracker Docs](https://github.com/firecracker-microvm/firecracker)
