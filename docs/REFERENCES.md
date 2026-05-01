# Command Reference & Configuration

This document provides detailed reference for all `mvm` commands, configuration options, and environment variables.

---

## Table of Contents

- [Command Reference](#command-reference)
  - [mvm init](#mvm-init) — First-time setup wizard
  - [mvm host](#mvm-host) — Host configuration
  - [mvm kernel](#mvm-kernel) — Kernel management
  - [mvm image](#mvm-image) — Image management
  - [mvm bin](#mvm-bin) — Binary management
  - [mvm vm](#mvm-vm) — VM lifecycle
  - [mvm console](#mvm-console) — VM console access
  - [mvm network](#mvm-network) — Network management
  - [mvm key](#mvm-key) — SSH key management
  - [mvm config](#mvm-config) — Configuration management
  - [mvm cache](#mvm-cache) — Cache management
  - [mvm logs](#mvm-logs) — VM logs
  - [mvm ssh](#mvm-ssh) — VM SSH access
- [Configuration](#configuration)
- [Cloud-Init](#cloud-init)
- [Environment Variables](#environment-variables)
- [Cache Directory Structure](#cache-directory-structure)

---

## Command Reference

### `mvm init`

First-time setup wizard. Walks through host init, binary/kernel/image download, and SSH key setup in one command.

| Flag | Description | Default |
|------|-------------|---------|
| `--non-interactive` | Use defaults, skip all prompts | false |
| `--skip-host` | Skip host init step | false |

---

### `mvm host`

Host configuration. One-time, machine-global setup.

| Command | Description |
|---------|-------------|
| `mvm host init` | Apply host config (KVM, modules, ip_forward, mvm group, sudoers). Idempotent. |
| `mvm host ls` | Show current host configuration state |
| `mvm host clean` | Remove networking config (bridges, TAPs, iptables). Does not touch sysctl/group. |
| `mvm host reset` | Full rollback: networking + sysctl + sudoers + group removal. |

---

### `mvm kernel`

Kernel management.

| Command | Description |
|---------|-------------|
| `mvm kernel ls` | List cached kernels |
| `mvm kernel fetch` | Download or build a kernel (official or Firecracker-optimized) |
| `mvm kernel set-default` | Set a kernel as the default for VM creation |
| `mvm kernel rm` | Remove a cached kernel |

**`fetch` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--type` | `firecracker` or `official` **(required)** | — |
| `--version VERSION` | Kernel version | (latest) |
| `--arch` | Architecture (`x86_64`, `arm64`) | auto-detect |
| `--set-default` | Set as default after fetch | false |
| `--jobs N` | Parallel build jobs (official only) | auto |
| `--keep-build-dir` | Keep build directory (official only) | false |
| `--clean-build` | Bypass cache and force clean build (official only) | false |
| `--config PATH` | Custom kernel config fragment file | — |

---

### `mvm image`

Image management.

| Command | Description |
|---------|-------------|
| `mvm image ls` | List available and cached images |
| `mvm image fetch ID` | Download an image by its ID |
| `mvm image import NAME PATH` | Import a local image file with a display name |
| `mvm image set-default` | Set the default image for VM creation |
| `mvm image rm ID` | Remove a cached image |
| `mvm image warm IMAGE` | Pre-decompress image for fast VM creation |

**Supported image IDs:**

| ID | Description |
|----|-------------|
| `ubuntu-24.04` | Ubuntu 24.04 LTS (Noble) |
| `ubuntu-22.04` | Ubuntu 22.04 LTS (Jammy) |
| `archlinux` | Arch Linux cloud image |
| `debian-bookworm` | Debian 12 (Bookworm) |
| `alpine-3.20` | Alpine Linux 3.20 |

**`fetch` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--force, -f` | Re-download even if cached | false |

---

### `mvm bin`

Firecracker binary management.

| Command | Description |
|---------|-------------|
| `mvm bin ls` | List local Firecracker versions |
| `mvm bin fetch VERSION` | Download a specific Firecracker release |
| `mvm bin default` | Set the active Firecracker version |
| `mvm bin rm VERSION` | Remove a cached version |

---

### `mvm vm`

VM lifecycle management.

| Command | Description |
|---------|-------------|
| `mvm vm create` | Create and start a new VM |
| `mvm vm start` | Start a stopped VM |
| `mvm vm stop` | Stop a running VM |
| `mvm vm reboot` | Reboot a VM |
| `mvm vm pause` | Pause a running VM |
| `mvm vm resume` | Resume a paused VM |
| `mvm vm rm` | Stop and remove a VM |
| `mvm vm ls` | List all VMs |
| `mvm vm ps` | List running/starting VMs |
| `mvm vm inspect` | Show detailed VM information |
| `mvm vm snapshot` | Snapshot a running VM |
| `mvm vm load` | Load a VM from a snapshot |
| `mvm vm export` | Export a VM config to portable JSON |
| `mvm vm import` | Create a VM from a portable config file |

**`vm create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name **(required)** | — |
| `--image IMAGE` | Image name, short ID, or path to .ext4 file | auto-detected |
| `--image-path PATH` | Direct path to rootfs image file (overrides `--image`) | — |
| `--kernel KERNEL` | Kernel short ID or path to vmlinux | auto-detected |
| `--kernel-path PATH` | Direct path to vmlinux file (overrides `--kernel`) | — |
| `--vcpus, --cpus N` | vCPU count | from config |
| `--mem, --memory N` | Memory in MiB | from config |
| `--disk-size, -s SIZE` | Disk size (e.g., `512M`, `1G`) | from config |
| `--ip ADDRESS` | Guest IP | auto-assigned |
| `--mac ADDRESS` | Custom MAC address | auto-generated |
| `--network, --net NAME` | Named network | from config |
| `--ssh-key NAME_OR_PATH` | SSH public key (name from cache or file path) | default keys |
| `--user USER` | Default SSH user | from config |
| `--cloud-init-mode MODE` | `inject`, `iso`, `net`, `off` | `inject` |
| `--nocloud-net-port N` | Port for nocloud-net HTTP server (0=auto) | auto-assign |
| `--user-data PATH` | Path to custom cloud-init user-data file | — |
| `--enable-pci/--no-enable-pci` | Enable PCI device support | from config |
| `--enable-logging/--no-enable-logging` | Enable Firecracker logging | from config |
| `--enable-metrics/--no-enable-metrics` | Enable Firecracker metrics | from config |
| `--lsm-flags FLAGS` | Linux Security Module kernel cmdline flags | from config |
| `--no-console` | Disable serial console | false |
| `--firecracker-bin PATH` | Path to firecracker binary | active version |
| `--skip-cleanup` | Keep resources on failure for debugging | false |

---

### `mvm console`

VM console access without SSH. Uses a PTY-over-vsock relay.

| Command | Description |
|---------|-------------|
| `mvm console [IDENTIFIER]` | Attach to a VM console interactively |
| `mvm console [IDENTIFIER] --state` | Show console state without attaching |
| `mvm console [IDENTIFIER] --kill` | Kill the console relay for a VM |

| Flag | Description |
|------|-------------|
| `[IDENTIFIER]` | VM name, ID prefix, IP, or MAC address (positional) |
| `--name, -n NAME` | VM name |
| `--ip IP` | VM guest IP address |
| `--mac MAC` | VM guest MAC address |
| `--state` | Show console relay state without attaching |
| `--kill` | Kill the console relay process |

---

### `mvm network`

Named network management.

| Command | Description |
|---------|-------------|
| `mvm network create NAME` | Create a named bridge network |
| `mvm network rm NAME` | Remove a named network |
| `mvm network ls` | List all networks |
| `mvm network inspect NAME` | Show network details and IP leases |
| `mvm network set-default NAME` | Set a network as the default for VM creation |
| `mvm network sync [IDENTIFIER]` | Sync iptables rules between database and host |

---

### `mvm key`

SSH key management.

| Command | Description |
|---------|-------------|
| `mvm key ls` | List cached keys |
| `mvm key add NAME PATH` | Import an existing public key |
| `mvm key create NAME` | Generate a new ED25519 keypair |
| `mvm key rm NAME` | Remove a key from the cache |
| `mvm key inspect NAME` | Show fingerprint and public key content |
| `mvm key set-default KEY1 [KEY2...]` | Set default keys for new VMs |
| `mvm key export NAME` | Export a key to ~/.ssh |

---

### `mvm config`

Configuration management.

| Command | Description |
|---------|-------------|
| `mvm config get CATEGORY [KEY]` | Get a configuration value |
| `mvm config set CATEGORY KEY VALUE` | Set a configuration value |
| `mvm config list` | List all overridable settings and current values |
| `mvm config reset [CATEGORY] [KEY] [--all]` | Reset overrides to defaults |

---

### `mvm cache`

Cache management.

| Command | Description |
|---------|-------------|
| `mvm cache init` | Initialize cache directories |
| `mvm cache prune [RESOURCE]` | Prune cache entries (vm, network, image, kernel, binary, misc) |
| `mvm cache clean` | Complete cache teardown: prune all, host clean, remove cache dir |

**`cache prune` flags:**

| Flag | Description |
|------|-------------|
| `--all, -a` | Remove ALL items including running VMs, default network, protected assets |
| `--dry-run` | Show what would be removed without actually removing |
| `--force, -f` | Skip confirmation |

**`cache clean` flags:**

| Flag | Description |
|------|-------------|
| `--dry-run` | Show what would be removed |
| `--force, -f` | Skip confirmation |

---

### `mvm logs`

VM log management.

| Command | Description |
|---------|-------------|
| `mvm logs IDENTIFIER` | View boot/serial console logs (default) |
| `mvm logs IDENTIFIER --os` | View Firecracker process logs |
| `mvm logs IDENTIFIER --lines N` | View last N lines |
| `mvm logs IDENTIFIER --follow` | Stream logs in real-time |

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID prefix, IP, or MAC address (positional) |
| `--os` | Show Firecracker OS log instead of boot log |
| `--lines, -n N` | Number of log lines to show |
| `--follow, -f` | Follow log output in real-time |

---

### `mvm ssh`

VM SSH access.

| Command | Description |
|---------|-------------|
| `mvm ssh IDENTIFIER` | Open an SSH session into a VM |
| `mvm ssh IDENTIFIER --user USER` | SSH with specific user |
| `mvm ssh IDENTIFIER --cmd CMD` | Execute a command via SSH |
| `mvm ssh IDENTIFIER --key PATH` | Use specific private key file |

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID prefix, IP, or MAC address (positional) |
| `--user, -u USER` | SSH user |
| `--key PATH` | SSH private key file or directory of keys |
| `--cmd, -c CMD` | Command to execute |
| `--ip IP` | IP address to connect to (skips validation) |
| `--mac MAC` | VM MAC address |
| `--name, -n NAME` | VM name |

---

## Configuration

`mvm` stores runtime configuration at `~/.config/mvmctl/config.json` (overridable with `MVM_CONFIG_DIR`) and asset/default state in `~/.cache/mvmctl/metadata.json` (overridable with `MVM_CACHE_DIR`).

### Configuration Priority (lowest → highest)

1. Built-in fallbacks (`constants.py`)
2. Runtime state files (`~/.config/mvmctl/config.json`)
3. `MVM_*` environment variables
4. CLI flags

### Example config.json

```json
{
  "assets": {
    "kernels_dir": "/home/user/.cache/mvmctl/kernels",
    "images_dir": "/home/user/.cache/mvmctl/images",
    "bin_dir": "/home/user/.cache/mvmctl/bin"
  }
}
```

### Asset Defaults in metadata.json

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
      "binary_path": "/home/user/.cache/mvmctl/bin/firecracker-v1.15.0",
      "is_default": 1
    }
  }
}
```

---

## Cloud-Init

`mvm` uses **nocloud-net** as the default method for delivering cloud-init configuration to VMs.

### How It Works

1. **HTTP Server**: A temporary HTTP server is started on the host (port range 8000-9000)
2. **Firewall Rules**: iptables rules allow the VM to reach the server
3. **Kernel Command Line**: The VM boots with `ds=nocloud-net;s=http://GATEWAY_IP:PORT/`
4. **Configuration Delivery**: cloud-init fetches `meta-data`, `user-data`, and `network-config` via HTTP
5. **Automatic Cleanup**: The HTTP server stops when the VM is removed

### Cloud-Init Modes

| Mode | Flag | Description |
|------|------|-------------|
| **inject (default)** | `--cloud-init-mode inject` | Direct injection into rootfs using libguestfs |
| **net** | `--cloud-init-mode net` | Serves cloud-init files via HTTP (nocloud-net) |
| **iso** | `--cloud-init-mode iso` | Uses a pre-existing ISO file |
| **off** | `--cloud-init-mode off` | Skips cloud-init entirely |

### Security Architecture

- **Per-VM Isolation**: Each VM gets its own HTTP server on a unique port
- **Source-Based Firewall**: Only the VM's IP can reach its nocloud server
- **Gateway Binding**: HTTP servers bind to the bridge gateway IP, not `0.0.0.0`
- **Rule Comments**: Firewall rules are tagged with `# mvm-nocloud:<vm_name>:<port>`

### Benefits Over ISO Mode

| Feature | nocloud-net | ISO Mode |
|---------|-------------|----------|
| Boot speed | Faster (no ISO generation) | Slower (genisoimage) |
| Portability | Works with any image | Requires CD-ROM drive |
| Cleanup | Automatic | Manual |

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CACHE_DIR` | Override cache directory | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override config directory | `~/.config/mvmctl` |
| `MVM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARNING`, `ERROR`) | `INFO` |
| `MVM_KERNEL` | Override default kernel path | (from metadata) |
| `MVM_FIRECRACKER_BIN` | Override Firecracker binary path | (from metadata) |

---

## Cache Directory Structure

```
~/.cache/mvmctl/
├── bin/               # Firecracker + jailer binaries
├── kernels/           # vmlinux kernel images
├── images/            # Root filesystem images (.ext4)
├── keys/              # Cached SSH public keys
├── networks/          # Per-network config + IP leases
├── vms/               # Per-VM state
│   └── <vm-sha>/      # VM state by full SHA256 hash
│       ├── rootfs.ext4
│       ├── firecracker.json
│       ├── firecracker.log
│       ├── firecracker.console.log
│       ├── firecracker.pid
│       ├── firecracker.sock
│       └── cloud-init/
├── metadata.json      # Asset registry
└── audit.log          # Append-only operation log
```

---

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) for common issues and solutions.
