# How Agents Run System Tests

## Purpose

This is the **execution plan** for running the mvmctl system test suite and
qualifying a release.

The only workflow is the **orchestrator-based** approach:
`scripts/run-system-tests.py` creates per-domain VMs from a custom base image,
runs tests in parallel, and destroys VMs. Documented in detail in
[system-test-architecture.md](../system-test-architecture.md).

An AI agent can start from a fresh clone and follow this guide linearly
without cross-referencing other docs.

**Do NOT deviate** from the commands below without approval. All paths,
environment variables, and flags are intentional.

---

## Table of Contents

1. [Prerequisite Check](#1-prerequisite-check)
2. [Build the Binary](#2-build-the-binary)
3. [Prepare Shared Assets](#3-prepare-shared-assets)
4. [Run System Tests via Orchestrator](#4-run-system-tests-via-orchestrator)
5. [Interpret Results](#5-interpret-results)
6. [Collect Release Evidence](#6-collect-release-evidence)
7. [Reference](#7-reference)

---

## 1. Prerequisite Check

**Two layers of prerequisites:**
- **Host** (your machine): Must be able to run Firecracker, build mvm, and
  communicate with runner VMs.
- **Runner VMs**: Automatically created and provisioned by the orchestrator
  (`scripts/run-system-tests.py`) from a custom base image — you don't need to
  install anything inside them.

### Runner VMs Are Full Bare-Metal Hosts

Each runner VM is **not a limited container or lightweight sandbox**. It is a
full Firecracker microVM with `nested_virt: true` and **direct KVM access** to
the host CPU virtualization extensions. From the perspective of mvm and the
system tests, each runner VM behaves **identically to bare metal**:

- `/dev/kvm` is fully accessible — mvm creates and runs nested VMs inside the
  runner VM
- `iptables`/`nftables` work — each runner VM has its own network stack with NAT
- `sudo`, `docker`, kernel module loading all work
- The 7.0.11 kernel has everything compiled in (no modules needed)
- tmpfs, overlayfs, btrfs, ext4 — all standard Linux facilities

The only limitations are those imposed by the kernel itself (which is a standard
upstream build with common features enabled). If Linux supports it and the
kernel is compiled with it, runner VMs support it.

### Special Case: libguestfs / Supermin

The `test_create_guestfs_backend` test requires guestfish (libguestfs) to work.
guestfish uses `supermin` to build a small appliance VM. Supermin needs:

1. A kernel in `/boot/` with a parseable version (e.g., `vmlinuz-6.8.0-124-generic`)
2. Kernel modules in `/lib/modules/<version>/`
3. The kernel file must be world-readable (supermin runs as the `runner` user, not root)

The runner VMs run a custom kernel (7.0.11) loaded externally by Firecracker —
there is no kernel package installed and `/boot/` is empty. Supermin cannot build
its appliance without a kernel.

**Fix:** Install `linux-image-kvm` (a lightweight kernel package for KVM guests)
and fix permissions:

```bash
apt-get install -y linux-image-kvm
chmod 644 /boot/vmlinuz-*
```

The base image builder (`_build_base_image` in `run-system-tests.py`) installs
this package. The dpkg trigger error about `initramfs-tools` / `packagekit.service`
is cosmetic (can't run in a Firecracker VM) — the kernel files are placed correctly
and guestfish works.

**Verify guestfish works:**
```bash
guestfish -a /dev/null run
```
Exit code 0 = success.

**Note:** The `guestfish session failed: incorrect number of arguments` bug has been
fixed — guestfish commands are passed via stdin instead of as CLI positional args.
This issue is separate from the supermin kernel problem documented above.

### 1.1 Host: Hardware

```bash
# KVM (required — host runs Firecracker for runner VMs)
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

See `docs/DEPENDENCIES.md` for the per-distribution package list.

### 1.5 Orchestrator Prerequisites (Automatic)

The orchestrator (`scripts/run-system-tests.py`) handles all provisioning
automatically — it creates runner VMs from a custom base image, mounts the
shared asset volume, and runs pytest inside each VM. Assets (kernels, images,
binaries) are pre-cached in `~/.cache/mvm-asset-mirror/` on the host.

The custom base image (`mvm-test-runner:<mvm-version>`) is built once during
`--prepare` and contains:
- The mvm binary at `/usr/local/bin/mvm`
- System tests at `/tests/system/`
- Python 3 + pytest
- qemu-utils

No manual installation on the host is needed beyond the tools in section 1.4.

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

## 3. Prepare Shared Assets

Before the orchestrator can run tests, shared assets must exist on the host.
The orchestrator's `--prepare` mode handles this automatically:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --prepare
```

This builds the custom base image (`mvm-test-runner:<version>`), creates the
shared read-only `asset-mirror` volume, and runs smoke tests (create T1 smoke
VM → mount volume → init → destroy; same for T2).

Run `--prepare` once after cloning or updating the mvm binary. Re-run when the
binary version changes or when `tests/system/` content changes (or use `--push`
at run time instead).

### 3.1 Rebuild

To force rebuild of the shared volume, base image, and binary:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --rebuild --all
```

---

## 4. Run System Tests via Orchestrator

**Architecture:**

```
Host (your machine)
│
├── scripts/run-system-tests.py   ← orchestrator
│
├── TIER 1 — Per-domain VMs (custom base image + shared RO volume)
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
└── TIER 3 — Tests run directly on host
    └── pytest tests/system/<domain>/
```

### 4.1 Run All Tests (Release Gate)

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --all
```

### 4.2 Run Specific Domains

```bash
python3 scripts/run-system-tests.py cli network vm_fresh_env
```

### 4.3 Run Specific Tiers

```bash
# Run only Tier 1 domains
python3 scripts/run-system-tests.py --tier 1

# Run only Tier 2 domains
python3 scripts/run-system-tests.py --tier 2

# Run Tier 1 then Tier 3
python3 scripts/run-system-tests.py --tier 1,3
```

### 4.4 Push Fresh Tests (No Rebuild)

If `tests/system/` changed but the base image wasn't rebuilt, use `--push`:

```bash
python3 scripts/run-system-tests.py cli --push
```

### 4.5 Limit Parallel Workers

```bash
python3 scripts/run-system-tests.py --all --workers 2
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

Results are printed to stdout at the end of the run:

```
  [PASS] Tier 1 cli
  [PASS] Tier 1 config
  [FAIL] Tier 2 vm_lifecycle
```

The runner prints a per-domain summary with PASS/FAIL status for each domain.

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
| `guestfish session failed: incorrect number of arguments` | Bug in Go guestfish invocation | Fixed: guestfish commands passed via stdin instead of CLI args (guestfish 1.56.x treats all positional tokens as args to the first command) |
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

# Prepare shared assets (build base image, shared volume)
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --prepare \
  > release-evidence/vX.Y.Z/env-prepare.log 2>&1

# System tests (all tiers)
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --all \
  > release-evidence/vX.Y.Z/system-e2e.log 2>&1

# Binary verification
~/.local/bin/mvm --version > release-evidence/vX.Y.Z/version.txt 2>&1
~/.local/bin/mvm --help > release-evidence/vX.Y.Z/help.txt 2>&1
sha256sum dist/mvm > release-evidence/vX.Y.Z/checksum.sha256
```

Each `.log` file must show zero failures. Any failure blocks the release.

---

## 7. Reference

- `docs/system-test-architecture.md` — Three-tier architecture (primary reference)
- `docs/RC_QA.md` — Release gates and checklist (human-facing)
- `docs/RELEASE.md` — Full release process (tagging, CI, AUR)
- `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` — How to write L0/L1/L2 tests
- `docs/development/SYSTEM_TEST_SETUP.md` — Host preparation and one-time setup
- `.opencode/agent/qa-engineer.md` — QA agent instructions
