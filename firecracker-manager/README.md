# firecracker-manager (fcm)

A Python CLI tool for managing Firecracker microVMs on Linux.

![CI](https://github.com/your-org/firecracker-manager/actions/workflows/ci.yml/badge.svg)

## What It Is

`fcm` replaces a collection of bash scripts with a production-quality CLI for running Firecracker microVMs. It handles the full lifecycle: downloading images, building kernels, setting up bridge networking, creating VMs, SSH access, logs, snapshots, and cleanup. The target environment is a Linux host with KVM and a multi-VM bridge network (`fc-br0`, `10.20.0.0/24`).

Built with Python 3.13, Typer, and Rich.

---

## Prerequisites

Before you start, make sure you have:

- **Linux** (x86_64 or aarch64) — Firecracker only runs on Linux
- **KVM access** — `/dev/kvm` must be readable by your user. Check with `ls -la /dev/kvm`
- **Python 3.13+** — check with `python3 --version`
- **uv** — the package manager used to install `fcm`
- **Firecracker binary** — download from [github.com/firecracker-microvm/firecracker/releases](https://github.com/firecracker-microvm/firecracker/releases) and put it on your `$PATH`
- **qemu-img** — needed for image conversion. Install with `sudo apt install qemu-utils`
- **Root access** — network operations (TAP devices, bridge setup, iptables) require `sudo`

### Check KVM access

```bash
ls -la /dev/kvm
# If you see "permission denied", add yourself to the kvm group:
sudo usermod -aG kvm $USER
# Then log out and back in.
```

---

## Installation

```bash
# Clone the repo
git clone <repo-url>
cd firecracker-manager

# Install with uv (creates .venv and installs the fcm binary)
uv sync
uv run fcm --help

# Or install globally into your current Python environment
pip install -e .
fcm --help
```

After `uv sync`, the `fcm` binary lives at `.venv/bin/fcm`. You can either use `uv run fcm` or activate the venv manually:

```bash
source .venv/bin/activate
fcm --help
```

---

## Quick Start

This walks through creating your first VM from scratch.

**Step 1: Download a VM image**

```bash
fcm image fetch ubuntu-24.04
```

This downloads the Ubuntu 24.04 cloud image and converts it to a raw ext4 filesystem. It ends up in `~/.cache/firecracker-manager/images/`.

**Step 2: Build a kernel**

```bash
fcm kernel build
```

This downloads the Linux kernel source and builds a minimal kernel configured for Firecracker. Takes 10-20 minutes depending on your machine. The output goes to `~/.cache/firecracker-manager/kernels/vmlinux`.

If you already have a kernel binary, you can skip this step and point `FCM_FIRECRACKER_KERNEL` at it instead.

**Step 3: Set up the network bridge**

```bash
sudo fcm vm setup
```

Creates the `fc-br0` bridge and sets up NAT so VMs can reach the internet. Run this once per host boot. It gets torn down on reboot.

**Step 4: Create your first VM**

```bash
sudo fcm vm create --name myvm --image ubuntu-24.04 --vcpus 1 --mem 512
```

This creates a new VM called `myvm` with 1 vCPU and 512 MiB of RAM, boots it, and waits for it to come up. The VM gets an IP from the `10.20.0.0/24` subnet.

**Step 5: Wait for it to boot**

Give it 30-60 seconds. Cloud-init runs on first boot and configures SSH keys. You can watch progress:

```bash
fcm vm logs myvm --follow
```

**Step 6: SSH into the VM**

```bash
fcm vm ssh myvm
```

This finds the VM's IP and SSHes in using the auto-generated key from `~/.cache/firecracker-manager/keys/`.

**Step 7: Check what's running**

```bash
fcm vm list
```

**Step 8: Delete the VM when done**

```bash
sudo fcm vm delete --name myvm
```

---

## Configuration

`fcm` works out of the box with no config file. For custom defaults, create `~/.config/fcm/config.yaml`:

```yaml
vm_defaults:
  vcpu_count: 2
  mem_size_mib: 512

networking:
  multi_vm:
    bridge: fc-br0
    gateway: 10.20.0.1
    subnet: 10.20.0.0/24
```

Show the current resolved config:

```bash
fcm config show
```

Validate your config file:

```bash
fcm config validate
```

---

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `FCM_CACHE_DIR` | Override the cache directory | `~/.cache/firecracker-manager` |
| `FCM_CONFIG_FILE` | Override the config file path | `~/.config/fcm/config.yaml` |
| `FCM_LOG_LEVEL` | Log verbosity: `DEBUG`, `INFO`, `WARNING`, `ERROR` | `INFO` |
| `FCM_FIRECRACKER_BIN` | Path to the Firecracker binary | auto-detected from `$PATH` |

---

## Complete Command Reference

### `fcm vm` — VM Lifecycle

| Command | Description |
|---|---|
| `fcm vm setup` | Create the `fc-br0` bridge and NAT rules. Run once per boot. Requires root. |
| `fcm vm create` | Create and start a VM. See options below. |
| `fcm vm delete` | Stop and remove a VM. |
| `fcm vm list` | List all VMs and their status. |
| `fcm vm ssh` | SSH into a VM. |
| `fcm vm logs` | View VM logs (boot log or OS console). |
| `fcm vm cleanup` | Remove all stopped VMs. |
| `fcm vm pause` | Pause a running VM (freeze execution). |
| `fcm vm resume` | Resume a paused VM. |
| `fcm vm snapshot` | Create a snapshot of a running VM. |
| `fcm vm load` | Load a VM from a previously saved snapshot. |

**`fcm vm create` options:**

```bash
sudo fcm vm create \
  --name myvm \            # VM name (required)
  --image ubuntu-24.04 \   # Image ID (required)
  --vcpus 2 \              # Number of vCPUs (default: 1)
  --mem 1024 \             # Memory in MiB (default: 512)
  --ip 10.20.0.5           # Static IP (optional, auto-assigned if omitted)
```

**`fcm vm delete` options:**

```bash
sudo fcm vm delete --name myvm
sudo fcm vm delete --name myvm --force  # Skip confirmation
```

**`fcm vm list` options:**

```bash
fcm vm list           # Show running VMs
fcm vm list --all     # Show all VMs including stopped
fcm vm list --json    # Machine-readable JSON output
```

**`fcm vm ssh` options:**

```bash
fcm vm ssh myvm
fcm vm ssh myvm --user ubuntu   # Default user is root
fcm vm ssh 10.20.0.5            # Connect by IP directly
```

**`fcm vm logs` options:**

```bash
fcm vm logs myvm
fcm vm logs myvm --type boot       # Serial console output (what you see during boot)
fcm vm logs myvm --type os         # Firecracker process log (hypervisor events)
fcm vm logs myvm --lines 100       # Last N lines
fcm vm logs myvm --follow          # Tail the log
```

**`fcm vm cleanup` options:**

```bash
sudo fcm vm cleanup               # Remove stopped VMs (with confirmation)
sudo fcm vm cleanup --all         # Remove all VMs
sudo fcm vm cleanup --dry-run     # Preview what would be removed
```

---

### `fcm image` — Image Management

| Command | Description |
|---|---|
| `fcm image fetch IMAGE_ID` | Download and prepare an image for use |
| `fcm image fetch-all` | Download all available images |
| `fcm image list` | List available images. Shows a checkmark if cached locally. |
| `fcm image convert INPUT OUTPUT` | Convert an image to a different format |
| `fcm image delete IMAGE_ID` | Delete a cached image |

**Examples:**

```bash
fcm image fetch ubuntu-24.04
fcm image fetch ubuntu-24.04 --out /custom/path
fcm image list
fcm image convert my-image.qcow2 my-image.ext4
fcm image delete ubuntu-24.04
```

---

### `fcm kernel` — Kernel Management

| Command | Description |
|---|---|
| `fcm kernel build` | Build a Linux kernel optimized for Firecracker |
| `fcm kernel list` | List kernels in the cache directory |
| `fcm kernel clean` | Remove kernel build artifacts (frees disk space) |

**Examples:**

```bash
fcm kernel build                          # Build default version
fcm kernel build --version 6.1.102        # Build a specific version
fcm kernel build --out ~/.cache/fcm/kernels/custom-vmlinux
fcm kernel list
fcm kernel clean
```

The build pulls the kernel tarball from kernel.org, applies Firecracker's recommended config, and compiles it. You need `gcc`, `make`, `bc`, `flex`, `bison`, and `libelf-dev` installed.

---

### `fcm config` — Configuration

| Command | Description |
|---|---|
| `fcm config show` | Show the full resolved configuration |
| `fcm config show --section networking` | Show a specific section |
| `fcm config validate` | Validate the config file and report errors |
| `fcm config dump-vm NAME` | Print the Firecracker JSON config for a VM |

---

## Cache Directory Structure

Everything `fcm` downloads or generates goes into `~/.cache/firecracker-manager/` (overridable with `FCM_CACHE_DIR`):

```
~/.cache/firecracker-manager/
├── images/                    # Downloaded and converted VM images
│   └── ubuntu-24.04.ext4
├── kernels/                   # Built or downloaded kernels
│   └── vmlinux
├── keys/                      # Auto-generated SSH keypairs
│   ├── id_ed25519
│   └── id_ed25519.pub
└── vms/                       # VM runtime state
    ├── state.json             # Registry of all known VMs
    └── myvm/                  # One directory per VM
        ├── rootfs.ext4        # VM's private root filesystem copy
        ├── firecracker.json   # Firecracker machine config
        ├── firecracker.log    # Firecracker process log
        └── firecracker.console.log  # VM serial console output
```

The project directory itself stays clean. No runtime files are written here.

---

## Troubleshooting

**"Permission denied: /dev/kvm"**

Your user isn't in the `kvm` group. Fix it:
```bash
sudo usermod -aG kvm $USER
# Log out and back in, then verify:
groups | grep kvm
```

**"Bridge fc-br0 not found" or "No such device"**

The network bridge hasn't been created yet, or it was lost on reboot.
```bash
sudo fcm vm setup
```

**"Kernel not found at ..."**

You need to build or download a kernel first.
```bash
fcm kernel build
# Takes 10-20 minutes. Get a coffee.
```

**VM isn't booting / SSH times out**

Cloud-init runs on first boot and takes 30-60 seconds. Check the console log:
```bash
fcm vm logs myvm --type boot --follow
```
Look for cloud-init finishing and `login:` appearing. If it never gets there, check the Firecracker process log:
```bash
fcm vm logs myvm --type os
```

**"SSH connection refused" immediately**

The VM's SSH daemon hasn't started yet. Wait a bit longer. If it still fails after 2 minutes, the VM might have panicked. Check `fcm vm logs myvm --type boot`.

**"Image not found: ubuntu-24.04"**

Fetch the image first:
```bash
fcm image fetch ubuntu-24.04
fcm image list  # Confirm it shows a checkmark
```

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, code style guide, and PR process.

---

## License

MIT. See [LICENSE](LICENSE).
