# mvmctl (mvm)

A production-grade Python CLI for managing [Firecracker](https://firecracker-microvm.github.io/) microVMs on Linux.

[![CI](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml/badge.svg)](https://github.com/AlanD20/mvmctl/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Python 3.13+](https://img.shields.io/badge/python-3.13%2B-blue)](https://www.python.org/downloads/)

`mvm` handles the full Firecracker VM lifecycle — downloading kernels and images, setting up bridge networking, creating and destroying VMs, SSH access, log streaming, snapshots, and cleanup. Built with Python 3.13, Typer, and Rich. Usable as a standalone binary, a pip package, or an importable Python library.

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Command Reference](#command-reference)
- [Configuration](#configuration)
- [Environment Variables](#environment-variables)
- [Building from Source](#building-from-source)
- [Cache Directory Structure](#cache-directory-structure)
- [Troubleshooting](#troubleshooting)
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
mvm vm logs --name myvm --type boot --follow

# 6. SSH in
mvm vm ssh --name myvm

# 7. List running VMs
mvm vm ls

# 8. Remove the VM
mvm vm rm --name myvm --force
```

Or run the interactive setup wizard which guides you through all of the above:

```bash
mvm configure
```

---

## Command Reference

### `mvm configure` — First-time setup wizard

Walks through host init, binary/kernel/image download, and SSH key setup in one command.

| Flag | Description | Default |
|------|-------------|---------|
| `--non-interactive` | Use defaults, skip all prompts | false |
| `--skip-host` | Skip the host init step | false |

---

### `mvm host` — Host configuration

One-time, machine-global setup for Firecracker. Pre-change state is snapshotted for full rollback.

| Command | Description |
|---------|-------------|
| `mvm host init` | Apply host config (KVM, modules, ip_forward, mvm group, sudoers). Idempotent. |
| `mvm host ls` | Show current host configuration state |
| `mvm host clean` | Remove networking config (bridges, TAPs, iptables). Does not touch sysctl/group. |
| `mvm host reset` | Full rollback: networking + sysctl + sudoers + group removal. |

---

### `mvm kernel` — Kernel management

| Command | Description |
|---------|-------------|
| `mvm kernel ls` | List cached kernels |
| `mvm kernel fetch` | Download a kernel (official or Firecracker-optimized) |
| `mvm kernel set-default` | Set a kernel as the default for VM creation |
| `mvm kernel rm` | Remove a cached kernel |

**`fetch` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--type` | `firecracker` or `official` | `firecracker` |
| `--version VERSION` | Kernel version | (latest) |

---

### `mvm image` — Image management

| Command | Description |
|---------|-------------|
| `mvm image ls` | List available and cached images |
| `mvm image fetch ID` | Download an image by its ID |
| `mvm image import PATH` | Import a local image file |
| `mvm image set-default` | Set the default image for VM creation |
| `mvm image rm ID` | Remove a cached image |

**Supported image IDs:**

| ID | Description |
|----|-------------|
| `ubuntu-24.04` | Ubuntu 24.04 LTS (Noble) — official cloud image |
| `ubuntu-22.04` | Ubuntu 22.04 LTS (Jammy) |
| `archlinux` | Arch Linux cloud image |
| `debian-bookworm` | Debian 12 (Bookworm) |

**`fetch` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--force, -f` | Re-download even if cached | false |

---

### `mvm bin` — Firecracker binary management

| Command | Description |
|---------|-------------|
| `mvm bin ls` | List local (and optionally remote) Firecracker versions |
| `mvm bin fetch VERSION` | Download a specific Firecracker release |
| `mvm bin set-default` | Set the active Firecracker version |
| `mvm bin rm VERSION` | Remove a cached version |

**`ls` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--remote, -r` | Include remote available versions | false |
| `--limit N` | Max remote versions to show | 5 |

---

### `mvm vm` — VM lifecycle

| Command | Description |
|---------|-------------|
| `mvm vm create` | Create and start a new VM |
| `mvm vm rm` | Stop and remove a VM |
| `mvm vm ls` | List VMs |
| `mvm vm ps` | List running VMs (alias for ls) |
| `mvm vm ssh` | SSH into a VM |
| `mvm vm logs` | View VM logs |
| `mvm vm prune` | Remove all stopped VMs |
| `mvm vm snapshot` | Snapshot a running VM |
| `mvm vm load` | Load a VM from a snapshot |

**`vm create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name **(required)** | — |
| `--image IMAGE` | Image ID or path to `.ext4` file **(required)** | — |
| `--kernel PATH` | Path to vmlinux | auto-detected |
| `--vcpus N` | vCPU count | 2 |
| `--mem N` | Memory in MiB | 2048 |
| `--ip ADDRESS` | Guest IP | auto-assigned |
| `--network, --net NAME` | Named network | `default` |
| `--mac ADDRESS` | Guest MAC | auto-generated |
| `--ssh-key NAME_OR_PATH` | SSH public key (cache name or file path) | auto-detected |
| `--user USER` | Default SSH user (cloud-init) | `root` |
| `--user-data PATH` | Custom cloud-init user-data file | — |
| `--import-config PATH` | Load all settings from a JSON config file | — |
| `--output-config PATH` | Write resolved config to a JSON file | — |
| `--enable-api-socket` | Expose Firecracker API socket | false |
| `--firecracker-bin PATH` | Path to `firecracker` binary | from config |

**`vm logs` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name NAME` | VM name **(required)** | — |
| `--type TYPE` | `boot` (serial console) or `os` (Firecracker process log) | `boot` |
| `--follow, -f` | Stream log output | false |
| `--lines N` | Last N lines | 50 |

---

### `mvm network` — Named network management

| Command | Description |
|---------|-------------|
| `mvm network create NAME` | Create a named bridge network |
| `mvm network rm NAME` | Remove a named network |
| `mvm network ls` | List all networks |
| `mvm network inspect NAME` | Show network details and IP leases |

---

### `mvm key` — SSH key management

| Command | Description |
|---------|-------------|
| `mvm key ls` | List cached keys |
| `mvm key add NAME PATH` | Import an existing public key |
| `mvm key create NAME` | Generate a new ED25519 keypair |
| `mvm key rm NAME` | Remove a key from the cache |
| `mvm key inspect NAME` | Show fingerprint and public key content |

---

### `mvm config` — Configuration management

| Command | Description |
|---------|-------------|
| `mvm config show` | Show resolved configuration |
| `mvm config validate` | Validate config file |
| `mvm config get KEY` | Get a configuration value |
| `mvm config set KEY VALUE` | Set a configuration value |
| `mvm config dump-vm NAME` | Print the Firecracker JSON boot config for a running VM |

---

### `mvm clear` — Clear asset cache

Remove all cached assets (binaries, kernels, images). Does **not** touch VMs.

| Flag | Description |
|------|-------------|
| `--force, -f` | Skip confirmation |

---

## Configuration

`mvm` stores user configuration at `~/.config/mvmctl/config.json` (overridable with `MVM_CONFIG_DIR`).

```json
{
  "firecracker": {
    "full_version": "v1.15.0",
    "ci_version": "v1.15",
    "default_binary_path": "/home/user/.cache/mvmctl/bin/firecracker-v1.15.0"
  },
  "assets": {
    "kernels_dir": "/home/user/.cache/mvmctl/kernels",
    "images_dir": "/home/user/.cache/mvmctl/images",
    "bin_dir": "/home/user/.cache/mvmctl/bin"
  },
  "defaults": {
    "image": "ubuntu-24.04",
    "kernel": "vmlinux-fc-v1.15-x86_64"
  }
}
```

This file is managed by `mvm configure` and `mvm config set`. Edit it manually only if you know what you're doing.

**Priority (lowest → highest):**
1. Built-in fallbacks (`constants.py`)
2. `~/.config/mvmctl/config.json`
3. `MVM_*` environment variables
4. CLI flags

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CACHE_DIR` | Override cache directory | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override config directory | `~/.config/mvmctl` |
| `MVM_KERNEL` | Override default kernel path | (from config) |
| `MVM_FIRECRACKER_BIN` | Override Firecracker binary path | (from config) |

---

## Building from Source

Produces a standalone single-file binary — no Python runtime required on the target machine:

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# Output: dist/mvm
./dist/mvm --version
```

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

---

## Contributing

Contributions are welcome — bug reports, feature requests, and pull requests.

### Development setup

```bash
git clone https://github.com/AlanD20/mvmctl
cd mvmctl
uv sync --group dev
```

### Running tests and linting

```bash
uv run pytest tests/ -x -q         # Tests (stops at first failure)
uv run ruff check src/              # Linter
uv run ruff format --check src/     # Format check
uv run mypy src/                    # Type checker (strict mode)
```

All four commands must pass before opening a PR — they are enforced by CI.

### Guidelines

- **Tests must not require root, KVM, or a real network.** Mock all subprocess calls.
- **Coverage gate:** 80% branch coverage minimum. Dropping coverage will fail CI.
- **Architecture layers:** `cli/` → `api/` → `core/` — no skipping layers. See [`AGENTS.md`](AGENTS.md) for the full architecture reference.
- **No hardcoded defaults** — use `FALLBACK_*` constants in `constants.py`.
- **Strict mypy** — no `type: ignore` suppressions.
- One feature or fix per PR; write a clear description of *why*, not just *what*.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contribution guide.

---

## License

MIT — see [LICENSE](LICENSE).
