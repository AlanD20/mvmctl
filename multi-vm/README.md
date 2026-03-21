# Firecracker Multi-VM Setup

A scalable multi-virtual machine setup using Firecracker with Ubuntu and bridge networking.

> **Note**: See the parent [README.md](../README.md) for shared prerequisites and troubleshooting.

## Overview

This setup is designed for running multiple microVMs concurrently:
- **Ubuntu** (default: 24.04 LTS Noble) - configurable
- **Default: 0.5 vCPU / 0.5GB RAM** (configurable)
- **Bridge + NAT** networking
- **Dynamic IP assignment** from pool (10.10.0.0/24)
- **Auto-scaling**: Create/destroy VMs quickly

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Host                                                       │
│                                                            │
│ ┌────────────┐ ┌─────────────────────────────┐            │
│ │ eth0       │ │ br0 (bridge)                │            │
│ │ (internet) │──NAT───►│ 10.10.0.1/24        │            │
│ └────────────┘   │ │ │                              │
│                  │ ┌─────┐ ┌─────┐ ┌─────┐            │
│                  │ │tap0 │ │tap1 │ │tap2 │            │
│                  │ └─────┘ └─────┘ └─────┘            │
│                  └─────────────────────────────┘            │
│                                                            │
└────────────────────────────────┼──────────────────────────┘
                                 │
┌────────────────────────────┼────────────────────────┐
│                            │                        │
┌────┴────┐ ┌────┴────┐ ┌────┴────┐
│ VM 1    │ │ VM 2    │ │ VM 3    │
│10.10.0.2│ │10.10.0.3│ │10.10.0.4│
└─────────┘ └─────────┘ └─────────┘
```

## Quick Start

```bash
cd multi-vm

# Step 1: One-time setup - creates bridge, downloads assets
sudo ./setup-bridge.sh

# Step 2: Create a VM (default: 0.5 vCPU, 0.5GB)
sudo ./create-vm.sh vm1

# Step 3: Create more VMs
sudo ./create-vm.sh vm2 1 2           # 1 vCPU, 2GB
sudo ./create-vm.sh vm3 2 4 10.10.0.50 # 2 vCPU, 4GB, static IP

# Step 4: List running VMs
ls -la vms/

# Step 5: Stop a specific VM
sudo ./stop-vm.sh vm1

# Step 6: Full cleanup (when done with all VMs)
sudo ./cleanup-all.sh
```

**For prerequisites and Ubuntu versions, see [parent README](../README.md)**.

## File Description

| File | Description |
|------|-------------|
| `config.env` | Configuration: bridge name, IP range, tap prefix |
| `setup-bridge.sh` | Downloads Firecracker, kernel, Ubuntu image; creates bridge + NAT |
| `get-kernel.sh` | Downloads vmlinux kernel |
| `create-vm.sh` | Creates a new VM with unique IP/MAC |
| `stop-vm.sh` | Stops and removes a specific VM |
| `cleanup-all.sh` | Complete cleanup: all VMs, bridge, NAT |
| `vms/` | Directory containing all VM configurations |

## Usage Examples

### Create VM with default resources (0.5 vCPU, 0.5GB)

```bash
sudo ./create-vm.sh testvm
```

### Create VM with custom resources

```bash
# 1 vCPU, 1GB RAM, auto IP
sudo ./create-vm.sh mediumvm 1 1

# 2 vCPUs, 4GB RAM, auto IP
sudo ./create-vm.sh largevm 2 4
```

### Create VM with static IP

```bash
# 1 vCPU, 1GB, specific IP
sudo ./create-vm.sh staticvm 1 1 10.10.0.100
```

### View all VMs

```bash
# List VM directories
ls -la vms/

# Show VM configs
cat vms/vm1/config.json

# Check VM process
cat vms/vm1/firecracker.pid
```

### Stop individual VM

```bash
sudo ./stop-vm.sh vm1
```

### Access VM

```bash
# Serial console (screen)
sudo screen -r $(cat vms/vm1/firecracker.pid)

# Or via SSH after VM is ready
ssh ubuntu@10.10.0.2
```

## Configuration

### Bridge Configuration (`config.env`)

```bash
BRIDGE_NAME="br0"           # Bridge interface name
BRIDGE_IP="10.10.0.1/24"  # Bridge IP/mask
GUEST_IP_START="10.10.0.2" # First available IP
GUEST_IP_END="10.10.0.254" # Last available IP
TAP_PREFIX="fc"           # Tap device prefix (e.g., fc-vm1-0)
```

### Customizing Default Resources

Edit `create-vm.sh` to change defaults:

```bash
# Lines 10-11
VM_VCPU="${2:-0.5}"  # Default: 0.5 vCPU
VM_MEM="${3:-0.5}"  # Default: 0.5GB
```

### Resource Limits

| Resource | Default | Minimum | Maximum |
|----------|---------|---------|---------|
| vCPU     | 0.5     | 0.5     | 16+     |
| Memory   | 0.5GB   | 128MB   | 64GB+   |
| Disk     | 10GB    | 1GB     | 100GB+  |

**For disk size configuration, see [parent README](../README.md)**.

## Network Configuration

### IP Address Pool

- **Network**: 10.10.0.0/24
- **Bridge IP**: 10.10.0.1
- **Available VMs**: 10.10.0.2 - 10.10.0.254 (253 VMs)

### Adding Static Routes on Host

If you need to access VMs from the host:

```bash
# Add route to VM network
sudo ip route add 10.10.0.0/24 via 10.10.0.1

# Or access directly
ssh ubuntu@10.10.0.2
```

### Firewall Considerations

The setup creates iptables NAT rules. For production:

```bash
# List NAT rules
sudo iptables -t nat -L -n -v

# Remove rules (use cleanup-all.sh instead)
sudo iptables -t nat -F
```

## Important Notes

1. **Run as root**: All scripts require sudo/root
2. **Setup once**: Run `setup-bridge.sh` only once per host
3. **Bridge persists**: Bridge stays up after VM shutdown (intentional)
4. **IP tracking**: Script auto-allocates IPs from pool
5. **Cleanup**: Use `cleanup-all.sh` to remove everything

## Scaling Considerations

### Maximum VMs

Theoretical limit: 253 VMs (limited by IP pool). Practical limits:
- **CPU**: Host CPU cores
- **Memory**: Host RAM / VM memory
- **Disk I/O**: SSD recommended for many VMs

### Performance Tips

1. Use base image with only needed packages
2. Consider using tmpfs for /tmp in VMs
3. Limit concurrent disk I/O per VM
4. Use VirtIO drivers (already configured)

### Resource Monitoring

```bash
# Host resources
free -h
htop

# Per-VM resources (inside VM)
free -h
df -h

# Network stats
ip -s link show br0
```

## Advanced Usage

### Add second drive to VM

Create the drive first:

```bash
qemu-img create -f raw mydata.ext4 5G
```

Then manually edit `vms/vm1/config.json` to add:

```json
{
  "drive_id": "data",
  "path_on_host": "/path/to/mydata.ext4",
  "is_root_device": false
}
```

### Snapshot/Clone VMs

```bash
# Copy rootfs
cp vms/vm1/rootfs.ext4 vms/vm2/rootfs.ext4

# Create new VM with existing rootfs
# (requires manual config.json creation)
```

### Custom Cloud-Init

Add cloud-init drive to config:

```json
{
  "drive_id": "cloudinit",
  "path_on_host": "cloudinit.iso",
  "is_root_device": false
}
```

## Cleanup Reference

| Command | What it does |
|---------|-------------|
| `./stop-vm.sh vm1` | Removes vm1 only |
| `./cleanup-all.sh` | Removes ALL VMs, bridge, NAT |

### Manual cleanup (if scripts fail)

```bash
# Kill all firecracker
sudo pkill firecracker

# Remove all taps
sudo ip link del fc-*-0 2>/dev/null

# Remove bridge
sudo ip link del br0

# Flush NAT
sudo iptables -t nat -F
```

**For security best practices and additional troubleshooting, see [parent README](../README.md)**.

## See Also

- [Parent README](../README.md) for prerequisites, Ubuntu versions, disk sizes, troubleshooting, and security
- [Custom Images](../custom-images.md) for using other distributions
