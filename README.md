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
- [Cloud-Init](#cloud-init)
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
| `mvm kernel fetch` | Download or build a kernel (official or Firecracker-optimized) |
| `mvm kernel set-default` | Set a kernel as the default for VM creation |
| `mvm kernel rm` | Remove a cached kernel |

**`fetch` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--type` | `firecracker` or `official` | `firecracker` |
| `--version VERSION` | Kernel version | (latest) |
| `--name NAME` | Override the base name of the output file | `vmlinux` or `vmlinux-fc` |
| `--clean-build` | Bypass cache and force a clean kernel build | false |

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
| `--cloud-init-iso PATH` | Path to custom cloud-init ISO file | — |
| `--nocloud-net` | Use nocloud-net HTTP datasource (default: auto) | false |
| `--nocloud-net-port PORT` | Port for nocloud-net HTTP server (0=auto) | 0 (auto) |
| `--no-cloud-init` | Disable cloud-init entirely | false |
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

`mvm` stores runtime configuration at `~/.config/mvmctl/config.json` (overridable with
`MVM_CONFIG_DIR`) and asset/default state in `~/.cache/mvmctl/metadata.json`
(overridable with `MVM_CACHE_DIR`).

```json
{
  "assets": {
    "kernels_dir": "/home/user/.cache/mvmctl/kernels",
    "images_dir": "/home/user/.cache/mvmctl/images",
    "bin_dir": "/home/user/.cache/mvmctl/bin"
  }
}
```

`config.json` is managed by `mvm configure` and `mvm config set`.

Asset defaults are stored in `metadata.json` with `is_default` markers:

```json
{
  "images": {
    "<image-full-id>": {
      "internal_id": "ubuntu-24.04",
      "filename": "ubuntu-24.04.ext4",
      "is_default": 1
    }
  },
  "kernels": {
    "<kernel-full-id>": {
      "filename": "vmlinux-fc-v1.15-x86_64",
      "is_default": 1
    }
  },
  "binaries": {
    "firecracker": {
      "binary_name": "firecracker",
      "binary_path": "/home/user/.cache/mvmctl/bin/firecracker-v1.15.0",
      "full_version": "v1.15.0",
      "ci_version": "v1.15",
      "default_binary_path": "/home/user/.cache/mvmctl/bin/firecracker",
      "is_default": 1
    },
    "jailer": {
      "binary_name": "jailer",
      "binary_path": "/home/user/.cache/mvmctl/bin/jailer-v1.15.0",
      "full_version": "v1.15.0",
      "ci_version": "v1.15",
      "default_binary_path": "/home/user/.cache/mvmctl/bin/jailer",
      "is_default": 1
    }
  }
}
```

**Priority (lowest → highest):**
1. Built-in fallbacks (`constants.py`)
2. Runtime state files (`~/.config/mvmctl/config.json` for general config,
   `~/.cache/mvmctl/metadata.json` for image/kernel/binary defaults)
3. `MVM_*` environment variables
4. CLI flags

---

## Cloud-Init

`mvm` uses **nocloud-net** as the default method for delivering cloud-init configuration to VMs.
This replaces the older ISO-based approach and offers several benefits.

### How It Works

When you create a VM with cloud-init enabled (the default):

1. **HTTP Server**: A temporary HTTP server is started on the host (port range 8000-9000)
2. **Firewall Rules**: iptables rules in the `MVM-NOCLOUD-INPUT` chain allow the VM to reach the server
3. **Kernel Command Line**: The VM boots with `ds=nocloud-net;s=http://GATEWAY_IP:PORT/`
4. **Configuration Delivery**: cloud-init inside the VM fetches `meta-data`, `user-data`, and `network-config` via HTTP
5. **Automatic Cleanup**: The HTTP server is stopped when the VM is removed

### Cloud-Init Modes

| Mode | Flag | Description |
|------|------|-------------|
| **nocloud-net (default)** | `--nocloud-net` or auto | Serves cloud-init files via HTTP server |
| **ISO** | `--cloud-init-iso PATH` | Uses a pre-existing ISO file |
| **Disabled** | `--no-cloud-init` | Skips cloud-init entirely |

**Example: Force ISO mode**
```bash
mvm vm create --name myvm --image ubuntu-24.04 --cloud-init-iso /path/to/cloud-init.iso
```

**Example: Explicit nocloud-net mode**
```bash
mvm vm create --name myvm --image ubuntu-24.04 --nocloud-net
```

### Security Architecture

- **Per-VM Isolation**: Each VM gets its own HTTP server on a unique port
- **Source-Based Firewall**: Only the VM's IP can reach its nocloud server (via `MVM-NOCLOUD-INPUT` chain)
- **Gateway Binding**: HTTP servers bind to the bridge gateway IP, not `0.0.0.0`
- **Rule Comments**: Firewall rules are tagged with `# mvm-nocloud:<vm_name>:<port>` for auditability

### Port Allocation

Ports are allocated from the range **8000-9000** with automatic collision detection:

- If port 8000 is in use, the system tries 8001, 8002, etc.
- Up to 100 retries are attempted before failing
- Each VM's port is tracked and released when the VM stops

### Benefits Over ISO Mode

| Feature | nocloud-net | ISO Mode |
|---------|-------------|----------|
| Boot speed | Faster (no ISO generation) | Slower (genisoimage) |
| Portability | Works with any image | Requires CD-ROM drive |
| Cleanup | Automatic | Manual (if using `--keep-cloud-init-iso`) |
| Debugging | Check logs for URL | Mount ISO to inspect |

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CACHE_DIR` | Override cache directory | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override config directory | `~/.config/mvmctl` |
| `MVM_KERNEL` | Override default kernel path | (from metadata default / runtime state) |
| `MVM_FIRECRACKER_BIN` | Override Firecracker binary path | (from metadata default / runtime state) |

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
