# mvmctl Dependencies

All external binary and system-level dependencies required for mvmctl to function correctly, organized by necessity.

## Table of Contents

- [A. Required (Every Install)](#a-required-every-install)
- [B. Firewall Kernel Support](#b-firewall-kernel-support)
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

These binaries support administrator setup and selected VM workflows; not every ordinary command invokes every tool.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `sudo` | Privilege escalation for `mvm host init` and privileged ops | `sudo` | `sudo` |
| `groupadd` | Creating the `mvm` system group | `passwd` | `shadow` |
| `usermod` | Adding users to the `mvm` group | `passwd` | `shadow` |
| `groupdel` | Removing the `mvm` group on `mvm host reset` | `passwd` | `shadow` |
| `visudo` | Validating sudoers drop-in files | `sudo` | `sudo` |
| `modprobe` | Loading KVM and networking kernel modules | `kmod` | `kmod` |
| `lsmod` | Checking KVM module status | `kmod` | `kmod` |
| `sysctl` | Enabling IP forwarding | `procps` | `procps-ng` |
| `ssh-keygen` | Generating SSH keypairs for microVMs | `openssh-client` | `openssh` |
| `tar` | Extracting rootfs from tarballs | `tar` | `tar` |

**Go toolchain:** The `mvm` binary is a single compiled Go binary. No runtime dependencies beyond the system packages listed.

---

## B. Firewall Kernel Support

v0.3.0 has one typed nftables implementation using the kernel netlink API. The `nft` and `iptables` command-line
packages are not firewall runtime dependencies, and the old selectable backend setting is retired. Managed networks
need kernel support for the required `inet` and `bridge` nftables families, connection tracking, and NAT expressions.
Host readiness probes the actual kernel behavior in a disposable network namespace and fails closed when unavailable;
installing an `nft` CLI alone cannot supply missing kernel support.

---

## C. Image & Storage

Required for pulling, importing, and converting VM images.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `qemu-img` | Image conversion and resize | `qemu-utils` | `qemu-img` |
| `mkfs.ext4` | Formatting extracted rootfs images | `e2fsprogs` | `e2fsprogs` |
| `e2fsck` | Journal-only preparation of disposable ext4 VM-import inspection copies with LoopMount | `e2fsprogs` | `e2fsprogs` |
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

Loop-mount provisioning uses the closed typed privileged receiver compiled into the same `mvm` binary. The former
public `mvm run provision` command and raw-sudo grant are removed. The receiver still needs the host filesystem tools
below; it does not accept arbitrary host commands or paths from the operator.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `losetup` | Loop device setup with partition scanning | `util-linux` | `util-linux` |
| `blkid` | Filesystem type detection | `util-linux` | `util-linux` |
| `blockdev` | Querying partition/device size | `util-linux` | `util-linux` |
| `mount` | Mounting the root partition | `util-linux` | `util-linux` |
| `umount` | Unmounting the root partition | `util-linux` | `util-linux` |
| `e2fsck` | Filesystem check before ext4 resize; journal-only replay on disposable VM-import inspection copies | `e2fsprogs` | `e2fsprogs` |
| `resize2fs` | Growing and shrinking ext4 filesystems | `e2fsprogs` | `e2fsprogs` |
| `tune2fs` | Reading ext4 block count for shrink calculation | `e2fsprogs` | `e2fsprogs` |
| `btrfs` | Growing and shrinking btrfs filesystems | `btrfs-progs` | `btrfs-progs` |
| `fstrim` | Discard unused blocks before shrink | `util-linux` | `util-linux` |
| `chroot` | Running commands inside the mounted rootfs | `coreutils` | `coreutils` |

> Most binaries (`util-linux`, `e2fsprogs`) are already required by the image pipeline (§C). Only `btrfs-progs` is unique to this path (and only needed for btrfs images).

LoopMount import from a VM invokes the fixed `/usr/sbin/e2fsck -p -E journal_only` as the ordinary operator,
against a retained descriptor for a new inspection copy. It does not run filesystem repair on the VM's protected
disk or grant the operator a new sudoers command. The `e2fsprogs` installation must provide that executable path;
missing or unsuccessful journal preparation fails import rather than skipping recovery or using a writable root
inspection mount. Ordinary file imports and the GuestFS path do not select this VM-only preparation.

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

The optional backend uses the `guestfish` CLI tool as a subprocess; it is not a separate sudoers grant.

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

### Managed Cache Storage

For v0.3 privileged operations, `MVM_CACHE_DIR` must reside on ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or
ZFS. FUSE, remote, overlay/stacked, and automount cache roots are unsupported. Custom local paths remain supported and
are pinned independently of unrelated ancestor filesystems.

### Firecracker Jailer

`mvm bin pull` resolves one release and invokes the typed trusted-release authority. Root either consumes the exact
optional mirror archive or fetches the fixed upstream release, verifies it, and atomically publishes the matching
Firecracker/Jailer pair. Users do not invoke an installation service directly; source-built pairs are unsupported.

---

## H. Command Dependency Map

| Command | External Binaries Invoked |
|---------|--------------------------|
| `mvm host install-system` | No external command; exact early root bootstrap uses descriptor-relative filesystem operations |
| `mvm host init` | `sudo` for sudo-invoker mode, plus administrator tools such as `groupadd`, `usermod`, `visudo`, `sysctl`, `lsmod`, `modprobe` |
| `mvm host clean` | Typed privileged actions; no `ip`, `iptables`, or `nft` firewall subprocess |
| `mvm host reset` | `sudo`, `groupdel`, `sysctl` |
| `mvm network create` | Typed root netlink and nftables-kernel operations; no `ip`, `iptables`, or `nft` CLI |
| `mvm network rm` / `sync` | Typed root netlink and nftables-kernel operations; no firewall CLI |
| `mvm policy traffic/exec` | Typed root policy actions; no firewall CLI |
| `mvm image pull` | `qemu-img` (may trigger conversion), `fakeroot` (tar-rootfs) |
| `mvm image import` | `qemu-img`, `sfdisk`, `blkid`, `mount`, `umount`, `tar`, `fakeroot` (tar-rootfs), `truncate`, `mkfs.ext4`, `unsquashfs`, `dumpe2fs`, `du`, `dd`; `/usr/sbin/e2fsck` for ext4 VM import with LoopMount |
| `mvm kernel pull --type official` | `make`, `gcc`, `ld`, `flex`, `bison`, `bc`, `pahole`, `git`, `curl`, `pkg-config` |
| `mvm kernel pull --type firecracker` | Download only (no build tools) |
| `mvm key create` | `ssh-keygen` |
| `mvm key import/ls/rm/inspect/export/default` | Internal only |
| `mvm bin pull/rm` | Typed privileged release actions; no public service subcommand |
| `mvm bin ls/default` | Internal architecture-scoped SQLite display projection only |
| `mvm vm create` | Trusted `jailer` + `firecracker`; provisioning may use `losetup`, `blkid`, `blockdev`, `mount`, `umount`, `e2fsck`, `resize2fs`, `tune2fs`, `fstrim`, `chroot` (+ `btrfs` for btrfs images) |
| `mvm vm start/stop/reboot/pause/resume` | Trusted `jailer` + `firecracker`; protected API access is root-brokered |
| `mvm vm rm` | Typed privileged VM cleanup, with no standalone Jailer cleanup command |
| `mvm snapshot create/restore` | Typed root snapshot actions and root-brokered Firecracker API access |
| `mvm volume attach/detach` | Typed root volume/mount operations and root-brokered Firecracker API access; live attach also checks authenticated guest readiness |
| `mvm volume create/rm/resize` | Internal raw-file allocation and typed privileged removal/growth; no `qemu-img` |
| `mvm volume ls` | Internal SQLite display projection |
| `mvm volume inspect` | Typed root observation of authoritative volume backing |
| `mvm kernel import/rm` | Typed root protected-kernel publication/retirement and local projection updates |
| `mvm kernel ls/default/inspect` | Internal local projection and file inspection |
| `mvm cache init` | Internal filesystem + DB operations |
| `mvm cache prune/clean` | Typed resource cleanup and protected-cache retirement, plus local filesystem/DB operations |
| `mvm config get/set/reset/list` | Internal (DB operations) |
| `mvm logs` | Internal (file read) |
| `mvm console` | Internal (Unix socket PTY relay) |
| `mvm ssh` | User's SSH client (`ssh` binary) |
