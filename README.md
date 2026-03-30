# mvmctl (mvm)

> **Container speed, VM isolation.**

[![CI](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml/badge.svg)](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Python 3.13+](https://img.shields.io/badge/python-3.13%2B-blue)](https://www.python.org/downloads/)

**mvm** is the modern way to run microVMs — get the startup speed of containers with the security and isolation of traditional VMs. Built for developers who need lightweight, fast-booting virtual machines without the overhead.

## Why mvm?

- **🚀 Blazing fast** — VMs boot in milliseconds, not minutes
- **🔒 Secure by default** — Hardware-level isolation with KVM
- **📦 Works with your images** — Ubuntu, Debian, Arch, and more
- **⚡ Simple CLI** — One command to create, start, and SSH into a VM
- **💻 Console access** — Interactive serial console without SSH (via `mvm console`)
- **🎯 Production ready** — Built for cloud-native and serverless workloads

```bash
# Create and SSH into a VM in under 60 seconds
mvm vm create --name myvm --image ubuntu-24.04
mvm vm ssh --name myvm
```

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Documentation](#documentation)
- [Building from Source](#building-from-source)
- [Contributing](#contributing)
- [License](#license)

---

## Prerequisites

- **Linux** (x86_64 or aarch64) — Firecracker only runs on Linux
- **KVM access** (`/dev/kvm`):
  ```bash
  sudo usermod -aG kvm $USER
  # Log out and back in
  ```
- **Python 3.13+**
- **System packages:**

  Ubuntu/Debian:
  ```bash
  sudo apt-get install -y iproute2 iptables genisoimage qemu-utils
  ```
  Arch Linux:
  ```bash
  sudo pacman -S --needed iproute2 iptables libisoburn qemu-base
  ```
- **Root access (one-time):** run `sudo mvm host init` once to create the `mvm` group and a sudoers drop-in; normal `mvm` commands require no `sudo` after that.

---

## Installation

### 1. Download prebuilt binary (recommended)

No Python required:

```bash
curl -L -o mvm https://github.com/AlanD20/mvmctl/releases/latest/download/mvm
chmod +x mvm
sudo mv mvm /usr/local/bin/
mvm --help
```

### 2. Install with pipx

```bash
pipx install mvmctl
mvm --help
```

### 3. Install via pip

```bash
pip install mvmctl
mvm --help
```

### 4. Install from source

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
uv sync
uv run mvm --help
```

---

## Quick Start

```bash
# 1. One-time host setup (KVM, ip_forward, mvm group, sudoers drop-in)
sudo mvm host init
# ⚠ Log out and back in, or run: newgrp mvm

# 2. Download a prebuilt Firecracker kernel
mvm kernel fetch

# 3. Download a root filesystem image
mvm image fetch ubuntu-24.04

# 4. Create and start a VM
mvm vm create --name myvm --image ubuntu-24.04

# 5. Follow the boot log until SSH is ready (~30-60 s)
mvm logs --name myvm --type boot --follow

# 6. SSH in
mvm ssh --name myvm

# 7. List running VMs
mvm vm ls

# 8. Remove the VM
mvm vm rm --name myvm --force
```

Or run the interactive setup wizard which guides you through all of the above:

```bash
mvm init
```

---

## Essential Commands

### VM Lifecycle

```bash
mvm vm create --name myvm --image ubuntu-24.04   # Create and start a VM
mvm vm ls                                         # List all VMs
mvm vm ssh --name myvm                           # SSH into a VM
mvm console --name myvm                          # Console access (no SSH)
mvm vm rm --name myvm --force                    # Remove a VM
```

### Resource Management

```bash
mvm image fetch ubuntu-24.04    # Download an OS image
mvm image ls                   # List available images
mvm kernel fetch               # Download Firecracker kernel
mvm bin fetch 1.15.0          # Download Firecracker binary
mvm key create mykey          # Generate SSH key
```

### System Setup

```bash
sudo mvm host init    # One-time host setup (KVM, networking)
mvm cache prune       # Clean up stale cache
```

See [docs/REFERENCES.md](docs/REFERENCES.md) for the complete command reference with all flags and options.

---

## Documentation

Comprehensive documentation is available in the `docs/` directory:

| Document | Description |
|----------|-------------|
| [docs/REFERENCES.md](docs/REFERENCES.md) | **Complete command reference** — all `mvm` commands, flags, and options<br>**Configuration** — config files, environment variables, cache structure<br>**Cloud-Init** — nocloud-net setup, security, modes |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Common issues and solutions<br>Debug mode, permission fixes, network issues |
| [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md) | System dependencies by category<br>Package names for Debian/Ubuntu/Arch |
| [docs/custom-kernel.md](docs/custom-kernel.md) | Building custom kernels for Firecracker |
| [docs/RELEASE.md](docs/RELEASE.md) | Release process and distribution packages |
| [docs/API.md](docs/API.md) | Python API reference for programmatic usage |

---

## Building from Source

Produces a standalone single-file binary — no Python runtime required on the target machine:

### Standard Build

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
uv sync --group dev --group build
uv run python -m nuitka --onefile --output-dir=dist --output-filename=mvm \
  --include-package=mvmctl --include-data-dir=src/mvmctl/assets=mvmctl/assets \
  --lto=yes --enable-plugin=anti-bloat src/mvmctl/main.py
# Output: dist/mvm
./dist/mvm --version
```

### Build with Guestfs Support (Optional)

For direct cloud-init injection mode (`--cloud-init-mode direct`), the binary must include the
`guestfs` module. Because mvmctl imports it dynamically (`importlib.import_module("guestfs")`),
you must pass `--include-package=guestfs` explicitly — static analysis cannot detect it.

> **`guestfs` is not on PyPI.** There is no `--group guestfs` uv dependency group in this repo.
> Install the Python bindings via your distro's package manager before building.

```bash
# 1. Install system libguestfs packages + Python bindings (distro only)
sudo apt-get install libguestfs0 libguestfs-tools python3-libguestfs supermin

# 2. Sync dev/build groups (no --group guestfs needed)
uv sync --group dev --group build

# 3. Build — explicitly include guestfs because of dynamic import
uv run python -m nuitka --onefile --output-dir=dist --output-filename=mvm \
  --include-package=mvmctl --include-package=guestfs \
  --include-data-dir=src/mvmctl/assets=mvmctl/assets \
  --lto=yes --enable-plugin=anti-bloat src/mvmctl/main.py
```

See [docs/RELEASE.md](docs/RELEASE.md) for detailed build instructions.

---

## Cache Directory Structure

```
~/.cache/mvmctl/
├── bin/               # Firecracker + jailer binaries
├── kernels/           # vmlinux kernel images
├── images/            # Root filesystem images (.ext4, .btrfs)
├── keys/              # Cached SSH public keys
├── networks/          # Per-network config + IP leases
├── vms/               # Per-VM state
│   └── <vm-name>/
│       ├── rootfs.ext4
│       ├── firecracker.json
│       ├── firecracker.log       # Firecracker process log (--type os)
│       ├── firecracker.console.log  # Serial console output (--type boot)
│       ├── firecracker.pid
│       ├── firecracker.sock      # API socket (--enable-api-socket only)
│       └── cloud-init/
├── metadata.json      # Asset registry (images, kernels, binaries)
└── audit.log          # Append-only operation log
```

---

## Troubleshooting

**`Permission denied: /dev/kvm`**
```bash
sudo usermod -aG kvm $USER
# Log out and back in, then verify: groups | grep kvm
```

**`Bridge mvm-default not found` / `No such device`**

Run `sudo mvm host init` once; the bridge is auto-created when you create a VM.

**`Kernel not found`**
```bash
mvm kernel fetch
```

**VM won't boot / SSH times out**

Cloud-init runs on first boot and takes 30–60 s. Follow the console log:
```bash
mvm vm logs --name myvm --type boot --follow
```
If it never reaches a `login:` prompt, check the Firecracker process log:
```bash
mvm vm logs --name myvm --type os
```

**`Image not found: ubuntu-24.04`**
```bash
mvm image fetch ubuntu-24.04
mvm image ls   # ✓ should appear
```

**`Firecracker binary not found`**
```bash
mvm bin fetch 1.15.0
mvm bin use 1.15.0
```

**`host init has not been run`**

`mvm host reset` requires a prior snapshot. Run `sudo mvm host init` first.

**`NoCloud-net server failed to start`**

The port range (8000-9000) may be exhausted. Check for stale servers:
```bash
# List processes using nocloud ports
sudo ss -tlnp | grep -E ':(8[0-9]{3}|9[0-9]{3})'
# Kill any orphaned mvm processes
pkill -f nocloud-net-server
```

**`VM can't fetch cloud-init data via nocloud-net`**

Verify firewall rules are configured:
```bash
sudo iptables -L MVM-NOCLOUD-INPUT -n -v
# Should show rules allowing source IP to destination ports
```

Check that the VM's network is correctly set up:
```bash
# From within the VM, test connectivity to the gateway
ping -c 1 10.0.0.1
# Test HTTP access to nocloud server
curl -v http://10.0.0.1:8080/
```

**`Cloud-init seems slow`**

nocloud-net is faster than ISO mode because it avoids ISO generation, but cloud-init
inside the VM still takes 30-60 seconds on first boot. To monitor progress:
```bash
mvm vm logs --name myvm --type boot --follow
```
Look for cloud-init status messages like `Cloud-init v. X.X.X running modules...`

---

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](.github/CONTRIBUTING.md) for the full guide on:
- Development setup
- Running tests and linting
- Adding new commands and images
- Build system and version bumping
- Development guidelines

---

## License

MIT — see [LICENSE](LICENSE).
