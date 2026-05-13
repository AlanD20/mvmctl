# mvmctl (mvm)

> **Container speed, VM isolation.**

[![CI](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml/badge.svg)](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Python 3.13+](https://img.shields.io/badge/python-3.13%2B-blue)](https://www.python.org/downloads/)

**mvmctl** is the modern way to run microVMs -- get the startup speed of containers with the security and isolation of traditional VMs. Built for developers who need lightweight, fast-booting virtual machines without the overhead.

## Why mvmctl?

- **Blazing fast** -- VMs boot in milliseconds, not minutes
- **Powered by Firecracker** -- AWS's battle-tested microVM technology, the engine behind Lambda and Fargate
- **Secure by default** -- Hardware-level isolation with KVM
- **Works with your images** -- Ubuntu, Debian, Arch, Alpine, and more
- **Simple CLI** -- One command to create, start, and SSH into a VM
- **Console access** -- Interactive serial console without SSH (via `mvm console`)
- **Pre-production** -- Still under active development.

```bash
# Create and SSH into a VM in under 60 seconds
mvm vm create --name myvm --image ubuntu-24.04
mvm ssh myvm
```

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Documentation](#documentation)
- [Core Architecture](#core-architecture)
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
- **Python 3.13+**
- **System packages:**

  Ubuntu/Debian:
  ```bash
  sudo apt-get install -y iproute2 iptables cloud-image-utils qemu-img e2fsprogs
  ```
  Arch Linux:
  ```bash
  sudo pacman -S --needed iproute2 iptables cloud-utils qemu-img e2fsprogs
  ```
- **Root access (one-time):** run `mvm init` once to create the `mvm` group and a sudoers drop-in; normal `mvm` commands require no `sudo` after that. See the `PRIVILEGED_BINARIES` mapping in `src/mvmctl/constants.py` for the list of binaries requiring escalated privileges
- **Execution pattern:** Use `sg mvm -c 'mvm ...'` when running as a non-root member of the `mvm` group (see [AGENTS.md](AGENTS.md))
- **Environment variables:** Configure runtime behavior via `MVM_*` variables -- e.g. `MVM_DB_FILENAME`, `MVM_UNIX_GROUP`, `MVM_FORWARD_CHAIN`. See [docs/REFERENCES.md](docs/REFERENCES.md#environment-variables) for the full list.

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

The easiest way to get started is with the interactive setup wizard. `mvm init` is the **only prerequisite** -- it handles database setup, host configuration (sudoers, mvm group), service binary extraction, and optional Firecracker download:

```bash
# Interactive setup -- guides you through everything (DB, sudoers, binaries, downloads)
# Handles privilege escalation automatically when prompted
mvm init
# Log out and back in when prompted, or run: newgrp mvm
#   (the wizard shows this message when the mvm group was created
#    but your current session hasn't picked it up yet)

# Run mvm host init separately if you skipped it during the wizard:
#   sudo mvm host init

# Create a key
mvm key create test
mvm key default test

# Create and start a VM
mvm vm create --name myvm --image ubuntu-24.04

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

### VM Lifecycle

```bash
mvm vm create --name myvm --image ubuntu-24.04   # Create and start a VM
mvm vm create --name cluster --count 3 --atomic   # Batch-create 3 VMs
mvm vm ls                                         # List all VMs
mvm ssh myvm                                      # SSH into a VM
mvm console myvm                                  # Console access (no SSH)
mvm vm rm myvm --force                            # Remove a VM
```

### Resource Management

```bash
mvm volume create data 10G          # Create persistent data disk
mvm volume ls                       # List volumes
mvm image pull ubuntu-24.04        # Download an OS image
mvm image ls                       # List available images
mvm kernel pull --type firecracker  # Download Firecracker kernel
mvm bin pull 1.15.0                # Download Firecracker + jailer binaries
mvm key create mykey               # Generate SSH key
```

### System Setup

```bash
mvm host init    # One-time host setup (KVM, networking)
mvm cache prune  # Clean up stale cache
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
| [docs/custom-kernel.md](docs/custom-kernel.md) | Building custom kernels for Firecracker |
| [docs/RELEASE.md](docs/RELEASE.md) | Release process and distribution packages |
| [docs/API.md](docs/API.md) | Python API reference for programmatic usage |

---

## Building from Source

Produces a standalone single-file binary -- no Python runtime required on the target machine:

### Standard Build

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
uv sync --group dev --group build
python scripts/build_services.py      # Build everything (fast mode)
# Or just the main binary:
python scripts/build_services.py --mvm
# Output: dist/mvm
./dist/mvm --version
```

See [docs/RELEASE.md](docs/RELEASE.md) for detailed build instructions.

---

## Cache Directory Structure

```
~/.cache/mvmctl/
├── bin/               # Firecracker + jailer binaries + service binaries (mvm-console-relay, mvm-nocloud-server, mvm-provision)
├── kernels/           # vmlinux kernel images
├── images/            # Root filesystem images (.ext4, .btrfs, .zst)
├── keys/              # Cached SSH public keys
├── volumes/           # Persistent data disks (raw / qcow2)
├── networks/          # Per-network config + IP leases
├── vms/               # Per-VM state
│   └── <vm-sha>/      # VM directories named by SHA256 hash
│       ├── rootfs.ext4
│       ├── firecracker.json
│       ├── firecracker.log
│       ├── firecracker.console.log
│       ├── firecracker.pid
│       └── cloud-init/
├── mvmdb.db           # SQLite database (canonical asset state)
└── audit.log          # Append-only operation log
```

---

## Core Architecture

### Three-Layer Design

```
CLI (argument parsing + Rich output)     -- imports from api only
  |
  v
API (orchestration + privilege checks)   -- sequences multiple core domains
  |
  v
Core (isolated business logic)           -- no cross-domain imports
```

- **CLI** (`src/mvmctl/cli/`): Thin command definitions using a custom Click group (`LazyMVMGroup`) at root with Typer sub-apps. Resolves runtime defaults from `constants.py`. Calls the API layer only. No business logic, no DB queries. Startup under 150ms via lazy-loading.
- **API** (`src/mvmctl/api/`): Public Python surface. Performs privilege escalation, resolves DB-backed defaults, and orchestrates **multiple core domains**. The ONLY layer allowed to import from multiple domains.
- **Core** (`src/mvmctl/core/`): Isolated domain logic -- each domain is self-contained with Controller, Service, Repository, and Resolver files. **No cross-domain imports**. Returns `*Item` model objects only.

### Domain Structure

14 core domains, each following the same 4-file pattern:

| Domain | Purpose |
|--------|---------|
| `vm/` | VM lifecycle (create, start, stop, snapshot) |
| `image/` | Root filesystem image management |
| `kernel/` | Kernel image management |
| `binary/` | Firecracker + jailer binary management |
| `network/` | Bridge/TAP interfaces and NAT rules |
| `volume/` | Persistent data disks |
| `key/` | SSH key generation and management |
| `host/` | Host-level configuration (sudoers, groups) |
| `config/` | User configuration management |
| `cache/` | Cache directory lifecycle and pruning |
| `console/` | Serial console relay management |
| `logs/` | VM log streaming |
| `ssh/` | SSH session management |
| `cloudinit/` | Cloud-init metadata generation |

Each domain has:
- **Controller** -- Stateful, bound to a single entity (start/stop/pause/resume -- no `create()` or `remove()`)
- **Service** -- Stateless infrastructure + intra-domain orchestration (does NOT validate caller input)
- **Repository** -- All SQLite DB queries (SQL-level computation: `COUNT(*)`, `WHERE IN (...)` -- no fetch-all in Python)
- **Resolver** -- Entity resolution by name/ID/IP/MAC with relation enrichment

### Shared Infrastructure (`core/_shared/`)

- **Database**: SQLite (`mvmdb.db`) at `~/.cache/mvmctl/` -- canonical asset state. Schema managed via `db/migrations/`.
- **Provisioning backends**: Two paths for root filesystem provisioning:
  - **LoopMount** (primary, ~200ms per VM) -- Standalone compiled `mvm-provision` binary extracted at `mvm init`. Uses `losetup`/`mount`/`chroot` directly.
  - **GuestFS** (fallback, disabled by default) -- Uses `libguestfs` Python bindings (~2600-3000ms per VM). Enabled during `mvm init`.
- **iptables tracker**: Tracks NAT/forwarding rules for clean teardown.
- **Resolver registry**: Central registry for entity resolvers.

### Build System

Standalone binaries via **Nuitka** multidist compilation (`scripts/build_services.py`):

```bash
python scripts/build_services.py              # Build everything (release mode)
python scripts/build_services.py --mvm        # Main CLI binary only
python scripts/build_services.py --services   # Service binaries only
python scripts/build_services.py --fast       # Fast compile (no optimization)
```

Outputs:
- `dist/mvm` -- Main CLI (single-file, no Python runtime required)
- `dist/mvm-services` -- Combined binary (console relay, nocloud server, loopmount provisioner -- dispatched via symlink in `argv[0]`)

---

## Troubleshooting

Common issues and quick fixes:

| Issue | Solution |
|-------|----------|
| **Permission denied: /dev/kvm** | If missing: `sudo modprobe kvm kvm_intel`. If unreadable: `sudo usermod -aG kvm $USER` then log out/back in |
| **Bridge not found** | Run `mvm host init` once |
| **VM won't boot / SSH times out** | Cloud-init takes 30-60s on first boot. Watch with `mvm logs myvm --follow` |
| **Kernel not found** | `mvm kernel pull` |
| **Image not found** | `mvm image pull ubuntu-24.04` |
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

MIT -- see [LICENSE](LICENSE).
