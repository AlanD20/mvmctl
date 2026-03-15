# Firecracker MicroVM Setup

A lightweight virtualization setup using Firecracker with Ubuntu.

## Table of Contents

- [Directory Structure](#directory-structure)
- [Prerequisites](#prerequisites)
- [Setup Options](#setup-options)
- [Shared Configuration](#shared-configuration)
- [Custom Images](#custom-images)
- [Troubleshooting](#troubleshooting)
- [Security Notes](#security-notes)

## Directory Structure

```
firecracker-ubuntu/
├── README.md                    # This file
│
├── single-vm/                   # Single VM setup
│   ├── README.md
│   ├── setup.sh
│   ├── network.sh
│   ├── start-vm.sh
│   ├── cleanup.sh
│   ├── firecracker.json
│   └── cloud-init/
│       └── user-data
│
└── multi-vm/                    # Multi-VM setup (recommended)
    ├── README.md
    ├── config.env               # Configuration
    ├── setup-bridge.sh
    ├── get-kernel.sh
    ├── create-vm.sh
    ├── stop-vm.sh
    ├── cleanup-all.sh
    └── vms/                    # VM directories (created at runtime)
```

## Prerequisites

### Arch Linux

```bash
# Install required packages
sudo pacman -S --needed qemu utils iptables bridge-utils curl bc wget

# Verify KVM
ls -la /dev/kvm

# Load KVM modules (if not already loaded)
sudo modprobe kvm
sudo modprobe kvm_intel  # For Intel CPUs
sudo modprobe kvm_amd   # For AMD CPUs

# Check KVM is working
kvm-ok
```

### Ubuntu/Debian

```bash
sudo apt-get update
sudo apt-get install -y qemu-utils cloud-utils genisoimage iptables curl bc bridge-utils

# Verify KVM
ls /dev/kvm
```

## Setup Options

### Option 1: Single VM (Simple)

For testing or running a single microVM:

```bash
cd single-vm
sudo ./setup.sh
sudo ./start-vm.sh
sudo ./cleanup.sh
```

**Use when:**
- You only need one VM
- Simpler setup
- Quick testing

### Option 2: Multi-VM (Recommended)

For running multiple microVMs concurrently:

```bash
cd multi-vm
sudo ./setup-bridge.sh           # Run once
sudo ./create-vm.sh vm1           # Create VMs
sudo ./create-vm.sh vm2 1 2
sudo ./stop-vm.sh vm1             # Remove specific VM
sudo ./cleanup-all.sh             # Full cleanup
```

**Use when:**
- You need multiple VMs
- You want dynamic IP management
- Better network isolation via bridge

## Shared Configuration

### Changing the Ubuntu Version

By default, the setup uses Ubuntu 24.04 LTS (Noble). You can change this:

#### Single VM

Edit `single-vm/config.env` and change `UBUNTU_VERSION`:

```bash
UBUNTU_VERSION="jammy"  # 22.04 LTS
# or
UBUNTU_VERSION="focal"   # 20.04 LTS
```

Then regenerate:
```bash
rm -f ubuntu-*-server-cloudimg-amd64.img rootfs.ext4
sudo ./setup.sh
```

#### Multi VM

Edit `config.env` and change `UBUNTU_VERSION`:

```bash
UBUNTU_VERSION="jammy"  # 22.04 LTS
# or
UBUNTU_VERSION="focal"   # 20.04 LTS
```

Then regenerate:
```bash
rm -f ubuntu-*-server-cloudimg-amd64.img base-rootfs.ext4
sudo ./setup-bridge.sh
```

#### Available Ubuntu Versions

| Codename | Version | Status |
|----------|---------|--------|
| `noble` | 24.04 LTS | Current stable |
| `jammy` | 22.04 LTS | LTS |
| `focal` | 20.04 LTS | LTS |
| `bionic` | 18.04 LTS | EOL |

### Using Different Distributions

The setup downloads Ubuntu cloud images. For other distributions:

#### AlmaLinux/RHEL

```bash
# Download AlmaLinux cloud image
curl -LO https://repo.almalinux.org/almalinux/9/BaseOS/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2
qemu-img convert -f qcow2 -O raw AlmaLinux-9-GenericCloud-latest.x86_64.qcow2 almalinux.ext4
```

#### Debian

```bash
# Download Debian cloud image
curl -LO https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2
qemu-img convert -f qcow2 -O raw debian-12-generic-amd64.qcow2 debian.ext4
```

#### Alpine

```bash
# Download Alpine virtual image
curl -LO https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-virt-3.19.1-x86_64.iso
# Convert to raw disk
```

### Custom Disk Size

#### Single VM

Edit `single-vm/config.env`:

```bash
DISK_SIZE="20G"  # Default: 10G
```

#### Multi VM

Edit `config.env`:

```bash
DISK_SIZE="20G"  # Default: 10G
```

### Custom Kernel

Both setups expect a `vmlinux` kernel image. To use a custom kernel:

1. Download or build your own kernel with Firecracker support
2. Ensure it's an uncompressed ELF binary (x86_64) or PE format (aarch64)
3. Place it as `vmlinux` in the respective directory

## Custom Images

For using different Linux distributions (Arch Linux, AlmaLinux, Debian, Alpine) or bringing your own custom image, see [custom-images.md](./custom-images.md).

## Troubleshooting

### KVM Not Available

```bash
# Check if KVM modules are loaded
lsmod | grep kvm

# Enable KVM
sudo modprobe kvm
sudo modprobe kvm_intel    # or kvm_amd

# Check permissions
ls -la /dev/kvm
sudo chmod 666 /dev/kvm    # Or add user to kvm group: sudo usermod -aG kvm $USER
```

### VM Not Starting

```bash
# Check Firecracker binary
./firecracker --version

# Check disk image exists
ls -la *.ext4

# Check kernel exists
ls -la vmlinux

# Run with verbose output
./firecracker --no-api --config-file config.json
```

### Network Not Working

```bash
# Check network interfaces
ip link
ip addr

# Check bridge (multi-vm)
ip link show br0

# Check tap devices
ip link show type tap

# Check NAT rules
sudo iptables -t nat -L -n -v

# Re-run network setup
# Single VM:
sudo ./network.sh
# Multi-vm:
sudo ./cleanup-all.sh && sudo ./setup-bridge.sh
```

### Permission Denied

```bash
# Ensure running as root
sudo -i

# Or fix KVM permissions
sudo chown root:kvm /dev/kvm
sudo chmod 660 /dev/kvm
```

### Disk Full

```bash
# Check disk space
df -h

# Clean up old VM images
rm -rf vms/*/

# Remove base image and regenerate smaller one
rm -f base-rootfs.ext4
# Edit config.env to set smaller DISK_SIZE
sudo ./setup-bridge.sh
```

### View Logs

```bash
# Firecracker logs (single-vm)
cat firecracker.log

# Firecracker logs (multi-vm)
cat vms/vm1/firecracker.log

# Kernel boot messages (serial console)
# Connect via: screen -r <pid> or microcom /dev/ttyS0
```

### Stuck VM Process

```bash
# Find stuck firecracker processes
ps aux | grep firecracker

# Kill all firecracker processes
sudo pkill -9 firecracker
```

### Recover Bridge After Crash

```bash
# Multi-vm: Remove and recreate bridge
sudo ip link del br0 2>/dev/null
sudo ./setup-bridge.sh
```

### Network Connectivity Issues

```bash
# Verify IP forwarding is enabled
cat /proc/sys/net/ipv4/ip_forward

# Manually enable if needed
sudo sysctl -w net.ipv4.ip_forward=1

# Check NAT rules
sudo iptables -t nat -L -n -v

# Verify routing
ip route

# Test ping from host
ping <guest-ip>
```

### Serial Console Not Working

```bash
# Check if screen is available
which screen

# Install screen
# Arch: sudo pacman -S screen
# Ubuntu: sudo apt install screen

# Connect to serial console
sudo screen -ls
sudo screen -r

# Alternative: use microcom
sudo microcom -s 115200 /dev/ttyS0
```

### Slow Boot or Hanging

```bash
# Check kernel boot args in config.json
# Remove "quiet" from boot_args for verbose output

# Common boot args for debugging:
# console=ttyS0 - Serial console
# earlyprintk=serial - Early kernel messages
# debug - Debug mode
```

## Security Notes

1. **Network Isolation**: The default setup uses NAT. For production, consider firewall rules
2. **SSH Keys**: Use SSH keys instead of passwords in cloud-init
3. **Disk Encryption**: For sensitive data, consider LUKS encryption
4. **Updates**: Keep the host and VM images updated
5. **Resource Limits**: Monitor resource usage to prevent DoS

## References

- [Firecracker Official Documentation](https://firecracker-microvm.github.io/)
- [Ubuntu Cloud Images](https://cloud-images.ubuntu.com/)
- [Firecracker GitHub](https://github.com/firecracker-microvm/firecracker)
