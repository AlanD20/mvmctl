# Firecracker Multi-VM Setup

A scalable multi-virtual machine setup using AWS Firecracker with Ubuntu, bridge networking, and cloud-init provisioning.

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
cd ../assets && sudo ./download-assets.sh

# 2. Setup bridge and base rootfs
cd ../multi-vm && sudo ./setup.sh

# 3. Create VMs (creates and starts automatically)
sudo ./create-vm.sh vm1              # 2 vCPU, 2048MiB, auto IP
sudo ./create-vm.sh vm2 1 1024       # 1 vCPU, 1024MiB, auto IP
sudo ./create-vm.sh vm3 2 4096       # 2 vCPU, 4096MiB, auto IP

# 4. List VMs with status
sudo ./list-vms.sh

# 5. SSH into VM (wait 30-60s for boot)
ssh -i env/vm.id_rsa root@10.20.0.2

# 6. Delete VM when done
sudo ./delete-vm.sh vm1

# 7. Cleanup everything
sudo ./cleanup.sh
```

## Prerequisites
- KVM support (`/dev/kvm` exists)
- Tools: `mkisofs`, `mount`, `umount`, `curl`, `ip`, `iptables`
- Assets downloaded: `../assets/download-assets.sh`
- Root privileges (sudo required)

## Scripts

| Script | Description |
|--------|-------------|
| `setup.sh` | Prepare bridge, base rootfs, and embed cloud-init |
| `create-vm.sh` | Create and start a new VM with auto-assigned IP |
| `delete-vm.sh` | Graceful shutdown, remove NAT rules, delete VM |
| `list-vms.sh` | List all VMs with status, IP, MAC |
| `cleanup.sh` | Stop all VMs, remove bridge, flush NAT |

## Configuration

Edit `config.env`:

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_VCPU` | 2 | Virtual CPUs (per VM) |
| `VM_MEM_MIB` | 2048 | Memory in MiB (per VM) |
| `DISK_SIZE` | 2G | Root disk size |
| `ENABLE_SOCKET` | false | Enable API socket for graceful shutdown |
| `ENABLE_PCI` | false | Enable PCI device support |
| `BOOT_ARG_LSM_FLAGS` | landlock,lockdown... | LSM modules for guest kernel |

### Network Settings

| Setting | Value | Description |
|---------|-------|-------------|
| Bridge | fc-br0 | Bridge interface name |
| Bridge IP | 10.20.0.1/24 | Host bridge IP |
| Guest Range | 10.20.0.2 - 10.20.0.254 | Auto-assigned IPs |
| Tap Prefix | fc | Tap device prefix |

### Cloud-Init

Edit `cloud-init/user-data` to customize hostname, users, passwords, SSH keys, and packages.

```bash
# Generate hashed password
echo -n "password" | mkpasswd -m sha-512 -s
```

## Network

```
                         Host
┌──────────────────────────────────────────┐
│ eth0 (internet)                          │
│ │                                        │
│ ▼                                        │
│ [NAT MASQUERADE]                         │
│ │                                        │
│ ▼                                        │
│ fc-br0 (10.20.0.1/24)                   │
│ │          │          │                 │
│ fc-vm1-0   fc-vm2-0   fc-vm3-0          │
└──┼─────────┼─────────┼─────────────────┘
   │         │         │
┌──┴───┐  ┌┴────┐  ┌─┴────┐
│ VM 1 │  │ VM 2│  │ VM 3 │
│10.20.│  │10.20│  │10.20 │
│  0.2  │  │ 0.3 │  │  0.4 │
└──────┘  └─────┘  └──────┘
```

VMs communicate via the bridge and access the internet via NAT.

## Access

```bash
# SSH as root (key-based)
ssh -i env/vm.id_rsa root@10.20.0.2

# Follow console logs
tail -f env/vm1/firecracker.console.log

# View firecracker logs
cat env/vm1/firecracker.log

# List all VMs
./list-vms.sh
```

## Troubleshooting

**VM won't start:**
```bash
ls -la /dev/kvm
ls -la ../assets/kernels/
ls -la ../assets/images/
ls -la env/base-rootfs.ext4
```

**No network:**
```bash
# Check bridge
ip link show fc-br0
ip addr show fc-br0

# Check NAT rules
sudo iptables -t nat -L -n -v
```

**Can't SSH:**
```bash
# Wait for boot (30-60s)
sleep 60

# Check cloud-init logs
ssh -i env/vm.id_rsa root@10.20.0.2 'cat /var/log/cloud-init-output.log'

# Remove old SSH fingerprint
ssh-keygen -R 10.20.0.2
```

**Manual cleanup:**
```bash
sudo pkill -f firecracker
sudo rm -rf env/
sudo ip link del fc-*-0 2>/dev/null || true
sudo ip link del fc-br0 2>/dev/null || true
sudo iptables -t nat -F
sudo iptables -F
```
