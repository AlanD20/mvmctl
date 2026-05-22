# Kernel Management

This guide covers how to obtain, build, import, and manage kernels for use with
mvmctl and Firecracker microVMs.

---

## Overview

Firecracker requires an **uncompressed ELF binary** (`vmlinux`) — not the compressed
`vmlinuz` used by traditional bootloaders. The kernel must be small and fast to boot.

`mvm` supports three workflows:

| Workflow | Command | Time | Use when |
|----------|---------|------|----------|
| **Firecracker CI kernel** | `mvm kernel pull --type firecracker` | ~30s (download only) | Production use, fastest start times |
| **Official upstream kernel** | `mvm kernel pull --type official` | 10-30 min (compile) | Custom configs, latest features, debugging |
| **Import custom kernel** | `mvm kernel import <name> <path>` | instant (file copy) | Pre-built or third-party vmlinux files |

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Workflow A: Firecracker CI Kernel (Recommended)](#workflow-a-firecracker-ci-kernel-recommended)
- [Workflow B: Upstream Kernel Build](#workflow-b-upstream-kernel-build)
- [Verifying Required Settings](#verifying-required-settings)
- [Workflow C: Importing a Custom Kernel](#workflow-c-importing-a-custom-kernel)
- [Managing Multiple Kernels](#managing-multiple-kernels)
- [Using a Custom Kernel with a VM](#using-a-custom-kernel-with-a-vm)
- [Troubleshooting](#troubleshooting)
- [Reference](#reference)

---

## Prerequisites

### All builds

```bash
# Verify KVM access
ls -la /dev/kvm

# Verify mvm host is initialized
mvm host status
```

### Official kernel builds only

See [DEPENDENCIES.md#f-kernel-build-optional](DEPENDENCIES.md#f-kernel-build-optional) for the
required build packages per distribution.

---

## Workflow A: Firecracker CI Kernel (Recommended)

The Firecracker project publishes pre-built kernels for each Firecracker release, tested
against the exact Firecracker version.

```bash
# Download the Firecracker CI kernel matching the active binary version
mvm kernel pull --type firecracker

# Download for a specific CI version
mvm kernel pull --type firecracker --version 1.12

# Download for a different architecture
mvm kernel pull --type firecracker --arch aarch64

# Using the type:version shorthand (equivalent to --type firecracker --version 6.1)
mvm kernel pull firecracker:6.1

# Set as default kernel after download
mvm kernel pull --type firecracker --default
```

The kernel is saved to `~/.cache/mvmctl/kernels/` with a name derived from the CI version and architecture.

`mvm kernel pull` also accepts the shorthand `type:version` syntax (e.g. `firecracker:6.1`), which is equivalent to `--type firecracker --version 6.1`.

**Why use this?** These kernels are curated by the Firecracker team, are the smallest and
fastest to boot, and have guaranteed compatibility with the matching Firecracker release.

---

## Workflow B: Upstream Kernel Build

Build any Linux kernel version from source with Firecracker's recommended configuration.

### Basic build

```bash
# Build the default version (6.19.9) with Firecracker config
mvm kernel pull --type official

# Build a specific version
mvm kernel pull --type official --version 6.1.102

# Using type:version shorthand (equivalent to --type official --version 6.19.9)
mvm kernel pull official:6.19.9

# Build with parallel jobs (faster)
mvm kernel pull --type official --jobs 8

# Bypass cache and force a clean build
mvm kernel pull --type official --clean-build

# Keep the build directory after completion (useful for debugging)
mvm kernel pull --type official --keep-build-dir
```

### Build process

`mvm kernel pull --type official` runs the following steps automatically:

| Step | Description |
|------|-------------|
| 1. Download source | Fetches `linux-<version>.tar.xz` from kernel.org |
| 2. Verify checksum | SHA-256 verification (fetched from kernel.org) |
| 3. Extract | Extracts the tarball to a temporary build directory |
| 4. Download config | Fetches Firecracker's recommended `.config` for the kernel version |
| 5. `make olddefconfig` | Resolves any missing config options to defaults |
| 6. Apply overrides | Enables/disables specific configs from `kernels.yaml` |
| 7. Build | Compiles `vmlinux` using `make vmlinux -jN` |
| 8. Copy & metadata | Copies `vmlinux` to kernels cache and saves metadata JSON |
| 9. Cleanup | Removes the build directory (unless `--keep-build-dir`) |

### Kernel features

`mvm kernel pull` supports enabling feature groups via `--features`:

```bash
# Enable KVM paravirtualization features
mvm kernel pull --type official --features kvm

# Enable multiple features (comma-separated)
mvm kernel pull --type official --features kvm,nftables
```

Supported feature names:
- `kvm` — Enable KVM paravirtualization options (required for Firecracker)
- `nftables` — Enable nftables kernel support (required for nftables firewall backend)
- `tuntap` — Enable TUN/TAP networking support (required for Firecracker network connectivity)

The `--type firecracker` kernel automatically includes the `kvm` feature when the VM has nested virtualization enabled.

### Custom kernel config overlay

To apply your own kernel config on top of Firecracker's defaults:

```bash
# Prepare your config changes (e.g., enable a device driver)
cat > /tmp/my-overrides.config << 'EOF'
CONFIG_VIRTIO_NET=y
CONFIG_9P_FS=y
CONFIG_9P_FS_POSIX_ACL=y
EOF

# Build with your custom overlay applied last
mvm kernel pull --type official \
  --version 6.1.102 \
  --config /tmp/my-overrides.config

# The overlay is applied AFTER the Firecracker defaults — your settings win
```

**Warning:** Enabling configs that conflict with Firecracker's microVM architecture
(e.g., `CONFIG_PCI`, `CONFIG_ACPI`) may prevent VMs from booting.

---

## Verifying Required Settings

After building, `mvm` verifies that all required kernel settings are present. The required
settings are defined under `required_settings` in `src/mvmctl/assets/kernels.yaml` for the `kernel-official` entry.

If a required setting is missing, a warning is logged and the build continues (the
`KernelConfigResult` has `success=False` but the pipeline does not abort). The missing
settings are collected and returned as warnings in the final result:

```
Required kernel settings missing: CONFIG_VIRTIO_BLK, CONFIG_VIRTIO_NET
```

---

## Workflow C: Importing a Custom Kernel

Register a pre-built vmlinux file you already have (from a custom build, third-party source, or another machine) into the kernel cache and database. This makes it visible in `mvm kernel ls` and usable with any VM — including across stop/restart cycles.

```bash
# Import a vmlinux file with auto-detected version and arch from filename
mvm kernel import my-custom-kernel ~/kernels/vmlinux-6.1-x86_64

# Override auto-detected values explicitly
mvm kernel import my-custom-kernel ~/kernels/vmlinux-custom \
  --version 6.1 \
  --arch arm64

# Set as default immediately
mvm kernel import my-custom-kernel ~/kernels/vmlinux-custom \
  --version 6.1 \
  --default

# Use the imported kernel with a VM
mvm kernel ls                           # Show ID prefix
mvm vm create myvm --image ubuntu:24.04 --kernel <id>
```

The kernel is copied to `~/.cache/mvmctl/kernels/`, registered in the database with `type: custom`, and a content-addressed SHA256 ID is generated from the file contents. You can stop and restart VMs using this kernel.

---

## Managing Multiple Kernels

```bash
# List all cached kernels (including imported ones)
mvm kernel ls

# List remote kernel versions available for download
mvm kernel ls --remote

# Set a kernel as default for vm create
mvm kernel default <id>    # Use the ID prefix from 'mvm kernel ls'

# Remove a kernel
mvm kernel rm <id>         # Use the ID prefix from 'mvm kernel ls'

# Import a pre-built vmlinux file
mvm kernel import my-custom ~/vmlinux-6.1-x86_64 --default
```

The `Def` column in `mvm kernel ls` shows the active default kernel. Imported kernels show `type: custom` in the listing.

---

## Using a Custom Kernel with a VM

```bash
# Use the default kernel (set via mvm kernel default)
mvm vm create myvm --image ubuntu:24.04

# Use a specific kernel by ID prefix
mvm kernel import my-custom ~/vmlinux-6.1-arm64
mvm vm create myvm --image ubuntu:24.04 --kernel <id>

# You can also pass a direct path to --kernel
# (but the kernel won't survive VM stop/restart — use import instead)
mvm vm create myvm --image ubuntu:24.04 --kernel /path/to/vmlinux
```

---

## Troubleshooting

### Build fails: "make: command not found"

Install the build tools as shown in [Prerequisites](#prerequisites).

### Build fails at "olddefconfig"

The config file may be incompatible with the kernel version. Try without a custom config:
```bash
mvm kernel pull --type official --version 6.1.102
```

### VM panics on boot

Check the boot log:
```bash
mvm logs myvm --follow
```

Common causes:
- Missing `CONFIG_VIRTIO_BLK` — VM cannot access the rootfs disk
- Missing `CONFIG_VIRTIO_NET` — VM has no network interface
- Missing `CONFIG_SERIAL_8250` — No serial console output (boot log empty)

---

## Reference

### Kernel versions tested with Firecracker

| Kernel | Status | Notes |
|--------|--------|-------|
| 6.1.x LTS | Supported | Long-term support, recommended for production |
| 6.6.x LTS | Supported | Newer LTS, recommended for production |
| 6.12.x LTS | Supported | Latest LTS |

> **Note:** The default kernel version (`6.19.9`) is **NOT** an LTS kernel. It is the latest upstream stable at the time of release. Use `--version latest` to resolve the most recent version from the upstream directory listing. If you need long-term support, explicitly pass `--version 6.1.102` or `--version 6.12.21` to `mvm kernel pull --type official`. The LTS versions in the table above are tested and known to work — use them for production deployments.

*See also: [ASSETS_CONFIGURATIONS.md](ASSETS_CONFIGURATIONS.md) for the kernel YAML config reference and [Firecracker official documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md).*
