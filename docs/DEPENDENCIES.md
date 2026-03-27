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

## 3. Kernel Build Dependencies (Optional)

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

## 4. Command Dependency Mapping

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

## 5. Host System Requirements

- **Kernel Modules**:
  - `kvm`: Required for hardware-accelerated virtualization.
  - `kvm_intel` or `kvm_amd`: Vendor-specific KVM extensions.
  - `tun`: Required for TAP networking.
  - `bridge`: Required for bridge networking.
- **Hardware Virtualization**: VT-x (Intel) or AMD-V must be enabled in the BIOS/UEFI.
- **Permissions**: The user running `mvmctl` must be in the `mvm` group (created by `mvm host init`).
