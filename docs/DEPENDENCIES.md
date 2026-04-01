# mvmctl Dependencies

This document lists all external binary and system-level dependencies required for `mvmctl` to function correctly.

## 1. Core Runtime Dependencies

These binaries are required for basic operations like managing VMs, networking, and host initialization.

| Binary | Category | Purpose | Package (Debian/Ubuntu) | Package (Arch) |
| :--- | :--- | :--- | :--- | :--- |
| `firecracker` | Core | The MicroVM VMM | Managed via `mvm bin fetch` | Managed via `mvm bin fetch` |
| `jailer` | Core | Security isolation for Firecracker | Managed via `mvm bin fetch` | Managed via `mvm bin fetch` |
| `ip` | Network | Bridge and TAP interface management | `iproute2` | `iproute2` |
| `iptables` | Network | NAT and firewall rule management | `iptables` | `iptables` |
| `sysctl` | System | Enabling IP forwarding on the host | `procps` | `procps-ng` |
| `sudo` | Privilege | Running `mvm host init` and privileged commands | `sudo` | `sudo` |
| `groupadd` | Privilege | Creating the `mvm` system group | `passwd` | `shadow` |
| `usermod` | Privilege | Adding users to the `mvm` group | `passwd` | `shadow` |
| `visudo` | Privilege | Validating sudoers drop-in files | `sudo` | `sudo` |
| `lsmod` | Kernel | Checking for KVM module status | `kmod` | `kmod` |
| `modprobe` | Kernel | Loading required KVM modules | `kmod` | `kmod` |

## 2. Image & Cloud-Init Dependencies

These binaries are required for importing images, converting formats, and generating Cloud-Init seeds.

| Binary | Category | Purpose | Package (Debian/Ubuntu) | Package (Arch) |
| :--- | :--- | :--- | :--- | :--- |
| `qemu-img` | Image | Converting and resizing disk images | `qemu-utils` | `qemu-img` |
| `sfdisk` | Image | Partition table manipulation | `util-linux` | `util-linux` |
| `blkid` | Image | Detecting root partitions and UUIDs | `util-linux` | `util-linux` |
| `mount` | Image | Mounting images for rootfs extraction | `util-linux` | `util-linux` |
| `umount` | Image | Unmounting images | `util-linux` | `util-linux` |
| `truncate` | Image | Creating sparse files for new images | `coreutils` | `coreutils` |
| `mkfs.ext4` | Image | Formatting extracted rootfs images | `e2fsprogs` | `e2fsprogs` |
| `unsquashfs` | Image | Extracting rootfs from SquashFS images | `squashfs-tools` | `squashfs-tools` |
| `tar` | Archive | Extracting rootfs from tarballs | `tar` | `tar` |
| `mkisofs` | Cloud-Init | Creating `nocloud` seed ISOs | `genisoimage` | `cdrtools` |
| `cloud-localds` | Cloud-Init | Helper for Cloud-Init seed generation | `cloud-image-utils` | `cloud-utils` |
| `ssh` | Remote | Connecting to microVMs via SSH | `openssh-client` | `openssh` |
| `ssh-keygen` | Remote | Generating SSH keypairs for microVMs | `openssh-client` | `openssh` |

*Note: `mkisofs` and `genisoimage` are interchangeable; `mvmctl` will use whichever is available.*

## 3. libguestfs Dependencies (Optional)

For cloud-init injection into disk images via direct injection mode (`--cloud-init-mode direct`), mvmctl uses libguestfs. This requires both system libraries and Python bindings.

### System Packages (Required for Runtime)

These provide the libguestfs C library, appliance tools, and supermin:

**Debian/Ubuntu:**
```bash
sudo apt-get install libguestfs0 libguestfs-tools supermin
```

**RHEL/CentOS/Fedora:**
```bash
sudo dnf install libguestfs libguestfs-tools supermin
```

**Arch Linux:**
```bash
sudo pacman -S libguestfs supermin
```

### Python Bindings (Required for Development/Builds)

The Python `guestfs` module is needed when:
- Running mvmctl from source with direct injection mode
- Building standalone binaries with guestfs support

**Install via system package manager (required — `guestfs` is not on PyPI):**

**Debian/Ubuntu:**
```bash
sudo apt-get install python3-libguestfs
```

**RHEL/CentOS/Fedora:**
```bash
sudo dnf install python3-libguestfs
```

**Arch Linux:**
```bash
sudo pacman -S libguestfs supermin  # Python bindings included in libguestfs package
```

> **Note:** The `guestfs` Python package is **not available on PyPI** and cannot be installed via
> `uv` or `pip`. There is no `--group guestfs` dependency group in this repository.
> You must install the Python bindings through your distribution's package manager before
> building or running mvmctl from source with direct injection mode.

### Sudoers Configuration

libguestfs uses `supermin` to build the appliance. Add to `/etc/sudoers.d/mvm`:

```
%mvm ALL=(ALL) NOPASSWD: /usr/bin/supermin
```

Or if supermin is in a different location:

```
%mvm ALL=(ALL) NOPASSWD: /usr/libexec/supermin/*
```

### Verification

Check libguestfs is working:

```bash
python3 -c "import guestfs; print('libguestfs available')"
```

Check supermin sudoers entry:

```bash
sg mvm -c 'sudo -n /usr/bin/supermin --version'
```

## 4. Kernel Build Dependencies (Optional)

These are only required if you intend to build custom kernels from source using `mvm kernel build`.

| Binary | Category | Package (Debian/Ubuntu) | Package (Arch) |
| :--- | :--- | :--- | :--- |
| `make` | Build | `build-essential` | `base-devel` |
| `gcc` | Build | `build-essential` | `base-devel` |
| `ld` | Build | `binutils` | `binutils` |
| `flex` | Build | `flex` | `flex` |
| `bison` | Build | `bison` | `bison` |
| `bc` | Build | `bc` | `bc` |
| `pahole` | Build | `dwarves` | `pahole` |
| `git` | Build | `git` | `git` |
| `curl` | Build | `curl` | `curl` |
| `pkg-config` | Build | `pkg-config` | `pkgconf` |

### Required Development Libraries (Kernel Build)
- **libelf**: `libelf-dev` (Debian/Ubuntu), `libelf` (Arch)
- **openssl**: `libssl-dev` (Debian/Ubuntu), `openssl` (Arch)
- **ncurses**: `libncurses-dev` (Debian/Ubuntu), `ncurses` (Arch)

## 5. Command Dependency Mapping

This section maps specific `mvm` commands to the external binaries they invoke.

| Command Group | Command(s) | Required Binaries |
| :--- | :--- | :--- |
| **`mvm host`** | `init` | `sudo`, `groupadd`, `usermod`, `visudo`, `sysctl`, `ip`, `iptables`, `iptables-save`, `lsmod`, `modprobe` |
| | `reset` | `sudo`, `groupdel`, `sysctl`, `iptables-restore` |
| **`mvm network`** | `init`, `create` | `ip`, `iptables`, `iptables-restore`, `sysctl` |
| | `ls`, `show`, `rm` | `ip`, `iptables` |
| **`mvm bin`** | `fetch`, `ls`, `rm`, `use` | (Internal Python logic) |
| **`mvm image`** | `import` | `qemu-img`, `sfdisk`, `blkid`, `mount`, `umount`, `tar`, `truncate`, `mkfs.ext4`, `unsquashfs` |
| | `ls`, `rm` | (Internal Python logic) |
| **`mvm kernel`** | `download` | (Internal Python logic) |
| | `build` | `make`, `gcc`, `ld`, `flex`, `bison`, `bc`, `pahole`, `git`, `curl`, `pkg-config` |
| | `ls`, `rm`, `use` | (Internal Python logic) |
| **`mvm key`** | `create` | `ssh-keygen` |
| | `add`, `ls`, `rm` | (Internal Python logic) |
| **`mvm vm`** | `create` | `firecracker`, `jailer`, `ip`, `iptables`, `mkisofs` (if cloud-init) |
| | `ls`, `show` | (Internal Python logic) |
| | `stop`, `rm` | `firecracker`, `ip`, `iptables`, `ssh-keygen` (cleanup) |
| | `ssh` | `ssh` |
| | `logs` | (Internal Python logic) |

## 6. Host System Requirements

- **Kernel Modules**:
  - `kvm`: Required for hardware-accelerated virtualization.
  - `kvm_intel` or `kvm_amd`: Vendor-specific KVM extensions.
  - `tun`: Required for TAP networking.
  - `bridge`: Required for bridge networking.
- **Hardware Virtualization**: VT-x (Intel) or AMD-V must be enabled in the BIOS/UEFI.
- **Permissions**: The user running `mvmctl` must be in the `mvm` group (created by `mvm host init`).
