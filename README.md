# mvmctl (mvm)

> **Container speed, VM isolation.**

[![CI](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml/badge.svg)](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Python 3.13+](https://img.shields.io/badge/python-3.13%2B-blue)](https://www.python.org/downloads/)

**mvmctl** is the modern way to run microVMs -- get the startup speed of containers with the security and isolation of traditional VMs. Built for developers who need lightweight, fast-booting virtual machines without the overhead.

## Why mvmctl?

- ⚡ **Blazing fast** -- VMs boot in milliseconds, not minutes
- 🔥 **Powered by Firecracker** -- AWS's battle-tested microVM technology, the engine behind Lambda and Fargate
- 🛡️ **Secure by default** -- Hardware-level isolation with KVM
- 🖼️ **Works with your images** -- Ubuntu, Debian, Arch, Alpine, and more
- ⌨️ **Simple CLI** -- One command to create, start, and SSH into a VM
- 🖥️ **Console access** -- Interactive serial console without SSH (via `mvm console`)
- 🚧 **Pre-production** -- Still under active development.

```bash
# Create and SSH into a VM in under 60 seconds
mvm vm create myvm --image ubuntu:24.04
mvm ssh myvm
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

- **Linux** (x86_64 or aarch64) -- Firecracker only runs on Linux
- **KVM access** (`/dev/kvm`):
  ```bash
  sudo usermod -aG kvm $USER
  # Log out and back in
  ```
- **Python 3.13+** (required for pip/pipx/source installs only — the prebuilt binary has no Python runtime dependency)
- **System packages:**

  Ubuntu/Debian:
  ```bash
  sudo apt-get install -y iproute2 iptables nftables cloud-image-utils qemu-img e2fsprogs kmod
  ```
  Arch Linux:
  ```bash
  sudo pacman -S --needed iproute2 iptables nftables cloud-utils qemu-img e2fsprogs kmod
  ```
- **Root access (one-time):** run `mvm init` once to create the `mvm` group and a sudoers drop-in; normal `mvm` commands require no `sudo` after that
- **Environment variables:** Configure runtime behavior via `MVM_*` variables. See [docs/REFERENCES.md](docs/REFERENCES.md#environment-variables) for the full list.

---

## Installation

### 1. Download prebuilt binary (recommended -- once a release is tagged)

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

The easiest way to get started is with the interactive setup wizard. `mvm init` handles host configuration, service binary extraction, and cache setup. System packages must still be installed separately (see [Prerequisites](#prerequisites) above). After that, download a kernel and OS image, create an SSH key, and create your first VM:

```bash
# Interactive setup -- guides you through everything
# Handles privilege escalation automatically when prompted
mvm init

# Download the Firecracker kernel
mvm kernel pull --type firecracker

# Download an OS image
mvm image pull ubuntu:24.04

# Create a key and set it as default in one step
mvm key create test --default

# Create and start a VM
mvm vm create myvm --image ubuntu:24.04

# Follow the boot log until SSH is ready (~30-60 s)
mvm logs myvm --follow

# SSH in
mvm ssh myvm

# List running VMs
mvm vm ls

# Remove the VM when done
mvm vm rm myvm
```

---

## Essential Commands

> **Shortcuts:** `net` is an alias for `network`, `img` for `image`, and `vol` for `volume`.
> E.g. `mvm net ls`, `mvm img pull ubuntu:24.04`, `mvm vol ls`.

### VM Lifecycle

```bash
mvm vm create myvm --image ubuntu:24.04                 # Create and start a VM
mvm vm create myvm --image ubuntu:24.04 --nested-virt --cpu-template ./t2.json --volume data  # VM with nested virt, CPU template, and volume
mvm vm create myvm --image ubuntu:24.04 --skip-deblob --skip-cleanup  # VM with debug flags (skip optimization / cleanup on failure)
mvm vm create cluster --count 3 --atomic                # Batch-create 3 VMs
mvm vm ls                                     # List all VMs
mvm vm ps                                     # List running VMs (active processes)
mvm ssh myvm                                  # SSH into a VM
mvm console myvm                              # Console access (no SSH)
mvm cp ./file.txt myvm:/root/                 # Copy files to/from a VM
mvm vm rm myvm -f                             # Remove a VM
```

Also includes: `mvm vm start`, `stop`, `reboot`, `pause`, `resume`, `snapshot`, `load`, `inspect`, `export`, `import`, `attach-volume`, `detach-volume`.

### Network Management

```bash
mvm network ls                                    # List all networks
mvm network create my-net --subnet 192.168.100.0/24  # Create a named network
mvm network rm my-net                             # Remove a network
mvm network default my-net                        # Set as default for VM creation
mvm network inspect my-net                        # Inspect network details
```

### Resource Management

```bash
mvm volume create data 10G                   # Create persistent data disk
mvm volume create data 10G --read-only       # Create read-only persistent data disk
mvm volume ls                                # List volumes
mvm volume inspect data                      # Inspect volume details
mvm volume resize data 20G                   # Resize a volume
mvm image pull ubuntu:24.04                 # Download an OS image
mvm image ls                                # List available images
mvm image inspect ubuntu:24.04              # Inspect image details
mvm image import ./myimage.qcow2            # Import local image file
mvm image warm ubuntu:24.04                 # Pre-decompress image to ready pool
mvm kernel pull --type firecracker           # Download Firecracker kernel
mvm kernel pull official:6.19.9 --features kvm,nftables --config ./my-fragment.config  # Build official kernel with features
mvm kernel pull official:6.19.9 --jobs 4 --keep-build-dir --clean-build  # Official kernel build with parallel jobs
mvm kernel inspect <kernel>                  # Inspect kernel details
mvm kernel import ./vmlinux                  # Register a vmlinux file
mvm bin pull firecracker --version 1.15.0               # Download Firecracker + jailer binaries
mvm bin pull firecracker --git-ref my-branch             # Build from source at a git ref
mvm bin default firecracker                              # Set default binary
mvm key create mykey --default              # Generate SSH key
mvm key add mykey ./id_ed25519.pub          # Add existing public key
mvm key inspect mykey                       # Inspect key details
mvm key export mykey ./backup               # Export keypair to directory
```

### System Setup

```bash
mvm host init    # One-time host setup (KVM, networking)
mvm host info    # Show host hardware, limits, VM capacity projection
mvm host status  # Show current host configuration state
mvm host clean   # Remove networking config
mvm host reset   # Full rollback of all host changes
mvm cache init   # Initialize all cache resources
mvm cache prune  # Clean up stale cache
mvm cache clean  # Nuclear option for cache cleanup
```

### Configuration

```bash
mvm config get defaults.vm vcpu_count             # Get a config value
mvm config set defaults.vm vcpu_count 4           # Set a config value
mvm config reset defaults.vm vcpu_count           # Reset a config value to default
mvm config list                                   # List all overridable settings
```

See [docs/REFERENCES.md](docs/REFERENCES.md) for the complete command reference with all flags and options.

---

## Documentation

Comprehensive documentation is available in the `docs/` directory:

| Document | Description |
|----------|-------------|
| [docs/REFERENCES.md](docs/REFERENCES.md) | **Complete command reference** -- all `mvm` commands, flags, and options<br>**Configuration** -- config files, environment variables, cache structure<br>**Cloud-Init** -- nocloud-net setup, security, modes |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Common issues and solutions<br>Debug mode, permission fixes, network issues |
| [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md) | System dependencies by category<br>Package names for Debian/Ubuntu/Arch |
| [docs/KERNEL.md](docs/KERNEL.md) | Building kernels for Firecracker (CI and official) |
| [docs/RELEASE.md](docs/RELEASE.md) | Release process and distribution packages |
| [docs/API.md](docs/API.md) | Python API reference for programmatic usage |
| [docs/ASSETS_CONFIGURATIONS.md](docs/ASSETS_CONFIGURATIONS.md) | Bundled asset configurations: image specs, kernel specs, and runtime defaults |
| [docs/PROJECT_ARCHITECTURE.md](docs/PROJECT_ARCHITECTURE.md) | Internal architecture: layers, domains, patterns, and conventions |
| [docs/RUNTIME.md](docs/RUNTIME.md) | Runtime internals: provisioning backends, service architecture, and firewall backend |
---

## Building from Source

Produces a standalone single-file binary -- no Python runtime required on the target machine:

### Standard Build

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
uv sync --group dev --group build
python scripts/build_services.py      # Build everything (default mode)
# Output: dist/mvm
./dist/mvm --version
sudo cp dist/mvm ~/.local/bin/mvm         # Install to PATH (required for sudo support)
```

See [docs/RELEASE.md](docs/RELEASE.md) for detailed build instructions.

---

## Troubleshooting

Common issues and quick fixes:

| Issue | Solution |
|-------|----------|
| **Permission denied: /dev/kvm** | If missing: `sudo modprobe kvm kvm_intel`. If unreadable: `sudo usermod -aG kvm $USER` then log out/back in |
| **Bridge not found** | Run `mvm host init` once |
| **VM won't boot / SSH times out** | Cloud-init takes 30-60s on first boot. Watch with `mvm logs myvm --follow` |
| **Kernel not found** | `mvm kernel pull` |
| **Image not found** | `mvm image pull ubuntu:24.04` |
| **nocloud-net server failed** | Port range exhausted. Check: `sudo ss -tlnp \| grep -E ':(8[0-9]{3}\|9[0-9]{3})'` |

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

MIT -- see [LICENSE](LICENSE).
