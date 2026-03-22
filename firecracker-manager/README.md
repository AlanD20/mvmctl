# firecracker-manager (fcm)

A Python CLI for managing Firecracker microVMs on Linux.

![CI](https://github.com/your-org/firecracker-manager/actions/workflows/ci.yml/badge.svg)

## What This Tool Does

`fcm` is a Python CLI for managing Firecracker microVMs — it replaces bash scripts with a production-quality CLI that handles the full lifecycle: downloading images and kernels, setting up bridge networking, creating and destroying VMs, SSH access, log streaming, snapshots, and cleanup. Built with Python 3.13, Typer, and Rich, it targets multi-VM management exclusively and can be used as a standalone binary, a pip package, or an importable Python library.

---

## Prerequisites

- **Linux** (x86_64 or aarch64) — Firecracker only runs on Linux
- **KVM access** (`/dev/kvm`) — add your user to the kvm group:
  ```bash
  sudo usermod -aG kvm $USER
  # Log out and back in for changes to take effect
  ```
- **Python 3.13+** — check with `python3 --version`
- **System packages:**
  - `ip` (iproute2) — for network management
  - `iptables` — for NAT rules
  - `mkisofs` or `genisoimage` — for cloud-init ISO creation
  - `qemu-img` (qemu-utils) — for image conversion
- **Root access** — networking operations (TAP devices, bridge setup, iptables) require `sudo`

Install system packages on Ubuntu/Debian:

```bash
sudo apt-get install -y iproute2 iptables genisoimage qemu-utils
```

Install system packages on Arch Linux:

```bash
sudo pacman -S --needed iproute2 iptables libisoburn qemu-base
```

---

## Installation

Choose one of the following three methods:

### 1. Download Prebuilt Binary (Recommended)

Download the latest release from GitHub (no Python required):

```bash
curl -L -o fcm <repo-url>/releases/latest/download/fcm-linux
chmod +x fcm
sudo mv fcm /usr/local/bin/
fcm --help
```

### 2. Install via pip

```bash
pip install firecracker-manager
fcm --help
```

### 3. Install from Source with uv

```bash
git clone <repo-url>
cd firecracker-manager
uv sync
uv run fcm --help
```

---

## Quick Start

Get a VM running in under 10 commands:

```bash
# 1. Initialize the host (KVM, modules, ip_forward) — run once per machine
sudo fcm host init
# ✓ net.ipv4.ip_forward: '0' → '1'
# ✓ Host initialized (1 change(s) applied).

# 2. Fetch a prebuilt kernel
fcm asset kernel fetch
# ✓ Kernel built: /home/user/.cache/firecracker-manager/kernels/vmlinux

# 3. Fetch an Ubuntu image
fcm asset image fetch ubuntu-24.04
# ✓ Image ready: /home/user/.cache/firecracker-manager/images/ubuntu-24.04.ext4

# 4. Create and start a VM
sudo fcm vm create --name myvm --image ubuntu-24.04
# ℹ Creating VM 'myvm'
# ℹ   IP:     10.20.0.2
# ℹ   vCPUs:  1
# ℹ   Memory: 512 MiB
# ✓ VM 'myvm' started (PID 12345)
# ℹ   SSH ready in ~30-60s: fcm vm ssh --name myvm

# 5. Watch the boot log until SSH is ready
fcm vm logs --name myvm --type boot --follow

# 6. SSH into the VM
fcm vm ssh --name myvm

# 7. List running VMs
fcm vm list
# ┌──────────────────────────────────────┐
# │           Firecracker VMs            │
# ├───────┬──────────┬─────────┬─────────┤
# │ Name  │ IP       │ Status  │ PID     │
# ├───────┼──────────┼─────────┼─────────┤
# │ myvm  │ 10.20.0.2│ running │ 12345   │
# └───────┴──────────┴─────────┴─────────┘

# 8. Delete the VM when done
sudo fcm vm delete --name myvm --force
# ✓ VM 'myvm' deleted
```

---

## Full Command Reference

### `fcm host` — Host Configuration

Manage system-level setup required for Firecracker VMs. These are one-time, machine-global changes (KVM access, kernel modules, IP forwarding). The pre-change state is snapshotted so it can be fully restored.

| Command | Description |
|---------|-------------|
| `fcm host init` | Apply host configuration changes (KVM, modules, ip_forward). Idempotent. |
| `fcm host ls` | Show current host configuration state |
| `fcm host restore` | Revert host changes using saved snapshot |

**Examples:**

```bash
# Initialize host — run once per machine
sudo fcm host init

# Check host status
fcm host ls

# Restore host to pre-init state
sudo fcm host restore
```

---

### `fcm asset` — Asset Management

Manage kernels, images, and Firecracker binaries.

#### `fcm asset kernel` — Kernel Management

| Command | Description |
|---------|-------------|
| `fcm asset kernel ls` | List cached kernels |
| `fcm asset kernel fetch` | Download the official minimal kernel |
| `fcm asset kernel build` | Build a custom upstream kernel from source |
| `fcm asset kernel rm NAME` | Remove a cached kernel |

**Flags for `fcm asset kernel fetch` / `fcm asset kernel build`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--version VERSION` | Kernel version to use | `6.1.102` |
| `--out PATH` | Output path | `~/.cache/firecracker-manager/kernels/vmlinux` |
| `--jobs N` | Parallel build jobs (build only) | auto |

**Examples:**

```bash
# List cached kernels
fcm asset kernel ls

# Download minimal kernel (default version)
fcm asset kernel fetch

# Download a specific version
fcm asset kernel fetch --version 6.1.102

# Build custom kernel from source (takes 10-20 minutes)
fcm asset kernel build --version 6.1.102 --jobs 4

# Remove a kernel
fcm asset kernel rm vmlinux
```

#### `fcm asset image` — Image Management

| Command | Description |
|---------|-------------|
| `fcm asset image ls` | List available images |
| `fcm asset image fetch NAME` | Download and convert an image |
| `fcm asset image rm NAME` | Remove a cached image |

**Supported image types for `fcm asset image fetch`:**

| ID | Description |
|----|-------------|
| `ubuntu-24.04` | Official Ubuntu cloud image (24.04 LTS) |
| `firecracker-ubuntu` | Firecracker's own minimal Ubuntu image — smaller, faster to boot |
| `arch` | Arch Linux cloud image |
| `debian` | Debian cloud image (bookworm) |

**Flags for `fcm asset image fetch`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--out DIR` | Output directory | `~/.cache/firecracker-manager/images/` |
| `--force, -f` | Re-download even if already cached | false |

**Examples:**

```bash
# List available images (✓ = already cached)
fcm asset image ls

# Fetch Ubuntu 24.04
fcm asset image fetch ubuntu-24.04

# Force re-download
fcm asset image fetch ubuntu-24.04 --force

# Remove an image
fcm asset image rm ubuntu-24.04
```

#### `fcm asset bin` — Binary Management

| Command | Description |
|---------|-------------|
| `fcm asset bin ls` | List Firecracker binary versions |
| `fcm asset bin fetch VERSION` | Download a specific Firecracker version |
| `fcm asset bin use VERSION` | Set active Firecracker version |
| `fcm asset bin rm VERSION` | Remove a cached version |

**Flags for `fcm asset bin ls`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--remote, -r` | Also show remote available versions | false |
| `--limit N` | Max remote versions to show | 10 |

**Examples:**

```bash
# List local binaries
fcm asset bin ls

# List local + remote available versions
fcm asset bin ls --remote

# Download Firecracker v1.12.0
fcm asset bin fetch 1.12.0

# Set active version
fcm asset bin use 1.12.0

# Remove a version
fcm asset bin rm 1.12.0
```

#### `fcm asset clear` — Clear Cache

Remove all cached assets without touching VM runtime state.

**Flags:**

| Flag | Description |
|------|-------------|
| `--force, -f` | Skip confirmation |

**Example:**

```bash
# Remove all cached assets (bin, kernels, images) — does NOT touch VMs
fcm asset clear

# Skip confirmation
fcm asset clear --force
```

---

### `fcm vm` — VM Lifecycle

Manage Firecracker microVMs.

| Command | Description |
|---------|-------------|
| `fcm vm setup` | Manually set up bridge and NAT |
| `fcm vm create` | Create and start a new VM |
| `fcm vm delete` | Stop and remove a VM |
| `fcm vm list` | List VMs |
| `fcm vm ssh` | SSH into a VM |
| `fcm vm logs` | View VM logs |
| `fcm vm cleanup` | Remove stopped VMs |
| `fcm vm pause` | Pause a running VM |
| `fcm vm resume` | Resume a paused VM |
| `fcm vm snapshot` | Create a snapshot of a running VM |
| `fcm vm load` | Load a VM from snapshot |

#### `fcm vm setup`

Manually create the bridge interface (`fcm-br0`) and configure NAT. This runs automatically when you create the first VM, but can be called explicitly.

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--bridge NAME` | Bridge interface name | `fcm-br0` |

**Example:**

```bash
sudo fcm vm setup
```

#### `fcm vm create`

Create and start a new Firecracker VM.

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name (required) | — |
| `--image IMAGE` | Image ID or path to `.ext4` file (required) | — |
| `--kernel PATH` | Path to vmlinux kernel | auto-detected |
| `--vcpus N` | Number of vCPUs | 1 |
| `--mem N` | Memory in MiB | 512 |
| `--ip ADDRESS` | Guest IP (auto-assigned if omitted) | auto |
| `--user USER` | Default SSH user for cloud-init | `root` |
| `--enable-socket` | Enable Firecracker API socket | false |
| `--firecracker-bin PATH` | Path to firecracker binary | `firecracker` |

**Environment variables for `fcm vm create`:**

| Variable | Description |
|----------|-------------|
| `FCM_KERNEL` | Override kernel path |
| `FCM_FIRECRACKER_BIN` | Override Firecracker binary path |

**Example:**

```bash
# Create VM with defaults
sudo fcm vm create --name myvm --image ubuntu-24.04

# Create VM with custom specs
sudo fcm vm create --name myvm --image ubuntu-24.04 --vcpus 4 --mem 4096

# Create VM with static IP and socket enabled
sudo fcm vm create --name myvm --image ubuntu-24.04 --ip 10.20.0.5 --enable-socket
```

#### `fcm vm delete`

Stop and remove a VM.

**Flags:**

| Flag | Description |
|------|-------------|
| `--name, -n NAME` | VM name (required) |
| `--force, -f` | Force kill and skip confirmation |

**Example:**

```bash
sudo fcm vm delete --name myvm
sudo fcm vm delete --name myvm --force
```

#### `fcm vm list`

List running and stopped VMs.

**Flags:**

| Flag | Description |
|------|-------------|
| `--all, -a` | Show all VMs including stopped |
| `--json` | Output as JSON |

**Example:**

```bash
fcm vm list
fcm vm list --all
fcm vm list --json
```

#### `fcm vm ssh`

Open an SSH session into a VM.

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name or IP address (required) | — |
| `--user, -u USER` | SSH user | `root` |
| `--key PATH` | SSH key path | auto-detected |
| `--cmd, -c COMMAND` | Execute command instead of interactive shell | — |

**Example:**

```bash
# Interactive SSH
fcm vm ssh --name myvm

# SSH as different user
fcm vm ssh --name myvm --user ubuntu

# Run a command
fcm vm ssh --name myvm --cmd "uname -a"
```

#### `fcm vm logs`

View VM logs.

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name (required) | — |
| `--type TYPE` | Log type: `boot` (serial console) or `os` (Firecracker process) | `os` |
| `--lines N` | Number of lines to show | 50 |
| `--follow, -f` | Follow log output | false |

**Example:**

```bash
# View serial console output (what you see during boot)
fcm vm logs --name myvm --type boot

# Follow the Firecracker process log
fcm vm logs --name myvm --type os --follow

# Last 100 lines
fcm vm logs --name myvm --lines 100
```

#### `fcm vm cleanup`

Remove stopped VMs and stale directories.

**Flags:**

| Flag | Description |
|------|-------------|
| `--all` | Remove all VMs, not just stopped |
| `--dry-run` | Show what would be removed without deleting |
| `--force, -f` | Skip confirmation |

**Example:**

```bash
# Preview what would be removed
sudo fcm vm cleanup --dry-run

# Remove stopped VMs
sudo fcm vm cleanup

# Remove all VMs
sudo fcm vm cleanup --all --force
```

#### `fcm vm pause` / `fcm vm resume`

Pause and resume VMs. Requires `--enable-socket` when creating the VM.

**Flags:**

| Flag | Description |
|------|-------------|
| `--name, -n NAME` | VM name (required) |

**Example:**

```bash
# Create VM with socket enabled
sudo fcm vm create --name myvm --image ubuntu-24.04 --enable-socket

# Pause the VM
fcm vm pause --name myvm

# Resume the VM
fcm vm resume --name myvm
```

#### `fcm vm snapshot` / `fcm vm load`

Create and restore VM snapshots. Requires `--enable-socket`.

**Flags for `fcm vm snapshot`:**

| Flag | Description |
|------|-------------|
| `--name, -n NAME` | VM name (required) |
| `--mem-out PATH` | Memory snapshot output path (required) |
| `--state-out PATH` | VM state output path (required) |

**Flags for `fcm vm load`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name (required) | — |
| `--mem-in PATH` | Memory snapshot input path (required) | — |
| `--state-in PATH` | VM state input path (required) | — |
| `--resume / --no-resume` | Resume VM after loading | `--resume` |

**Example:**

```bash
# Create snapshot
fcm vm snapshot --name myvm \
  --mem-out /tmp/myvm.mem.snap \
  --state-out /tmp/myvm.state.snap

# Load snapshot (resumes automatically)
fcm vm load --name myvm \
  --mem-in /tmp/myvm.mem.snap \
  --state-in /tmp/myvm.state.snap

# Load snapshot without resuming
fcm vm load --name myvm \
  --mem-in /tmp/myvm.mem.snap \
  --state-in /tmp/myvm.state.snap \
  --no-resume
```

---

### `fcm config` — Configuration

Inspect and validate fcm configuration.

| Command | Description |
|---------|-------------|
| `fcm config show` | Show resolved configuration |
| `fcm config validate` | Validate config file |
| `fcm config dump-vm NAME` | Print Firecracker JSON config for a VM |

**Flags for `fcm config show`:**

| Flag | Description |
|------|-------------|
| `--section SECTION` | Show only a specific config section (e.g. `network`, `defaults`) |

**Flags for `fcm config dump-vm`:**

| Flag | Description |
|------|-------------|
| `--name NAME` | VM name (required) |

**Examples:**

```bash
# Show full resolved config
fcm config show

# Show only the network section
fcm config show --section network

# Validate the config file
fcm config validate

# Print the Firecracker JSON for an existing VM
fcm config dump-vm --name myvm
```

---

## Configuration Reference

### Config File Location

`fcm` looks for a config file in this order (first match wins):

1. Path specified in the `FCM_CONFIG` environment variable
2. `./fcm.yaml` in the current working directory
3. `~/.config/fcm/config.yaml`

### All Config Keys with Defaults

```yaml
# Firecracker runtime settings
firecracker:
  binary: ""              # Path to firecracker binary (auto-detected if empty)
  enable_socket: false    # Enable Firecracker API socket by default
  enable_pci: false       # Enable PCI device support

# Default values for new VMs
vm_defaults:
  vcpu_count: 2           # Default vCPUs
  mem_size_mib: 2048      # Default memory in MiB
  network_interface: "eth0"
  boot_args: "console=ttyS0 reboot=k panic=1 pci=off"
  disk_size: "2G"

# Network topology
network:
  host_bridge: "fcm-br0"          # Bridge interface name (uses project name as prefix)
  gateway: "10.20.0.1"            # Bridge/gateway IP on the host
  guest_ip_range: "10.20.0.0/24"  # IP pool for auto-assignment
  mask: "255.255.255.0"
  tap_prefix: "fcm"               # Prefix for TAP device names

# Kernel boot parameters
boot:
  lsm_flags: "landlock,lockdown,yama,integrity,apparmor,bpf"
  extra_boot_args: ""

# Defaults used when flags are omitted
defaults:
  kernel: "minimal"             # "minimal", "upstream", or a file path
  image: "firecracker-ubuntu"   # Image ID or file path
  ssh_key: "~/.ssh/id_rsa"      # SSH key injected via cloud-init
  vcpus: 2                      # Default vCPU count
  memory: 2048                  # Default memory in MiB
```

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `FCM_CONFIG` | Override config file path | `~/.config/fcm/config.yaml` |
| `FCM_CACHE_DIR` | Override cache directory | `~/.cache/firecracker-manager` |
| `FCM_LOG_LEVEL` | Log verbosity: `DEBUG`, `INFO`, `WARNING`, `ERROR` | `INFO` |
| `FCM_KERNEL` | Override kernel path (used by `vm create`) | (from config) |
| `FCM_FIRECRACKER_BIN` | Override Firecracker binary path | `firecracker` |

Note: the `FCM_` prefix is derived from the CLI name defined in `constants.py`, which in turn is driven by the project name in `pyproject.toml`.

---

## Building from Source

To build a standalone single-file binary that requires no Python at runtime:

```bash
# Clone the repository
git clone <repo-url>
cd firecracker-manager

# Install Python 3.13 and build dependencies
pip install -e ".[dev]" pyinstaller

# Build the binary
pyinstaller --onefile --name fcm src/fcm/main.py

# Output location
ls dist/fcm

# Verify the build
./dist/fcm --version
./dist/fcm --help
```

The project name is defined once in `pyproject.toml` under `[project] name`. Changing it there automatically updates the CLI name, environment variable prefix (`FCM_`), cache directory name, and network device prefixes — no grep-and-replace required. To produce a renamed binary, update `pyproject.toml` and re-run the `pyinstaller` command with `--name <new-name>`.

---

## Cache Directory Structure

Everything `fcm` downloads or generates lives under `~/.cache/firecracker-manager/` (overridable with `FCM_CACHE_DIR`):

```
~/.cache/firecracker-manager/
├── bin/                              # Firecracker and jailer binaries
│   ├── firecracker-v1.12.0
│   └── jailer-v1.12.0
├── kernels/                          # Kernel images
│   └── vmlinux
├── images/                           # VM rootfs images
│   ├── ubuntu-24.04.ext4
│   └── firecracker-ubuntu.ext4
├── keys/                             # Auto-generated or injected SSH keys
│   ├── id_ed25519
│   └── id_ed25519.pub
├── host/                             # Host init snapshot
│   └── state.json
└── vms/                              # Per-VM runtime state
    └── <vm-name>/
        ├── rootfs.ext4               # VM's private root filesystem (copy of image)
        ├── firecracker.json          # Generated Firecracker machine config
        ├── firecracker.log           # Firecracker process log (--type os)
        ├── firecracker.console.log   # Serial console output (--type boot)
        ├── firecracker.pid           # Process ID
        ├── firecracker.sock          # API socket (only if --enable-socket)
        └── cloud-init/               # Generated cloud-init seed files
            ├── meta-data
            ├── network-config
            └── user-data
```

---

## Troubleshooting

**"Permission denied: /dev/kvm"**

Your user isn't in the `kvm` group. Fix it:

```bash
sudo usermod -aG kvm $USER
# Log out and back in, then verify:
groups | grep kvm
```

**"Bridge fcm-br0 not found" or "No such device"**

The network bridge hasn't been created yet, or it was lost on reboot.

```bash
sudo fcm vm setup
# Or let it auto-create when you create a VM:
sudo fcm vm create --name myvm --image ubuntu-24.04
```

**"Kernel not found at ..."**

You need to fetch or build a kernel first.

```bash
# Download prebuilt minimal kernel
fcm asset kernel fetch

# Or build from source (takes 10-20 minutes)
fcm asset kernel build
```

**VM isn't booting / SSH times out**

Cloud-init runs on first boot and takes 30-60 seconds. Check the console log:

```bash
fcm vm logs --name myvm --type boot --follow
```

Look for `fcm cloud-init done` and a `login:` prompt. If it never gets there, check the Firecracker process log:

```bash
fcm vm logs --name myvm --type os
```

**"SSH connection refused" immediately**

The VM's SSH daemon hasn't started yet. Wait a bit longer. If it still fails after 2 minutes, the VM might have panicked. Check:

```bash
fcm vm logs --name myvm --type boot
```

**"Image not found: ubuntu-24.04"**

Fetch the image first:

```bash
fcm asset image fetch ubuntu-24.04
fcm asset image ls  # Confirm the ✓ appears
```

**"Firecracker binary not found"**

Either install Firecracker on your `$PATH`, or download a versioned binary via `fcm`:

```bash
fcm asset bin fetch 1.12.0
fcm asset bin use 1.12.0
# Or point directly at a binary:
sudo fcm vm create --name myvm --image ubuntu-24.04 --firecracker-bin /path/to/firecracker
```

**"host init has not been run"**

`fcm host restore` requires a prior snapshot. Run `sudo fcm host init` first.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, code style guide, testing instructions, and PR process.

---

## License

MIT. See [LICENSE](LICENSE).
