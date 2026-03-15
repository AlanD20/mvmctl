# Custom Images Guide

This guide explains how to use different Linux distributions with Firecracker, including Arch Linux and custom images.

> **Note**: This is a supplement to the main README. See [README.md](./README.md) for general setup instructions.

## Table of Contents

- [Ubuntu Versions](#ubuntu-versions)
- [Arch Linux](#arch-linux)
- [AlmaLinux/RHEL](#almalinuxrhel)
- [Debian](#debian)
- [Alpine](#alpine)
- [Bring Your Own Image](#bring-your-own-image)

---

## Ubuntu Versions

All setups support multiple Ubuntu versions. Change the version in your config file:

### Single-VM

Edit `single-vm/config.env`:
```bash
UBUNTU_VERSION="jammy"  # 22.04 LTS
```

### Multi-VM

Edit `multi-vm/config.env`:
```bash
UBUNTU_VERSION="jammy"  # 22.04 LTS
```

### Available Versions

| Codename | Version | Status |
|----------|---------|--------|
| `noble` | 24.04 LTS | Current stable |
| `jammy` | 22.04 LTS | LTS |
| `focal` | 20.04 LTS | LTS |
| `bionic` | 18.04 LTS | EOL |

After changing, regenerate the image:
```bash
# Single-vm
rm -f ubuntu-*-server-cloudimg-amd64.img rootfs.ext4
sudo ./setup.sh

# Multi-vm
rm -f ubuntu-*-server-cloudimg-amd64.img base-rootfs.ext4
sudo ./setup-bridge.sh
```

---

## Arch Linux

Arch Linux provides a minimal cloud image that works well with Firecracker.

### Download Arch Linux Cloud Image

```bash
# Download the Arch Linux cloud image
curl -LO https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2

# Convert to raw format
qemu-img convert -f qcow2 -O raw Arch-Linux-x86_64-cloudimg.qcow2 archlinux.ext4

# Resize if needed
truncate -s 10G archlinux.ext4
e2fsck -f archlinux.ext4
resize2fs archlinux.ext4
```

### Network Configuration

Arch Linux uses systemd-networkd. The kernel boot args should include:

```json
{
  "boot_args": "ro console=ttyS0 noapic reboot=k panic=1 pci=off ip=<IP>::<Gateway>:<Netmask>::eth0:off"
}
```

### Cloud-Init for Arch

Arch doesn't use cloud-init by default. You can:

1. **Use NoCloud seed**: Create a NoCloud seed ISO with user data
2. **Manual setup**: Log in and configure manually
3. **Customize image**: Pre-configure the image before use

### SSH Access

By default, Arch uses SSH key authentication. Generate keys:

```bash
# Generate SSH key (on host)
ssh-keygen -t ed25519 -f archVM_key

# Add to VM (via console)
# In VM: mkdir -p ~/.ssh && chmod 700 ~/.ssh
# In VM: echo "ssh-ed25519 AAAA..." >> ~/.ssh/authorized_keys
```

---

## AlmaLinux/RHEL

### Download AlmaLinux Cloud Image

```bash
# AlmaLinux 9
curl -LO https://repo.almalinux.org/almalinux/9/BaseOS/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2

# Convert to raw
qemu-img convert -f qcow2 -O raw AlmaLinux-9-GenericCloud-latest.x86_64.qcow2 almalinux.ext4

# Resize
truncate -s 10G almalinux.ext4
e2fsck -f almalinux.ext4
resize2fs almalinux.ext4
```

### CentOS/RHEL Alternative

```bash
# CentOS Stream 9
curl -LO https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2
```

### Cloud-Init Configuration

AlmaLinux uses cloud-init similar to Ubuntu:

```yaml
#cloud-config
chpasswd:
  list: |
    clouduser:password123
  expire: false
ssh_pwauth: true
users:
  - name: clouduser
    primary_group: clouduser
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-ed25519 AAAA...
```

---

## Debian

### Download Debian Cloud Image

```bash
# Debian 12 (Bookworm)
curl -LO https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2

# Convert to raw
qemu-img convert -f qcow2 -O raw debian-12-generic-amd64.qcow2 debian.ext4

# Resize
truncate -s 10G debian.ext4
e2fsck -f debian.ext4
resize2fs debian.ext4
```

### Cloud-Init for Debian

```yaml
#cloud-config
autoinstall:
  version: 1
  locale: en_US.UTF-8
  identity:
    hostname: debian-vm
    password: "$6$rounds=4096$..."
    username: debian
  ssh:
    install-server: true
    allow-pw: true
```

---

## Alpine

### Download Alpine Virtual Image

```bash
# Alpine virtual image
curl -LO https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-virt-3.19.1-x86_64.iso

# Or use the raw disk image
curl -LO https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-virt-3.19.1-x86_64.tar.gz
```

### Convert to Raw Disk

```bash
# Extract and convert
tar -xzf alpine-virt-3.19.1-x86_64.tar.gz
mv alpine-virt-3.19.1-x86_64 alpine.ext4
```

### Network Configuration

Alpine uses `/etc/network/interfaces` or OpenRC:

```bash
# In VM
echo "auto eth0" >> /etc/network/interfaces
echo "iface eth0 inet static" >> /etc/network/interfaces
echo "  address 10.10.0.x" >> /etc/network/interfaces
echo "  netmask 255.255.255.0" >> /etc/network/interfaces
echo "  gateway 10.10.0.1" >> /etc/network/interfaces
```

---

## Bring Your Own Image

This section explains how to use any Linux distribution with Firecracker.

### General Requirements

Your image must:

1. **Be a disk image** - Raw (.img/.ext4), QCOW2, VMDK, etc.
2. **Have virtio drivers** - For network and disk
3. **Be configured for serial console** - ttyS0 for Firecracker
4. **Have a compatible init system** - systemd, OpenRC, etc.

### Converting Your Image

```bash
# From QCOW2
qemu-img convert -f qcow2 -O raw source.qcow2 destination.ext4

# From VMDK
qemu-img convert -f vmdk -O raw source.vmdk destination.ext4

# From VDI (VirtualBox)
qemu-img convert -f vdi -O raw source.vdi destination.ext4

# From ISO (less common)
# Mount ISO and copy files to new raw disk
```

### Resizing the Disk

```bash
# Increase size
truncate -s 20G disk.ext4
e2fsck -f disk.ext4
resize2fs disk.ext4

# Decrease size (more complex)
resize2fs -M disk.ext4
truncate -s $(stat -c%s disk.ext4) disk.ext4
```

### Preparing the Image

#### 1. Add Serial Console

Edit `/etc/default/grub` in the VM:

```bash
GRUB_CMDLINE_LINUX_DEFAULT="console=tty1 console=ttyS0,115200"
GRUB_TERMINAL="serial"
```

Then run `grub-mkconfig -o /boot/grub/grub.cfg`

#### 2. Enable Virtio

Ensure these modules are loaded:
- `virtio_blk` - Block device
- `virtio_net` - Network
- `virtio_scsi` - SCSI
- `virtio_balloon` - Memory ballooning

#### 3. Install Cloud-Init (Optional)

```bash
# Ubuntu/Debian
apt-get install cloud-init

# RHEL/AlmaLinux
dnf install cloud-init

# Alpine
apk add cloud-init
```

### Creating a Custom cloud-init ISO

If your image supports NoCloud datasource:

```bash
# Create user-data
cat > user-data <<EOF
#cloud-config
users:
  - name: ubuntu
    ssh_authorized_keys:
      - ssh-ed25519 AAAA...
    sudo: ALL=(ALL) NOPASSWD:ALL
EOF

# Create meta-data
cat > meta-data <<EOF
instance-id: $(uuidgen)
local-hostname: myvm
EOF

# Create ISO
genisoimage -output cloud-init.iso -volid cidata -joliet -rock user-data meta-data
```

### Using Custom Image with Scripts

#### Single-VM

1. Place your image as `rootfs.ext4` in `single-vm/`
2. Run `./start-vm.sh`

```bash
cp my-custom-image.ext4 single-vm/rootfs.ext4
cd single-vm
sudo ./start-vm.sh
```

#### Multi-VM

1. Place your image as `base-rootfs.ext4` in `multi-vm/`
2. Each VM will copy from this base

```bash
cp my-custom-image.ext4 multi-vm/base-rootfs.ext4
cd multi-vm
sudo ./create-vm.sh vm1
```

### Troubleshooting Custom Images

#### VM Won't Boot

- Check serial console output
- Verify kernel supports Firecracker (no special drivers needed)
- Ensure disk is not too large for available memory

#### No Network

- Verify virtio-net driver is loaded
- Check IP configuration in boot args
- Ensure MAC address matches config

#### Cannot Connect

- Check SSH service is enabled
- Verify firewall allows SSH
- Try password authentication temporarily

---

## Quick Reference: Image Download URLs

| Distribution | URL |
|--------------|-----|
| Ubuntu | https://cloud-images.ubuntu.com/ |
| AlmaLinux | https://repo.almalinux.org/almalinux/ |
| CentOS | https://cloud.centos.org/centos/ |
| Debian | https://cloud.debian.org/images/cloud/ |
| Alpine | https://dl-cdn.alpinelinux.org/alpine/ |
| Arch Linux | https://geo.mirror.pkgbuild.com/images/ |
| Fedora | https://download.fedoraproject.org/pub/fedora/linux/releases/ |

---

## Notes

- Firecracker works best with cloud-optimized images
- Always convert to raw format for best performance
- Test images in single-vm mode first before scaling
- Keep backups of base images before modifications
