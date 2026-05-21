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
  - [mvm volume](#mvm-volume) — Persistent storage
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

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm host init` | --- | Apply host configuration changes. Idempotent. Creates mvm group, sudoers drop-in, enables IP forwarding, creates default network. |
| `mvm host status` | `--json` | Show current host configuration state vs expected |
| `mvm host info` | `--refresh, --json` | Show host hardware, limits, and VM capacity projection |
| `mvm host clean` | `-f, --force` | Remove all networking config (bridges, TAPs, iptables). Does not touch sysctl/group. |
| `mvm host reset` | `-f, --force` | Full rollback: remove networking, revert sysctl, remove sudoers... |

---

### `mvm kernel`

Kernel management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm kernel ls` | `--json, -r, --remote, --no-cache` | List cached kernels (or available remote kernels with --remote) |
| `mvm kernel pull` | `[KERNEL_SELECTOR]`, `--type`, `--version`, `--arch`, `--default, -d`, `--jobs`, `--keep-build-dir`, `--clean-build`, `--config` | Pull or build a kernel. Supports type:version shorthand (e.g. official:6.19.9) |
| `mvm kernel import` | `NAME`, `PATH`, `--version`, `--arch`, `--default, -d` | Register a vmlinux file as a kernel in the database |
| `mvm kernel default` | `KERNEL_ID` | Set a kernel as the default |
| `mvm kernel rm` | `[IDENTIFIERS]...`, `-f, --force` | Remove one or more kernels |
| `mvm kernel inspect` | `PREFIX`, `--json` | Show detailed information about a kernel |

**`pull` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `KERNEL_SELECTOR` | (positional) type:version shorthand e.g. official:6.19.9 | --- |
| `--type` | `firecracker` or `official` (optional — inferred from type:version shorthand) | --- |
| `--version VERSION` | Kernel version | (latest) |
| `--arch` | Architecture (`x86_64`, `arm64`) | auto-detect |
| `--default` | Set as default after fetch | false |
| `--jobs N` | Parallel build jobs (official only) | auto |
| `--keep-build-dir` | Keep build directory (official only) | false |
| `--clean-build` | Bypass cache and force clean build (official only) | false |
| `--config PATH` | Custom kernel config fragment file | --- |
| `--features TEXT` | Comma-separated kernel features (e.g. `kvm`, `nftables`) | --- |

> **Note:** Use `mvm kernel ls -r` or `mvm kernel ls --remote` to list available versions from upstream.

---

### `mvm image`

Image management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm image ls` | `--json, -r, --remote, --no-cache, --type` | List cached images (or available remote images with --remote) |
| `mvm image pull` | `IMAGE_SELECTOR`, `--type`, `--version`, `--arch`, `--force, -f`, `--no-cache`, `--default, -d`, `--skip-optimization`, `--disable-detector` | Download an image by type:version (e.g. ubuntu:24.04), ID, or type |
| `mvm image import` | `NAME`, `PATH`, `--format`, `--arch`, `--root-partition`, `--default, -d`, `--force, -f`, `--skip-optimization`, `--disable-detector` | Import a local image file (qcow2, raw, tar-rootfs) |
| `mvm image default` | `PREFIX` | Set the default image for VM creation |
| `mvm image rm` | `PREFIXES...`, `--force, -f` | Remove cached images by ID prefix |
| `mvm image warm` | `IMAGE`, `--all, -a` | Pre-decompress image to ready pool for fast VM creation |
| `mvm image inspect` | `PREFIX`, `--json` | Show detailed information about an image |

**`pull` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--type TEXT` | Image type from images.yaml (e.g. ubuntu, debian, firecracker) | (first matching) |
| `--force, -f` | Re-download even if cached | false |
| `--default` | Set as default after fetch | false |
| `--arch ARCH` | Architecture (e.g., `x86_64`) | host arch |
| `--version VERSION` | Version override | (latest) |
| `--no-cache` | Skip cached version listing and fetch live from upstream | false |
| `--skip-optimization` | Skip filesystem optimization | false |
| `--disable-detector NAME` | Comma-separated detectors to disable: type,label,size,filesystem,all | --- |

**`import` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--format FORMAT` | Image format: qcow2, raw, tar-rootfs, or auto | auto |
| `--arch ARCH` | Image architecture (e.g., x86_64) | host arch |
| `--root-partition N` | Root partition number (e.g. 1, 2) | auto-detect |
| `--default` | Set as default after import | false |
| `--force, -f` | Overwrite existing | false |
| `--skip-optimization` | Skip shrink and compression, keep plain ext4 | false |
| `--disable-detector NAME` | Comma-separated detectors to disable: type,label,size,filesystem,all | --- |

---

### `mvm bin`

Firecracker binary management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm bin ls` | `-r, --remote, --limit, --json` | List local (and optionally remote) Firecracker versions |
| `mvm bin pull` | `BINARY_NAME`, `--version`, `--git-ref`, `--default, -d`, `--force, -f` | Download a Firecracker version or build from source |
| `mvm bin default` | `BINARY_ID` | Set a binary as the active default |
| `mvm bin rm` | `[IDENTIFIERS]...`, `--version`, `-f, --force` | Remove one or more binaries, or use --version to remove a version pair |

---

### `mvm vm`

VM lifecycle management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm vm create` | --- | Create and start a new Firecracker VM |
| `mvm vm start` | `IDENTIFIER` | Start a stopped VM |
| `mvm vm stop` | `IDENTIFIER`, `-f, --force` | Stop a running VM |
| `mvm vm reboot` | `IDENTIFIER`, `-f, --force` | Reboot a VM |
| `mvm vm pause` | `IDENTIFIER` | Pause a running VM |
| `mvm vm resume` | `IDENTIFIER` | Resume a paused VM |
| `mvm vm rm` | `[NAMES]...`, `-f, --force` | Remove one or more VMs |
| `mvm vm ls` | `--json` | List all VMs |
| `mvm vm ps` | --- | List running VMs (active processes) |
| `mvm vm inspect` | `IDENTIFIER`, `--json` | Show detailed information about a VM |
| `mvm vm snapshot` | `IDENTIFIER`, `MEM_FILE`, `STATE_FILE` | Snapshot VM memory and disk state |
| `mvm vm load` | `IDENTIFIER`, `MEM_FILE`, `STATE_FILE`, `--resume` | Load VM from snapshot |
| `mvm vm export` | `IDENTIFIER`, `[OUTPUT]` | Export a VM's configuration to a portable JSON file |
| `mvm vm import` | `CONFIG_PATH`, `--name, -n` | Create a VM from a portable config file |
| `mvm vm attach-volume` | `IDENTIFIER`, `VOLUME_NAME` | Attach a volume to a stopped VM |
| `mvm vm detach-volume` | `IDENTIFIER`, `VOLUME_NAME` | Detach a volume from a stopped VM |

**`vm create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `NAME` | VM name **(required, positional)** | --- |
| `--image IMAGE` | Image name, short ID, or path to .ext4 file | auto-detected |
| `--kernel KERNEL` | Kernel short ID or path to vmlinux | auto-detected |
| `--vcpus, --cpus N` | vCPU count | from config |
| `--mem, --memory N` | Memory in MiB | from config |
| `--disk-size, -s SIZE` | Disk size (e.g., `512M`, `1G`) | from config |
| `--ip ADDRESS` | Guest IP | auto-assigned |
| `--mac ADDRESS` | Custom MAC address | auto-generated |
| `--network, --net NAME` | Named network | from config |
| `--volume, -v TEXT` | Attach volume(s) to the VM | none |
| `--ssh-key NAME_OR_PATH` | SSH public key (name from cache or file path) | default keys |
| `--user USER` | Default SSH user | from config |
| `--cloud-init-mode MODE` | `inject`, `iso`, `net`, `off` | `off` (no cloud-init) |
| `--nocloud-net-port N` | Port for nocloud-net HTTP server (0=auto) | auto-assign |
| `--user-data PATH` | Path to custom cloud-init user-data file | --- |
| `--no-pci` | Disable PCI device support (opt-out) | from config |
| `--nested-virt/--no-nested-virt` | Enable nested virtualization (requires PCI) | from config |
| `--cpu-template PATH` | Path to CPU template JSON file (merged with nested-virt config) | --- |
| `--enable-logging/--no-enable-logging` | Enable Firecracker logging | from config |
| `--enable-metrics/--no-enable-metrics` | Enable Firecracker metrics | from config |
| `--lsm-flags FLAGS` | Linux Security Module kernel cmdline flags | from config |
| `--boot-args ARGS` | Kernel boot arguments | from config |
| `--no-console` | Disable serial console | false |
| `--firecracker-bin PATH` | Path to firecracker binary | active version |
| `--skip-cleanup` | Keep resources on failure for debugging | false |
| `--count, -c N` | Create N VMs in batch (base name keeps, subsequent get `-N` suffix) | 1 |
| `--atomic` | All-or-nothing batch: roll back all VMs if any creation fails | false |
| `--skip-deblob` | Skip debloat operations on rootfs (removes OS caches, cleans package manager caches) | false |

---

### `mvm console`

VM console access without SSH. Uses a PTY relay.

| Command | Description |
|---------|-------------|
| `mvm console IDENTIFIER` | Attach to a VM console interactively |
| `mvm console IDENTIFIER --state` | Show console state without attaching |
| `mvm console IDENTIFIER --kill` | Kill the console relay for a VM |

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID, IP, or MAC address (positional, required) |
| `--state` | Show console relay state without attaching |
| `--kill` | Kill the console relay process |

---

### `mvm network`

Named network management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm network create` | `[NAME]`, `--subnet`, `--ipv4-gateway`, `--no-nat`, `--nat-gateways`, `--non-interactive`, `--default, -d` | Create a named bridge network |
| `mvm network rm` | `[NAMES]...`, `-f, --force` | Remove one or more networks by name |
| `mvm network ls` | `--json` | List all networks |
| `mvm network inspect` | `[NAME]`, `--json`, `--tree` | Show network details and IP leases |
| `mvm network default` | `[NAME]` | Set a network as the default for VM creation |
| `mvm network sync` | `[IDENTIFIER]`, `--json` | Sync firewall rules between database and host |

---

### `mvm key`

SSH key management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm key ls` | `--json` | List all SSH keys |
| `mvm key add` | `NAME`, `PATH`, `-f, --force` | Add an existing public key to the cache |
| `mvm key create` | `NAME`, `--algorithm`, `--bits`, `--comment`, `--out`, `--default, -d`, `-f, --force` | Generate a new SSH keypair |
| `mvm key rm` | `[NAMES]...`, `-f, --force` | Remove one or more SSH keys |
| `mvm key inspect` | `[NAME]`, `--json` | Inspect an SSH key |
| `mvm key default` | `[NAMES]...`, `--clear` | Set default SSH keys, or clear with --clear |
| `mvm key export` | `[NAME]`, `--out`, `-f, --force` | Export a keypair to a directory |

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
| `mvm cache init` | Initialize all cache resources |
| `mvm cache prune [RESOURCE] [--all] [--dry-run] [--force, -f]` | Prune cache resources (vm, network, image, kernel, binary, misc) |
| `mvm cache clean [--dry-run] [--force, -f]` | Completely clean all cache: prune everything, host clean, remove cache dir |

**`cache prune` flags:**

| Flag | Description |
|------|-------------|
| `--all, -a` | Remove ALL items including running VMs, default network, protected assets |
| `--dry-run` | Show what would be removed without actually removing |
| `--force, -f` | Skip confirmation prompts |

> **Note:** Omitting RESOURCE prunes all resource types. Each per-resource prune subcommand (`vm`, `network`, `image`, `kernel`, `binary`, `misc`) prompts for confirmation unless `--force` is passed.

**`cache clean` flags:**

| Flag | Description |
|------|-------------|
| `--dry-run` | Show what would be removed without actually removing |
| `--force, -f` | Skip confirmation prompts |

---

### `mvm logs`

VM log management.

| Command | Description |
|---------|-------------|
| `mvm logs IDENTIFIER [--os] [--lines N] [--follow]` | View VM log (serial console by default, Firecracker log with --os) |

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID, IP, or MAC address (positional, required) |
| `--os` | Show Firecracker OS log instead of boot log |
| `--lines, -n N` | Number of log lines to show |
| `--follow, -f` | Follow log output in real-time |

---

### `mvm ssh`

VM SSH access.

| Command | Description |
|---------|-------------|
| `mvm ssh IDENTIFIER [--user USER] [--key PATH] [--cmd CMD] [--timeout SECONDS]` | Open an SSH session into a VM, or execute a command |

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID prefix, IP, or MAC address (positional, required) |
| `--user, -u USER` | SSH user (default: from user config) |
| `--key PATH` | SSH private key file or directory of keys |
| `--cmd, -c CMD` | Command to execute |
| `--timeout, -t SECONDS` | SSH connection timeout in seconds |

---

### `mvm cp`

Copy files between host and microVMs using tar-over-SSH.

| Command | Description |
|---------|-------------|
| `mvm cp [OPTIONS] SOURCE... DESTINATION` | Copy files between host and microVMs |

| Flag | Description | Default |
|------|-------------|---------|
| `SOURCE...` | Source path(s); use `vm_name:/path` for VM paths (positional, at least 2 args) | --- |
| `DESTINATION` | Destination path; last positional arg (positional) | --- |
| `--user, -u TEXT` | SSH user for VM connections | from config |
| `--key PATH` | SSH private key path or registered key name | auto-detected |
| `--force, -f` | Overwrite existing destination files | false |

**Examples:**
```
# Copy local files to VM
mvm cp ./myfile.txt my-vm:/root/

# Copy multiple local files to VM
mvm cp file1.txt file2.txt my-vm:/dst/

# Copy file from VM to local
mvm cp my-vm:/var/log/syslog ./syslog

# Copy between VMs
mvm cp vm1:/data/file.txt vm2:/data/
```

> **Note:** Destination must be a directory (end with `/`) for host → VM copies.
> Multiple sources are only supported for host → VM direction.

---

### `mvm volume`

Persistent data disk management. Create, remove, list, inspect, and resize volumes.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm volume create` | `NAME`, `SIZE`, `--format` | Create a new persistent volume |
| `mvm volume rm` | `[IDENTIFIERS]...`, `-f, --force` | Remove one or more volumes |
| `mvm volume ls` | `--json` | List all volumes |
| `mvm volume inspect` | `IDENTIFIER`, `--json` | Show detailed information about a volume |
| `mvm volume resize` | `IDENTIFIER`, `SIZE` | Resize a volume |

**`volume create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `NAME` | Volume name **(required)** | --- |
| `SIZE` | Volume size, e.g. `10G`, `512M` **(required)** | --- |
| `--format FORMAT` | Disk format: `raw` or `qcow2` | `raw` |
| `--read-only` | Mount volume as read-only | writable |

**`volume rm` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `IDENTIFIERS...` | Volume names or ID prefixes **(required)** | --- |
| `--force, -f` | Remove even if attached to VMs | false |

**`volume inspect` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `IDENTIFIER` | Volume name or ID prefix **(required)** | --- |
| `--json` | Output as JSON | false |

---

## Configuration

### Configuration Priority (highest overrides lowest)

1. CLI flags (highest priority)
2. `MVM_*` environment variables
3. SQLite database (`~/.cache/mvmctl/mvmdb.db`) — canonical store for user overrides
4. Built-in fallbacks (`constants.py`)

### Kernel Defaults

Built-in fallbacks for kernel operations (defined in `constants.py` under `defaults.kernel`):

| Key | Default | Description |
|-----|---------|-------------|
| `arch` | `x86_64` | Default architecture |
| `version` | `6.19.9` | Default version for `--type official` |
| `build_jobs` | `None` | Parallel build jobs (None = all cores) |
| `remote_list_limit` | `5` | Max remote versions to list per type |
| `remote_list_cache_ttl` | `14400` | Cache TTL in seconds (4 hours) for remote version listings |

The **canonical source of truth** is the SQLite database (`~/.cache/mvmctl/mvmdb.db`) which stores images, kernels, binaries, networks, keys, and VM state.

---

## Cloud-Init

`mvm` defaults to `--cloud-init-mode off` (no cloud-init). To enable provisioning, pass one of:

- `--cloud-init-mode inject` — injects cloud-init files directly into the rootfs
- `--cloud-init-mode iso` — generates a cloud-init ISO via `cloud-localds` (or use a custom ISO path)
- `--cloud-init-mode net` — serves cloud-init files via a temporary HTTP (nocloud-net) server

### How It Works

1. **HTTP Server**: A temporary HTTP server is started on the host (port range 8000-9000)
2. **Firewall Rules**: Firewall rules allow the VM to reach the server (via the active `nftables` or `iptables` backend)
3. **Kernel Command Line**: The VM boots with `ds=nocloud-net;s=http://GATEWAY_IP:PORT/`
4. **Configuration Delivery**: cloud-init fetches `meta-data`, `user-data`, and `network-config` via HTTP
5. **Automatic Cleanup**: The HTTP server stops when the VM is removed

### Cloud-Init Modes

| Mode | Flag | Description |
|------|------|-------------|
| **inject** | `--cloud-init-mode inject` | Direct injection into rootfs via loop-mount provisioner (guestfs fallback) |
| **net** | `--cloud-init-mode net` | Serves cloud-init files via HTTP (nocloud-net) |
| **iso** | `--cloud-init-mode iso` | Generates a cloud-init ISO via cloud-localds, or uses a provided custom ISO path |
| **off** | `--cloud-init-mode off` | Skips cloud-init entirely (default) |

### Security Architecture

- **Per-VM Isolation**: Each VM gets its own HTTP server on a unique port
- **Source-Based Firewall**: Only the VM's IP can reach its nocloud server
- **Gateway Binding**: HTTP servers bind to the bridge gateway IP, not `0.0.0.0`
- **Rule Comments**: Firewall rules are tagged with `# mvm-nocloud:<vm_name>:<port>`

### Benefits Over ISO Mode

| Feature | nocloud-net | ISO Mode |
|---------|-------------|----------|
| Boot speed | Faster (no ISO generation) | Slower (cloud-localds) |
| Portability | Works with any image | Requires CD-ROM drive |
| Cleanup | Automatic | Manual |

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CACHE_DIR` | Override cache directory | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override config directory | `~/.config/mvmctl` |
| `MVM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARNING`, `ERROR`) | `WARNING` |
| `MVM_FIRECRACKER_BIN` | Override Firecracker binary path | (default from DB) |
| `MVM_ASSET_MIRROR` | Local mirror directory for downloaded assets | (not set) |
| `MVM_ESCALATED` | Set by sudo wrapper to indicate privilege escalation | `1` |
| `MVM_TEMP_DIR` | Override temp directory for microVMs | `/tmp/mvmctl` |

---

## Cache Directory Structure

```
~/.cache/mvmctl/
├── bin/               # Firecracker + jailer binaries + service binaries (mvm-console-relay, mvm-nocloud-server, mvm-provision)
├── kernels/           # vmlinux kernel images
├── images/            # Root filesystem images (.ext4, .btrfs, .zst)
├── logs/              # VM and process log files
├── volumes/           # Persistent disk volume files
├── vms/               # Per-VM state
│   └── <vm-sha>/      # VM directories named by SHA256 hash
│       ├── rootfs.ext4
│       ├── firecracker.json
│       ├── firecracker.log
│       ├── firecracker.console.log
│       ├── firecracker.pid
│       └── cloud-init/
├── mvmdb.db           # SQLite database (canonical asset state)
├── audit.log          # Append-only operation log
└── ...

~/.config/mvmctl/
└── keys/              # Cached SSH public keys
```

---

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) for common issues and solutions.
