# Custom Images Guide

This guide explains how to use different Linux distributions with Firecracker, including Arch Linux, Debian, Alpine, and custom images.

> **Note**: This is a supplement to the main README. See [README.md](./README.md) for general setup instructions.

## Table of Contents

- [How Images Work](#how-images-work)
- [Ubuntu Versions](#ubuntu-versions)
- [Arch Linux](#arch-linux)
- [Debian](#debian)
- [Alpine Linux](#alpine-linux)
- [AlmaLinux/RHEL](#almalinuxrhel)
- [Fedora](#fedora)
- [Bring Your Own Image](#bring-your-own-image)

---

## How Images Work

The Firecracker setup uses a two-stage image system:

### Stage 1: Asset Download (`assets/download-assets.sh`)

1. Downloads kernel and rootfs images to `assets/` directory
2. Supports two modes:
   - **firecracker-ci**: Minimal Ubuntu images from Firecracker CI
   - **ubuntu-cloud**: Full Ubuntu cloud images with cloud-init

### Stage 2: VM Preparation

**Single-VM:** (`single-vm/setup.sh`)
- Copies `assets/images/<version>.ext4` → `env/rootfs.ext4`
- Embeds cloud-init configuration into the rootfs

**Multi-VM:** (`multi-vm/setup.sh`)
- Copies `assets/images/<version>.ext4` → `env/base-rootfs.ext4`
- Each VM clones from this base image

### Image Requirements

For Firecracker compatibility, images must:
1. **Be in raw format** (`.ext4` or `.img`)
2. **Have virtio drivers** (`virtio_blk`, `virtio_net`)
3. **Support serial console** (`ttyS0`)
4. **Use a compatible init system** (systemd, OpenRC, or runit)

---

## Ubuntu Versions

All setups support multiple Ubuntu versions. Change the version in your config file:

### Configuration

Edit `assets/config.env`:

```bash
# Choose image source
IMAGE_SOURCE="ubuntu-cloud"  # or "firecracker-ci"

# Ubuntu version (for ubuntu-cloud)
UBUNTU_VERSION="noble"  # 24.04 LTS
# or
UBUNTU_VERSION="jammy"  # 22.04 LTS
# or
UBUNTU_VERSION="focal"  # 20.04 LTS
```

### Available Versions

| Codename | Version | Status |
|----------|---------|--------|
| `noble` | 24.04 LTS | Current stable |
| `jammy` | 22.04 LTS | LTS |
| `focal` | 20.04 LTS | LTS |
| `bionic` | 18.04 LTS | EOL |

### Regenerate Images

```bash
# Download new images
cd assets
rm -rf images/ kernels/
./download-assets.sh

# Single-VM: Re-setup
cd ../single-vm
rm -rf env/
sudo ./setup.sh

# Multi-VM: Re-setup
cd ../multi-vm
rm -rf env/
sudo ./setup.sh
```

---

## Arch Linux

Arch Linux provides cloud images that work well with Firecracker.

### Download and Prepare

```bash
cd assets/images

# Download the Arch Linux cloud image
curl -LO https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2

# Convert to raw format
qemu-img convert -f qcow2 -O raw Arch-Linux-x86_64-cloudimg.qcow2 arch.ext4

# Resize if needed (optional)
truncate -s 10G arch.ext4
e2fsck -f arch.ext4
resize2fs arch.ext4

cd ../..
```

### Update Configuration

Edit `assets/config.env`:

```bash
# Use custom image path
IMAGE_SOURCE="custom"
CUSTOM_ROOTFS="images/arch.ext4"
CUSTOM_KERNEL="kernels/vmlinux"  # Arch uses generic kernel
```

### Cloud-Init for Arch

Create `single-vm/cloud-init/user-data-arch`:

```yaml
#cloud-config
users:
  - name: arch
    groups: wheel
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... your-key

packages:
  - openssh
  - sudo
  - cloud-init

runcmd:
  - systemctl enable sshd
  - systemctl start sshd
```

### Network Configuration

Arch uses systemd-networkd. Boot args example:

```json
{
  "boot_args": "console=ttyS0 reboot=k panic=1 pci=off ip=10.10.0.2::10.10.0.1:255.255.255.252::eth0:off rw"
}
```

### Single-VM Setup

```bash
cd single-vm

# Update config.env to point to Arch image
sed -i 's|ROOTFS_PATH=.*|ROOTFS_PATH="../assets/images/arch.ext4"|' config.env

sudo ./setup.sh
sudo ./create-vm.sh
```

### Multi-VM Setup

```bash
cd multi-vm

# Update config.env to point to Arch image
sed -i 's|ROOTFS_PATH=.*|ROOTFS_PATH="../assets/images/arch.ext4"|' config.env

sudo ./setup.sh
sudo ./create-vm.sh arch-vm1
```

---

## Debian

Debian provides official cloud images with cloud-init support.

### Download and Prepare

```bash
cd assets/images

# Download Debian 12 (Bookworm)
curl -LO https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2

# Convert to raw
qemu-img convert -f qcow2 -O raw debian-12-generic-amd64.qcow2 debian.ext4

# Resize (optional)
truncate -s 10G debian.ext4
e2fsck -f debian.ext4
resize2fs debian.ext4

cd ../..
```

### Cloud-Init Configuration

Debian uses standard cloud-init. Example `cloud-init/user-data`:

```yaml
#cloud-config
hostname: debian-vm
fqdn: debian-vm.local

users:
  - name: debian
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: sudo
    shell: /bin/bash
    passwd: "$6$rounds=4096$saltsalt$hashedpassword"
    lock_passwd: false
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... your-key

packages:
  - openssh-server
  - curl
  - vim
  - htop

runcmd:
  - systemctl enable ssh
  - systemctl start ssh
```

### Configuration

Update `single-vm/config.env` or `multi-vm/config.env`:

```bash
ROOTFS_PATH="../assets/images/debian.ext4"
KERNEL_PATH="../assets/kernels/vmlinux"  # Use generic kernel
```

---

## Alpine Linux

Alpine Linux is lightweight and works well in Firecracker VMs.

### Download and Prepare

```bash
cd assets/images

# Download Alpine virtual image (raw disk format)
curl -LO https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-virt-3.19.1-x86_64.raw

# Rename to .ext4
mv alpine-virt-3.19.1-x86_64.raw alpine.ext4

# Resize if needed
truncate -s 2G alpine.ext4

cd ../..
```

### Important: Enable Serial Console

Alpine needs serial console enabled. Mount the image and edit:

```bash
mkdir -p /tmp/alpine-mnt
sudo mount alpine.ext4 /tmp/alpine-mnt

# Enable serial console in inittab
sudo sed -i 's/^#ttyS0/ttyS0/' /tmp/alpine-mnt/etc/inittab

# Or for newer Alpine with systemd/openrc:
sudo mkdir -p /tmp/alpine-mnt/etc/default
sudo sh -c 'echo "console=ttyS0,115200" >> /tmp/alpine-mnt/etc/default/grub'

sudo umount /tmp/alpine-mnt
rmdir /tmp/alpine-mnt
```

### Cloud-Init

Alpine requires cloud-init package:

```bash
# Install cloud-init inside the VM after first boot
apk add cloud-init
```

Or pre-install by mounting the image:

```bash
# Mount and enter chroot (advanced)
sudo mount alpine.ext4 /mnt
sudo chroot /mnt /bin/sh

# Install packages
apk add cloud-init openssh

# Configure
exit
sudo umount /mnt
```

### Boot Arguments

Alpine uses different boot args:

```json
{
  "boot_args": "console=ttyS0,115200n8 reboot=k panic=1 pci=off ip=10.10.0.2::10.10.0.1:255.255.255.252::eth0:off"
}
```

### Network Configuration

Alpine uses `/etc/network/interfaces`:

```bash
# In VM
cat > /etc/network/interfaces <<EOF
auto eth0
iface eth0 inet static
    address 10.10.0.2
    netmask 255.255.255.252
    gateway 10.10.0.1
EOF

rc-service networking restart
```

---

## AlmaLinux/RHEL

AlmaLinux and CentOS Stream provide cloud images compatible with Firecracker.

### Download AlmaLinux

```bash
cd assets/images

# AlmaLinux 9
curl -LO https://repo.almalinux.org/almalinux/9/BaseOS/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2

# Convert to raw
qemu-img convert -f qcow2 -O raw AlmaLinux-9-GenericCloud-latest.x86_64.qcow2 almalinux.ext4

# Resize
truncate -s 10G almalinux.ext4
e2fsck -f almalinux.ext4
resize2fs almalinux.ext4

cd ../..
```

### Download CentOS Stream

```bash
cd assets/images

# CentOS Stream 9
curl -LO https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2

# Convert
qemu-img convert -f qcow2 -O raw CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2 centos.ext4

cd ../..
```

### Cloud-Init Configuration

AlmaLinux uses standard cloud-init:

```yaml
#cloud-config
chpasswd:
  list: |
    root:changeme
    almauser:changeme
  expire: false

ssh_pwauth: true

users:
  - name: almauser
    groups: wheel
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... your-key

packages:
  - vim
  - htop
  - curl

runcmd:
  - systemctl enable sshd
  - systemctl start sshd
```

---

## Fedora

Fedora provides cloud images optimized for cloud environments.

### Download

```bash
cd assets/images

# Fedora Cloud 39 (or latest)
curl -LO https://download.fedoraproject.org/pub/fedora/linux/releases/39/Cloud/x86_64/images/Fedora-Cloud-Base-39-latest.x86_64.qcow2

# Convert
qemu-img convert -f qcow2 -O raw Fedora-Cloud-Base-39-latest.x86_64.qcow2 fedora.ext4

# Resize
truncate -s 10G fedora.ext4
e2fsck -f fedora.ext4
resize2fs fedora.ext4

cd ../..
```

### Cloud-Init

Standard cloud-init works with Fedora:

```yaml
#cloud-config
hostname: fedora-vm
fqdn: fedora-vm.local

users:
  - name: fedora
    groups: wheel
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... your-key

packages:
  - vim
  - htop
  - curl
  - wget

runcmd:
  - systemctl enable sshd
  - systemctl start sshd
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

# From ISO (create new disk)
# Mount ISO and copy files to new raw disk
dd if=/dev/zero of=destination.ext4 bs=1M count=2048
mkfs.ext4 destination.ext4
# Mount and copy files...
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

For GRUB-based systems, edit `/etc/default/grub`:

```bash
GRUB_CMDLINE_LINUX_DEFAULT="console=tty1 console=ttyS0,115200"
GRUB_TERMINAL="serial"
```

Then regenerate GRUB config:

```bash
grub-mkconfig -o /boot/grub/grub.cfg
# or
grub2-mkconfig -o /boot/grub2/grub.cfg
```

#### 2. Enable Virtio

Ensure these modules are loaded:

```bash
# Add to /etc/modules or /etc/modules-load.d/*.conf
virtio_blk
virtio_net
virtio_scsi
virtio_balloon
```

#### 3. Install Cloud-Init (Optional)

```bash
# Ubuntu/Debian
apt-get install cloud-init

# RHEL/AlmaLinux/Fedora
dnf install cloud-init

# Alpine
apk add cloud-init

# Arch Linux
pacman -S cloud-init
```

### Using Custom Image with Scripts

#### Single-VM

```bash
# Place your custom image
cp my-custom-image.ext4 single-vm/env/rootfs.ext4

# Or update config.env to point to it
sed -i 's|ROOTFS_PATH=.*|ROOTFS_PATH="../assets/images/custom.ext4"|' single-vm/config.env

# Setup and run
cd single-vm
sudo ./setup.sh
sudo ./create-vm.sh
```

#### Multi-VM

```bash
# Place as base image
cp my-custom-image.ext4 multi-vm/env/base-rootfs.ext4

# Or update config.env
sed -i 's|ROOTFS_PATH=.*|ROOTFS_PATH="../assets/images/custom.ext4"|' multi-vm/config.env

# Setup and create VMs
cd multi-vm
sudo ./setup.sh
sudo ./create-vm.sh vm1
```

### Creating a Custom cloud-init ISO

If your image supports NoCloud datasource but you can't embed cloud-init:

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

# Add to Firecracker config as a second drive (not yet supported)
# Or mount inside the VM manually
```

**Note**: The current setup embeds cloud-init directly into the rootfs instead of using ISOs.

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

## Troubleshooting Custom Images

### VM Won't Boot

- Check serial console output: `tail -f single-vm/env/firecracker.console.log`
- Verify kernel supports Firecracker (generic vmlinux works for most)
- Ensure disk is not too large for available memory
- Check for missing virtio drivers

### No Network

- Verify virtio-net driver is loaded: `lsmod | grep virtio`
- Check IP configuration in boot args
- Ensure MAC address matches config
- For Alpine: verify `/etc/network/interfaces` is configured

### Cannot Connect via SSH

- Check SSH service is enabled and running
- Verify firewall allows SSH (port 22)
- Try password authentication temporarily
- Check cloud-init logs: `cat /var/log/cloud-init-output.log`

### Cloud-Init Not Working

- Verify cloud-init is installed: `which cloud-init`
- Check datasource: cloud-init requires a datasource (NoCloud, EC2, etc.)
- The setup embeds cloud-init in `/var/lib/cloud/seed/nocloud/`
- Check cloud-init logs: `cat /var/log/cloud-init.log`

### Kernel Issues

```bash
# Test with generic Firecracker kernel
cp assets/kernels/vmlinux assets/kernels/custom-vmlinux

# Update config to use it
sed -i 's|KERNEL_PATH=.*|KERNEL_PATH="../assets/kernels/custom-vmlinux"|' config.env
```

---

## Notes

- Firecracker works best with cloud-optimized images
- Always convert to raw format for best performance
- Test images in single-vm mode first before scaling
- Keep backups of base images before modifications
- The same kernel (vmlinux) can often be used across different distributions
- Cloud-init is optional but recommended for automatic configuration
