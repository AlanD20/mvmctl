# Command Reference & Configuration

This document provides detailed reference for all `mvm` commands, configuration options, and environment variables.

---

## Table of Contents

- [Command Reference](#command-reference)
  - [mvm init](#mvm-init)
  - [mvm host](#mvm-host)
  - [mvm kernel](#mvm-kernel)
  - [mvm image](#mvm-image)
  - [mvm bin](#mvm-bin)
- [mvm vm](#mvm-vm)
- [mvm snapshot](#mvm-snapshot)
- [mvm console](#mvm-console)
- [mvm network](#mvm-network)
- [mvm key](#mvm-key)
- [mvm config](#mvm-config)
- [mvm cache](#mvm-cache)
- [mvm logs](#mvm-logs)
- [mvm exec](#mvm-exec)
- [mvm ssh](#mvm-ssh)
- [mvm cp](#mvm-cp)
- [mvm volume](#mvm-volume)
- [mvm env](#mvm-env)
- [Configuration](#configuration)
- [Cloud-Init](#cloud-init)
  - [How Net Mode Works](#how-net-mode-works)
  - [Net Mode Security Architecture](#net-mode-security-architecture)
- [Environment Variables](#environment-variables)
- [Cache Directory Structure](#cache-directory-structure)

---

## Command Reference

**Root command flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--verbose` | persistent | Enable verbose output |
| `--debug` | persistent | Enable debug mode |
| `--version` | root | Show version and exit |

### `mvm init`

First-time setup wizard. Walks through host init, binary/kernel/image download, and SSH key setup.

```
mvm init [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--non-interactive` | Use defaults, skip all prompts | `false` |
| `--skip-host` | Skip host init step | `false` |
| `--skip-network` | Skip default network creation | `false` |

---

### `mvm host`

Host configuration commands for one-time, machine-global setup.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm host init` | — | Apply host configuration changes. Idempotent. Creates the `mvm` group, sudoers drop-in, enables IP forwarding, creates firewall chains, and snapshots initial state for rollback. |
| `mvm host status` | `--json` | Show current host configuration state vs expected |
| `mvm host info` | `--refresh`, `--json` | Show host hardware, limits, and VM capacity projection |
| `mvm host clean` | `-f, --force` | Remove all VM networking config (bridges, TAPs, iptables). Does not touch sysctl or mvm group. |
| `mvm host reset` | `-f, --force` | Full rollback: remove networking, revert sysctl, remove sudoers and mvm group |

---

### `mvm kernel`

Kernel management — list, pull, remove, inspect, set default, and import kernels.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm kernel ls` | `--json`, `-r, --remote`, `--no-cache`, `--long` | List cached kernels (or available remote kernels with `--remote`) |
| `mvm kernel pull` | `[type:version]`, `--type`, `--version`, `--default, -d`, `--jobs`, `--keep-build-dir`, `--clean-build`, `--config`, `--features` | Pull or build a kernel. Supports `type:version` shorthand (e.g. `official:6.19.9`) |
| `mvm kernel import` | `NAME`, `PATH`, `--version`, `--default, -d` | Register a vmlinux file as a kernel in the database |
| `mvm kernel default` | `KERNEL_ID` | Set a kernel as the default |
| `mvm kernel rm` | `[IDENTIFIERS]...`, `-f, --force` | Remove one or more kernels |
| `mvm kernel inspect` | `PREFIX`, `--json` | Show detailed information about a kernel |

**`pull` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `[type:version]` | (positional) type:version shorthand e.g. `official:6.19.9` | — |
| `--type TYPE` | Kernel type: `firecracker` or `official` | — |
| `--version VERSION` | Kernel version | (latest) |
| `--default, -d` | Set as default after fetch | `false` |
| `--jobs N` | Parallel build jobs (official only) | `0` (not explicitly set) |
| `--keep-build-dir` | Keep build directory (official only) | `false` |
| `--clean-build` | Skip cache and force clean build (official only) | `false` |
| `--config PATH` | Custom kernel config file to apply as a fragment | — |
| `--features TEXT` | Comma-separated kernel features (e.g. `kvm`, `nftables`, `tuntap`, `btrfs`) | — |

```
# List available remote kernel versions
mvm kernel ls --remote

# Pull the official kernel at version 6.19.9
mvm kernel pull official:6.19.9

# Pull with custom config fragment
mvm kernel pull --type official --version 6.19.9 --config /path/to/config.fragment
```

---

### `mvm image`

Image management — download, list, inspect, import, and manage VM images.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm image ls` | `--json`, `-r, --remote`, `--no-cache`, `--type`, `--long` | List cached images (or available remote images with `--remote`) |
| `mvm image pull` | `[SELECTOR]`, `--type`, `--version`, `--force, -f`, `--no-cache`, `--default, -d`, `--skip-optimization`, `--disable-detector` | Download an image by type:version (e.g. `ubuntu:24.04`), ID, or type |
| `mvm image import` | `NAME`, `PATH`, `--format`, `--root-partition`, `--default, -d`, `--force, -f`, `--skip-optimization`, `--disable-detector` | Import a local image file (qcow2, raw, tar-rootfs) |
| `mvm image default` | `PREFIX` | Set the default image for VM creation |
| `mvm image rm` | `PREFIXES...`, `--force, -f` | Remove cached images by ID prefix |
| `mvm image warm` | `[IMAGE]`, `--all, -a` | Pre-decompress image to ready pool for fast VM creation (warms all images if IMAGE omitted) |
| `mvm image inspect` | `PREFIX`, `--json` | Show detailed information about an image |

**`pull` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `-t, --type TEXT` | Image type from images.yaml (e.g. `ubuntu`, `debian`, `firecracker`) | (first matching) |
| `--version VERSION` | Version override | (latest) |
| `--force, -f` | Re-download even if cached | `false` |
| `--default, -d` | Set as default after fetch | `false` |
| `--no-cache` | Skip cached version listing and fetch live from upstream | `false` |
| `--skip-optimization` | Skip filesystem optimization | `false` |
| `--disable-detector NAME` | Comma-separated detectors to disable: `type`, `label`, `size`, `filesystem`, `all` | — |

**`import` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--format FORMAT` | Image format: `qcow2`, `raw`, `tar-rootfs`, or `auto` | `auto` |
| `--root-partition N` | Root partition number (e.g. 1, 2) | `0` (auto-detect) |
| `--version VERSION` | Set image version | — |
| `--default, -d` | Set as default after import | `false` |
| `--force, -f` | Overwrite existing | `false` |
| `--skip-optimization` | Skip OS cache cleanup (deblob), keep plain ext4 | `true` |
| `--disable-detector NAME` | Comma-separated detectors to disable: `type`, `label`, `size`, `filesystem`, `all` | — |

```
# List available remote image types
mvm image ls --remote

# Pull Ubuntu 24.04
mvm image pull ubuntu:24.04

# Import a local qcow2 image
mvm image import my-image /path/to/image.qcow2
```

---

### `mvm bin`

Firecracker binary management — download, list, and remove Firecracker and jailer binaries.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm bin ls` | `-r, --remote`, `--limit`, `--json`, `--long` | List local (and optionally remote) Firecracker versions |
| `mvm bin pull` | `[SELECTOR]`, `--version`, `--git-ref`, `--default, -d`, `--force, -f` | Download a Firecracker version or build from source |
| `mvm bin default` | `BINARY_ID` | Set a binary as the active default |
| `mvm bin rm` | `[IDENTIFIERS]...`, `--version`, `-f, --force` | Remove one or more binaries, or use `--version` to remove a version pair |

```
# List local and remote versions
mvm bin ls --remote

# Download a specific Firecracker version
mvm bin pull firecracker:1.15.0

# Build from git source
mvm bin pull firecracker --git-ref v1.15.0
```

---

### `mvm vm`

VM lifecycle management — create, start, stop, reboot, pause, resume, remove, list, and inspect VMs.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm vm create` | `NAME [flags]` | Create and start a new Firecracker microVM |
| `mvm vm start` | `[IDENTIFIERS]...` | Start one or more stopped VMs |
| `mvm vm stop` | `[IDENTIFIERS]...`, `-f, --force` | Stop one or more running VMs |
| `mvm vm reboot` | `[IDENTIFIERS]...`, `-f, --force` | Reboot one or more VMs |
| `mvm vm pause` | `[IDENTIFIERS]...` | Pause one or more running VMs |
| `mvm vm resume` | `[IDENTIFIERS]...` | Resume one or more paused VMs |
| `mvm vm rm` | `[NAMES]...`, `-f, --force` | Remove one or more VMs |
| `mvm vm ls` | `--json`, `--long` | List all VMs |
| `mvm vm ps` | `--json` | List running VMs (active processes) |
| `mvm vm inspect` | `IDENTIFIER`, `--json` | Show detailed information about a VM |

**`vm create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `NAME` | VM name (required, positional) | — |
| `--image IMAGE` | Image name, type:version (e.g. `ubuntu:24.04`), short ID, or path to .ext4 file | auto-detected |
| `--kernel KERNEL` | Kernel short ID or path to vmlinux file | auto-detected |
| `--vcpu N` | vCPU count | from config |
| `--mem, --memory SIZE` | Memory (e.g. `512M`, `1G`, `4096`) | from config |
| `--disk-size, -s SIZE` | Disk size (e.g. `512M`, `1G`) | from config |
| `--ip ADDRESS` | Guest IP | auto-assigned |
| `--mac ADDRESS` | Custom MAC address | auto-generated |
| `--network, --net NAME` | Named network | from config |
| `--volume, -v TEXT` | Attach volume(s) to the VM | none |
| `--ssh-key NAME_OR_PATH` | SSH public key (name from cache or file path) | default keys |
| `--user USER` | Default SSH user | from config |
| `--cloud-init-mode MODE` | Cloud-init mode: `inject`, `iso`, `net`, `off` | `off` (effective) |
| `--writeback` | Use writeback cache mode for drives (guest fsync honored) | `false` |
| `--nocloud-net-port N` | Port for nocloud-net HTTP server (0 = auto) | auto-assign |
| `--cloudinit-config PATH` | Path to custom cloud-init config file | — |
| `--no-pci` | Disable PCI device support | from config |
| `--nested-virt` / `--no-nested-virt` | Enable/disable nested virtualization (requires PCI) | from config |
| `--cpu-template PATH` | Path to CPU template JSON file | — |
| `--enable-logging` / `--no-enable-logging` | Enable/disable Firecracker logging | from config |
| `--enable-metrics` / `--no-enable-metrics` | Enable/disable Firecracker metrics | from config |
| `--lsm-flags FLAGS` | Linux Security Module kernel cmdline flags | from config |
| `--boot-args ARGS` | Kernel boot arguments | from config |
| `--console` | Enable serial console relay | `false` |
| `--vsock-port N` | Vsock port for the guest agent | `1024` |
| `--writeback` | Use writeback cache mode for drives | from config |
| `--skip-cleanup` | Keep resources on failure for debugging | `false` |
| `--force, -f` | Skip confirmation prompts | `false` |
| `--count, -c N` | Create N VMs in batch | `1` |
| `--atomic` | All-or-nothing batch: roll back all VMs if any creation fails | `false` |
| `--skip-deblob` | Skip debloat operations on rootfs | `false` |

**Examples:**

```
# Create a single VM with default settings
mvm vm create my-vm

# Create with specific image, kernel, and resources
mvm vm create my-vm --image ubuntu:24.04 --kernel official:6.19.9 --vcpu 2 --mem 2G --disk-size 20G

# Create 3 VMs in batch
mvm vm create cluster-node --count 3
```

---

### `mvm snapshot` (alias: `ss`)

Create, list, inspect, restore, and remove VM snapshots.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm snapshot create` | `VM_IDENTIFIER`, `--name`, `--pause` | Snapshot a running VM |
| `mvm snapshot ls` | `--json` | List all snapshots |
| `mvm snapshot inspect` | `SNAPSHOT_ID`, `--json` | Show detailed information about a snapshot |
| `mvm snapshot restore` | `SNAPSHOT_ID`, `NAME`, `--network`, `--resume`, `--count N` | Restore one or more VMs from a snapshot |
| `mvm snapshot rm` | `SNAPSHOT_ID`, `--force, -f` | Remove a snapshot |

---

### `mvm console`

VM serial console access without SSH. Uses a PTY relay subprocess.

```
mvm console IDENTIFIER [flags]
```

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID, IP, or MAC address (positional, required) |
| `--state` | Show console relay state without attaching |
| `--kill` | Kill the console relay process |

Press `Ctrl+X` then `D` to detach from an active console session.

---

### `mvm network`

Named bridge network management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm network create` | `[NAME]`, `--subnet`, `--ipv4-gateway`, `--no-nat`, `--nat-gateways`, `--non-interactive`, `--default, -d` | Create a named bridge network |
| `mvm network rm` | `[NAMES]...`, `-f, --force` | Remove one or more networks by name |
| `mvm network ls` | `--json, --long` | List all networks |
| `mvm network inspect` | `[NAME]`, `--json` | Show network details and IP leases |
| `mvm network default` | `[NAME]` | Set a network as the default for VM creation |
| `mvm network sync` | `[NAMES]...`, `--json` | Sync firewall rules between database and host |

---

### `mvm key`

SSH key management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm key ls` | `--json, --long` | List all SSH keys |
| `mvm key import` | `NAME`, `PATH`, `--default, -d`, `-f, --force` | Import an existing public key to the cache |
| `mvm key create` | `NAME`, `--algorithm, -a`, `--bits`, `--comment`, `--out`, `--default, -d`, `-f, --force` | Generate a new SSH keypair |
| `mvm key rm` | `[NAMES]...`, `-f, --force` | Remove one or more SSH keys |
| `mvm key inspect` | `[NAME]`, `--json` | Inspect an SSH key |
| `mvm key default` | `[NAMES]...`, `--clear` | Set default SSH keys, or clear with `--clear` |
| `mvm key export` | `[NAME]`, `[PATH]`, `-f, --force` | Export a keypair to a directory |

---

### `mvm config`

Configuration management for overridable settings.

| Command | Description |
|---------|-------------|
| `mvm config get CATEGORY [KEY]` | Get a configuration value |
| `mvm config set CATEGORY KEY VALUE` | Set a configuration value |
| `mvm config ls` | List all overridable settings and current values (alias: `list`) |
| `mvm config reset [CATEGORY] [KEY]` | Reset overrides to defaults. Use `--all, -a` for global reset; `--force, -f` to skip confirmation |

---

### `mvm cache`

Cache management.

| Command | Description |
|---------|-------------|
| `mvm cache init` | Initialize all cache resources |
| `mvm cache prune [RESOURCE]` | Prune cache resources: `vm`, `network`, `image`, `kernel`, `binary`, `misc` |
| `mvm cache clean` | Completely clean all cache: prune everything, host clean, remove cache dir |

**`cache prune` flags:**

| Flag | Description |
|------|-------------|
| `--all, -a` | Remove ALL items including running VMs, default network, protected assets |
| `--dry-run` | Show what would be removed without removing |
| `--force, -f` | Skip confirmation prompts |

Omit `RESOURCE` to get an error unless `--all` is passed. Each per-resource prune subcommand (`vm`, `network`, `image`, `kernel`, `binary`, `misc`) prompts for confirmation unless `--force` is set.

**`cache clean` flags:**

| Flag | Description |
|------|-------------|
| `--dry-run` | Show what would be removed without removing |
| `--force, -f` | Skip confirmation prompts |

---

### `mvm logs`

View VM logs.

```
mvm logs IDENTIFIER [flags]
```

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID, IP, or MAC address (positional, required) |
| `--os` | Show Firecracker OS log instead of boot log |
| `--lines, -n N` | Number of log lines to show |
| `--follow, -f` | Follow log output in real-time |

---

### `mvm exec`

Execute a command inside a VM via the vsock guest agent. If no command is provided, starts an interactive shell session.

```
mvm exec IDENTIFIER [-- <command>...] [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `IDENTIFIER` | VM name, ID, MAC, or IP (positional, required) | — |
| (args after `--`) | Command to execute (omit for interactive shell) | — |
| `--user, -u TEXT` | User to run the command as | `root` |
| `--timeout, -t N` | Vsock agent connect/probe timeout in seconds | `0` (no timeout) |
| `--no-sync` | Skip final `sync()` after command | `false` |
| `--port, -p N` | Vsock port for the guest agent | `1024` |

**Examples:**

```
# Interactive shell
mvm exec my-vm

# Run a command
mvm exec my-vm -- ls -la /etc

# With timeout
mvm exec my-vm --timeout 30 -- apt-get update

# As a different user
mvm exec my-vm --user ubuntu
```

---

### `mvm ssh`

Open an SSH session into a VM, or execute a command.

```
mvm ssh IDENTIFIER [flags]
```

| Flag | Description |
|------|-------------|
| `IDENTIFIER` | VM name, ID prefix, IP, or MAC address (positional, required) |
| `--user, -u USER` | SSH user (default: from user config) |
| `--key PATH` | SSH private key file or directory of keys |
| `--cmd, -c CMD` | Command to execute |
| `--timeout, -t SECONDS` | SSH connection timeout in seconds |

---

### `mvm cp`

Copy files between host and microVMs using vsock binary frame protocol (no SSH, no guest dependencies).

```
mvm cp [OPTIONS] SOURCE... DESTINATION
```

| Flag | Description | Default |
|------|-------------|---------|
| `SOURCE...` | Source path(s); use `vm_name:/path` for VM paths (at least 2 args) | — |
| `DESTINATION` | Destination path; last positional arg | — |
| `--force, -f` | Overwrite existing destination files | `false` |
| `--no-sync` | Skip final sync() after transfer (faster but risks data loss on VM stop) | `false` |

**Examples:**

```
# Copy local files to VM
mvm cp ./myfile.txt my-vm:/root/

# Copy file from VM to local
mvm cp my-vm:/var/log/syslog ./syslog

# Copy between VMs
mvm cp vm1:/data/file.txt vm2:/data/
```

---

### `mvm volume`

Persistent data disk management.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm volume create` | `NAME`, `SIZE`, `--format` | Create a new persistent volume |
| `mvm volume rm` | `[IDENTIFIERS]...`, `-f, --force` | Remove one or more volumes |
| `mvm volume ls` | `--json, --long` | List all volumes |
| `mvm volume inspect` | `IDENTIFIER`, `--json` | Show detailed information about a volume |
| `mvm volume resize` | `IDENTIFIER`, `SIZE` | Resize a volume |
| `mvm volume attach` | `VM_ID`, `VOLUME_ID` | Attach a volume to a VM |
| `mvm volume detach` | `VM_ID`, `VOLUME_ID` | Detach a volume from a VM |

**`volume create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `NAME` | Volume name (required) | — |
| `SIZE` | Volume size, e.g. `10G`, `512M` (required) | — |
| `--format FORMAT` | Disk format: `raw` or `qcow2` | `raw` |
| `--read-only`, `--readonly`, `--ro` | Mount volume as read-only | writable |
| `--shareable, -s` | Allow volume to be attached to multiple VMs | `false` |
| `--writeback` | Use writeback cache mode | `false` |

---

### `mvm env`

Declarative environment workflow management. Define and provision full VM environments from a YAML spec.

| Command | Description |
|---------|-------------|
| `mvm env apply [spec-path]` | Apply an environment spec (alias: `up`). Defaults to `mvmctl.yaml` then `mvmctl.yml`. Supports `--env KEY=VAL` for exec step overrides. |
| `mvm env ls` | List applied environments (alias: `list`) |
| `mvm env diff [spec-path]` | Show what would change (spec vs state) |
| `mvm env destroy [wf-id\|path]` | Tear down exactly what was provisioned (alias: `down`) |

See [ENV_SPEC_REFERENCE.md](ENV_SPEC_REFERENCE.md) for the full YAML spec reference.

---

## Configuration

### Configuration Priority (highest overrides lowest)

1. CLI flags (highest priority)
2. `MVM_*` environment variables (e.g. `MVM_LOG_LEVEL`, `MVM_CACHE_DIR`)
3. SQLite database (`~/.cache/mvmctl/mvmdb.db`) — canonical store for user overrides
4. Built-in fallbacks (`internal/infra/constants.go`)

### Kernel Defaults

Built-in fallbacks for kernel operations (defined in `internal/infra/constants.go` under `defaults.kernel`):

| Key | Default | Description |
|-----|---------|-------------|
| `version` | `6.19.9` | Default version for `--type official` |
| `build_jobs` | `nil` | Parallel build jobs (`nil` = all available cores) |
| `remote_list_limit` | `5` | Max remote versions to list per type |
| `remote_list_cache_ttl` | `14400` | Cache TTL in seconds (4 hours) for remote version listings |

### Image Defaults

| Key | Default | Description |
|-----|---------|-------------|
| `import_format` | `auto` | Default import format when no `--format` specified |
| `remote_list_limit` | `5` | Max remote versions to list per type |
| `remote_list_cache_ttl` | `3600` | Cache TTL in seconds (1 hour) for remote version listings |

### Network Defaults

| Key | Default | Description |
|-----|---------|-------------|
| `name` | `net` | Default bridge name |
| `subnet` | `172.27.0.0/24` | Default subnet CIDR |
| `nat_enabled` | `true` | Default NAT/masquerade setting |

---

## Cloud-Init

The effective default is `--cloud-init-mode off` (no cloud-init). When SSH keys are provided without an explicit mode, the system enables direct key injection regardless of mode.

### Modes

| Mode | Flag | Description |
|------|------|-------------|
| **inject** | `--cloud-init-mode inject` | Direct injection into rootfs via the active provisioner backend (loop-mount or GuestFS) |
| **net** | `--cloud-init-mode net` | Serves cloud-init files via HTTP (nocloud-net) |
| **iso** | `--cloud-init-mode iso` | Generates a cloud-init ISO via `cloud-localds`, or uses a provided custom ISO path |
| **off** | `--cloud-init-mode off` | Skips cloud-init entirely (default) |

### How Net Mode Works

The net mode delivers cloud-init configuration via HTTP. Steps below apply to the net mode only.

1. **HTTP Server**: A temporary HTTP server is started on the host (port range 8000-9000)
2. **Firewall Rules**: Firewall rules allow the VM to reach the server (via the active `nftables` or `iptables` backend)
3. **Kernel Command Line**: The VM boots with `ds=nocloud;seedfrom=http://GATEWAY_IP:PORT/`
4. **Configuration Delivery**: cloud-init fetches `meta-data`, `user-data`, and `network-config` via HTTP
5. **Automatic Cleanup**: The HTTP server stops when the VM is removed

### Net Mode Security Architecture

The security model applies to the net cloud-init mode only:

- **Per-VM Isolation**: Each VM gets its own HTTP server on a unique port
- **Source-Based Firewall**: Only the VM's IP can reach its nocloud server
- **Gateway Binding**: HTTP servers bind to the bridge gateway IP, not `0.0.0.0`
- **Rule Comments**: Firewall rules are tagged with `# nocloudnet:<vm_name>:<port>`

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CACHE_DIR` | Override cache directory | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override config directory | `~/.config/mvmctl` |
| `MVM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARN`, `ERROR`) | `WARN` |
| `MVM_ASSET_MIRROR` | Local mirror directory for downloaded assets | (not set) |
| `MVM_WARM_POOL` | Warm image pool backend (`disk` for disk-backed, default is tmpfs) | (not set — tmpfs) |
| `MVM_ESCALATED` | Set by sudo wrapper to indicate privilege escalation | `1` |
| `MVM_TEMP_DIR` | Override temp directory for microVMs | `/tmp/mvmctl` |
| `MVM_SUDO_RESTART` | Set internally when re-running with sudo for host init | (not set) |

---

## Cache Directory Structure

```
~/.cache/mvmctl/
├── bin/                  # Firecracker + jailer binaries
├── kernels/              # vmlinux kernel images
├── images/               # Root filesystem images (.ext4, .btrfs, .img, .raw, .ext4.zst, .btrfs.zst)
├── volumes/              # Persistent disk volume files
├── vms/                  # Per-VM state
│   └── <vm-sha>/         # VM directories named by SHA256 hash
│       ├── rootfs.ext4 (or rootfs.btrfs, rootfs.xfs)
│       ├── firecracker.json
│       ├── firecracker.log
│       ├── firecracker.console.log
│       ├── firecracker.pid
│       ├── firecracker.api.socket
│       ├── firecracker.metrics
│       ├── console.sock
│       ├── console.pid
│       └── cloud-init/
├── workflows/            # Workflow state persistence
├── nocloudnet/           # nocloud-net batch server dirs and logs
├── snapshots/            # Snapshot files (mem, vmstate, disk)
│   └── <snapshot-id>/
├── logs/                 # Application log files
├── firecracker-src/      # Firecracker git clone (for building from source)
├── mvmdb.db              # SQLite database (canonical asset state)
├── mvmctl.log            # Main application log
├── timing.log            # Performance/timing log
├── audit.log             # Rotating operation log (10MB, 3 backups)
└── ...
```
