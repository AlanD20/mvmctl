# Firecracker Multi-VM Setup

A scalable multi-virtual machine setup using AWS Firecracker with Ubuntu and bridge networking.

## Overview

This setup creates multiple microVMs with:
- **Bridge networking** for VM-to-VM communication
- **NAT** for internet access
- **Auto IP assignment** from configurable pool
- **Cloud-init provisioning** (from base image)
- **Graceful shutdown** via API when socket mode is enabled

**Default Resources Per VM:**
- 2 vCPUs (configurable)
- 2048 MiB RAM (configurable)
- 2 GB Disk (from base image)

## Prerequisites

Before using this setup, ensure you have:

1. **KVM support**: `/dev/kvm` must exist
2. **Required tools**: `mkisofs`, `mount`, `umount`, `ip`, `iptables`, `curl`
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

# 3. Create VMs (creates and starts automatically)
sudo ./create-vm.sh vm1                    # 2 vCPU, 2048MiB, auto IP
sudo ./create-vm.sh vm2 1 1024         # 1 vCPU, 1024MiB, auto IP
sudo ./create-vm.sh vm3 2 4096         # 2 vCPU, 4096MiB, auto IP

# 4. List VMs with status
sudo ./list-vms.sh

# 5. Delete a VM (graceful shutdown + removal)
sudo ./delete-vm.sh vm1

# 6. Cleanup everything when done
sudo ./cleanup.sh
```

## File Structure

| File | Description |
|------|-------------|
| `config.env` | VM and network configuration |
| `setup.sh` | Prepare bridge and base rootfs |
| `create-vm.sh` | Create and start a new VM |
| `delete-vm.sh` | Graceful shutdown and delete VM |
| `list-vms.sh` | List all VMs with status |
| `cleanup.sh` | Stop all VMs, remove bridge, flush NAT |
| `cloud-init/` | Cloud-init configuration templates |
| `env/` | Runtime directory (created by setup.sh) |
| `env/base-rootfs.ext4` | Base rootfs copied for each VM |

## Configuration

### VM Resources

Edit `config.env`:

```bash
# Default disk size (from IMAGE_SIZE)
DISK_SIZE="${IMAGE_SIZE:-2G}"

# Firecracker API socket mode
# Set to "true" to enable API socket (allows graceful shutdown)
# Set to "false" for --no-api mode
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
# 1 vCPU, 1024MiB RAM, auto IP
sudo ./create-vm.sh mediumvm 1 1024

# 2 vCPUs, 4096MiB RAM, auto IP
sudo ./create-vm.sh largevm 2 4096
```

### List VMs

```bash
# Show all VMs with status, IP, MAC
sudo ./list-vms.sh

# Output format:
# NAME         IP ADDRESS      MAC ADDRESS       STATUS     PID
# vm1          10.20.0.2       02:FC:xx:xx:xx:xx running    12345
```

### Delete VM (Graceful Shutdown)

```bash
# Graceful shutdown (if socket mode enabled) then delete
sudo ./delete-vm.sh vm1
```

If `ENABLE_SOCKET=true`, the delete script will:
1. Send `SendCtrlAltDel` action via API (graceful shutdown)
2. Wait up to 5 seconds for shutdown
3. Force kill if graceful shutdown fails
4. Remove tap device
5. Delete VM directory

If `ENABLE_SOCKET=false`:
1. Send SIGTERM
2. Send SIGKILL if needed
3. Remove tap device
4. Delete VM directory

### View VM Logs

```bash
# Console logs
tail -f env/vm1/firecracker.console.log

# Firecracker logs
cat env/vm1/firecracker.log
```

## How It Works

### 1. Setup Phase (`setup.sh`)

1. **Check dependencies**: Verifies required tools and KVM
2. **Check assets**: Ensures kernel and base image exist
3. **Prepare base rootfs**: Copy from assets
4. **Create bridge**: Sets up br0 with IP
5. **Configure NAT**: Adds iptables rules for internet access

### 2. Create VM Phase (`create-vm.sh`)

1. **Parse arguments**: name, vcpu, memory
2. **Validate**: Check KVM, bridge, base rootfs exist
3. **Assign IP**: Auto-assign from pool
4. **Create directory**: `env/<name>/`
5. **Copy rootfs**: From base-rootfs.ext4
6. **Generate config**: firecracker.json with boot args, network, resources
7. **Create cloud-init**: meta-data, network-config, user-data with hostname
8. **Embed cloud-init**: Mount rootfs and copy files
9. **Create tap**: fc-<name>-0, attach to bridge
10. **Start VM**: Firecracker with --enable-pci
11. **InstanceStart**: Send InstanceStart action via API (if socket mode)

### 3. Delete VM Phase (`delete-vm.sh`)

1. **Check if running**: Read PID from firecracker.pid
2. **Graceful shutdown**: Send SendCtrlAltDel via API (if socket file exists)
3. **Force kill**: SIGTERM, then SIGKILL if graceful shutdown fails
4. **Clean up**: Remove PID and socket files
5. **Remove tap**: Delete fc-<name>-0 interface
6. **Delete directory**: Remove env/<name>/

### 4. Cleanup Phase (`cleanup.sh`)

1. **Delete all VMs**: Run delete-vm for each VM
2. **Remove taps**: Delete all fc-* interfaces
3. **Remove bridge**: Delete br0
4. **Flush iptables**: Remove NAT rules

## Important Notes

1. **Run with sudo**: All scripts require root for networking
2. **Setup once**: Run `setup.sh` before creating VMs
3. **IP pool**: Auto-assigns from 10.20.0.2 - 10.20.0.254
4. **Bridge persists**: Bridge stays up after VM deletion
5. **Socket mode**: Enable in config.env for graceful shutdown support
6. **VM lifecycle**: Each VM is created, runs, and deleted - no pause/resume

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
grep -r "10.20.0." env/*/firecracker.json
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
- `mkisofs` - Create ISO files
- `mount/umount` - Filesystem mounting
- `ip` - Network configuration
- `iptables` - Firewall/NAT
- `curl` - API requests (for graceful shutdown)

Required assets (from `../assets/`):
- `kernels/${KERNEL_NAME}` - Kernel image
- `images/${UBUNTU_VERSION}.ext4` - Base rootfs
- `bin/firecracker` - Firecracker binary
- `keys/id_rsa.pub` - SSH public key for cloud-init

## See Also

- [Parent README](../README.md) - Full project documentation
- [Single-VM Setup](../single-vm/) - Single VM with NAT
- [Firecracker Docs](https://github.com/firecracker-microvm/firecracker)
