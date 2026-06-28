# mvmctl Dependencies

All external binary and system-level dependencies required for mvmctl to function correctly, organized by necessity.

## Table of Contents

- [A. Required (Every Install)](#a-required-every-install)
- [B. Firewall Backend (Choose One)](#b-firewall-backend-choose-one)
- [C. Image & Storage](#c-image-storage)
- [D. Cloud-Init](#d-cloud-init)
- [E. Image Provisioning Backend (Choose One)](#e-image-provisioning-backend-choose-one)
  - [E1. Loop-Mount (Default)](#e1-loop-mount-default)
  - [E2. libguestfs (Alternative)](#e2-libguestfs-alternative)
- [F. Kernel Build (Optional)](#f-kernel-build-optional)
- [G. Host System Requirements](#g-host-system-requirements)
  - [Kernel Modules](#kernel-modules)
  - [Hardware](#hardware)
- [H. Command Dependency Map](#h-command-dependency-map)

---

## A. Required (Every Install)

These binaries must be present on the host for basic mvmctl operation. The `mvm init` wizard checks for them automatically.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `sudo` | Privilege escalation for `mvm host init` and privileged ops | `sudo` | `sudo` |
| `groupadd` | Creating the `mvm` system group | `passwd` | `shadow` |
| `usermod` | Adding users to the `mvm` group | `passwd` | `shadow` |
| `groupdel` | Removing the `mvm` group on `mvm host reset` | `passwd` | `shadow` |
| `visudo` | Validating sudoers drop-in files | `sudo` | `sudo` |
| `ip` | Bridge and TAP interface management | `iproute2` | `iproute2` |
| `modprobe` | Loading KVM and networking kernel modules | `kmod` | `kmod` |
| `lsmod` | Checking KVM module status | `kmod` | `kmod` |
| `sysctl` | Enabling IP forwarding | `procps` | `procps-ng` |
| `ssh-keygen` | Generating SSH keypairs for microVMs | `openssh-client` | `openssh` |
| `tar` | Extracting rootfs from tarballs | `tar` | `tar` |
| `iptables` or `nft` | Firewall rule management | `iptables` or `nftables` | `iptables` or `nftables` |

**Go toolchain:** The `mvm` binary is a single compiled Go binary. No runtime dependencies beyond the system packages listed.

---

## B. Firewall Backend (Choose One)

mvmctl supports two firewall backends for NAT and forwarding rules. **nftables is the default** (`settings.firewall_backend: "nftables"`). Only one backend needs to be installed.

| Backend | Binary | Debian/Ubuntu | Arch |
|---------|--------|---------------|------|
| **nftables** (default) | `nft` | `nftables` | `nftables` |
| **iptables** | `iptables` | `iptables` | `iptables` |

**Switch backends:**
```bash
mvm config set settings firewall_backend nftables   # default
mvm config set settings firewall_backend iptables
mvm network sync                                    # reload rules
```

---

## C. Image & Storage

Required for pulling, importing, and converting VM images.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `qemu-img` | Image conversion and resize | `qemu-utils` | `qemu-img` |
| `mkfs.ext4` | Formatting extracted rootfs images | `e2fsprogs` | `e2fsprogs` |
| `fakeroot` | Preserving tarball ownership during tar-rootfs extraction | `fakeroot` | `fakeroot` |
| `blkid` | Detecting root partitions and UUIDs | `util-linux` | `util-linux` |
| `sfdisk` | Partition table manipulation | `util-linux` | `util-linux` |
| `truncate` | Creating sparse files for new images | `coreutils` | `coreutils` |
| `dd` | Raw block-level file copy | `coreutils` | `coreutils` |
| `du` | Disk usage reporting | `coreutils` | `coreutils` |
| `chmod` | File permission changes | `coreutils` | `coreutils` |
| `unsquashfs` | Extracting SquashFS images | `squashfs-tools` | `squashfs-tools` |
| `dumpe2fs` | Inspecting ext4 filesystem metadata | `e2fsprogs` | `e2fsprogs` |

---

## D. Cloud-Init

Required when using cloud-init to configure VMs.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `cloud-localds` | Creating nocloud seed ISOs | `cloud-image-utils` | `cloud-utils` |
| `ssh-keygen` | Generating SSH keypairs | `openssh-client` | `openssh` (see §A) |

---

## E. Image Provisioning Backend (Choose One)

Both `mvm vm create` (rootfs provisioning) and `mvm image pull`/`mvm image import` (image optimization — shrink, deblob, OS detection) use the same provisioning backend. **Loop-mount is the default and recommended**.

### E1. Loop-Mount (Default)

The `mvm run provision` subcommand is a built-in CLI subcommand compiled into the `mvm` binary. It uses system tools directly — no extra runtime dependencies beyond system packages.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `losetup` | Loop device setup with partition scanning | `util-linux` | `util-linux` |
| `blkid` | Filesystem type detection | `util-linux` | `util-linux` |
| `blockdev` | Querying partition/device size | `util-linux` | `util-linux` |
| `mount` | Mounting the root partition | `util-linux` | `util-linux` |
| `umount` | Unmounting the root partition | `util-linux` | `util-linux` |
| `e2fsck` | Filesystem check before ext4 resize | `e2fsprogs` | `e2fsprogs` |
| `resize2fs` | Growing and shrinking ext4 filesystems | `e2fsprogs` | `e2fsprogs` |
| `tune2fs` | Reading ext4 block count for shrink calculation | `e2fsprogs` | `e2fsprogs` |
| `btrfs` | Growing and shrinking btrfs filesystems | `btrfs-progs` | `btrfs-progs` |
| `fstrim` | Discard unused blocks before shrink | `util-linux` | `util-linux` |
| `chroot` | Running commands inside the mounted rootfs | `coreutils` | `coreutils` |

> Most binaries (`util-linux`, `e2fsprogs`) are already required by the image pipeline (§C). Only `btrfs-progs` is unique to this path (and only needed for btrfs images).

### E2. libguestfs (Alternative)

libguestfs provides filesystem-agnostic rootfs access via a QEMU appliance. Enable with:

```bash
mvm config set settings guestfs_enabled true
```

| Distro | Command |
|--------|---------|
| Debian/Ubuntu | `sudo apt-get install libguestfs0 libguestfs-tools supermin` |
| RHEL/Fedora | `sudo dnf install libguestfs libguestfs-tools supermin` |
| Arch | `sudo pacman -S libguestfs supermin` |

The codebase uses the `guestfish` CLI tool as a subprocess — no cgo or Go bindings required. `mvm init` checks for the `guestfish` binary and configures the sudoers entry for it automatically.

---

## F. Kernel Build (Optional)

Only needed for `mvm kernel pull --type official --clean-build` (building the official kernel from source).

| Binary | Debian/Ubuntu | Arch |
|--------|---------------|------|
| `make` | `build-essential` | `base-devel` |
| `gcc` | `build-essential` | `base-devel` |
| `ld` | `binutils` | `binutils` |
| `flex` | `flex` | `flex` |
| `bison` | `bison` | `bison` |
| `bc` | `bc` | `bc` |
| `pahole` | `dwarves` | `pahole` |
| `git` | `git` | `git` |
| `curl` | `curl` | `curl` |
| `pkg-config` | `pkg-config` | `pkgconf` |

**Development libraries:**
- `libelf-dev` / `libelf` (Debian/Arch)
- `libssl-dev` / `openssl` (Debian/Arch)

---

## G. Host System Requirements

### Kernel Modules

| Module | Required For |
|--------|-------------|
| `kvm` | Hardware-accelerated virtualization |
| `kvm_intel` or `kvm_amd` | Vendor-specific KVM extensions |
| `tun` | TAP networking |
| `bridge` | Bridge networking (loaded on demand) |
| `vhost_vsock` | Vsock device support (loaded on demand) |
| `nft_chain_nat` | nftables NAT support (loaded on demand) |

### Hardware

- **Virtualization**: VT-x (Intel) or AMD-V must be enabled in BIOS/UEFI.
- **Permissions**: The user must be in the `mvm` group (created by `mvm host init`).

---

## H. Command Dependency Map

| Command | External Binaries Invoked |
|---------|--------------------------|
| `mvm host init` | `sudo`, `groupadd`, `usermod`, `visudo`, `sysctl`, `lsmod`, `iptables`/`nft`, `modprobe` |
| `mvm host clean` | `sudo`, `ip`, `iptables`/`nft` |
| `mvm host reset` | `sudo`, `groupdel`, `sysctl` |
| `mvm network create` | `ip`, `iptables`/`nft` |
| `mvm network rm` / `sync` | `ip`, `iptables`/`nft` |
| `mvm image pull` | `qemu-img` (may trigger conversion), `fakeroot` (tar-rootfs) |
| `mvm image import` | `qemu-img`, `sfdisk`, `blkid`, `mount`, `umount`, `tar`, `fakeroot` (tar-rootfs), `truncate`, `mkfs.ext4`, `unsquashfs`, `dumpe2fs`, `du`, `dd` |
| `mvm kernel pull --type official` | `make`, `gcc`, `ld`, `flex`, `bison`, `bc`, `pahole`, `git`, `curl`, `pkg-config` |
| `mvm kernel pull --type firecracker` | Download only (no build tools) |
| `mvm key create` | `ssh-keygen` |
| `mvm key import/ls/rm/inspect/export/default` | Internal only |
| `mvm bin pull/ls/rm/default` | Internal only (downloads from GitHub API) |
| `mvm vm create` | `firecracker`, `ip`, `iptables`/`nft`, `mvm run provision`, `losetup`, `blkid`, `blockdev`, `mount`, `umount`, `e2fsck`, `resize2fs`, `tune2fs`, `fstrim`, `chroot` (+ `btrfs` for btrfs images) |
| `mvm vm start/stop/reboot/pause/resume` | `firecracker`, `ip`, `iptables`/`nft` |
| `mvm vm rm` | `firecracker`, `ip`, `iptables`/`nft` |
| `mvm snapshot create/restore` | Internal (Firecracker API via Unix socket) |
| `mvm volume attach/detach` | `firecracker` |
| `mvm volume create/rm/resize` | `qemu-img` |
| `mvm volume ls/inspect` | Internal only |
| `mvm kernel import/ls/rm/default/inspect` | Internal only |
| `mvm cache init/prune/clean` | Internal (filesystem + DB operations) |
| `mvm config get/set/reset/list` | Internal (DB operations) |
| `mvm logs` | Internal (file read) |
| `mvm console` | Internal (Unix socket PTY relay) |
| `mvm ssh` | User's SSH client (`ssh` binary) |
| `mvm init` | `sudo`, `groupadd`, `usermod`, `visudo` |
