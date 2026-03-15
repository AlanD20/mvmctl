# Firecracker Single-VM Setup

A simple, single virtual machine setup using Firecracker with Ubuntu.

> **Note**: See the parent [README.md](../README.md) for shared prerequisites and troubleshooting.

## Overview

This setup creates a single microVM with:
- **Ubuntu** (default: 24.04 LTS Noble) - configurable via `config.env`
- **2 vCPUs / 2GB RAM / 10GB Disk** - configurable via `config.env`
- NAT networking via tap interface
- Serial console access

## Quick Start

```bash
cd single-vm

# Step 1: Setup (downloads assets, prepares disk/cloud-init)
sudo ./setup.sh

# Step 2: Start the VM in a background screen session
sudo ./start-vm.sh

# Step 3: Connect to serial console
sudo screen -r fc-single

# Step 4: Detach from console (keep VM running)
# Press Ctrl+A, then D

# Step 5: Clean up when done
sudo ./cleanup.sh
```

## Changing the Ubuntu Version

Edit `config.env` to change the Ubuntu version:

```bash
# config.env - Change this value
UBUNTU_VERSION="noble"  # 24.04 LTS

# Available versions:
# - "noble"      = Ubuntu 24.04 LTS
# - "jammy"       = Ubuntu 22.04 LTS
# - "focal"       = Ubuntu 20.04 LTS
# - "bionic"      = Ubuntu 18.04 LTS
```

Then remove the existing image and re-run setup:
```bash
rm -f ubuntu-*-server-cloudimg-amd64.img rootfs.ext4
sudo ./setup.sh
```

## File Description

| File | Description |
|------|-------------|
| `config.env` | Configuration: Ubuntu version, disk size, network settings, VM resources |
| `setup.sh` | Downloads Firecracker, kernel, Ubuntu cloud image; creates rootfs |
| `network.sh` | Creates tap interface and sets up NAT |
| `start-vm.sh` | Starts the Firecracker VM |
| `cleanup.sh` | Removes tap interface, flushes NAT rules, stops VM |
| `firecracker.json` | Firecracker VM configuration |
| `cloud-init/user-data` | Cloud-init configuration for VM provisioning |

## Configuration

### VM Specifications

Edit `firecracker.json` to customize:

```json
{
  "machine-config": {
    "vcpu_count": 2,      // Number of virtual CPUs
    "mem_size_mib": 2048 // Memory in MB (2GB)
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "rootfs.ext4",
      "is_root_device": true,
      "is_read_only": false
    }
  ]
}
```

### Adding Additional Drives

Add more drive entries to the `drives` array:

```json
"drives": [
  {
    "drive_id": "rootfs",
    "path_on_host": "rootfs.ext4",
    "is_root_device": true,
    "is_read_only": false
  },
  {
    "drive_id": "data",
    "path_on_host": "data.ext4",
    "is_root_device": false,
    "is_read_only": false
  }
]
```

### Cloud-Init Configuration

Edit `cloud-init/user-data` to customize the VM:

```yaml
#cloud-config
autoinstall:
  version: 1
  locale: en_US.UTF-8
  identity:
    hostname: my-vm
    password: "$6$rounds=4096$..."  # Use: echo -n password | mkpasswd -m sha-512 -s
    username: ubuntu
  ssh:
    install-server: true
    allow-pw: true
```

Generate password hash:
```bash
echo -n "yourpassword" | mkpasswd -m sha-512 -s
```

## Network Configuration

The default setup uses a tap interface with proxy ARP. Network settings are defined in `config.env`:

```
Host                          Guest
┌─────────────┐             ┌─────────────┐
│  eth0       │──NAT───►   │  eth0       │
│  (internet) │             │  169.254.0.21 │
└─────────────┘             └─────────────┘
       ▲
       │
  ┌────┴────┐
  │ fc-tap0 │
  │ proxy_arp│
  └─────────┘
```

- **Guest IP**: 169.254.0.21 (configurable in `config.env`)
- **Host IP**: 169.254.0.22 (configurable in `config.env`)
- **Network**: 169.254.0.20/30

## Important Notes

1. **Run as root**: Most operations require sudo/root
2. **KVM required**: Firecracker needs KVM virtualization
3. **Serial console**: Default login is via serial console (ttyS0)
4. **Cleanup always**: Always run `./cleanup.sh` when done to remove network resources

## Usage Examples

### Create VM with custom resources

Edit `firecracker.json` before running `./start-vm.sh`:

```json
{
  "machine-config": {
    "vcpu_count": 4,
    "mem_size_mib": 8192
  }
}
```

### Access VM via SSH (after cloud-init config)

The VM will be accessible at 169.254.0.21 after cloud-init completes:

```bash
ssh ubuntu@169.254.0.21
```

## See Also

- [Parent README](../README.md) for shared prerequisites, troubleshooting, and advanced configuration
- [Custom Images](../custom-images.md) for using Arch Linux or bringing your own image
- [Ubuntu Cloud Images](https://cloud-images.ubuntu.com/)
