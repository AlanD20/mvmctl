> **⚠️ ARCHIVED — Historical document from an earlier phase.**
> The project has evolved significantly. See [CONTEXT.md](../../CONTEXT.md) for current domain language,
> [docs/PROJECT_ARCHITECTURE.md](../../docs/PROJECT_ARCHITECTURE.md) for the current architecture,
> and [docs/API.md](../../docs/API.md) for the current API reference.
> This file is kept for historical reference only.

# Firecracker Single-VM Setup

A lightweight single microVM using AWS Firecracker with Ubuntu, NAT networking, and cloud-init provisioning.

## Table of Contents
- [Quick Start](#quick-start)
- [Scripts](#scripts)
- [Configuration](#configuration)
- [Network](#network)
- [Access](#access)
- [Troubleshooting](#troubleshooting)

## Quick Start

```bash
# 1. Download assets (from parent directory)
cd ../assets && ./download-assets.sh

# 2. Setup VM (copies rootfs, configures cloud-init, sets up network)
cd ../single-vm && ./setup.sh

# 3. Start the VM
./create-vm.sh

# 4. SSH into VM (wait 30-60s for boot)
ssh -i env/vm.id_rsa root@10.10.0.2

# 5. Delete VM when done (graceful shutdown + cleanup)
./delete-vm.sh
```

## Prerequisites
- KVM support (`/dev/kvm` exists)
- Tools: `mount`, `umount`, `curl`, `ip`, `iptables`
- Assets downloaded: `../assets/download-assets.sh`
- Root privileges (sudo required)

## Scripts

| Script | Description |
|--------|-------------|
| `setup.sh` | Prepare assets, configure cloud-init, setup network |
| `create-vm.sh` | Start the Firecracker VM |
| `delete-vm.sh` | Graceful shutdown and cleanup |
| `logs-vm.sh` | View VM logs (boot or OS) |

## Configuration

Edit `config.env`:

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_VCPU` | 2 | Virtual CPUs |
| `VM_MEM_MIB` | 2048 | Memory in MiB |
| `DISK_SIZE` | 2G | Root disk size |
| `ENABLE_SOCKET` | false | Enable API socket for graceful shutdown |

### Cloud-Init

Edit `cloud-init/user-data` to customize hostname, users, passwords, SSH keys, and packages.

```bash
# Generate hashed password
echo -n "password" | mkpasswd -m sha-512 -s
```

## Network

```
Host                    Guest
┌─────────────┐       ┌─────────────┐
│ eth0        │─NAT─►│ eth0        │
│ (internet)  │       │ 10.10.0.2   │
└─────────────┘       └─────────────┘
        ▲
   ┌────┴────┐
   │fc-tap0  │ (10.10.0.1)
   └─────────┘
```

| Setting | Value |
|---------|-------|
| Guest IP | 10.10.0.2 |
| Host IP | 10.10.0.1 |
| Tap Device | fc-tap0 |
| MAC | 02:FC:00:00:00:01 |

## Access

```bash
# SSH as root (key-based)
ssh -i env/vm.id_rsa root@10.10.0.2

# Follow console logs
./logs-vm.sh
./logs-vm.sh boot

# View firecracker logs
./logs-vm.sh os
```

## Troubleshooting

**VM won't start:**
```bash
ls -la /dev/kvm
ls -la ../assets/kernels/
ls -la ../assets/images/
```

**No network:**
```bash
# Check tap interface
ip link show fc-tap0
# Check NAT rules
iptables -t nat -L -n -v
```

**Can't SSH:**
```bash
# Wait for boot (30-60s)
sleep 60
# Check cloud-init logs
ssh -i env/vm.id_rsa root@10.10.0.2 'cat /var/log/cloud-init-output.log'
```

**Manual cleanup:**
```bash
pkill -f firecracker
rm -rf env/
sudo ip link del fc-tap0 2>/dev/null || true
sudo iptables -t nat -F
```
