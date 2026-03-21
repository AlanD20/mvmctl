# Firecracker MicroVM Setup

A lightweight virtualization setup using Firecracker with Ubuntu and other Linux distributions.

See [single-vm/README.md](single-vm/README.md) for single-VM specific configuration and usage.
See [multi-vm/README.md](multi-vm/README.md) for multi-VM specific configuration and usage.

## Prerequisites

### Dependencies (Host)

The following packages are required on the host system:
- `qemu-utils` (for `qemu-img`)
- `cloud-utils` or `genisoimage` (for cloud-init ISO)
- `bridge-utils`
- `iptables`
- `curl`, `bc`, `screen`

### Arch Linux

```bash
sudo pacman -S --needed qemu-desktop libisoburn iptables bridge-utils curl bc wget screen
sudo modprobe kvm
sudo modprobe kvm_intel  # For Intel CPUs
sudo modprobe kvm_amd    # For AMD CPUs
sudo usermod -aG kvm $USER
```

### Ubuntu/Debian

```bash
sudo apt-get update
sudo apt-get install -y qemu-utils genisoimage iptables curl bc bridge-utils screen
sudo usermod -aG kvm $USER
```

## Generic Configuration

### Changing the Ubuntu Version

By default, the setup uses Ubuntu 24.04 LTS (Noble). Edit the appropriate config file based on your setup:

- **Single-VM**: Edit `single-vm/config.env`
- **Multi-VM**: Edit `multi-vm/config.env`

```bash
UBUNTU_VERSION="jammy"  # 22.04 LTS
# or
UBUNTU_VERSION="focal"  # 20.04 LTS
# or
UBUNTU_VERSION="bionic" # 18.04 LTS (EOL)
```

Then regenerate the base image:

**Single-VM:**
```bash
cd single-vm
rm -f ubuntu-*-server-cloudimg-amd64.img rootfs.ext4
sudo ./setup.sh
```

**Multi-VM:**
```bash
cd multi-vm
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

See [custom-images.md](custom-images.md) for detailed instructions on using other distributions:

- AlmaLinux/RHEL
- Debian
- Alpine Linux
- Arch Linux
- Bring Your Own (BYO) images

### Custom Disk Size

Edit the appropriate config file:

- **Single-VM**: Edit `single-vm/config.env`
- **Multi-VM**: Edit `multi-vm/config.env`

```bash
DISK_SIZE="20G"  # Default: 10G
```

Then regenerate as shown above.

### Custom Kernel

Both setups expect a `vmlinux` kernel image. To use a custom kernel:

1. Download or build your own kernel with Firecracker support
2. Ensure it's an uncompressed ELF binary (x86_64) or PE format (aarch64)
3. Place it as `vmlinux` in the respective directory

## Troubleshooting

### KVM Not Available

```bash
lsmod | grep kvm
sudo modprobe kvm
sudo modprobe kvm_intel  # or kvm_amd
sudo usermod -aG kvm $USER
```

### VM Not Starting

```bash
./firecracker --version
ls -la *.ext4
ls -la vmlinux
./firecracker --no-api --config-file config.json
```

### Network Issues

```bash
ip link
ip addr
ip link show br0  # Multi-VM
ip link show type tap
sudo iptables -t nat -L -n -v
```

### Permission Denied

```bash
sudo chown root:kvm /dev/kvm
sudo chmod 660 /dev/kvm
```

### View Logs

```bash
cat firecracker.log              # Single-VM
cat vms/vm1/firecracker.log      # Multi-VM
```

### Stuck VM Process

```bash
ps aux | grep firecracker
sudo pkill -9 firecracker
```

### Disk Full

```bash
df -h
rm -f base-rootfs.ext4
# Edit config.env for smaller DISK_SIZE
```

### Network Connectivity

```bash
cat /proc/sys/net/ipv4/ip_forward
sudo sysctl -w net.ipv4.ip_forward=1
sudo iptables -t nat -L -n -v
ssh ubuntu@<guest-ip>
```

### Serial Console Connection

```bash
sudo screen -ls
sudo screen -r <pid>
# Alternative: sudo microcom -s 115200 /dev/ttyS0
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
