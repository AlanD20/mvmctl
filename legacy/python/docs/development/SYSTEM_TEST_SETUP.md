# System Test Setup

**Purpose:** Define the hardware, software, and configuration required to run
the complete mvmctl system test suite with zero skips on a dedicated test machine.

System tests (`tests/system/`) are black-box CLI subprocess tests that operate
against real infrastructure — real kernels, images, bridges, iptables rules,
and SQLite state. They are the **primary release gate**: a domain is not
production-ready until its system tests pass on real hardware.

---

## Table of Contents

1. [Hardware Requirements](#1-hardware-requirements)
2. [Software Dependencies](#2-software-dependencies)
3. [User & Group Setup](#3-user--group-setup)
4. [Asset Mirror (Recommended)](#4-asset-mirror-recommended)
5. [Running the Suite](#5-running-the-suite)
6. [Verification Checklist](#6-verification-checklist)
7. [Troubleshooting](#7-troubleshooting)

---

## 1. Hardware Requirements

| Component | Requirement | Verification |
|-----------|-------------|--------------|
| CPU | x86_64 with VMX/SVM extensions | `egrep -c '(vmx|svm)' /proc/cpuinfo` → > 0 |
| RAM | 8 GB minimum (16 GB recommended) | `free -g` |
| Disk | 20 GB free for assets (images, kernels, binaries) | `df -h ~/.cache/mvmctl` |
| KVM | `/dev/kvm` accessible | `test -c /dev/kvm && echo OK` |
| Network | Outbound HTTP/HTTPS for asset downloads | `curl -sI https://example.com` |

---

## 2. Software Dependencies

### 2.1 System Packages

Packages are provided for both **Debian/Ubuntu** (`apt-get`) and **Arch Linux** (`pacman`).

<details>
<summary><b>Debian / Ubuntu (apt-get)</b></summary>

```bash
# Base tooling
sudo apt-get install -y \
  iproute2 iptables procps kmod sudo \
  genisoimage cloud-image-utils \
  squashfs-tools util-linux tar \
  openssh-client coreutils curl

# Image import tests (qcow2 conversion, ext4 formatting, compression)
sudo apt-get install -y \
  qemu-utils \
  e2fsprogs \
  zstd

# Kernel build tests (optional — only if running kernel_build marker)
sudo apt-get install -y \
  build-essential \
  linux-headers-$(uname -r)
```
</details>

<details>
<summary><b>Arch Linux (pacman)</b></summary>

```bash
# Base tooling
sudo pacman -S --needed \
  iproute2 iptables procps-ng kmod sudo \
  libisoburn cloud-image-utils \
  squashfs-tools util-linux tar \
  openssh coreutils curl

# Image import tests (qcow2 conversion, ext4 formatting, compression)
sudo pacman -S --needed \
  qemu-img \
  e2fsprogs \
  zstd

# Kernel build tests (optional — only if running kernel_build marker)
sudo pacman -S --needed \
  base-devel \
  linux-headers
```
</details>

### 2.2 Runtime Dependencies

| Dependency | Required By | Purpose | Verification |
|---|---|---|---|
| `qemu-img` | Image import tests | Convert raw ↔ qcow2 | `qemu-img --version` |
| `mkfs.ext4` | Image import tests | Format ext4 filesystems | `mkfs.ext4 -V` |
| `truncate` | Image import tests | Create sparse files | `truncate --version` |
| `zstd` | Image decompression | Decompress `.zst` images | `zstd --version` |
| `genisoimage` | Cloud-init ISO mode | Create cloud-init ISOs | `genisoimage --version` |
| `ssh-keygen` | SSH key tests | Generate SSH keys | `ssh-keygen -A` (checks) |
| `ip` (iproute2) | Network tests | Bridge, addr, link management | `ip link show` |
| `nft` / `iptables` | Network tests | Firewall rule verification | `sudo nft --version` or `sudo iptables --version` |

### 2.3 Python Toolchain

```bash
# Install uv (if not already present)
curl -LsSf https://astral.sh/uv/install.sh | sh

# Sync project dependencies
uv sync --group dev --group build

# Build the mvm-services binary and the onefile mvm binary (required for host clean/reset tests)
python scripts/build_services.py
cp dist/mvm ~/.local/bin/mvm
```

---

## 3. User & Group Setup

```bash
# Create mvm group (if not exists)
sudo groupadd --force mvm

# Add user to required groups
sudo usermod -aG mvm $USER
sudo usermod -aG kvm $USER
sudo usermod -aG disk $USER        # /dev/loop* access for loop-mount

# Log out and back in for group changes to take effect
# Verify:
groups
# Should show: mvm kvm disk
```

### 3.1 One-Time Initialization

```bash
# Initialize mvmctl (creates DB, caches, iptables chains)
# Use the built binary for proper sudo handling:
sudo ~/.local/bin/mvm init

# Verify initialization:
mvm host status --json
```

### 3.2 Sudo Configuration

The mvm application handles privilege escalation internally via `run_cmd()`/
`stream_cmd()`. The following must be configured:

```bash
# Passwordless sudo for mvm group
echo "%mvm ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/mvm
sudo chmod 440 /etc/sudoers.d/mvm
```

---

## 4. Asset Mirror (Recommended)

An asset mirror caches downloaded kernels, images, and binaries so repeated
test runs don't re-download large files (43 MB kernel, 203 MB Alpine image,
220 MB Ubuntu image).

```bash
# Set up the mirror path
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mkdir -p "$MVM_ASSET_MIRROR"

# Seed the mirror with commonly-used assets
# (these will be pulled on first test run, then cached)
```

The mirror is automatically populated by `utils/http.py` — assets downloaded
during test runs are copied into the mirror. Subsequent runs use the local
copy. The mirror lives **outside** `~/.cache/mvmctl/` so `cache clean --force`
does not wipe it.

---

## 5. Running the Suite

### 5.1 Per-File Execution (Required)

System tests are **stateful**. Running `pytest tests/system/` as a single batch
causes cross-file state pollution (VMs, bridges, iptables). Each file MUST be
run individually. See [ADR 0007](/docs/adr/0007-system-test-execution-strategy.md).

### 5.2 Using the Unified Test Runner

```bash
# Run ALL system tests
uv run scripts/run_tests.py --system

# Build onefile binary first, then run (faster, sudo handled internally)
uv run scripts/run_tests.py --system --build

# Run a specific domain
uv run scripts/run_tests.py --system --domain vm

# Run a single test file
uv run scripts/run_tests.py --system --test tests/system/network/test_network.py

# Re-run only previously failed tests
uv run scripts/run_tests.py --system --failed-only

# Run all three levels (unit, integration, system)
uv run scripts/run_tests.py
```

The script:
1. Seeds the asset mirror at `~/.cache/mvm-asset-mirror/` if empty
2. Builds `dist/mvm` if `--build` is passed
3. Runs each test file one by one with `-n 0` (serial)
4. Runs the skip-ratio gate (`scripts/check_skip_ratio.py`) after completion
5. Saves results to `.reports/system-test-results-latest.txt`

### 5.3 Marker-Based Filtering

```bash
# Exclude slow tests (>30s each)
uv run pytest tests/system/vm/ -n 0 -m "not slow"

# Run only kernel build tests (requires build tools)
uv run pytest tests/system/kernel/ -n 0 -m kernel_build

# Run only destructive host tests (requires explicit opt-in)
uv run pytest tests/system/host/ -n 0 -m host_reset

# Run everything except kernel builds
uv run scripts/run_tests.py --system -- -m "not kernel_build"
```

### 5.4 Manual Per-File

```bash
uv run scripts/run_tests.py --system --test tests/system/network/test_network.py

# Directly with pytest (fallback):
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror MVM_BINARY=dist/mvm \
  uv run pytest tests/system/network/test_network.py -n 0
```

---

## 6. Running Inside a Firecracker VM (Nested Virtualization)

The system test suite can run inside a Firecracker microVM that itself has
nested KVM enabled. This is useful for CI/CD isolation or testing in
ephemeral environments.

mvmctl supports nested virtualization via the `--nested-virt` flag on
`mvm vm create`. When enabled, it:

- Sends a `cpu-config` to Firecracker with `kvm_capabilities: []` (preserving
  all default KVM capabilities including nested virt)
- Adds `kvm-intel.nested=1` or `kvm-amd.nested=1` to the guest kernel boot args
  (auto-detected from `host_state.cpu_vendor`)
- Forces `pci_enabled=true` (required for nested virt)
- Supports custom CPU templates via `--cpu-template PATH` which are deep-merged
  with the nested_virt base configuration

The global default is controlled by `defaults.vm.nested_virt = false` in the
mvmctl config system (settable via `mvm config set defaults.vm nested_virt true`).

### 6.1 Prerequisites

| Requirement | Detail |
|---|---|
| Host CPU | Must support VMX (Intel) or SVM (AMD) — `egrep -c '(vmx\|svm)' /proc/cpuinfo` > 0 |
| Host kernel | Must have `kvm_intel.nested=1` or `kvm_amd.nested=1` — check with `cat /sys/module/kvm_intel/parameters/nested` → should print `Y` |
| Firecracker version | ≥ 1.5.0 (kvm_capabilities added in v1.5.0; we use v1.15.1) |
| Guest kernel | Must have `CONFIG_KVM_INTEL` or `CONFIG_KVM_AMD` built in (our firecracker kernel includes these) |
| Guest resources | At minimum 4 vCPUs and 4 GB RAM to run VMs inside the guest |

### 6.2 Enable Nested Virtualization on the Host

```bash
# Check current state
cat /sys/module/kvm_intel/parameters/nested   # Intel
# or
cat /sys/module/kvm_amd/parameters/nested      # AMD

# If disabled, enable it (persistent: add to /etc/modprobe.d/kvm.conf):
sudo modprobe -r kvm_intel
sudo modprobe kvm_intel nested=1

# Verify
cat /sys/module/kvm_intel/parameters/nested   # should print Y
```

### 6.3 Create the Firecracker Guest VM

The guest VM needs to be configured with sufficient resources and KVM access.
Use an Ubuntu or Alpine image with development tools pre-installed.

#### Via mvmctl (recommended)

```bash
# 1. Create a network for the test runner VM
mvm network create testrunner-net --subnet 10.77.0.0/24

# 2. Create the test runner VM with ENOUGH resources and nested virt enabled
#    --vcpus 4, --mem 4096 to give the guest room to spawn nested VMs
#    --nested-virt enables KVM passthrough and adds kvm-intel.nested=1 to boot args
mvm vm create testrunner \
  --image ubuntu:24.04 \
  --network testrunner-net \
  --vcpus 4 \
  --mem 4096 \
  --disk-size 20G \
  --ssh-key my-key \
  --nested-virt

# 3. Provision inside the guest
mvm ssh testrunner -u ubuntu --cmd "
  sudo apt-get update
  sudo apt-get install -y iproute2 iptables procps kmod sudo \
    genisoimage cloud-image-utils squashfs-tools util-linux tar \
    openssh-client coreutils curl qemu-utils e2fsprogs zstd
"

# 4. Copy the mvm binary and source into the guest
mvm ssh testrunner -u ubuntu --cmd "git clone <repo-url> && cd mvmctl && uv sync --group dev"
```

To verify nested virtualization is working inside the Firecracker VM:

```bash
# Inside the guest, verify /dev/kvm is accessible
test -c /dev/kvm && echo "KVM available"

# Check the nested virt kernel param took effect
cat /sys/module/kvm_intel/parameters/nested   # should print Y

# Check CPU flags include vmx/svm
grep -o 'vmx\|svm' /proc/cpuinfo | sort -u   # should show vmx or svm
```

#### Via Firecracker directly (advanced)

```bash
# Boot a Firecracker guest with nested KVM enabled.
# The guest kernel must be booted with kvm-intel.nested=1
# and the KVM capabilities must be set via PUT /cpu-config (NOT machine-config).

# The cpu-config endpoint accepts:
# {
#   "kvm_capabilities": []     # preserve all default KVM capabilities
# }
#
# An empty kvm_capabilities list means "do not remove any capabilities
# from Firecracker's default check list". It does NOT grant full KVM
# access — Firecracker's default capabilities already cover the essentials.
# For nested virtualization, the guest kernel additionally needs:
# - kvm-intel.nested=1 on the kernel cmdline (Intel)
# - kvm-amd.nested=1 on the kernel cmdline (AMD)
#
# mvmctl handles both the cpu-config and the boot args automatically
# when --nested-virt is used. This section is for manual/advanced use only.
```

### 6.4 Inside the Guest: Setup

Once inside the guest VM, follow the same setup as a bare-metal machine:

```bash
# Add user to mvm group
sudo groupadd --force mvm
sudo usermod -aG mvm $USER

# Initialize mvmctl (inside the guest — this creates iptables chains,
# cache directories, and the SQLite database)
sudo ~/.local/bin/mvm host init

# Verify KVM is accessible inside the guest
test -c /dev/kvm && echo "KVM available inside guest"

# Verify nested virtualization works
cat /sys/module/kvm_intel/parameters/nested   # should print Y
```

### 6.5 Running the Tests Inside the Guest

```bash
# Run the full suite inside the guest (same commands as bare metal)
uv run scripts/run_tests.py --system --build
```

### 6.6 Resource Considerations

| Resource | Minimum | Recommended | Why |
|---|---|---|---|
| Guest vCPUs | 4 | 8 | Each nested VM consumes 1-2 vCPUs |
| Guest RAM | 4 GB | 8 GB | Each nested VM needs 256-1024 MB |
| Guest disk | 20 GB | 40 GB | Assets (kernel, images) + build artifacts |
| Host RAM | 16 GB | 32 GB | Host + guest + guest's VMs |

### 6.7 Known Limitations

- **Performance**: Running VMs inside VMs (L2 guests) is ~20-50% slower than
  bare metal due to nested VM-exit overhead.
- **Kernel build tests**: Building kernels inside a nested VM is extremely
  slow (>30 min). Use `--kernel_build` marker to exclude these:
  `uv run scripts/run_tests.py --system -- -m "not kernel_build"`.
- **`/dev/kvm` ownership**: The guest user must have access to `/dev/kvm`.
  If using a Firecracker guest, the device is owned by root:root by default.
  Fix with `sudo chmod 666 /dev/kvm` or add udev rules.
- **Assetc mirror**: Strongly recommended inside nested VMs to avoid
  re-downloading assets on every test run. Set `MVM_ASSET_MIRROR` to a
  host-mounted volume.

---

## 7. Verification Checklist

Run these commands to verify the test machine is ready:

```bash
# === Hardware ===
echo "KVM:        $(test -c /dev/kvm && echo OK || echo MISSING)"
echo "VMX/SVM:    $(egrep -c '(vmx|svm)' /proc/cpuinfo) cores"
echo "Memory:     $(free -g | awk '/Mem:/{print $2}') GB"

# === Groups ===
echo "Groups:     $(groups)"
echo "mvm group:  $(getent group mvm >/dev/null && echo OK || echo MISSING)"

# === Tools ===
for tool in qemu-img mkfs.ext4 truncate zstd genisoimage ssh-keygen ip nft; do
  echo "$tool:      $(which $tool 2>/dev/null && echo OK || echo MISSING)"
done

# === Python ===
echo "uv:         $(uv --version 2>/dev/null || echo MISSING)"

# === mvm init ===
echo "mvm init:   $(~/.local/bin/mvm host status --json 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print("OK" if d.get("kvm_accessible") else "INITIALIZED")' 2>/dev/null || echo NOT INITIALIZED)"

# === Binary ===
echo "mvm binary: $(test -f ~/.local/bin/mvm && echo OK || echo MISSING)"
echo "services:   $(python3 -c "
import json, subprocess, shlex
r = subprocess.run(['uv','run','mvm','bin','ls','--json'], capture_output=True, text=True, timeout=30)
if r.returncode == 0:
    bins = json.loads(r.stdout)
    for name in ['mvm-provision','mvm-console-relay','mvm-nocloud-server']:
        print(f'  {name}: {\"OK\" if any(b.get(\"name\")==name for b in bins) else \"MISSING\"}')" 2>/dev/null || echo "  (check failed)")"
```

---

## 8. Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `iptables` errors in tests | Firewall not initialized | `sudo ~/.local/bin/mvm host init` |
| VM creation hangs | Onefile binary missing embedded services | Build with `--build` or `python scripts/build_services.py` |
| `mvm` group errors | User not in group | `sudo usermod -aG mvm $USER` then log out/back in |
| `/dev/loop*` permission denied | User not in `disk` group | `sudo usermod -aG disk $USER` |
| Address already in use | Stale VMs from previous run | `mvm vm ls --json` then `mvm vm rm <name> --force` |
| mvm-provision not found | Service binaries missing from cache | Run `cache init` or `python scripts/build_services.py` |
| Console relay spawn failure | Nuitka temp dir cleanup | Use `--build` or ensure symlink in bin cache |
| Kernel build >10 min | Full kernel compilation from source | Use `--keep-build-dir` for incremental builds, or use firecracker kernels (default) |
| Skip ratio >10% on CI | Missing dependencies | Run verification checklist (section 6) |
| `qemu-img` not found | qemu-utils/not installed | Debian: `sudo apt-get install qemu-utils` Arch: `sudo pacman -S qemu-img` |
| `mkfs.ext4` not found | e2fsprogs not installed | Debian: `sudo apt-get install e2fsprogs` Arch: `sudo pacman -S e2fsprogs` |
| `zstd` decompress fails | zstd not installed | Debian: `sudo apt-get install zstd` Arch: `sudo pacman -S zstd` |
| `genisoimage` not found | Not installed | Debian: `sudo apt-get install genisoimage` Arch: `sudo pacman -S libisoburn` |
| `cloud-image-utils` not found | Not installed | Debian: `sudo apt-get install cloud-image-utils` Arch: `yay -S cloud-image-utils` (AUR) |
| Network-dependent tests skip | No outbound HTTP access | Configure `MVM_ASSET_MIRROR` with pre-seeded assets |

---

## Related Documents

- [ADR 0007: System Test Execution Strategy](../adr/0007-system-test-execution-strategy.md) — per-file execution mandate
- [ADR 0016: System Test Coverage Standard](../adr/0016-system-test-coverage-standard.md) — depth standard, skip discipline
- [HOW_AGENTS_WRITE_TESTS.md](HOW_AGENTS_WRITE_TESTS.md) — scenario catalogs, test writing rules
- [COVERAGE_MATRIX.md](../../tests/system/COVERAGE_MATRIX.md) — accountability matrix, current coverage status
