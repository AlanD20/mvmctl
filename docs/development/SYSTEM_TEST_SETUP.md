# System Test Setup — Host Preparation

**Purpose:** Define the steps to prepare a host machine for running L2 system tests
using the orchestrator-based approach.

**See also:**
- [system-test-architecture.md](../system-test-architecture.md) — three-tier architecture, shared volume, per-domain VMs
- [HOW_AGENTS_RUN_SYSTEM_TESTS.md](HOW_AGENTS_RUN_SYSTEM_TESTS.md) — step-by-step execution guide
- [HOW_AGENTS_WRITE_SYSTEM_TESTS.md](HOW_AGENTS_WRITE_SYSTEM_TESTS.md) — how to write L0/L1/L2 tests

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Host Requirements](#2-host-requirements)
3. [One-Time Setup](#3-one-time-setup)
4. [Orchestrator Prepare Mode](#4-orchestrator-prepare-mode)
5. [Verification Checklist](#5-verification-checklist)
6. [Troubleshooting](#6-troubleshooting)

---

## 1. Architecture Overview

System tests run inside **disposable Firecracker VMs** (the "runner VMs") created
and destroyed by `scripts/run-system-tests.py`. The orchestrator:

1. Builds a custom base image (`mvm-test-runner:<version>`) containing the mvm
   binary and system tests.
2. Creates per-domain runner VMs from that image, attaches a shared read-only
   volume with cached assets, and runs `pytest` inside each VM.
3. Destroys all VMs after the test session.

```
Host (Go + KVM only)
│
├── scripts/run-system-tests.py   ← orchestrator
│
├── TIER 1 — Per-domain VMs (host-level CLI tests)
│   ├── mvm vm create --image mvm-test-runner:<version> --volume asset-mirror
│   ├── mount /dev/vdb /mnt && MVM_ASSET_MIRROR=/mnt mvm init
│   ├── pytest tests/system/<domain>/    ← runs INSIDE the VM
│   └── mvm vm rm --force
│
├── TIER 2 — Same as T1 + nested-virt
│   ├── mvm vm create --nested-virt --volume asset-mirror
│   ├── mount + init + kernel pull / image pull / bin pull (cache hits)
│   ├── pytest tests/system/<domain>/    ← runs INSIDE the VM
│   └── mvm vm rm --force
│
└── TIER 3 — Tests run directly on host (no runner VM)
    └── pytest tests/system/<domain>/
```

---

## 2. Host Requirements

### Hardware

| Component | Requirement | Verification Command |
|-----------|-------------|----------------------|
| CPU | x86_64 with VMX/SVM | `grep -c '(vmx\|svm)' /proc/cpuinfo` — expected: > 0 |
| RAM | 8 GB minimum (16 GB recommended) | `free -g` |
| Disk | 20 GB free for caches and volumes | `df -h ~/.cache/mvmctl` |
| KVM | `/dev/kvm` accessible | `test -c /dev/kvm && echo OK` |
| Nested virt | `nested=Y` (for Tier 2 runner VMs) | `cat /sys/module/kvm_intel/parameters/nested` |

### Software

| Tool | Required For | Verification |
|------|-------------|--------------|
| Go 1.26.3+ | Building the mvm binary | `go version` |
| qemu-img | Volume operations | `which qemu-img` |
| mkfs.ext4 | Shared volume creation | `which mkfs.ext4` |
| truncate (coreutils) | Sparse file operations | `which truncate` |
| zstd | Image decompression | `which zstd` |
| genisoimage | Cloud-init ISO creation | `which genisoimage` |
| ssh-keygen | Key generation in tests | `which ssh-keygen` |
| ip | Network management | `which ip` |
| nft | Firewall management | `which nft` |

### Groups

```
groups
# Expected: kvm mvm disk (all three)
```

If a group is missing, ask a human admin:
```
sudo usermod -aG <group> $USER && newgrp <group>
```

---

## 3. One-Time Setup

### 3.1 Build the binary

```bash
./scripts/build.sh release

# Verify it exists and is executable
test -x dist/mvm && echo "binary: OK ($(dist/mvm --version 2>/dev/null))"

# Copy to path that system tests expect
cp dist/mvm ~/.local/bin/mvm
```

### 3.2 Populate the asset mirror

Set up the local asset mirror cache. This directory is the source of truth for
the shared test volume:

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mkdir -p "$MVM_ASSET_MIRROR"

# Pull the official kernel used by runner VMs
mvm kernel pull official:7.0.11 --features nftables,tuntap,kvm,btrfs --default

# Pull remaining assets
mvm image pull alpine:3.23
mvm image pull ubuntu:noble
mvm kernel pull --type firecracker --version v1.15 --default
mvm bin pull firecracker --version 1.16.0 --default
```

**What a cache hit looks like:** On subsequent runs, each pull command prints
messages about reading from the local mirror at `MVM_ASSET_MIRROR` instead of
downloading from the internet.

**Troubleshooting:** If a pull downloads from the internet instead of using the
local mirror, verify `MVM_ASSET_MIRROR` is set and the mirror directory contains
the expected files:

```bash
ls ~/.cache/mvm-asset-mirror/
# Expected: vmlinux files, image files, firecracker tarballs
```

---

## 4. Orchestrator Prepare Mode

The orchestrator's `--prepare` flag handles all provisioning automatically:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --prepare
```

This does the following:

1. **Detects the mvm version** from `mvm --version`
2. **Creates the shared volume** (`asset-mirror`, 6 GB, raw format, shareable, read-only)
3. **Populates the volume** with the contents of `~/.cache/mvm-asset-mirror/` via loop mount (requires sudo)
4. **Creates the test network** (`sys-test-net`, subnet `10.88.0.0/24`)
5. **Builds the custom base image** (`mvm-test-runner:<version>`) — creates a builder VM from `ubuntu-minimal:noble`, installs test dependencies (Python 3, pytest, qemu-utils, etc.), copies the mvm binary and test suite, then imports the rootfs as a custom image
6. **Smoke-tests T1 provisioning** — creates a T1 VM from the base image, mounts the shared volume, runs `mvm init`, then destroys the VM
7. **Smoke-tests T2 provisioning** — creates a T2 VM with nested virt, mounts the volume, runs `mvm init`, pulls a cache-hit asset, then destroys the VM

**What success looks like:** The final lines of the output show:

```
=== Prepare: ALL STEPS PASSED ===
  Base image: mvm-test-runner:<version>
  T1: '<name>' — created, binary copied, init completed
  T2: '<name>' — created, volume attached, cache hit verified
  Volume status: available
  Environment is ready for running tests.
```

**Run `--prepare` once** after cloning or updating the mvm binary. Re-run when
the binary version changes or when `tests/system/` content changes.

### Rebuild flags

| Flag | What it rebuilds |
|------|-----------------|
| `--rebuild` | Binary, shared volume, and base image (full reset) |
| `--volume` | Shared asset volume only |
| `--image` | Custom base image only |

```bash
# Full rebuild
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --rebuild --all
```

---

## 5. Verification Checklist

Run these commands to verify the host is ready:

```bash
# Host checks
echo "KVM:   $(test -c /dev/kvm && echo OK || echo MISSING)"
echo "Nest:  $(cat /sys/module/kvm_intel/parameters/nested 2>/dev/null || echo N/A)"
echo "mvm:   $(~/.local/bin/mvm --version 2>/dev/null)"

# Asset mirror
ls ~/.cache/mvm-asset-mirror/ | head -10

# Shared volume
mvm volume inspect asset-mirror --json 2>/dev/null && echo "Volume: OK" || echo "Volume: MISSING — run --prepare"

# Custom base image
mvm image inspect mvm-test-runner:$(~/.local/bin/mvm --version 2>/dev/null | awk '{print $2}') --json 2>/dev/null && echo "Base image: OK" || echo "Base image: MISSING — run --prepare"

# Test network
mvm network inspect sys-test-net --json 2>/dev/null && echo "Network: OK" || echo "Network: MISSING — run --prepare"
```

---

## 6. Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `mvm: command not found` | Binary not built or not in PATH | Run `./scripts/build.sh release && cp dist/mvm ~/.local/bin/mvm` |
| Shared volume creation fails | `mvmctl` not initialized | Run `mvm init --non-interactive --skip-host` first |
| Base image build fails | Builder VM cannot install packages | Check network connectivity and `mvm exec` works inside builder VM |
| Cache hit downloads from network | Mirror not set or empty | Verify `MVM_ASSET_MIRROR` points to populated directory |
| `--prepare` smoke test fails | Missing assets in mirror | Run asset pulls from section 3.2 |
| T2 VM creation fails inside runner VM | Out of resources | Adjust runner VM specs in `provision_t2()` inside `run-system-tests.py` |
| Nested KVM missing inside VM | Host lacks `nested=1` | `cat /sys/module/kvm_intel/parameters/nested` — must be `Y` |
| Permission denied on cache dir | `mvm init` run as root | Destroy and recreate the VM ensuring init runs as `runner` user |
