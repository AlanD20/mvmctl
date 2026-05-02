# mvmctl (mvm)

> **Container speed, VM isolation.**

[![CI](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml/badge.svg)](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Python 3.13+](https://img.shields.io/badge/python-3.13%2B-blue)](https://www.python.org/downloads/)

**mvmctl** is the modern way to run microVMs — get the startup speed of containers with the security and isolation of traditional VMs. Built for developers who need lightweight, fast-booting virtual machines without the overhead.

## Why mvmctl?

- **🚀 Blazing fast** — VMs boot in milliseconds, not minutes
- **🔥 Powered by Firecracker** — AWS's battle-tested microVM technology, the engine behind Lambda and Fargate
- **🔒 Secure by default** — Hardware-level isolation with KVM
- **📦 Works with your images** — Ubuntu, Debian, Arch, and more
- **⚡ Simple CLI** — One command to create, start, and SSH into a VM
- **💻 Console access** — Interactive serial console without SSH (via `mvm console`)
- **🎯 ~Production ready~ (still under development to ensure stability)** — Built for cloud-native and serverless workloads

```bash
# Create and SSH into a VM in under 60 seconds
mvm vm create --name myvm --image ubuntu-24.04
mvm ssh --name myvm
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
  sudo apt-get install -y iproute2 iptables cloud-image-utils qemu-utils
  ```
  Arch Linux:
  ```bash
  sudo pacman -S --needed iproute2 iptables cloud-utils qemu-img
  ```
- **Root access (one-time):** run `mvm init` once to create the `mvm` group and a sudoers drop-in; normal `mvm` commands require no `sudo` after that. Review the `PRIVILEGED_BINARIES` dict in `src/mvmctl/constants.py` for allowed binaries

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

The easiest way to get started is with the interactive setup wizard:

```bash
# Interactive setup - guides you through host init, downloads, and configuration
# (handles privilege escalation automatically when needed)
mvm init
# ⚠ Log out and back in when prompted, or run: newgrp mvm

# Create a key
mvm key create test
mvm key set-default test

# Create and start a VM
mvm vm create --name myvm --image ubuntu-24.04

# Follow the boot log until SSH is ready (~30-60 s)
mvm logs myvm --follow

# SSH in
mvm ssh --name myvm

# List running VMs
mvm vm ls

# Remove the VM when done
mvm vm rm --name myvm
```

---

## Essential Commands

### VM Lifecycle

```bash
mvm vm create --name myvm --image ubuntu-24.04   # Create and start a VM
mvm vm ls                                         # List all VMs
mvm ssh --name myvm                           # SSH into a VM
mvm console myvm                                 # Console access (no SSH)
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
│   └── <vm-sha>/           # VM directories named by SHA256 hash
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

Common issues and quick fixes:

| Issue | Solution |
|-------|----------|
| **Permission denied: /dev/kvm** | `sudo usermod -aG kvm $USER` then log out/back in |
| **Bridge not found** | Run `sudo mvm host init` once |
| **VM won't boot / SSH times out** | Cloud-init takes 30-60s on first boot. Watch with `mvm logs myvm --follow` |
| **Kernel not found** | `mvm kernel fetch` |
| **Image not found** | `mvm image fetch ubuntu-24.04` |
| **NoCloud server failed** | Port range exhausted. Check: `sudo ss -tlnp \| grep -E ':(8[0-9]{3}\|9[0-9]{3})'` |

See [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for complete troubleshooting guide including:
- Debug mode
- Console relay issues
- Network permission problems
- Cache corruption fixes

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
