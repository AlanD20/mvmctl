# Firecracker Single-VM Setup

A simple, single virtual machine setup using AWS Firecracker with Ubuntu and NAT networking.

## Overview

This setup creates a single microVM with:
- **NAT networking** via tap interface
- **Cloud-init provisioning** for automatic configuration
- **SSH access** using pre-generated keys
- **Serial console** access via log files
- **Graceful shutdown** via API when socket mode is enabled

**Default Resources:**
- 2 vCPUs
- 2GB RAM
- 2GB Disk (configurable)

## Prerequisites

Before using this setup, ensure you have:

1. **KVM support**: `/dev/kvm` must exist
2. **Required tools**: `mkisofs`, `mount`, `umount`, `curl`, `ip`, `iptables`
3. **Assets downloaded**: Run `../assets/download-assets.sh` first
4. **Root privileges**: Most operations require sudo

## Quick Start

```bash
cd single-vm

# 1. Download assets first (from parent directory)
cd ../assets && sudo ./download-assets.sh
cd ../single-vm

# 2. Setup VM (copies rootfs, creates cloud-init config)
sudo ./setup.sh

# 3. Start the VM
sudo ./start-vm.sh

# 4. View console logs
sudo tail -f env/firecracker.console.log

# 5. SSH into VM (wait 30-60s for boot)
ssh -i env/vm.id_rsa root@10.10.0.2

# 6. Delete the VM (graceful shutdown + cleanup)
sudo ./delete-vm.sh

# 7. Cleanup everything when done (optional, delete-vm.sh already cleans up)
sudo ./cleanup.sh
```

## File Structure

| File | Description |
|------|-------------|
| `config.env` | VM configuration (CPU, memory, network, paths) |
| `setup.sh` | Prepare VM assets and embed cloud-init |
| `start-vm.sh` | Start the Firecracker VM |
| `delete-vm.sh` | Graceful shutdown and delete VM (with cleanup) |
| `network.sh` | Configure tap interface and NAT rules |
| `cleanup.sh` | Force stop VM, remove files, cleanup network |
| `cloud-init/user-data` | Cloud-init configuration template |
| `cloud-init/99-nocloud.cfg` | Cloud-init datasource config |
| `env/` | Runtime directory (created by setup.sh) |

## Configuration

### VM Resources

Edit `config.env`:

```bash
# Virtual CPUs
VM_VCPU=2

# Memory in MiB
VM_MEM_MIB=2048

# Disk size (from IMAGE_SIZE, default 2G)
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
| Guest IP | 10.10.0.2 | VM's static IP |
| Host IP | 10.10.0.1 | Host's tap interface IP |
| Tap Device | fc-tap0 | Tap interface name |
| MAC | 02:FC:00:00:00:01 | VM's MAC address |

```
Network Topology:

    Host                    Guest
┌─────────────┐         ┌─────────────┐
│ eth0        │──NAT───►│ eth0        │
│ (internet)  │         │ 10.10.0.2   │
└─────────────┘         └─────────────┘
     ▲
     │
┌────┴────┐
│fc-tap0  │ (10.10.0.1)
└─────────┘
```

The guest accesses the internet via NAT on the host.

### Cloud-Init

Edit `cloud-init/user-data` to customize:
- **Hostname**: Will be set to VM_NAME automatically
- **Users**: Modify the `users` section
- **Password**: Generate with `echo -n "password" | mkpasswd -m sha-512 -s`
- **SSH Keys**: Automatically injected from `../assets/keys/`
- **Packages**: Add to the `packages` list

See [Cloud-init docs](https://cloudinit.readthedocs.io/) for full syntax.

## Usage Examples

### Create and Start VM

```bash
# Configure resources
vim config.env

# Setup and start
sudo ./setup.sh
sudo ./start-vm.sh
```

### View Console Logs

```bash
# Follow logs in real-time
sudo tail -f env/firecracker.console.log

# View full log
sudo cat env/firecracker.log
```

### SSH Access

```bash
# As root (passwordless via SSH key)
ssh -i env/vm.id_rsa root@10.10.0.2

# As ubuntu user (password: "ubuntu" by default)
ssh -i env/vm.id_rsa ubuntu@10.10.0.2
```

### Delete VM (Graceful Shutdown)

```bash
# Graceful shutdown (if socket mode enabled) then delete
sudo ./delete-vm.sh
```

If `ENABLE_SOCKET=true`, the delete script will:
1. Send `SendCtrlAltDel` action via API (graceful shutdown)
2. Wait up to 5 seconds for shutdown
3. Force kill if graceful shutdown fails
4. Remove tap device
5. Remove VM files
6. Flush iptables rules

If `ENABLE_SOCKET=false`:
1. Send SIGTERM
2. Send SIGKILL if needed
3. Remove tap device
4. Remove VM files
5. Flush iptables rules

### Force Cleanup

```bash
# Force stop and cleanup (if delete-vm.sh fails)
sudo ./cleanup.sh

# To start fresh, run setup.sh again
sudo ./setup.sh
```

## How It Works

### 1. Setup Phase (`setup.sh`)

1. **Check dependencies**: Verifies required tools and KVM
2. **Check assets**: Ensures kernel, rootfs, and SSH keys exist
3. **Copy rootfs**: Copies rootfs to `env/rootfs.ext4`
4. **Copy SSH key**: Copies key to `env/vm.id_rsa`
5. **Generate cloud-init**:
   - Creates metadata with random instance ID
   - Creates network-config with static IP
   - Injects SSH keys into user-data
   - Sets hostname to VM_NAME
6. **Embed cloud-init**: Mounts rootfs and copies files to `/var/lib/cloud/seed/nocloud/`
7. **Generate config**: Creates `env/firecracker.json` with all VM settings

### 2. Start Phase (`start-vm.sh`)

1. **Check running**: Exits if VM already running
2. **Validate files**: Ensures all required files exist
3. **Setup network**: Calls `network.sh` if needed
4. **Start Firecracker**: Launches with `--enable-pci` and `--no-api` or `--api-sock`
5. **Save PID**: Writes PID to `env/firecracker.pid`

### 3. Network Setup (`network.sh`)

1. **Create tap**: `ip tuntap add dev fc-tap0 mode tap`
2. **Configure IP**: Assigns 10.10.0.1/30 to tap device
3. **Enable forwarding**: Sets sysctl for proxy ARP
4. **Setup NAT**: Adds iptables rules for masquerade

### 4. Delete Phase (`delete-vm.sh`)

1. **Read PID**: From `env/firecracker.pid`
2. **Graceful shutdown**: Send SendCtrlAltDel via API (if socket mode)
3. **Force kill**: SIGTERM, then SIGKILL if graceful shutdown fails
4. **Remove tap**: Deletes `fc-tap0` interface
5. **Remove files**: Deletes `env/` directory
6. **Flush iptables**: Removes NAT rules

### 5. Force Cleanup Phase (`cleanup.sh`)

1. **Stop processes**: Force kills all Firecracker processes
2. **Remove files**: Deletes `env/` directory
3. **Remove socket**: Deletes API socket if exists
4. **Remove tap**: Deletes `fc-tap0` interface
5. **Flush iptables**: Removes NAT rules

## Important Notes

1. **Always cleanup**: Run `./delete-vm.sh` when done to remove network rules
2. **Sudo required**: Most scripts need root for networking and mounting
3. **Boot time**: Guest takes 30-60 seconds to fully boot
4. **Socket mode**: Enable in config.env for graceful shutdown support
5. **Image source**: Controlled by `IMAGE_SOURCE` in `../assets/config.env`
   - `ubuntu-cloud`: Full Ubuntu with cloud-init
   - `firecracker-ci`: Minimal Firecracker test images

## Troubleshooting

### VM Won't Start

```bash
# Check KVM
ls -la /dev/kvm

# Check assets exist
ls -la ../assets/kernels/
ls -la ../assets/images/
ls -la ../assets/keys/

# Check Firecracker binary
ls -la ../assets/bin/firecracker
```

### No Network

```bash
# Recreate network
sudo ./network.sh

# Check tap exists
ip link show fc-tap0

# Check iptables rules
sudo iptables -t nat -L -n -v
```

### Can't SSH

```bash
# Wait longer for boot
sleep 60

# Check cloud-init log
ssh -i env/vm.id_rsa root@10.10.0.2 'cat /var/log/cloud-init-output.log'

# Regenerate cloud-init
sudo ./setup.sh
```

### Cleanup Fails

```bash
# Manual cleanup
sudo pkill -f firecracker
sudo rm -rf env/
sudo ip link del fc-tap0 2>/dev/null || true
sudo iptables -t nat -F
sudo iptables -F
```

## Dependencies

Required commands (checked by `setup.sh`):
- `mkisofs` - Create ISO files (genisoimage package)
- `mount/umount` - Filesystem mounting
- `sudo` - Privilege escalation
- `ip` - Network configuration (iproute2)
- `iptables` - Firewall rules
- `curl` - API requests (for graceful shutdown)

Required assets (from `../assets/`):
- `kernels/vmlinux` or `kernels/{version}-vmlinux`
- `images/ubuntu-*.ext4` or `images/{version}.ext4`
- `keys/id_rsa` and `keys/id_rsa.pub`
- `bin/firecracker`

## See Also

- [Parent README](../README.md) - Full project documentation
- [Firecracker Docs](https://github.com/firecracker-microvm/firecracker)
- [Ubuntu Cloud Images](https://cloud-images.ubuntu.com/)
