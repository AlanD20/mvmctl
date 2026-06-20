# How Agents Run System Tests

## Purpose

This is the **authoritative execution plan** for running the mvmctl system test
suite and qualifying a release. An AI agent can start from a fresh clone and
follow this guide linearly without cross-referencing other docs.

**Do NOT deviate** from the commands below without approval. All paths,
environment variables, and flags are intentional.

---

## Table of Contents

1. [Prerequisite Check](#1-prerequisite-check)
2. [Build the Binary](#2-build-the-binary)
3. [Deploy the Environment](#3-deploy-the-environment)
4. [Run System Tests Inside the VM](#4-run-system-tests-inside-the-vm)
5. [Interpret Results](#5-interpret-results)
6. [Collect Release Evidence](#6-collect-release-evidence)
7. [Reference](#7-reference)

---

## 1. Prerequisite Check

**Two layers of prerequisites:**
- **Host** (your machine): Must be able to run Firecracker and build mvm.
- **rc-vm** (outer VM): Automatically provisioned by `rc-env.yaml` — you don't
  need to install anything on the host beyond what's listed below.

### rc-vm is a Full Bare-Metal Host

rc-vm is **not a limited container or lightweight sandbox**. It is a full
Firecracker microVM with `nested_virt: true` and **direct KVM access** to the
host CPU virtualization extensions. From the perspective of mvm and the system
tests, rc-vm behaves **identically to bare metal**:

- `/dev/kvm` is fully accessible — mvm creates and runs nested VMs inside rc-vm
- `iptables`/`nftables` work — rc-vm has its own network stack with NAT
- `sudo`, `docker`, kernel module loading all work
- The 7.0.11 kernel has everything compiled in (no modules needed)
- Pipewire, tmpfs, overlayfs, btrfs, ext4 — all standard Linux facilities

The only limitations are those imposed by the kernel itself (which is a standard
upstream build with common features enabled). If Linux supports it and the
kernel is compiled with it, rc-vm supports it.

### Special Case: libguestfs / Supermin

The `test_create_guestfs_backend` test requires guestfish (libguestfs) to work.
guestfish uses `supermin` to build a small appliance VM. Supermin needs:

1. A kernel in `/boot/` with a parseable version (e.g., `vmlinuz-6.8.0-124-generic`)
2. Kernel modules in `/lib/modules/<version>/`
3. The kernel file must be world-readable (supermin runs as the `runner` user, not root)

The rc-vm runs a custom kernel (7.0.11) loaded externally by Firecracker — there
is no kernel package installed and `/boot/` is empty. Supermin cannot build its
appliance without a kernel.

**Fix:** Install `linux-image-kvm` (a lightweight kernel package for KVM guests)
and fix permissions:

```bash
apt-get install -y linux-image-kvm
chmod 644 /boot/vmlinuz-*
```

The `rc-env.yaml` has a dedicated `install guestfs kernel` SSH step that does
this. The dpkg trigger error about `initramfs-tools` / `packagekit.service` is
cosmetic (can't run in a Firecracker VM) — the kernel files are placed correctly
and guestfish works.

**Verify guestfish works:**
```bash
guestfish -a /dev/null run
```
Exit code 0 = success.

**Note:** Even with guestfish working, the `test_create_guestfs_backend` test may
fail due to a pre-existing bug in the Go guestfish invocation
(`guestfish session failed: incorrect number of arguments`). The test was
previously skipped (masking the bug). This is a separate issue from the supermin
kernel problem documented above.

### 1.1 Host: Hardware

```bash
# KVM (required — host runs Firecracker for rc-vm)
test -c /dev/kvm && echo "KVM: OK" || echo "KVM: MISSING"

# Virtualization extensions
egrep -c '(vmx|svm)' /proc/cpuinfo
# Expected: > 0

# Memory (total)
free -g | awk '/Mem:/{print $2 " GB"}'
# Expected: >= 8 (16 recommended)

# Disk (free)
df -h ~/.cache/mvmctl 2>/dev/null || echo "~/.cache/mvmctl does not exist yet"
# Expected: >= 20 GB free
```

### 1.2 Host: Software

```bash
# Go (needed to build mvm binary)
go version
# Expected: 1.26+ (match the project's go.mod)
```

### 1.3 Host: Groups

```bash
groups
# Expected: kvm mvm disk (all three)

# Verify each explicitly:
getent group kvm >/dev/null && echo "kvm group: OK" || echo "kvm group: MISSING"
getent group mvm >/dev/null && echo "mvm group: OK" || echo "mvm group: MISSING"
getent group disk >/dev/null && echo "disk group: OK" || echo "disk group: MISSING"
```

If missing, **ask a human admin** to add you:
`sudo usermod -aG <group> $USER && newgrp <group>`

### 1.4 Host: System Tools

```bash
for tool in qemu-img mkfs.ext4 truncate zstd genisoimage ssh-keygen ip nft; do
  which "$tool" >/dev/null 2>&1 && echo "$tool: OK" || echo "$tool: MISSING"
done
```

If any tool is missing, **ask a human admin** to install it:
- Debian/Ubuntu: `sudo apt-get install -y <package>`
- Arch: `sudo pacman -S --needed <package>`

See `docs/development/SYSTEM_TEST_SETUP.md` §2 for the package list.

### 1.5 rc-vm: Prerequisites (Automatic)

These are installed inside rc-vm by `rc-env.yaml` — you do NOT need them on the host:

| Prerequisite | Where | Provided by |
|---|---|---|---|
| Python 3 + pytest | Inside rc-vm | `ssh:install packages` step in rc-env.yaml |
| Kernel (`official:7.0.11`) | Inside rc-vm `/mnt/` | `copy:copy kernel` step |
| Guestfs kernel (`linux-image-kvm`) | Inside rc-vm `/boot/` | `ssh:install guestfs kernel` step |
| Images (`alpine:3.21`, `ubuntu:24.04`) | Inside rc-vm `/mnt/` | `copy:copy alpine image`, `copy:copy ubuntu image` |
| Firecracker binary | Inside rc-vm `/mnt/` | `copy:copy firecracker tarball` |
| mvm binary | Inside rc-vm `/usr/bin/mvm` | `ssh:install mvm` step |
| E2E tests | Inside rc-vm `~/tests/e2e/` | `copy:copy e2e tests` step |

### 1.6 Resource Pre-Seeding (Inside rc-vm)

The e2e test suite needs assets (kernels, images, binaries) to be available
inside rc-vm. The `rc-env.yaml` handles this automatically during deployment.
To manually pre-seed assets inside rc-vm:

```bash
# Inside rc-vm:
MVM_CACHE_DIR=~/.cache/mvmctl MVM_BINARY=/usr/bin/mvm \
  mvm kernel pull --type firecracker --version v1.15
MVM_CACHE_DIR=~/.cache/mvmctl MVM_BINARY=/usr/bin/mvm \
  mvm kernel pull --type firecracker --version v1.13
MVM_CACHE_DIR=~/.cache/mvmctl MVM_BINARY=/usr/bin/mvm \
  mvm kernel default $(MVM_CACHE_DIR=~/.cache/mvmctl MVM_BINARY=/usr/bin/mvm \
    mvm kernel ls --json | python3 -c "import json,sys;print(json.load(sys.stdin)[-1]['id'][:6])")
MVM_CACHE_DIR=~/.cache/mvmctl MVM_BINARY=/usr/bin/mvm \
  mvm image pull alpine:3.21
MVM_CACHE_DIR=~/.cache/mvmctl MVM_BINARY=/usr/bin/mvm \
  mvm image pull ubuntu:24.04
```

`MVM_CACHE_DIR` MUST be set to the same path the test framework will use
(`~/.cache/mvmctl`). The conftest.py monkeypatches `MVM_CACHE_DIR` to this
value. If you init or pull resources without it, the DB will be at a different
path and the tests will report "not initialized".

No manual installation on the host is needed for any of the above.

---

## 2. Build the Binary

```bash
# Build the release binary
./scripts/build.sh release

# Verify it exists and is executable
test -x dist/mvm && echo "binary: OK ($(dist/mvm --version 2>/dev/null))"

# Copy to ~/.local/bin (for sudo operations — this path has sudoers access)
cp dist/mvm ~/.local/bin/mvm
```

**IMPORTANT**: The binary at `~/.local/bin/mvm` is what system tests use for
sudo operations. The runner script finds `dist/mvm` via auto-detection.

---

## 3. Deploy the Environment (Host)

### 3.1 Initialize the Host

One-time host setup (iptables, mvm group, directories):

```bash
sudo ~/.local/bin/mvm host init
```

### 3.2 Deploy the rc Environment

This is the **only step the host performs**. After this, everything runs inside rc-vm.

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror

# Deploy
~/.local/bin/mvm env apply rc-env.yaml
```

**What this does (in order):**
1. Creates network (`rc-net`), SSH key (`rc-key`)
2. Pulls/creates image (`ubuntu:noble`), kernel (`official:7.0.11`), binary (`firecracker 1.16.0`)
3. Creates VM (`rc-vm`) with 6 vcpu, 4G mem, 25G disk, `nested_virt: true`
4. Copies into the VM: mvm binary, firecracker tarball, kernel, alpine image, ubuntu image, system tests
5. SSH into VM: installs packages (`qemu-utils`, `net-tools`, `python3`, `pytest`, ...)
6. SSH into VM: installs `mvm` into `/usr/bin/mvm`, creates `/mnt/tmp`

---

## 4. Run System Tests Inside rc-vm

**Architecture:**
- **Host**: Only runs `env apply`. All assets (kernel, images, firecracker) are copied to rc-vm.
- **rc-vm (outer VM)**: The test environment. **rc-vm is a full bare-metal-quality
  host** — it has `nested_virt: true`, direct KVM access, its own iptables/nftables
  stack, full Docker support, sudo, and can run any Linux software. There is no
  artificial limitation. If the kernel supports it, rc-vm supports it.
- **Nested VMs (test subjects)**: Created and destroyed by system tests inside
  rc-vm. These are the actual microVMs being tested.

**Sudo policy inside rc-vm:**
The `runner` user has passwordless sudo (configured by the provisioner's `SetupSudo`).
Sudo is available for genuine system operations (nftables, iptables, sysctl).
The `~/.local/bin/mvm` binary is set up with sudoers access for `mvm host init`
and related commands.

```
┌─────────────────────────────────────────┐
│ Host (your machine)                     │
│  Only: mvm env apply rc-env.yaml          │
│                                         │
│  ┌─────────────────────────────────┐    │
│  │ rc-vm (outer VM)            │    │
│  │  nested_virt: true              │    │
│  │  Assets: /mnt/kernel, /mnt/*.vhd│    │
│  │  Tests: ~/tests/e2e/            │    │
│  │  mvm: /usr/bin/mvm              │    │
│  │                                 │    │
│  │  ┌───────────────────────────┐  │    │
│  │  │ nested-vm-1 (test VM)     │  │    │
│  │  │  Created by pytest        │  │    │
│  │  │  Destroyed after test     │  │    │
│  │  └───────────────────────────┘  │    │
│  │  ┌───────────────────────────┐  │    │
│  │  │ nested-vm-2 (test VM)     │  │    │
│  │  └───────────────────────────┘  │    │
│  └─────────────────────────────────┘    │
└─────────────────────────────────────────┘
```

### 4.1 Verify the Environment

```bash
# From the HOST — confirm rc-vm is running
~/.local/bin/mvm vm ls --json

# Check mvm version inside the outer VM
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm ssh rc-vm -u runner --cmd 'mvm --version'

# Check tests and assets are present
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm ssh rc-vm -u runner --cmd 'ls ~/tests/e2e/ /mnt/'
```

### 4.2 Run All E2E Tests (Release Gate)

**Critical environment variables inside rc-vm:**

| Variable | Value | Why |
|----------|-------|-----|
| `MVM_ASSET_MIRROR` | `/mnt` | Assets (kernels, images, binaries) are stored at `/mnt/` |
| `MVM_BINARY` | `/usr/bin/mvm` | Tests use this to find the mvm binary |
| `MVM_CACHE_DIR` | `$HOME/.cache/mvmctl` | Test framework sets this automatically via conftest.py |

Run the full e2e suite as a single non-interactive SSH command. The host sends one command,
rc-vm executes all tests against nested VMs:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm ssh rc-vm -u runner --timeout 600 --cmd \
  "cd ~ && MVM_ASSET_MIRROR=/mnt MVM_BINARY=/usr/bin/mvm \
  python3 -m pytest --timeout 300 \
  tests/e2e/ --tb=short -q"
```

Note: The `--timeout 300` is pytest's per-test timeout (some VM tests take 5+
minutes). The `--timeout 600` on `mvm ssh` is the SSH command timeout.

### 4.3 Run a Single Test File

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm ssh rc-vm -u runner --timeout 600 --cmd \
  "cd ~ && MVM_ASSET_MIRROR=/mnt MVM_BINARY=/usr/bin/mvm \
  python3 -m pytest --timeout 300 \
  tests/e2e/test_network.py --tb=short -q"
```

### 4.4 Interactive Session (Debugging)

SSH in interactively to debug failures:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm ssh rc-vm -u runner

# Inside rc-vm:
cd ~
MVM_ASSET_MIRROR=/mnt MVM_BINARY=/usr/bin/mvm \
  python3 -m pytest --timeout 300 -x tests/e2e/ --tb=long
```

### 4.5 Reset and Re-deploy

To start fresh:

```bash
# Destroy everything
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm env destroy rc-env.yaml

# Re-deploy
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm env apply rc-env.yaml
```

### 4.6 Troubleshooting Guestfs / libguestfs

If the guestfish probe fails (`guestfish -a /dev/null run`), verify:

```bash
# 1. Kernel exists in /boot
ls /boot/vmlinuz-*

# 2. Kernel modules exist
ls /lib/modules/*/

# 3. Kernel is world-readable
ls -la /boot/vmlinuz-*  # should be 644 (rw-r--r--)

# 4. Debug supermin
LIBGUESTFS_DEBUG=1 guestfish -a /dev/null run 2>&1 | tail -20

# 5. Reinstall if missing
sudo apt-get install -y linux-image-kvm
sudo chmod 644 /boot/vmlinuz-*
```

Common errors:
- `"cannot parse filename"` — kernel filename doesn't match `vmlinuz-<version>` pattern
- `"no modpath"` — kernel found but modules are missing in `/lib/modules/<version>/`
- `"Permission denied"` on `/boot/vmlinuz-*` — file is 600 root-only; `chmod 644`

---

## 5. Interpret Results

The runner prints a summary at the end:
```
  12 passed  0 failed  0 skipped  (3m12s)
```

Results are saved to `.reports/system-test-results-latest.txt`:

```
test_network.py: PASS
test_nftables.py: PASS
```

### 5.1 Pass / Fail / Skip Rules

| Status | Meaning | Release Gate |
|--------|---------|--------------|
| **PASS** | All tests in file passed | ✓ |
| **FAIL** | At least one test failed | ✗ — must fix before release |
| **SKIP** | All tests skipped (prerequisite missing) | ⚠ — investigate why |

### 5.1a Release Candidate (RC) Zero-Tolerance Policy

**A Release Candidate MUST have zero failures AND zero skips.** Every test must
pass. A skip is treated as a release blocker — it means either:
- The environment is missing a prerequisite (fix the environment)
- The test is testing a feature that doesn't exist in the Go CLI (remove the test or add the feature)
- The test's skip condition is too broad (tighten the condition)

Before signing off an RC, every test file must produce `X passed, 0 failed, 0 skipped`.

### 5.2 What a Skip Means

A skipped test file means ALL tests in that file were skipped, typically
because a prerequisite check failed. Investigate:
- Is `/dev/kvm` accessible?
- Is the asset mirror reachable?
- Is the `mvm` group set up?

A skip on a required test (e.g., `test_vm_lifecycle.py` skipped because no
KVM) is a **release blocker** — fix the environment, not the code.

### 5.3 Common Failure Patterns

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| `iptables` errors | `mvm host init` not run | Run `sudo ~/.local/bin/mvm host init` |
| VM creation hangs | Binary not built or missing | Re-run `./scripts/build.sh release` |
| `bridge already exists` | Stale bridges from previous run | Clean up with `mvm network rm --force` |
| `Text file busy` on service binary | Stale service processes | `killall -9 mvm-console-relay mvm-nocloud-server mvm-provision` |
| `guestfish -a /dev/null run` fails | Supermin can't find a kernel | Install `linux-image-kvm` + `chmod 644 /boot/vmlinuz-*` |
| `guestfish session failed: incorrect number of arguments` | Bug in Go guestfish invocation | Fixed: `sh -c` → `sh ""` (guestfish takes 1 arg) |
| `pending migrations detected` | DB schema mismatch after binary update | `rm -f ~/.cache/mvmctl/mvmdb.db && mvm init --non-interactive --skip-host --skip-network` then re-pull resources |
| `not initialized` after setup | MVM_CACHE_DIR mismatch between init and test run | Always use `MVM_CACHE_DIR=~/.cache/mvmctl` consistently |
| `Text file busy` pulling binary | Conftest unconditionally pulled v1.15.1 while firecracker processes held the file | Fixed: removed unconditional pull from `_ensure_mvm_db` |
| All tests skip with "KVM not available" | KVM not accessible | Check `/dev/kvm` permissions, user groups |
| High skip ratio (>10%) | Missing dependencies | Run the prerequisite check (section 1) |

---

## 6. Collect Release Evidence

Before signing off a release, collect and archive evidence:

```bash
mkdir -p release-evidence/vX.Y.Z

# Build + unit gates
go build ./... > release-evidence/vX.Y.Z/build.log 2>&1
go vet ./... > release-evidence/vX.Y.Z/vet.log 2>&1
go test ./... > release-evidence/vX.Y.Z/test.log 2>&1

# Deploy the rc environment
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm env destroy rc-env.yaml > /dev/null 2>&1
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm env apply rc-env.yaml \
  > release-evidence/vX.Y.Z/env-deploy.log 2>&1

# System tests (inside rc-vm, single e2e run)
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  ~/.local/bin/mvm ssh rc-vm -u runner --timeout 600 --cmd \
  "cd ~ && MVM_ASSET_MIRROR=/mnt MVM_BINARY=/usr/bin/mvm \
  python3 -m pytest --timeout 300 \
  tests/e2e/ --tb=short -q" \
  > release-evidence/vX.Y.Z/system-e2e.log 2>&1

# Binary verification
~/.local/bin/mvm --version > release-evidence/vX.Y.Z/version.txt 2>&1
~/.local/bin/mvm --help > release-evidence/vX.Y.Z/help.txt 2>&1
sha256sum dist/mvm > release-evidence/vX.Y.Z/checksum.sha256
```

Each `.log` file must show zero failures. Any failure blocks the release.

---

## 7. Reference

- `docs/RC_QA.md` — Release gates and checklist (human-facing)
- `docs/RELEASE.md` — Full release process (tagging, CI, AUR)
- `docs/development/SYSTEM_TEST_SETUP.md` — Detailed environment setup
- `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` — Three-level test architecture (L0/L1/L2)
- `.opencode/agent/qa-engineer.md` — QA agent instructions
