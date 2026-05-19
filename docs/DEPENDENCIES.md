# mvmctl Dependencies

This document lists all external binary and system-level dependencies required for `mvmctl`
to function correctly, organized by necessity: what you always need vs what's only needed
for specific features or chosen backends.

## Table of Contents

- [Required (Every Install)](#a-required-every-install)
- [Firewall Backend (Choose One)](#b-firewall-backend-choose-one)
- [Image & Storage](#c-image--storage)
- [Cloud-Init](#d-cloud-init)
- [Image Provisioning Backend (Choose One)](#e-image-provisioning-backend-choose-one)
- [Kernel Build (Optional)](#f-kernel-build-optional)
- [Host System Requirements](#g-host-system-requirements)
- [Command Dependency Map](#h-command-dependency-map)

---

## A. Required (Every Install)

These binaries must be present on the host for basic mvmctl operation. The `mvm init` wizard
checks for them automatically.

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

**Python libraries (bundled into compiled binary; included via `uv sync` when running from source):**

| Library | Purpose |
|---------|---------|
| `typer-slim` | CLI framework (lightweight, without Rich/Markdown deps) |
| `rich` | Console output formatting (progress bars, tables, syntax highlighting) |
| `pyyaml` | YAML config parsing for bundled assets (images.yaml, kernels.yaml) |
| `jinja2` | Template rendering for cloud-init and Firecracker config generation |
| `zstandard` | Zstandard compression/decompression of images |
| `passlib` | Password hashing (bcrypt scheme support) |
| `bcrypt` | BCrypt password hashing for cloud-init user data |

---

## B. Firewall Backend (Choose One)

mvmctl supports two firewall backends for NAT and forwarding rules. **nftables is the
default** (`settings.firewall_backend: "nftables"`). Only one backend needs to be installed.

| Backend | Binary | Debian/Ubuntu | Arch | Notes |
|---------|--------|---------------|------|-------|
| **nftables** (default) | `nft` | `nftables` | `nftables` | Modern, atomic batch, no legacy split |
| iptables | `iptables` | `iptables` | `iptables` | Legacy; also needs `iptables-save` + `iptables-restore` for rule persistence |

**Switch backends at any time:**
```bash
mvm config set settings firewall_backend nftables   # default
mvm config set settings firewall_backend iptables
mvm network sync                                    # reload rules
```

---

## C. Image & Storage

These are required for pulling, importing, and converting VM images.

| Binary | Purpose | Debian/Ubuntu | Arch |
|--------|---------|---------------|------|
| `qemu-img` | Image conversion and resize | `qemu-utils` | `qemu-img` |
| `mkfs.ext4` | Formatting extracted rootfs images | `e2fsprogs` | `e2fsprogs` |
| `blkid` | Detecting root partitions and UUIDs | `util-linux` | `util-linux` |
| `sfdisk` | Partition table manipulation | `util-linux` | `util-linux` |
| `dumpe2fs` | Filesystem inspection | `e2fsprogs` | `e2fsprogs` |
| `truncate` | Creating sparse files for new images | `coreutils` | `coreutils` |
| `dd` | Raw block-level file copy | `coreutils` | `coreutils` |
| `du` | Disk usage reporting | `coreutils` | `coreutils` |
| `chmod` | File permission changes (used across all domains) | `coreutils` | `coreutils` |
| `unsquashfs` | Extracting SquashFS images | `squashfs-tools` | `squashfs-tools` |

**Python library:** `zstandard` — for zstd compression/decompression of images.
Bundled into the compiled binary; included via `uv sync` when running from source.

---

## D. Cloud-Init

Required when using cloud-init to configure VMs.

| Binary | Purpose | Debian/Ubuntu | Arch | Required For |
|--------|---------|---------------|------|-------------|
| `cloud-localds` | Creating nocloud seed ISOs | `cloud-image-utils` | `cloud-utils` | ISO mode (`--cloud-init-mode iso`) |
| `ssh-keygen` | Generating SSH keypairs | `openssh-client` | `openssh` | Already listed in §A |

---

## E. Image Provisioning Backend (Choose One)

Both `mvm vm create` (rootfs provisioning) and `mvm image pull`/`mvm image import`
(image optimization — shrink, deblob, OS detection) use the same provisioning backend.
**Loop-mount is the default and recommended** (~200ms per VM).

### E1. Loop-Mount (Default)

The `mvm-provision` binary is a symlink to the combined `mvm-services` multidist binary
(compiled via Nuitka). It uses system tools directly — no Python dependencies beyond stdlib.

**Installed by:** `mvm init` (extracts `mvm-provision` from the `mvm-services` bundle).

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

> Most binaries (`util-linux`, `e2fsprogs`) are already required by the image pipeline (§C).
> Only `btrfs-progs` is unique to this path (and only needed for btrfs images).

### E2. libguestfs (Alternative)

libguestfs provides filesystem-agnostic rootfs access via a QEMU appliance.
Slower (~2600ms per VM) but more capable OS detection. Enable with:

```bash
mvm config set settings guestfs_enabled true
```

**System packages:**

| Distro | Command |
|--------|---------|
| Debian/Ubuntu | `sudo apt-get install libguestfs0 libguestfs-tools supermin python3-libguestfs` |
| RHEL/Fedora | `sudo dnf install libguestfs libguestfs-tools supermin python3-libguestfs` |
| Arch | `sudo pacman -S libguestfs supermin` (Python bindings included) |

> **Note:** The `guestfs` Python package is **not on PyPI**. It must be installed via your
> system package manager. `mvm init` checks for it and configures the sudoers entry for
> `supermin` automatically.

---

## F. Kernel Build (Optional)

Only needed for `mvm kernel pull --type official --clean-build`.

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
- `libncurses-dev` / `ncurses` (Debian/Arch)

---

## G. Host System Requirements

### Kernel Modules

| Module | Required For |
|--------|-------------|
| `kvm` | Hardware-accelerated virtualization |
| `kvm_intel` or `kvm_amd` | Vendor-specific KVM extensions |
| `tun` | TAP networking |
| `bridge` | Bridge networking (loaded on demand) |
| `vhost_vsock` | Console relay |

### Hardware

- **Virtualization**: VT-x (Intel) or AMD-V must be enabled in BIOS/UEFI.
- **Permissions**: The user must be in the `mvm` group (created by `mvm host init`).

---

## H. Command Dependency Map

| Command | External Binaries Invoked |
|---------|--------------------------|
| `mvm host init` | `sudo`, `groupadd`, `usermod`, `visudo`, `sysctl`, `ip`, `iptables`/`nft`, `modprobe` |
| `mvm host clean` | `sudo`, `ip`, `iptables`/`nft` |
| `mvm host reset` | `sudo`, `groupdel`, `sysctl` |
| `mvm network create` | `ip`, `iptables`/`nft` |
| `mvm network rm` / `sync` | `ip`, `iptables`/`nft` |
| `mvm image pull` | `qemu-img` (may trigger conversion) |
| `mvm image import` | `qemu-img`, `sfdisk`, `blkid`, `mount`, `umount`, `tar`, `truncate`, `mkfs.ext4`, `unsquashfs` |
| `mvm kernel pull --type official` | `make`, `gcc`, `ld`, `flex`, `bison`, `bc`, `pahole`, `git`, `curl`, `pkg-config` |
| `mvm kernel pull --type firecracker` | (internal Python logic — download only) |
| `mvm key` | `create` → `ssh-keygen`; `add/ls/rm/inspect/export/default` → internal only |
| `mvm bin` | `pull/ls/rm/default` → internal only (downloads from GitHub API) |
| `mvm vm create` | `firecracker` + `jailer`, `ip`, `iptables`/`nft`, `mvm-provision`, `losetup`, `blkid`, `blockdev`, `mount`, `umount`, `e2fsck`, `resize2fs`, `tune2fs`, `fstrim`, `chroot` (+ `btrfs` for btrfs images) |
| `mvm vm start/stop/reboot/pause/resume` | `firecracker`, `ip`, `iptables`/`nft` |
| `mvm vm rm` | `firecracker`, `ip`, `iptables`/`nft` |
| `mvm vm snapshot/load` | internal (Firecracker API via Unix socket) |
| `mvm vm attach-volume` / `detach-volume` | `firecracker` |
| `mvm volume` | `ls/inspect` → internal; `create/rm/resize` → `qemu-img` |
| `mvm kernel import/ls/rm/default/inspect` | internal only |
| `mvm cache init/prune/clean` | internal (filesystem + DB operations) |
| `mvm config get/set/reset/list` | internal (DB operations) |
| `mvm logs` | internal (file read) |
| `mvm console` | internal (Unix socket PTY relay) |
| `mvm ssh` | User's SSH client |
| `mvm init` | `sudo`, `groupadd`, `usermod`, `visudo` |
