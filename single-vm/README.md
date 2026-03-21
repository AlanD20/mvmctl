# Firecracker Single-VM Setup

A simple, single virtual machine setup using Firecracker with Ubuntu and NAT networking.

**See the parent [README.md](../README.md) for**: prerequisites, troubleshooting, Ubuntu version options, disk sizing, custom kernels, and security best practices.

## Overview

This setup creates a single microVM with:
- NAT networking via tap interface
- Serial console access
- Cloud-init provisioning

**Default resources**: 2 vCPUs / 2GB RAM / 10GB Disk (configurable in `config.env` and `firecracker.json`)

## Quick Start

```bash
cd single-vm

# Download and prepare VM assets
sudo ./setup.sh

# Start the VM
sudo ./start-vm.sh

# Connect to serial console
sudo screen -r fc-single

# When done
sudo ./cleanup.sh
```

## File Structure

| File | Description |
|------|-------------|
| `config.env` | VM and network configuration |
| `setup.sh` | Download assets and prepare disk |
| `start-vm.sh` | Start the Firecracker VM |
| `network.sh` | Configure tap interface and NAT |
| `cleanup.sh` | Stop VM and cleanup resources |
| `firecracker.json` | Firecracker VM configuration |
| `cloud-init/user-data` | Cloud-init provisioning config |

## Configuration

### VM Resources

Edit `config.env`:
```bash
VM_VCPU=2          # Virtual CPUs
VM_MEM_MIB=2048    # Memory in MB
DISK_SIZE="10G"    # Disk size
```

Edit `firecracker.json` for advanced Firecracker configuration (see root README).

### Network

Defined in `config.env`:
- **Guest IP**: 169.254.0.21
- **Host IP**: 169.254.0.22  
- **Network**: 169.254.0.20/30

```
Host               Guest
┌─────────────┐ ┌─────────────┐
│ eth0        │──NAT───►│ eth0        │
│ (internet)  │         │ 169.254.0.21│
└─────────────┘         └─────────────┘
                ▲
                │
          ┌────┴────┐
          │ fc-tap0 │
          └─────────┘
```

The guest can access the internet via NAT on the host.

### Cloud-Init

Edit `cloud-init/user-data` to customize:
- Hostname
- User password (generate hash with: `echo -n "password" | mkpasswd -m sha-512 -s`)
- SSH settings
- Packages to install

See Ubuntu's [autoinstall reference](https://ubuntu.com/server/docs/install/autoinstall) for full syntax.

## Usage Examples

### Create VM with custom resources

```bash
# Edit config.env or firecracker.json
sudo ./setup.sh
sudo ./start-vm.sh
```

### Connect to serial console

```bash
sudo screen -r fc-single
# Detach with: Ctrl+A, then D
```

### SSH access

After cloud-init completes:
```bash
ssh ubuntu@169.254.0.21
```

### Adding data drives

Create an image:
```bash
qemu-img create -f raw data.ext4 5G
```

Then edit `firecracker.json` and add to the `drives` array:
```json
{
  "drive_id": "data",
  "path_on_host": "data.ext4",
  "is_root_device": false
}
```

## Important Notes

1. **Run with sudo**: Most operations require root
2. **Cleanup required**: Always run `./cleanup.sh` when done
3. **KVM required**: Check with `ls -la /dev/kvm`
4. **Guest may take 30-60 seconds to fully boot**

## Ubuntu Version

The default is Ubuntu 24.04 LTS (Noble). See [parent README](../README.md) to change Ubuntu versions.

## Troubleshooting

Common issues:
- **VM won't start**: Check KVM (`ls -la /dev/kvm`), verify vmlinux and rootfs.ext4 exist
- **No network**: Run `./network.sh` to reconfigure tap device
- **Console blank**: Wait 30-60 seconds for boot, or regenerate cloud-init

See [parent README](../README.md#troubleshooting) for comprehensive troubleshooting.

## See Also

- [Ubuntu Cloud Images](https://cloud-images.ubuntu.com/)
