# How to Run System Tests

This guide covers everything needed to run the mvmctl system test suite:
from a fresh clone to a passing test run. Follow it linearly — each section
builds on the previous one.

The only workflow is the **orchestrator-based** approach:
[`scripts/run-system-tests.py`](../../scripts/run-system-tests.py) creates
per-domain VMs from a custom base image, runs tests in parallel, and destroys
VMs. The three-tier architecture is documented in
[system-test-architecture.md](../system-test-architecture.md).

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

### Runner VMs Exercise Real Linux and Nested KVM

Each runner VM is **not a limited container or lightweight sandbox**. It is a
full Firecracker microVM with `nested_virt: true` and **direct KVM access** to
the host CPU virtualization extensions. This exercises real kernel, KVM,
networking, filesystem, and privilege paths, but it is not identical to bare
metal: device exposure, kernel configuration, nesting depth, timing, and
resource limits still differ.

- `/dev/kvm` is fully accessible — mvm creates and runs nested VMs inside the
  runner VM
- `iptables`/`nftables` work — each runner VM has its own network stack with NAT
- `sudo`, `docker`, kernel module loading all work
- The 7.0.11 kernel has everything compiled in (no modules needed)
- tmpfs, overlayfs, btrfs, ext4 — all standard Linux facilities

Tier 3 exists for cases that cannot be qualified reliably under nesting. Those
tests run directly on the outer host and therefore require a dedicated clean
qualification host or disposable outer VM.

### Special Case: libguestfs / Supermin

The `guestfish` tool (libguestfs) is not used by system tests in the Go codebase.
No guestfish-specific tests exist. This section is retained only for reference if
libguestfs-based features are re-added in the future.

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
# Expected after `mvm host init`: kvm mvm

# Verify each explicitly:
getent group kvm >/dev/null && echo "kvm group: OK" || echo "kvm group: MISSING"
getent group mvm >/dev/null && echo "mvm group: OK" || echo "mvm group: MISSING"
```

`mvm host init` is responsible for creating the `mvm` group and adding the invoking user; verify the result instead of
assuming a partial setup succeeded. If `kvm` is missing, ask a human administrator to grant KVM access, then start a new
login session; do not add the `disk` group for mvmctl.

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

The orchestrator (`scripts/run-system-tests.py`) handles runner provisioning:
it creates VMs from a custom base image, mounts the shared asset volume, and
runs pytest inside each VM. Release qualification requires a populated writable
host mirror at `MVM_ASSET_MIRROR` (normally `~/.cache/mvm-asset-mirror/`). The
orchestrator copies that point-in-time content into the read-only QA volume; it
does not download missing assets into or repair the host mirror.

The custom base image (`mvm-test-runner:<candidate-version>`) is built once during
`--prepare` and contains:
- The release candidate installed at `/usr/local/bin/mvm` through `host install-system`
- System tests at `/tests/system/`
- Python 3 + pytest + pytest-timeout
- qemu-utils, fakeroot, nftables, iptables, zstd
- cloud-image-utils (for `--cloud-init-mode iso` tests)
- build-essential, bc, bison, flex, libncurses-dev, libssl-dev, libelf-dev, git, curl, dwarves (for `kernel_build` tests)

The orchestrator installs runner-VM contents automatically. The host needs an existing installed controller for outer
resource management, but that controller is not the artifact under test and must not be overwritten by the candidate.

---

## 2. Build the Binary

```bash
# Untagged RC builds must carry their intended identity explicitly
./scripts/build.sh release --version 0.3.0-rc.1 --output dist/mvm

# Verify it exists and is executable
test -x dist/mvm && echo "binary: OK ($(dist/mvm --version 2>/dev/null))"

# Select an existing installed controller and keep the release artifact separate
export MVM_BINARY=/usr/local/bin/mvm
export MVM_CANDIDATE_BINARY=./dist/mvm
```

**IMPORTANT**: `MVM_BINARY` only manages disposable outer resources. `MVM_CANDIDATE_BINARY` is installed through the
administrator route only inside runner VMs. The orchestrator rejects aliases and `--rebuild` writes only the candidate.
Use the separate clean-host installation gate in `docs/RC_QA.md` to qualify the final host transition.

Do not omit `--version` for an untagged RC. The build script otherwise uses `git describe`, which can inherit the
previous release tag and give the new candidate and runner image the wrong version identity.

---

## 3. Prepare Shared Assets

Before the orchestrator can run release tests, the required shared assets must
exist and pass their upstream checksum validation in the writable host mirror.
Local focused tests may populate this mirror lazily, but an empty or stale
mirror is not acceptable release evidence.

### 3.1 Prepare the Host Asset Mirror

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mkdir -p "$MVM_ASSET_MIRROR"

# Pull the kernel and assets that the orchestrator will need
"$MVM_BINARY" kernel pull official:7.0.11 --features nftables,tuntap,kvm,btrfs --default
"$MVM_BINARY" image pull alpine:3.23 --force
"$MVM_BINARY" image pull ubuntu-minimal:24.04 --force
"$MVM_BINARY" image pull ubuntu --version 24.04 --force
"$MVM_BINARY" kernel pull --type firecracker --version v1.15 --default
"$MVM_BINARY" bin pull firecracker --version 1.16.0 --default --force
"$MVM_BINARY" bin pull firecracker --version 1.16.1 --force
```

These pulls populate the local mirror directory. On subsequent runs, each pull
reads the large object from the mirror instead of downloading it again. Small checksum/version metadata requests still
go to the fixed upstream source so cached content can be verified.

The shared `asset-mirror` volume is a point-in-time, read-only copy. If a pull replaces a stale host object or adds a
missing one, rebuild the QA volume before continuing:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  python3 scripts/run-system-tests.py --volume
```

Do not expect the read-only guest copy to self-heal. It can detect a checksum mismatch and use an HTTP fallback for that
run, but it cannot persist the replacement. Repair the writable host mirror and reseed the volume instead.

### 3.2 Run `--prepare`

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  python3 scripts/run-system-tests.py --prepare
```

This builds the custom base image (`mvm-test-runner:<version>`), creates the
shared read-only `asset-mirror` volume, and runs smoke tests (create T1 smoke
VM → mount volume → init → destroy; same for T2).

Run `--prepare` once after cloning or updating the mvm binary. Re-run when the
binary version changes or when `tests/system/` content changes (or use `--push`
at run time instead).

### 3.3 Rebuild

To force rebuild of the shared volume, base image, and binary:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  python3 scripts/run-system-tests.py \
  --release-qualification --host-direct --rebuild \
  --candidate-version 0.3.0-rc.1 --all
```

`--rebuild` requires `--candidate-version` unless `HEAD` is one clean checkout with exactly one valid release tag. This
prevents a release branch from inheriting the previous tag through `git describe`.

### 3.4 Verification Checklist

Run these to confirm the host is ready for tests:

```bash
# Host checks
echo "KVM:   $(test -c /dev/kvm && echo OK || echo MISSING)"
echo "Nest:  $(cat /sys/module/kvm_intel/parameters/nested 2>/dev/null || echo N/A)"
echo "controller: $(/usr/local/bin/mvm --version 2>/dev/null)"
echo "candidate:  $(./dist/mvm --version 2>/dev/null)"

# Asset mirror has content
ls ~/.cache/mvm-asset-mirror/ | head -10

# Shared volume exists
"$MVM_BINARY" volume inspect asset-mirror --json 2>/dev/null \
  && echo "Volume: OK" \
  || echo "Volume: MISSING — run --prepare"

# Custom base image exists
"$MVM_BINARY" image inspect \
  mvm-test-runner:$(./dist/mvm --version 2>/dev/null | awk '{print $2}') \
  --json 2>/dev/null \
  && echo "Base image: OK" \
  || echo "Base image: MISSING — run --prepare"

# Test network exists
"$MVM_BINARY" network inspect sys-test-net --json 2>/dev/null \
  && echo "Network: OK" \
  || echo "Network: MISSING — run --prepare"
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
│   ├── mount /dev/vdb /mnt && MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm init \
│   │   --non-interactive --binary-version 1.16.0
│   ├── pytest tests/system/<domain>/    ← runs INSIDE the VM
│   └── mvm vm rm --force
│
├── TIER 2 — Same as T1 + nested-virt
│   ├── mvm vm create --nested-virt --volume asset-mirror
│   ├── mount + pinned init + kernel pull / image pull / bin pull (cache hits)
│   ├── pytest tests/system/<domain>/    ← runs INSIDE the VM
│   └── mvm vm rm --force
│
└── TIER 3 — Tests run directly on host
    └── pytest tests/system/<domain>/
```

### 4.1 Run All Tests (Release Gate Only)

`--all` includes Tier 3 and may create or destroy outer-host resources. The
runner requires `--host-direct` before any work. For release evidence,
`--release-qualification` also requires an unfiltered full matrix, an
explicit-version rebuild, and exact candidate/controller binary identity before
resource preparation. Run it only on a dedicated clean qualification host or
disposable outer VM where the candidate is installed at `/usr/local/bin/mvm`.
On a workstation with state that must be preserved, use focused T1/T2 domains or
`--tier 1,2`: consent does not prove ownership-safe cleanup.

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  python3 scripts/run-system-tests.py \
  --release-qualification --host-direct --rebuild \
  --candidate-version 0.3.0-rc.1 --all
```

### 4.2 Run Specific Domains

```bash
python3 scripts/run-system-tests.py cli network
python3 scripts/run-system-tests.py --host-direct fresh_env
```

### 4.3 Run Specific Tiers

```bash
# Run only Tier 1 domains
python3 scripts/run-system-tests.py --tier 1

# Run only Tier 2 domains
python3 scripts/run-system-tests.py --tier 2

# Run Tier 1 then Tier 3
python3 scripts/run-system-tests.py --host-direct --tier 1,3
```

### 4.4 Push Fresh Tests (No Rebuild)

If `tests/system/` changed but the base image wasn't rebuilt, use `--push`:

```bash
python3 scripts/run-system-tests.py cli --push
```

### 4.5 Limit Parallel Workers

```bash
python3 scripts/run-system-tests.py --host-direct --all --workers 2
```

### 4.6 Orchestrator Flags Reference

| Flag | Default | Description |
|------|---------|-------------|
| _domains_ (positional) | — | Specific domains to test (e.g., `cli network`) |
| `--all` | `false` | Run all T1 + T2 + host-direct T3 domains; dedicated clean qualification host only |
| `--host-direct` | `false` | Required acknowledgment before selecting any Tier 3 domain or `--all`; does not guarantee isolation or safe cleanup |
| `--release-qualification` | `false` | Require the unfiltered full-matrix release identity gate; requires `--all --host-direct --rebuild --candidate-version` |
| `--tier` | — | Comma-separated tier numbers; executed in given order (e.g., `--tier 1,3`) |
| `--workers` | `4` | Maximum parallel VMs |
| `--rebuild` | `false` | Build only `MVM_CANDIDATE_BINARY`, rebuild shared volume/base image, and run prepare; never replace `MVM_BINARY` |
| `--candidate-version` | — | Exact `X.Y.Z` candidate identity for `--rebuild`; required unless `HEAD` is one clean, exact release tag |
| `--volume` | `false` | Rebuild only the shared asset volume (`asset-mirror`) |
| `--image` | `false` | Ensure the shared volume exists and rebuild the custom base image from the existing candidate |
| `--prepare` | `false` | Validate provisioning pipeline (ensure shared volume + base image exist, build from scratch if missing, run T1/T2 smoke tests) |
| `--push` | `false` | Push test files into each VM before running (overrides baked-in tests) |
| `--skip-volume-check` | `false` | Skip shared volume existence check (assume it exists) |

Choose exactly one test selector: positional domains, `--tier`, or `--all`. Unknown names, repeated domains/tiers, and
selected domains with no registered test files fail before binary probes, builds, or resource mutation. The runner also
requires every canonical regular `tests/system/**/test_*.py` file to be registered exactly once, so an omitted file or
duplicate alias cannot produce release evidence.

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
The current orchestrator's final domain summary does not independently count
pytest skips/xfails/xpasses. Until it does, QA must inspect and archive every
per-domain pytest summary; `[PASS]` at the domain level alone is insufficient.

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
| `iptables` errors | `mvm host init` not run | Run `sudo /usr/local/bin/mvm host init` |
| VM creation hangs | Binary not built or missing | Re-run `./scripts/build.sh release` |
| `bridge already exists` | Stale bridges from previous run | Clean up with `mvm network rm --force` |
| `Text file busy` on service binary | Stale disposable runner or base image | Destroy the affected runner through the outer controller and rebuild the base image; do not kill host processes by basename |
| `pending migrations detected` | Stale disposable runner/base image built with a different schema | Rebuild the candidate runner image with `--image --prepare`; do not delete a workstation database as test cleanup |
| All tests skip with "KVM not available" | KVM not accessible | Check `/dev/kvm` permissions, user groups |
| High skip ratio (>10%) | Missing dependencies | Run the prerequisite check (section 1) |

---

## 6. Collect Release Evidence

See [docs/RC_QA.md](../RC_QA.md) §4 for the evidence collection checklist and archive procedure.

---

## 7. Reference

- [docs/system-test-architecture.md](../system-test-architecture.md) — Three-tier architecture (primary reference)
- [docs/RC_QA.md](../RC_QA.md) — Release gates, evidence collection, regression criteria
- [docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md](HOW_AGENTS_WRITE_SYSTEM_TESTS.md) — How to write L0/L1/L2 tests
