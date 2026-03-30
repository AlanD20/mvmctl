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
  - [mvm clear](#mvm-clear) — Clear asset cache
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
| `--type` | `firecracker` or `official` | `firecracker` |
| `--version VERSION` | Kernel version | (latest) |
| `--clean-build` | Bypass cache and force a clean kernel build | false |

---

### `mvm image`

Image management.

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
| `mvm bin set-default` | Set the active Firecracker version |
| `mvm bin rm VERSION` | Remove a cached version |

---

### `mvm vm`

VM lifecycle management.

| Command | Description |
|---------|-------------|
| `mvm vm create` | Create and start a new VM |
| `mvm vm rm` | Stop and remove a VM |
| `mvm vm ls` | List VMs |
| `mvm vm inspect` | Show detailed VM information |
| `mvm vm prune` | Remove all stopped VMs |
| `mvm vm snapshot` | Snapshot a running VM |
| `mvm vm load` | Load a VM from a snapshot |

**`vm create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name **(required)** | — |
| `--image IMAGE` | Image ID or path **(required)** | — |
| `--kernel PATH` | Path to vmlinux | auto-detected |
| `--vcpus N` | vCPU count | 2 |
| `--mem N` | Memory in MiB | 2048 |
| `--disk-size SIZE` | Disk size (e.g., `10G`, `512M`) | auto |
| `--ip ADDRESS` | Guest IP | auto-assigned |
| `--network, --net NAME` | Named network | `default` |
| `--ssh-key NAME_OR_PATH` | SSH public key | default keys |
| `--user USER` | Default SSH user | `root` |
| `--cloud-init-mode MODE` | `auto`, `nocloud-net`, `iso`, `direct`, `disabled` | auto |
| `--no-cloud-init` | Disable cloud-init | false |

---

### `mvm console`

VM console access without SSH. Uses a PTY-over-vsock relay.

| Command | Description |
|---------|-------------|
| `mvm console` | Attach to a VM console interactively |
| `mvm console --state` | Show console state without attaching |
| `mvm console --kill` | Kill the console relay for a VM |

---

### `mvm network`

Named network management.

| Command | Description |
|---------|-------------|
| `mvm network create NAME` | Create a named bridge network |
| `mvm network rm NAME` | Remove a named network |
| `mvm network ls` | List all networks |
| `mvm network inspect NAME` | Show network details and IP leases |

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
| `mvm config show` | Show resolved configuration |
| `mvm config validate` | Validate config file |
| `mvm config get KEY` | Get a configuration value |
| `mvm config set KEY VALUE` | Set a configuration value |
| `mvm config dump-vm NAME` | Print the Firecracker JSON boot config |

---

### `mvm cache`

Cache management.

| Command | Description |
|---------|-------------|
| `mvm cache init` | Initialize cache directories |
| `mvm cache prune` | Prune stale cache entries |
| `mvm cache prune vm` | Prune only VMs |
| `mvm cache prune network` | Prune unused networks |
| `mvm cache prune image` | Prune unused images |
| `mvm cache prune kernel` | Prune unused kernels |

**`cache prune` flags:**

| Flag | Description |
|------|-------------|
| `--include-stopped` | Include stopped VMs in pruning |
| `--include-running` | Include running VMs (use with caution) |
| `--all, -a` | Prune everything with confirmation |
| `--dry-run` | Show what would be removed |
| `--force, -f` | Skip confirmation |

---

### `mvm logs`

VM log management.

| Command | Description |
|---------|-------------|
| `mvm logs --name NAME` | View logs for a VM |
| `mvm logs --name NAME --type boot` | View boot/serial console logs |
| `mvm logs --name NAME --type os` | View Firecracker process logs |
| `mvm logs --name NAME --follow` | Stream logs in real-time |

---

### `mvm ssh`

VM SSH access.

| Command | Description |
|---------|-------------|
| `mvm ssh --name NAME` | SSH into a VM by name |
| `mvm ssh --name NAME --user USER` | SSH with specific user |

---

### `mvm clear`

Clear asset cache. Remove all cached assets (binaries, kernels, images). Does **not** touch VMs.

| Flag | Description |
|------|-------------|
| `--force, -f` | Skip confirmation |

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
| **auto (default)** | `--cloud-init-mode auto` | Automatically selects best mode |
| **nocloud-net** | `--cloud-init-mode nocloud-net` | Serves cloud-init files via HTTP |
| **ISO** | `--cloud-init-mode iso` | Uses a pre-existing ISO file |
| **Direct Injection** | `--cloud-init-mode direct` | Injects into rootfs using libguestfs |
| **Disabled** | `--cloud-init-mode disabled` | Skips cloud-init entirely |

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
