# Building a Custom Kernel for Firecracker

This guide covers how to build a custom Linux kernel optimised for Firecracker microVMs,
either from the pre-configured Firecracker CI kernel or from upstream kernel sources.

---

## Overview

Firecracker requires a kernel that is:

- An **uncompressed ELF binary** (`vmlinux`) on x86_64
- Built with a Firecracker-compatible configuration (minimal, no PCI/ACPI by default)
- Small enough to start in under 125ms (the default SLA target)

`mvm` supports two workflows:

| Workflow | Command | Time | Use when |
|----------|---------|------|----------|
| **Firecracker CI kernel** | `mvm kernel pull --type firecracker` | ~30s (download only) | Production use, fastest start times |
| **Official upstream kernel** | `mvm kernel pull --type official` | 10-30 min (compile) | Custom configs, latest features, debugging |

---

## Prerequisites

### All builds

```bash
# Verify KVM access
ls -la /dev/kvm

# Verify mvm host is initialized
mvm host ls
```

### Official kernel builds only

The following packages are required to compile the kernel from source:

**Ubuntu / Debian:**
```bash
sudo apt-get install -y \
  build-essential \
  flex \
  bison \
  libelf-dev \
  libssl-dev \
  libncurses-dev \
  bc \
  gcc \
  make
```

**Arch Linux:**
```bash
sudo pacman -S --needed \
  base-devel \
  flex \
  bison \
  libelf \
  openssl \
  ncurses \
  bc
```

**Fedora / RHEL / AlmaLinux:**
```bash
sudo dnf groupinstall "Development Tools"
sudo dnf install -y \
  flex \
  bison \
  elfutils-libelf-devel \
  openssl-devel \
  ncurses-devel \
  bc
```

Additional build tools that may be needed (required for `--type official --clean-build`):
```bash
# Debian/Ubuntu
sudo apt-get install -y dwarves git curl pkg-config

# Arch
sudo pacman -S --needed pahole git curl pkgconf
```

Verify tools are present:
```bash
which make gcc flex bison bc
```

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

**Code reference:** `src/mvmctl/core/kernel/_service.py` lines 616-624

---

## Managing Multiple Kernels

```bash
# List all cached kernels
mvm kernel ls

# List remote kernel versions available for download
mvm kernel ls --remote

# Set a kernel as default for vm create
mvm kernel default vmlinux-firecracker-1.12-x86_64

# Remove a kernel
mvm kernel rm vmlinux-firecracker-1.10-x86_64
```

The `Def` column in `mvm kernel ls` shows the active default kernel.

---

## Using a Custom Kernel with a VM

```bash
# Use the default kernel (set via mvm kernel default)
mvm vm create -n myvm --image ubuntu-24.04

# Use a specific kernel by path
mvm vm create -n myvm \
  --image ubuntu-24.04 \
  --kernel ~/.cache/mvmctl/kernels/vmlinux-custom
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

### Kernel too large

The Firecracker CI kernel is typically ~5 MiB. If your custom kernel is much larger,
check for unnecessary configs. Consider using the Firecracker CI kernel as your base config.

### "Required kernel settings missing" during build

The Firecracker config URL may have changed. Check kernels.yaml:
```bash
python3 -c "
import yaml, importlib.resources
with importlib.resources.files('mvmctl.assets').joinpath('kernels.yaml').open() as f:
    k = yaml.safe_load(f)
    print(k.get('kernel-official',{}).get('config_url_template','Not found'))
"
```

Then update `config_url_template` in `src/mvmctl/assets/kernels.yaml` if needed.

---

## Reference

### Kernel versions tested with Firecracker

| Kernel | Status | Notes |
|--------|--------|-------|
| 6.1.x LTS | Supported | Long-term support, recommended for production |
| 6.6.x LTS | Supported | Newer LTS, recommended for production |
| 6.12.x LTS | Supported | Latest LTS |

> **Note:** The default kernel version (`6.19.9`) is **NOT** an LTS kernel. It is the latest upstream stable at the time of release. Use `--version latest` to resolve the most recent version from the upstream directory listing. If you need long-term support, explicitly pass `--version 6.1.102` or `--version 6.12.21` to `mvm kernel pull --type official`. The LTS versions in the table above are tested and known to work — use them for production deployments.

### Relevant constants (src/mvmctl/constants.py)

| Constant | Description |
|----------|-------------|
| `OVERRIDABLE_DEFAULTS["defaults.kernel"]["version"]` | Default kernel version for `mvm kernel pull --type official` |
| `KERNEL_TYPE_OFFICIAL` | The string `"official"` for kernel type references |
| `KERNEL_TYPE_FIRECRACKER` | The string `"firecracker"` for kernel type references |

### Kernel config URLs (src/mvmctl/assets/kernels.yaml)

The Firecracker config URLs are per-kernel in `kernels.yaml`, not in `constants.py`:

| YAML field | Description |
|------------|-------------|
| `config_url_template` | URL template for Firecracker's recommended `.config` file |

### Per-kernel config lists (src/mvmctl/assets/kernels.yaml — `kernel-official`)

The kernel config lists are defined per-kernel in `kernels.yaml`. Load them at runtime
by reading the YAML file through the `AssetManager` or via `core.kernel._service.KernelService`.

| YAML field | Description |
|------------|-------------|
| `required_settings` | Config options that must be `=y` after build; missing ones trigger a confirmation prompt |
| `enabled_configs` | Config options always enabled during the build |
| `disabled_configs` | Config options always disabled during the build |
| `set_val_configs` | Config options set to a specific integer value during the build |

---

*See also: [Firecracker official documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)*
