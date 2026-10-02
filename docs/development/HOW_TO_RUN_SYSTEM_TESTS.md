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
- The runner has its own network stack, but its kernel must support the **modeled bridge and inet nftables rules**, not
  merely create an inet table. Check the installed candidate's `mvm host status --json` and require
  `data.resources.nftables_available: true` before treating network or policy tests as release evidence.
- Check `sudo` and `docker` availability in the exact runner image. Kernel module loading also depends on that
  image's matching module tree; do not assume every required feature is compiled into the 7.0.11 asset.
- tmpfs, overlayfs, btrfs, ext4 — all standard Linux facilities

The older `vmlinux-official-7.0.11-x86_64` image used by `mvmctl-030-test` on 2026-09-26 accepted a bare inet
nftables table but rejected a bridge-family table. The named runner was replaced on 2026-09-27 with a rebuilt
bridge-capable 7.0.11 kernel. Its installed 0.3.0 candidate reports `nftables_available: true`, and a bridge-table
create/list/delete probe passed. This does not qualify the orchestrator's separately provisioned runner images:
check the capability on each exact runner before using its network or policy results as release evidence.

The bundled `kernel-official` nftables feature now also requests `CONFIG_NF_CONNTRACK_BRIDGE=y`. A fresh
2026-09-28 readback confirmed that the named runner boots the rebuilt `mvmctl-030-bridge-ct` kernel
(SHA-256 `5b56e531b3f9621f176289847b1f1a295daea4bf0a4e89fed63a3d182664d4b2`), whose live config has
`CONFIG_BRIDGE_NETFILTER`, `CONFIG_NF_TABLES_BRIDGE`, and `CONFIG_NF_CONNTRACK_BRIDGE` enabled. Its selected
inner default and the older mirrored official 7.0.11 file are different bytes and lack both bridge nftables flags.
Do not infer kernel capability from version or filename, or treat this runner result as proof that the mirror or
every future runner image has been rebuilt. Historical source/config/toolchain provenance for the retained kernel
artifacts is incomplete; the flag change does not reconstruct that provenance.

The retained config and ELF identify GCC `16.2.1 20260810`, GNU assembler/linker `2.47`, and GNU build ID
`edf249dea9c6b0a5afa1aa47526a1fd9626009b3`. Embedded version strings do not identify the actual compiler binaries,
source tree, upstream base config, overlays, or producer invocation. Closing this provenance gap requires a fresh
mirrored-source build with an input/output receipt, followed by a candidate-qualified reusable-base provisioning
check. Do not rebuild or reboot the current runner merely to repeat its already recorded capability check.

Fresh official-kernel producer receipts must retain the `--debug` event `Kernel base config written`, including
the consumed base-config digest and URL before configuration mutation, together with the source/spec/toolchain
and final config/output hashes. Its diagnostic source change passed focused kernel Go/vet/format checks in the
named runner. On 2026-09-29 the installed candidate then completed a fresh Linux 7.0.11 build from mirrored
source using GCC 13.3.0, the bundled spec, and `--features nftables,tuntap,kvm,btrfs --jobs 1 --clean-build
--keep-build-dir --debug`. Its consumed base-config SHA-256 was
`153ca1b40f3312bfb40b7587b471ea548a915f98f480f0d0892a1537957a2147`; the final config was
`86f0a0147b7cafa6d62caad1c6bb52221c8e0ba13100cef1f5791ea2dfc221fd`, and both the built ELF and managed-cache
copy were `3a7eefcb29812f1b8b58fa1f3d276ee6afe25f97e1f154e4b6f946af9ec3ee0d`. The final config enables inet
and bridge nftables, bridge connection tracking, KVM, TUN and Btrfs. Input/toolchain/output receipts were retained.
This producer pass does not reconstruct the older kernel's provenance, prove the new kernel boots, replace the
running or mirrored kernel, or qualify a reusable-base image; those remain separate gates.

A subsequent 2026-09-29 bounded boot check passed inside `mvmctl-030-test` using its installed candidate and an
owned 256 MiB networkless nested VM. The guest reported Linux 7.0.11 and GNU build ID
`79ee949b4f07aa6b8567741ea49862ab1a4f6b41`, matching the fresh ELF; it opened `/dev/kvm` read/write and exposed
TUN and Btrfs. The strict one-case report passed with checked typed cleanup and preserved defaults, original assets
and configuration. This proves boot and device availability, not `KVM_CREATE_VM`, child nftables rule application
or reusable-base provisioning. Neither the outer runner's selected kernel nor the mirrored default was replaced.

On 2026-10-02, one bounded case on that exact fresh import also passed actual KVM API 12/empty-VM creation
and the production readiness probe's disposable bridge/inet nftables rule application, readback and removal.
An over-strict interphase kernel timestamp guard initially retained the seeds; a narrow guard correction then
completed normal typed cleanup and restored the exact baseline without rerunning the test. This does not prove
reusable-base provisioning, traffic isolation or the complete release matrix.

The stock reusable-base workflow also has its own resource budget: the builder uses 3 GiB RAM and a 12 GiB
logical disk; T2 provisioning uses 4 GiB RAM and a 9 GiB logical disk. The shared mirror volume is another
6 GiB logical disk, with physical use determined by its cached contents. The replacement `mvmctl-030-test`
has 6 GiB RAM and a 40 GiB logical disk as verified on 2026-10-01; this is not a provisioning capacity guarantee.
That day's parent free space was approximately 9.9 GiB, insufficient to admit even the 12 GiB builder under
the conservative no-sparse-credit budget. Check current physical storage, parent memory headroom and preserved
state before provisioning; another runner replacement requires separate approval. Logical disk sizes alone
do not prove that the required physical storage is available. The stock preparation path selects
`official:7.0.11`; changing a default does not bind a separately imported fresh producer artifact.

Tier 3 exists for cases that cannot be qualified reliably under nesting. Those
tests run directly on the outer host and therefore require a dedicated clean
qualification host or disposable outer VM.

### Using an Already Imported Runner Kernel

For a fresh custom Linux 7.0.11/x86_64 artifact, set both `MVM_RUNNER_KERNEL_ID` and
`MVM_RUNNER_KERNEL_SHA256` in the controller context that runs the orchestrator. Use the full imported
64-character ID from `mvm kernel inspect <id> --json` and the SHA-256 of that exact ELF, not its filename,
version or the kernel ID itself. The artifact must already be present under that controller's managed cache.

```bash
MVM_ASSET_MIRROR="$HOME/.cache/mvm-asset-mirror" \
MVM_RUNNER_KERNEL_ID="<full-imported-kernel-id>" \
MVM_RUNNER_KERNEL_SHA256="<exact-elf-sha256>" \
python3 scripts/run-system-tests.py --prepare --workers 1 vm_lifecycle
```

Run this only in the approved disposable qualification environment, with the separate controller/candidate
paths configured as described below. This selects the imported kernel explicitly for the builder, both preparation
runners and normal T1/T2 runners. It does not pull, relabel or make that kernel the default. Invalid, missing or
changed input fails without an official-kernel fallback.

Imported-kernel runs require `--prepare` or `--rebuild` and cannot use `--skip-volume-check`. The `--image`
action alone builds an image; it does not qualify it. The reusable image key is
`mvm-test-runner-kernel-<full-imported-id>:<candidate-version>`, separate from the stock image. Its
`/opt/mvmctl-test/base-inputs.json` records candidate version/content SHA and kernel ID/ELF SHA/version/architecture/
build ID; reuse requires an exact receipt match. Preparation checks the selected kernel and live GNU build ID against
the imported ELF, plus installed-candidate nftables readiness and explicit local-mirror reads. Existing typed
cleanup and preparation gates remain required.

This input binding does not admit the resource budget, qualify the complete Python matrix, or prove nested
KVM workload behavior by itself. Omit both variables to retain the stock official-kernel workflow.

### Optional Local GuestFS Qualification

GuestFS remains an optional backend, disabled by default. With
`mvm config set settings guestfs_enabled true`, `mvm cache init` requests a
selected-cache appliance build. Qualify appliance construction and reuse locally
inside the disposable runner; these heavy checks do not belong in ordinary Go CI.

The cache system tests also exercise preservation of unrelated GuestFS appliance
state and processes without requiring GuestFS installation. Those preservation
cases do not prove that an enabled appliance builds or warms successfully. Cache
cleanup no longer performs automatic global GuestFS process or orphan cleanup.

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
- util-linux (`script` supplies a PTY for interactive `mvm exec` tests)
- build-essential, bc, bison, flex, libncurses-dev, libssl-dev, libelf-dev, pkg-config, git, curl, dwarves (for `kernel_build` tests)

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

For candidates built inside a runner, use a clean standalone checkout with a genuine `.git` directory. With the
pinned Go 1.26.3 toolchain, a linked worktree nested inside another Git checkout can compile the intended source yet
stamp the enclosing checkout's revision: Go's VCS discovery skips the worktree's `.git` file. Check
`go version -m <candidate>` for the intended full `vcs.revision` and `vcs.modified=false` before installation.
Stop on a mismatch; do not disable VCS stamping or override metadata to make the check pass. Source-editing worktrees
remain useful, but their Git `HEAD` alone does not prove the built candidate's identity.

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
"$MVM_BINARY" bin pull 1.16.0 --force
"$MVM_BINARY" bin pull 1.16.2 --default --force
```

These pulls populate the local mirror directory. On subsequent runs, each pull
reads the large object from the mirror instead of downloading it again. Small checksum/version metadata requests still
go to the fixed upstream source so cached content can be verified.

The shared `asset-mirror` volume is a point-in-time, read-only copy. If a pull replaces a stale host object or adds a
missing one, stop and remove test VMs retaining that volume, then rebuild the QA volume before continuing:

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  python3 scripts/run-system-tests.py --volume
```

With a 0.3.0 controller, protected-volume inspection intentionally hides the backing path. Ordinary preparation
reuses its ID-bound protected accounting; it does not treat that empty path as a stale record. Explicit volume
rebuild uses controller-mediated removal of the exact inspected ID before creating a new source. If root authority
refuses removal because references remain or source identity changed, preparation stops; do not delete protected
files or journals manually. Fresh raw sources use `volumes/<volume-id>.raw` and are seeded through a pinned descriptor.

Do not expect the read-only guest copy to self-heal. The downloader can detect a checksum mismatch and attempt its fixed
HTTP source, but the system-test runner rejects that fallback and any HTTP-downloaded mirror auto-population. Each
builder artifact pull must report an explicit local-mirror read; the runner captures and re-emits bounded pull output so
the failure remains diagnosable. Repair the writable host mirror and reseed the volume instead. Small checksum/version
metadata requests may still use their fixed upstream sources and are not treated as artifact fallback.

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
The shared-volume replacement has the same controller-authorized removal requirements as `--volume`; it does not
force removal past retained root references or uncertain file ownership.

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
│   │   --non-interactive --binary-version 1.16.2
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
  RESULTS: 12 passed, 0 failed, 12 total
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
| **PASS** | The bounded report proves non-empty collection and every selected item passed | ✓ |
| **FAIL** | A test/outcome failed or exact machine-readable evidence was unavailable | ✗ — must fix before release |

### 5.1a Release Candidate (RC) Zero-Tolerance Policy

**A Release Candidate MUST have zero failures AND zero skips.** Every test must
pass. A skip is treated as a release blocker — it means either:
- The environment is missing a prerequisite (fix the environment)
- The test is testing a feature that doesn't exist in the Go CLI (remove the test or add the feature)
- The test's skip condition is too broad (tighten the condition)

Before signing off an RC, each domain must produce one strict outcome report with non-empty collection, matching pytest
and report exit status, every selected item passed, and zero failures, errors, collection errors, deselections, skips,
XFAIL, or XPASS. The orchestrator enforces this for T1/T2 inside runner VMs and T3 in a private local report. Missing,
malformed, duplicate-field, oversized, inconsistent, or uncleared reports turn the domain into `FAIL` and retain pytest
output plus the validation reason. A printed `[PASS]` therefore includes this machine-evidence gate.

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
| `nftables_available: false` or bridge-family `operation not supported` | Runner kernel lacks the required bridge/inet nftables behavior | Use a runner kernel with the required nftables capabilities, then rerun `mvm host status --json`; installing the `nft` or `iptables` CLI does not supply missing kernel support. |
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
