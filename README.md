# mvmctl (mvm)

A Python CLI for managing Firecracker microVMs on Linux.

![CI](https://github.com/your-org/firecracker-manager/actions/workflows/ci.yml/badge.svg)

## What This Tool Does

`mvm` is a Python CLI for managing Firecracker microVMs — it replaces bash scripts with a production-quality CLI that handles the full lifecycle: downloading images and kernels, setting up bridge networking, creating and destroying VMs, SSH access, log streaming, snapshots, and cleanup. Built with Python 3.13, Typer, and Rich, it targets multi-VM management exclusively and can be used as a standalone binary, a pip package, or an importable Python library.

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
- **Root access (one-time)** — run `sudo mvm host init` once to create the `mvm` group and sudoers drop-in; after that, no `sudo` is needed for normal `mvm` commands

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
curl -L -o mvm https://github.com/your-org/firecracker-manager/releases/latest/download/mvm
chmod +x mvm
sudo mv mvm /usr/local/bin/
mvm --help
```

### 2. Install with pipx (recommended for isolated installation)

```bash
pipx install mvmctl
mvm --help
```

Or with uvx:

```bash
uvx install mvmctl
mvm --help
```

### 3. Install via pip

```bash
pip install mvmctl
mvm --help
```

### 4. Install from Source with uv

```bash
git clone https://github.com/your-org/firecracker-manager
cd firecracker-manager
uv sync
uv run mvm --help
```

---

## Quick Start

Get a VM running in under 10 commands:

```bash
# 1. Initialize the host (KVM, modules, ip_forward, group, sudoers) — run once per machine
sudo mvm host init
# ✓ group:mvm: None → 'mvm'
# ✓ Host initialized (N change(s) applied).
# ⚠ ACTION REQUIRED: Log out and back in for group membership to take effect.
# Or run immediately: newgrp mvm

# 2. Fetch a prebuilt kernel
mvm kernel fetch
# ✓ Kernel built: /home/user/.cache/mvmctl/kernels/vmlinux

# 3. Fetch an Ubuntu image
mvm image fetch ubuntu-24.04
# ✓ Image ready: /home/user/.cache/mvmctl/images/ubuntu-24.04.ext4

# 4. Create and start a VM
sudo mvm vm create --name myvm --image ubuntu-24.04
# ℹ Creating VM 'myvm'
# ℹ   IP:     10.20.0.2
# ℹ   vCPUs:  2
# ℹ   Memory: 2048 MiB
# ✓ VM 'myvm' started (PID 12345)
# ℹ   SSH ready in ~30-60s: mvm vm ssh --name myvm

# 5. Watch the boot log until SSH is ready
mvm vm logs --name myvm --type boot --follow

# 6. SSH into the VM
mvm vm ssh --name myvm

# 7. List running VMs
mvm vm ls
# ┌──────────────────────────────────────┐
# │           Firecracker VMs            │
# ├───────┬──────────┬─────────┬─────────┤
# │ Name  │ IP       │ Status  │ PID     │
# ├───────┼──────────┼─────────┼─────────┤
# │ myvm  │ 10.20.0.2│ running │ 12345   │
# └───────┴──────────┴─────────┴─────────┘

# 8. Remove the VM when done
sudo mvm vm remove --name myvm --force
# ✓ VM 'myvm' removed
```

---

## Full Command Reference

### `mvm host` — Host Configuration

Manage system-level setup required for Firecracker VMs. These are one-time, machine-global changes (KVM access, kernel modules, IP forwarding). The pre-change state is snapshotted so it can be fully restored.

| Command | Description |
|---------|-------------|
| `mvm host init` | Apply host configuration (KVM, modules, ip_forward, group, sudoers). Idempotent. |
| `mvm host ls` | Show current host configuration state |
| `mvm host clean` | Remove all networking config (bridges, TAPs, iptables). Does not touch sysctl or group. |
| `mvm host reset` | Full rollback: networking + sysctl + sudoers + group removal. |

**Examples:**

```bash
# Initialize host — run once per machine
sudo mvm host init
# Log out and back in (or: newgrp mvm)

# Check host status
mvm host ls

# Tear down networking only
sudo mvm host clean

# Full rollback to pre-init state
sudo mvm host reset
```

---

### `mvm kernel` / `mvm image` / `mvm bin` — Asset Management

Manage kernels, images, and Firecracker binaries.

#### `mvm kernel` — Kernel Management

| Command | Description |
|---------|-------------|
| `mvm kernel ls` | List cached kernels |
| `mvm kernel fetch` | Download the official minimal kernel |
| `mvm kernel build` | Build a custom upstream kernel from source |
| `mvm kernel remove NAME` | Remove a cached kernel (`rm` is an alias) |

**Flags for `mvm kernel fetch` / `mvm kernel build`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--version VERSION` | Kernel version to use | `6.1.102` |
| `--out PATH` | Output path | `~/.cache/mvmctl/kernels/vmlinux` |
| `--jobs N` | Parallel build jobs (build only) | auto |

**Examples:**

```bash
# List cached kernels
mvm kernel ls

# Download minimal kernel (default version)
mvm kernel fetch

# Download a specific version
mvm kernel fetch --version 6.1.102

# Build custom kernel from source (takes 10-20 minutes)
mvm kernel build --version 6.1.102 --jobs 4

# Remove a kernel
mvm kernel remove vmlinux
```

#### `mvm image` — Image Management

| Command | Description |
|---------|-------------|
| `mvm image ls` | List available images |
| `mvm image fetch NAME` | Download and convert an image |
| `mvm image remove NAME` | Remove a cached image (`rm` is an alias) |

**Supported image types for `mvm image fetch`:**

| ID | Description |
|----|-------------|
| `ubuntu-24.04` | Official Ubuntu cloud image (24.04 LTS) |
| `firecracker-ubuntu` | Firecracker's own minimal Ubuntu image — smaller, faster to boot |
| `arch` | Arch Linux cloud image |
| `debian` | Debian cloud image (bookworm) |

**Flags for `mvm image fetch`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--out DIR` | Output directory | `~/.cache/mvmctl/images/` |
| `--force, -f` | Re-download even if already cached | false |

**Examples:**

```bash
# List available images (✓ = already cached)
mvm image ls

# Fetch Ubuntu 24.04
mvm image fetch ubuntu-24.04

# Force re-download
mvm image fetch ubuntu-24.04 --force

# Remove an image
mvm image remove ubuntu-24.04
```

#### `mvm bin` — Binary Management

| Command | Description |
|---------|-------------|
| `mvm bin ls` | List Firecracker binary versions |
| `mvm bin fetch VERSION` | Download a specific Firecracker version |
| `mvm bin use VERSION` | Set active Firecracker version |
| `mvm bin remove VERSION` | Remove a cached version (`rm` is an alias) |

**Flags for `mvm bin ls`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--remote, -r` | Also show remote available versions | false |
| `--limit N` | Max remote versions to show | 10 |

**Examples:**

```bash
# List local binaries
mvm bin ls

# List local + remote available versions
mvm bin ls --remote

# Download Firecracker v1.12.0
mvm bin fetch 1.12.0

# Set active version
mvm bin use 1.12.0

# Remove a version
mvm bin remove 1.12.0
```

#### `mvm clear` — Clear Asset Cache

Remove all cached assets without touching VM runtime state.

**Flags:**

| Flag | Description |
|------|-------------|
| `--force, -f` | Skip confirmation |

**Example:**

```bash
# Remove all cached assets (bin, kernels, images) — does NOT touch VMs
mvm clear

# Skip confirmation
mvm clear --force
```

---

### `mvm vm` — VM Lifecycle

Manage Firecracker microVMs.

| Command | Description |
|---------|-------------|
| `mvm vm create` | Create and start a new VM |
| `mvm vm remove` | Stop and remove a VM |
| `mvm vm ls` | List VMs (alias: `list`) |
| `mvm vm ssh` | SSH into a VM |
| `mvm vm logs` | View VM logs |
| `mvm vm cleanup` | Remove stopped VMs |
| `mvm vm snapshot` | Create a snapshot of a running VM |
| `mvm vm load` | Load a VM from snapshot |

#### `mvm vm create`

Create and start a new Firecracker VM.

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name (required) | — |
| `--image IMAGE` | Image ID or path to `.ext4` file (required) | — |
| `--kernel PATH` | Path to vmlinux kernel | auto-detected |
| `--vcpus N` | Number of vCPUs | 2 |
| `--mem N` | Memory in MiB | 2048 |
| `--ip ADDRESS` | Guest IP (auto-assigned if omitted) | auto |
| `--network, --net NAME` | Named network to attach to | `default` |
| `--mac ADDRESS` | Custom MAC address (auto-generated if omitted) | auto |
| `--ssh-key NAME_OR_PATH` | SSH public key name (from key cache) or file path | auto-detected |
| `--user-data PATH` | Path to custom cloud-init user-data file | — |
| `--user USER` | Default SSH user for cloud-init | `root` |
| `--enable-api-socket` | Enable Firecracker API socket | false |
| `--enable-pci` | Enable PCI device support | false |
| `--firecracker-bin PATH` | Path to firecracker binary | `firecracker` |

**Environment variables for `mvm vm create`:**

| Variable | Description |
|----------|-------------|
| `MVM_KERNEL` | Override kernel path |
| `MVM_FIRECRACKER_BIN` | Override Firecracker binary path |

**Example:**

```bash
# Create VM with defaults
sudo mvm vm create --name myvm --image ubuntu-24.04

# Create VM with custom specs
sudo mvm vm create --name myvm --image ubuntu-24.04 --vcpus 4 --mem 4096

# Create VM with static IP and socket enabled
sudo mvm vm create --name myvm --image ubuntu-24.04 --ip 10.20.0.5 --enable-api-socket

# Create VM on a specific network with a custom MAC
sudo mvm vm create --name myvm --image ubuntu-24.04 --network my-net --mac 02:FC:00:00:00:05

# Create VM with a specific SSH key and custom user-data
sudo mvm vm create --name myvm --image ubuntu-24.04 --ssh-key my-key --user-data ./cloud-init.yaml
```

#### `mvm vm remove`

Stop and remove a VM.

**Flags:**

| Flag | Description |
|------|-------------|
| `--name, -n NAME` | VM name (required) |
| `--force, -f` | Force kill and skip confirmation |

**Example:**

```bash
sudo mvm vm remove --name myvm
sudo mvm vm remove --name myvm --force
```

#### `mvm vm ls`

List running and stopped VMs. Also available as `mvm vm list`.

**Flags:**

| Flag | Description |
|------|-------------|
| `--all, -a` | Show all VMs including stopped |
| `--json` | Output as JSON |

**Example:**

```bash
mvm vm ls
mvm vm ls --all
mvm vm ls --json
```

#### `mvm vm ssh`

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
mvm vm ssh --name myvm

# SSH as different user
mvm vm ssh --name myvm --user ubuntu

# Run a command
mvm vm ssh --name myvm --cmd "uname -a"
```

#### `mvm vm logs`

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
mvm vm logs --name myvm --type boot

# Follow the Firecracker process log
mvm vm logs --name myvm --type os --follow

# Last 100 lines
mvm vm logs --name myvm --lines 100
```

#### `mvm vm cleanup`

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
sudo mvm vm cleanup --dry-run

# Remove stopped VMs
sudo mvm vm cleanup

# Remove all VMs
sudo mvm vm cleanup --all --force
```

#### `mvm vm snapshot` / `mvm vm load`

Create and restore VM snapshots. Requires `--enable-api-socket`.

**Flags for `mvm vm snapshot`:**

| Flag | Description |
|------|-------------|
| `--name, -n NAME` | VM name (required) |
| `--mem-out PATH` | Memory snapshot output path (required) |
| `--state-out PATH` | VM state output path (required) |

**Flags for `mvm vm load`:**

| Flag | Description | Default |
|------|-------------|---------|
| `--name, -n NAME` | VM name (required) | — |
| `--mem-in PATH` | Memory snapshot input path (required) | — |
| `--state-in PATH` | VM state input path (required) | — |
| `--resume / --no-resume` | Resume VM after loading | `--resume` |

**Example:**

```bash
# Create snapshot
mvm vm snapshot --name myvm \
  --mem-out /tmp/myvm.mem.snap \
  --state-out /tmp/myvm.state.snap

# Load snapshot (resumes automatically)
mvm vm load --name myvm \
  --mem-in /tmp/myvm.mem.snap \
  --state-in /tmp/myvm.state.snap

# Load snapshot without resuming
mvm vm load --name myvm \
  --mem-in /tmp/myvm.mem.snap \
  --state-in /tmp/myvm.state.snap \
  --no-resume
```

---

### `mvm network` — Network Management

Manage named bridge networks for VM connectivity.

| Command | Description |
|---------|-------------|
| `mvm network ls` | List all networks (alias: `list`) |
| `mvm network create NAME` | Create a named bridge network |
| `mvm network remove NAME` | Remove a named network (alias: `rm`) |
| `mvm network inspect NAME` | Show detailed information about a network |

#### `mvm network create`

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--cidr CIDR` | IP subnet in CIDR notation (required) | — |
| `--gateway IP` | Gateway IP for the bridge | first usable host in CIDR |
| `--no-nat` | Disable NAT/masquerade | NAT enabled |

#### `mvm network ls`

**Flags:**

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

#### `mvm network inspect`

**Flags:**

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

#### `mvm network remove`

**Flags:**

| Flag | Description |
|------|-------------|
| `--force, -f` | Skip confirmation |

**Examples:**

```bash
# List all networks
mvm network ls

# Create a custom network
mvm network create my-net --cidr 192.168.100.0/24

# Create a network without NAT
mvm network create isolated-net --cidr 10.50.0.0/24 --no-nat

# Inspect a network (shows bridge status, attached VMs, iptables rules)
mvm network inspect my-net

# Remove a network
mvm network remove my-net --force
```

---

### `mvm key` — SSH Key Management

Manage SSH keys used for cloud-init injection into VMs.

| Command | Description |
|---------|-------------|
| `mvm key ls` | List all keys in the cache (alias: `list`) |
| `mvm key add NAME PATH` | Import an existing public key into the cache |
| `mvm key create NAME` | Generate a new ED25519 keypair |
| `mvm key remove NAME` | Remove a key from the cache (alias: `rm`) |
| `mvm key inspect NAME` | Show detailed information about a key |

#### `mvm key add`

**Flags:**

| Flag | Description |
|------|-------------|
| `--overwrite` | Overwrite existing key with the same name |

#### `mvm key create`

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--output DIR` | Directory for the private key file | `~/.ssh/` |
| `--comment TEXT` | Comment for the key | `name@hostname` |
| `--overwrite` | Overwrite existing key files | false |

#### `mvm key ls`

**Flags:**

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

#### `mvm key inspect`

**Flags:**

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON |

#### `mvm key remove`

**Flags:**

| Flag | Description |
|------|-------------|
| `--force, -f` | Skip confirmation |

**Examples:**

```bash
# List all cached keys
mvm key ls

# Import an existing public key
mvm key add my-key ~/.ssh/id_ed25519.pub

# Generate a new keypair
mvm key create vm-key

# Generate a keypair with custom output and comment
mvm key create vm-key --output /tmp --comment "firecracker VMs"

# Inspect a key (fingerprint, algorithm, public key content)
mvm key inspect my-key

# Remove a key from cache
mvm key remove my-key --force
```

---

### `mvm config` — Configuration

Inspect and validate mvm configuration.

| Command | Description |
|---------|-------------|
| `mvm config show` | Show resolved configuration |
| `mvm config validate` | Validate config file |
| `mvm config dump-vm NAME` | Print Firecracker JSON config for a VM |

**Flags for `mvm config show`:**

| Flag | Description |
|------|-------------|
| `--section SECTION` | Show only a specific config section (e.g. `network`, `defaults`) |

**Flags for `mvm config dump-vm`:**

| Flag | Description |
|------|-------------|
| `--name NAME` | VM name (required) |

**Examples:**

```bash
# Show full resolved config
mvm config show

# Show only the network section
mvm config show --section network

# Validate the config file
mvm config validate

# Print the Firecracker JSON for an existing VM
mvm config dump-vm --name myvm
```

---

### `mvm configure` — Guided Setup Wizard

First-time setup wizard that walks through all prerequisites in one command. Each step checks whether the component is already present and skips it if so.

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--non-interactive` | Use defaults and skip all prompts | false |
| `--skip-host` | Skip the host init step (Step 1) | false |

**Steps:**

| Step | What It Does |
|------|--------------|
| [1/6] Privilege setup | Runs `sudo mvm host init` (group, sudoers, KVM) — skippable with `--skip-host` |
| [2/6] Firecracker binary | Downloads the latest Firecracker release if none is cached |
| [3/6] Kernel | Builds the default minimal kernel (v6.1.102) if none is cached |
| [4/6] Image | Downloads a root filesystem image (interactive menu or first available) |
| [5/6] SSH key | Generates an ED25519 keypair or imports an existing public key |
| [6/6] Summary | Prints a status table showing which components are ready vs missing |

**Examples:**

```bash
# Interactive wizard — prompts at each step
mvm configure

# Fully automated — downloads defaults, no prompts
mvm configure --non-interactive

# Skip host init (useful when re-running after group membership is active)
mvm configure --skip-host

# Fully automated, skip host init
mvm configure --non-interactive --skip-host
```

---

## Configuration Reference

### Config File Location

`mvm` looks for a config file in this order (first match wins):

1. Path specified in the `MVM_CONFIG` environment variable
2. `./mvm.yaml` in the current working directory
3. `~/.config/mvmctl/config.yaml`

### All Config Keys with Defaults

```yaml
# Firecracker runtime settings
firecracker:
  binary: /usr/local/bin/firecracker   # Path to firecracker binary
  socket_dir: /tmp/mvm/sockets         # Directory for API sockets
  run_dir: /tmp/mvm/run                # Runtime directory
  log_dir: /tmp/mvm/logs               # Log directory

# Default values for new VMs
vm_defaults:
  vcpu_count: 2                        # Default vCPUs
  mem_size_mib: 2048                   # Default memory in MiB
  network_interface: eth0
  boot_args: "console=ttyS0 reboot=k panic=1 pci=off"
  disk_size: "2G"
  enable_api_socket: false
  enable_pci: false
  lsm_flags: "landlock,lockdown,yama,integrity,selinux,bpf"

# Network topology
network:
  single_vm:
    tap_dev: "mvm-tap0"
    guest_ip: "10.10.0.2"
    host_ip: "10.10.0.1"
    mask: "255.255.255.252"
    mac: "02:FC:00:00:00:01"
  vm_network:
    bridge_name: "mvm-br0"
    bridge_ip: "10.10.0.1/24"
    guest_ip_start: "10.10.0.2"
    guest_ip_end: "10.10.0.254"
    tap_prefix: "mvm"

# Path overrides
paths:
  assets_dir: "../assets"
  single_vm_dir: "../single-vm"
```

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CONFIG` | Override config file path | `~/.config/mvmctl/config.yaml` |
| `MVM_CACHE_DIR` | Override cache directory | `~/.cache/mvmctl` |
| `MVM_LOG_LEVEL` | Log verbosity: `DEBUG`, `INFO`, `WARNING`, `ERROR` | `INFO` |
| `MVM_KERNEL` | Override kernel path (used by `vm create`) | (from config) |
| `MVM_FIRECRACKER_BIN` | Override Firecracker binary path | `firecracker` |

Note: the `MVM_` prefix is derived from the CLI name defined in `constants.py`, which in turn is driven by the project name in `pyproject.toml`.

---

## Building from Source

To build a standalone single-file binary that requires no Python at runtime:

```bash
# Clone the repository
git clone https://github.com/your-org/firecracker-manager
cd firecracker-manager

# Install Python 3.13 and build dependencies
pip install -e ".[dev]" pyinstaller

# Build the binary
pyinstaller --onefile --name mvm src/mvmctl/main.py

# Output location
ls dist/mvm

# Verify the build
./dist/mvm --version
./dist/mvm --help
```

The project name is defined once in `pyproject.toml` under `[project] name`. Changing it there automatically updates the CLI name, environment variable prefix (`MVM_`), cache directory name, and network device prefixes — no grep-and-replace required. To produce a renamed binary, update `pyproject.toml` and re-run the `pyinstaller` command with `--name <new-name>`.

---

## Cache Directory Structure

Everything `mvm` downloads or generates lives under `~/.cache/mvmctl/` (overridable with `MVM_CACHE_DIR`):

```
~/.cache/mvmctl/
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
        ├── firecracker.sock          # API socket (only if --enable-api-socket)
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

**"Bridge mvm-br0 not found" or "No such device"**

The network bridge hasn't been created yet, or it was lost on reboot.

```bash
# The bridge is auto-created when you create a VM:
sudo mvm vm create --name myvm --image ubuntu-24.04
```

**"Kernel not found at ..."**

You need to fetch or build a kernel first.

```bash
# Download prebuilt minimal kernel
mvm kernel fetch

# Or build from source (takes 10-20 minutes)
mvm kernel build
```

**VM isn't booting / SSH times out**

Cloud-init runs on first boot and takes 30-60 seconds. Check the console log:

```bash
mvm vm logs --name myvm --type boot --follow
```

Look for `mvm cloud-init done` and a `login:` prompt. If it never gets there, check the Firecracker process log:

```bash
mvm vm logs --name myvm --type os
```

**"SSH connection refused" immediately**

The VM's SSH daemon hasn't started yet. Wait a bit longer. If it still fails after 2 minutes, the VM might have panicked. Check:

```bash
mvm vm logs --name myvm --type boot
```

**"Image not found: ubuntu-24.04"**

Fetch the image first:

```bash
mvm image fetch ubuntu-24.04
mvm image ls  # Confirm the ✓ appears
```

**"Firecracker binary not found"**

Either install Firecracker on your `$PATH`, or download a versioned binary via `mvm`:

```bash
mvm bin fetch 1.12.0
mvm bin use 1.12.0
# Or point directly at a binary:
sudo mvm vm create --name myvm --image ubuntu-24.04 --firecracker-bin /path/to/firecracker
```

**"host init has not been run"**

`mvm host reset` requires a prior snapshot. Run `sudo mvm host init` first.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, code style guide, testing instructions, and PR process.

---

## License

MIT. See [LICENSE](LICENSE).
