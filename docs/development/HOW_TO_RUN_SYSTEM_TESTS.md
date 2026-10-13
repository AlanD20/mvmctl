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
   - [Retain the Full Current Collection](#61-retain-the-full-current-collection)
   - [Record the Exact Selected Remainder](#62-record-the-exact-selected-remainder)
   - [Join Verified Receipts Read-Only](#63-join-verified-receipts-read-only)
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
- Verify the filesystem facilities needed by the selected domains, including tmpfs, overlayfs, Btrfs and ext4.

Kernel capability must be checked on each exact runner. A bare inet-table probe is insufficient for bridge
nftables, connection tracking or the modeled production expressions. The bundled `kernel-official` nftables
feature requests `CONFIG_BRIDGE_NETFILTER`, `CONFIG_NF_TABLES_BRIDGE` and
`CONFIG_NF_CONNTRACK_BRIDGE`; verify the actual live configuration and readiness result. A rebuilt or
imported kernel does not replace a differently selected default, mirrored artifact or already-running kernel.
Do not infer capability from a version or filename, or treat one runner's result as proof for future images.

Kernel producer provenance, boot readiness and reusable-base provisioning are separate gates. Embedded
compiler versions and GNU build IDs do not identify the actual compiler binaries, source tree, upstream base
config, overlays or producer invocation. Retain an input/output receipt for a mirrored-source build, including
source/spec/toolchain identity and final config/output hashes. With `--debug`, retain the event
`Kernel base config written` and its consumed base-config digest and URL before configuration mutation.
Retain the actual producer invocation, including selected features, concurrency and clean-build/build-directory
choices. A fresh build cannot reconstruct another artifact's missing provenance or establish that the new artifact boots.

A bounded boot check should bind the live GNU build ID to the exact ELF and check the required devices.
Opening `/dev/kvm` does not prove `KVM_CREATE_VM`; an empty-VM creation and the candidate's disposable
bridge/inet nftables rule application, readback and removal establish additional readiness. Neither readiness
nor a kernel-producer pass qualifies traffic isolation, reusable-base provisioning or the full release matrix.
Retain each gate's exact source/artifact binding and cleanup proof.

The stock reusable-base builder uses 3 GiB RAM and a 12 GiB logical disk. T1 runners use 1 GiB RAM and
a 9 GiB logical disk, except the `cache` domain, which uses an 11 GiB logical disk. T2 provisioning uses
4 GiB RAM and a 9 GiB logical disk. The shared mirror volume is another 6 GiB logical disk, with physical use determined
by its cached contents. Check current physical storage, concurrent allocations and preserved state before
provisioning: logical sizes and sparse files do not establish available storage. The stock preparation path
selects `official:7.0.11`; changing a default does not bind a separately imported producer artifact. Use the
explicit imported-kernel binding below when that artifact is required.

Tier 3 exists for cases that cannot be qualified reliably under nesting. Those tests run directly on the outer
host and require a dedicated clean qualification host or disposable outer VM and explicit authorization.

Check the outer runner's actual cgroup controls as well as the resources visible inside it. An inner
candidate's pressure-only policy cannot remove a finite enclosing limit. Retain exact kernel OOM/process
and cgroup-counter evidence before lifecycle recovery; guest `MemAvailable` and free disk alone do not
establish outer VMM memory safety. Any administrative control adjustment must preserve unrelated VM and
global policy and remain separate from candidate behavior.

**QA resource policy:** Memory measurements and OOM counters are observations only. QA uses no synthetic
`MemAvailable` floor/reserve, full-envelope admission, host-memory-loss rejection, process-group RSS abort
or fixed memory watchdog. Actual kernel limits, workload assertions, timeouts, physical-disk admission,
identity checks and owned cleanup remain in force. Production
[ADR-0019](../adr/0019-pressure-only-vm-memory-control.md) is unchanged; this QA policy neither changes
the actual outer envelope nor guarantees survival under host or ancestor OOM.

**Administrator fault-test admission:** Normal T1/T2 orchestration sets both `MVM_TEST_VM` and
`MVM_TEST_QUALIFICATION_HOST` to the exact disposable runner it provisioned. Fault helpers require a nonempty
exact match while retaining their resource/boot/namespace/process authority checks. For an explicitly approved
direct run inside `mvmctl-030-test`, set both variables to `mvmctl-030-test` in that guest. This opt-in does not
authorize host-direct testing or changes to other VMs; the orchestrator never grants it automatically to Tier 3.

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

First create and qualify the reusable imported-kernel base with explicit `--prepare` or `--rebuild`.
Ordinary T1/T2 execution can then reuse one present cached image matching the exact
`mvm-test-runner-kernel-<full-imported-id>:<candidate-version>` key without either flag. Missing or stale backing
and ambiguous image identity are refused instead of automatically building a base; prepare it separately.
Imported-kernel runs cannot use `--skip-volume-check`. Combining `--image` or `--volume` with imported T1/T2
execution still requires `--prepare` or `--rebuild`; the `--image` action alone builds an image without qualifying it.

The imported image key is separate from the stock image. Its closed baked receipt at
`/opt/mvmctl-test/base-inputs.json` records candidate version/content SHA and kernel ID/ELF SHA/version/architecture/
build ID. Every spawned imported T1/T2 runner requires an exact receipt match before candidate initialization,
then verifies the selected kernel, live Linux version/GNU build ID against the imported ELF and installed-candidate
nftables readiness before tests. Explicit preparation retains its two smoke journeys and explicit local-mirror reads;
cached execution does not repeat the full preparation workflow or qualify the complete matrix. Existing typed
cleanup and mirror checks remain required.

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

Protected-cache retirement fixtures choose an unused physical cache identity before initializing a protected store.
A new pathname alone does not prove freshness: the filesystem can reuse an inode already bound to retired root
authority. Post-clean recovery fixtures likewise select a fresh physical identity after confirmed removal. They
preserve permanent root records, locks and logical bindings; they do not rebind retired authority or change the
[protected-cache retirement contract](../adr/0022-explicit-protected-cache-retirement.md).

The registered Tier 2 `guestfs` domain exercises enabled appliance construction, actual guestfish launch,
reuse, incomplete-appliance repair and selected-cache pruning while preserving unrelated appliance state.
The reusable-base builder installs `libguestfs-tools`, `supermin`, `qemu-system-x86`, `file`, `e2fsprogs`
and `linux-image-generic` with no recommendations. The packaged kernel/module tree is for the GuestFS
appliance; it does not change the explicitly selected external runner kernel.

The unprivileged appliance builder must also be able to read its packaged kernel. Ubuntu can install that image
as root-only `0600`; installing the package therefore does not establish appliance readiness. Administrator setup
may apply the [upstream Ubuntu readability remedy](https://libguestfs.org/guestfs-faq.1.html#Ubuntu), `0644`, to
the exact verified package-owned image while retaining root ownership and content. Resolve the image from package
metadata and verify its identity/checksum before changing permissions; do not chmod every file under `/boot`.
Recheck readability after package replacement. This is an administrator prerequisite, not a product autochmod.
Reusable-base provisioning performs this setup for verified package-owned boot images after installing the
GuestFS dependencies. A no-op readability check on an existing image does not prove fresh-base provisioning;
qualify the newly provisioned image and actual appliance launch separately.

Admit this heavy domain separately: matching supermin/libguestfs sources use a 4 GiB default root and retain
both cached and fixed copies; a genuine cache rebuild can temporarily retain another root. Allow for installed
dependencies, extraction, overlays and scratch as well as the free-space floor. Neither a sparse file nor the
stock 9 GiB Tier 2 disk proves adequate capacity. Stop before provisioning or testing if the physical budget
is unresolved; do not turn a missing prerequisite into a skip or a qualification claim.

### 1.1 Host: Hardware

```bash
# KVM (required — host runs Firecracker for runner VMs)
test -c /dev/kvm && echo "KVM: OK" || echo "KVM: MISSING"

# Virtualization extensions
egrep -c '(vmx|svm)' /proc/cpuinfo
# Expected: > 0

# Memory (total)
free -g | awk '/Mem:/{print $2 " GB"}'
# Observe available RAM and the actual outer cgroup controls; no synthetic QA threshold.

# Disk (free)
df -h ~/.cache/mvmctl 2>/dev/null || echo "~/.cache/mvmctl does not exist yet"
# Admit the selected workload against current physical storage and preserved allocations.
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

Go CI and system qualification remain separate. Run the pinned CI sequence and selector required by
`AGENTS.md`; a green unprivileged selector does not prove the complete root-owned Jailer lifecycle.
Run the full root-Jailer package inside an approved disposable runner. Preserve supervisor/task permissions
as private, but give the Go test child umask `022` so exact fixture modes are exercised; do not relax
production permission admission. Carry prior Go/build evidence only for verified unchanged relevant
source/module/build inputs, preserving its original revision and candidate bindings. Build, unit or focused
runtime success does not qualify the reusable base or complete installed Python matrix.

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
"$MVM_BINARY" bin pull 1.17.0 --default --force
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
  --candidate-version 0.3.0-rc.1 --all \
  --host-reboot-task /absolute/path/to/closed-host-reboot-task
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
│   │   --non-interactive --binary-version 1.17.0
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
resource preparation. It also requires `--host-reboot-task` with completed strict PRE+reboot+POST evidence
bound to the same clean source, candidate and controller. This consumes the retained closed proof read-only;
it does not query a still-running historical runner or weaken the existing quiescent-host admission.
Run it only on a dedicated clean qualification host or
disposable outer VM where the candidate is installed at `/usr/local/bin/mvm`.
On a workstation with state that must be preserved, use focused T1/T2 domains or
`--tier 1,2`: consent does not prove ownership-safe cleanup.

```bash
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  python3 scripts/run-system-tests.py \
  --release-qualification --host-direct --rebuild \
  --candidate-version 0.3.0-rc.1 --all \
  --host-reboot-task /absolute/path/to/closed-host-reboot-task
```

### 4.2 Run Specific Domains

```bash
python3 scripts/run-system-tests.py cli network
python3 scripts/run-system-tests.py --host-direct fresh_env
```

The Tier 2 `host_admin` domain exercises enrollment, marker-only authorization and quiescent reset/re-init
inside its own disposable child VM. It needs nested virtualization and the mirrored launch assets. It never
resets the shared runner or the outer controller. Collection is not a runtime pass: clean-host execution,
checked child cleanup and retained administrator-state receipts are required before qualifying this journey.
The module also contains separate password-authenticated second-operator/typed-dispatch and active-network
reset-refusal coverage, plus real read-only-filesystem init/reset failure-and-retry journeys. Qualify each
registered case with strict installed-candidate evidence and checked preservation/cleanup. A package-setup
timeout before fault injection does not diagnose a product fault. Abrupt process interruption and
ambiguous-runtime recovery require separate evidence.

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

The scheduler allows 6,000 seconds without a completed T2 domain (900 seconds for T1), including
provisioning, tests, reporting and cleanup. The stock T2 remote pytest caller still has a 960-second
limit. These are separate limits: the scheduler waits again after each completion, so its completion
gap does not bound the total batch duration. On expiry it cancels queued work and joins active workers;
affected domains fail even if they finish successfully during that join.

Allow for provisioning, pytest, report retrieval and worker cleanup when setting an external task or
transport deadline. The scheduler allowance does not extend individual command or external deadlines,
and joining active workers can outlast the completion-gap limit.

### 4.6 Orchestrator Flags Reference

| Flag | Default | Description |
|------|---------|-------------|
| _domains_ (positional) | — | Specific domains to test (e.g., `cli network`) |
| `--all` | `false` | Run all T1 + T2 + host-direct T3 domains; dedicated clean qualification host only |
| `--host-direct` | `false` | Required acknowledgment before selecting any Tier 3 domain or `--all`; does not guarantee isolation or safe cleanup |
| `--release-qualification` | `false` | Require the unfiltered full-matrix release identity gate and closed reboot proof; requires `--all --host-direct --rebuild --candidate-version --host-reboot-task` |
| `--host-reboot-phase` | — | Explicit `pre` or `post` continuation against existing `mvmctl-030-test`; separate from ordinary tier batches |
| `--host-reboot-task` | — | Absolute private owned receipt directory for explicit phases; completed strict POST proof is mandatory for full release qualification |
| `--host-reboot-envelope` | — | POST-only private external administrator receipt binding the fresh runner boot/process and exact observed outer controls (canonical finite integers or literal `"max"`) |
| `--collect-release-nodes` | — | Standalone full-registry collection inside the named runner; retain evidence in an empty private task directory (§6.1) |
| `--record-evidence` | — | Retain literal reports for a named-runner test selection; does not finalize qualification (§6.2) |
| `--collection-task` / `--selected-node-file` | — | Bind exact-node execution to retained current collection and an explicit JSON remainder (§6.2) |
| `--completed-matrix-task` | — | Repeat for verified current receipts to subtract already-proved nodes (§6.2) |
| `--official-kernel-carry` | — | Optional single sealed historical eight-case proof; exact candidate and relevant source inputs must match (§6.3) |
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

### 4.7 Explicit Named-Runner Host-Reboot Journey

This journey is deliberately separate from ordinary T1/T2 batches. It operates only on the existing
`mvmctl-030-test`, pins the configured outer controller without replacing it, and runs pytest inside that
runner. A full release invocation cannot count a PRE checkpoint or ordinary tier success as a reboot pass.
Retain failed or incomplete tasks unchanged; do not repair their files and relabel them successful.
The runner checks a guest-filesystem durability barrier before force reboot and exact staged-source
verification after reconnect, before POST. A staging failure requires source/durability diagnosis; it is
not evidence of memory exhaustion or a reason to introduce a RAM guard.

Host-phase orchestration uses the unchanged outer controller's `mvm ssh --cmd` with the runner's attached
`aland` key at `~/.config/mvmctl/keys/aland`. This includes source staging and receipt retrieval, avoiding
the legacy controller's reboot-time vsock omission. It does not replace that controller, introduce a
production fallback, or weaken candidate-side VM exec/vsock assertions. Actual PRE/reboot/POST evidence remains required.

Use one clean, fixed source and candidate for both phases. Select the existing imported ELF with
`MVM_RUNNER_KERNEL_ID` and `MVM_RUNNER_KERNEL_SHA256`; bind `MVM_BINARY` to the installed outer controller
and `MVM_CANDIDATE_BINARY` to the exact candidate already installed inside the named runner. Set the asset
mirror as usual. Create an absolute, operator-owned `0700` task directory under ignored project-local storage.

```bash
python3 scripts/run-system-tests.py host_reboot --host-reboot-phase pre \
  --host-reboot-task /absolute/path/to/private-host-reboot-task
```

PRE creates only its owned nested VM, writes and reads a guest marker, retains source/candidate/inventory
proof, and reboots the named runner through the unchanged outer controller. A successful PRE exit means
**phase complete, release incomplete**. Do not repeat it against the same task.

After reconnect, retain a fresh private receipt of the exact observed outer controls for this boot, runner
UUID and outer PID/start tick. Encode each `memory.high` and `memory.max` value as a canonical nonnegative
JSON integer when finite, or the literal JSON string `"max"` when observed. POST must match that receipt
exactly against actual controls and the bound runner identity. No 8 GiB adjustment is required; POST does
not guess headroom, add a memory watchdog, or modify controls itself. Then continue the same task:

```bash
python3 scripts/run-system-tests.py host_reboot --host-reboot-phase post \
  --host-reboot-task /absolute/path/to/private-host-reboot-task \
  --host-reboot-envelope /absolute/path/to/fresh-admin-envelope.json
```

POST must prove changed host boot, stale-running reconciliation, restart and retained-marker exec, exact
persistent inventory preservation, and typed owned cleanup. Both phases need native exit zero and exact
singleton JSON/JUnit proof. Only a closed successful POST task can be supplied to full release qualification.
Changing bound source, candidate or controller invalidates the proof; retained historical results are never
relabeled as a run on different inputs.

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

The optional exact-node workflow retains a full current collection, runs only explicitly unproved nodes, then joins
verified receipts without replaying tests. It does not replace the release gates or the separate
[host-reboot journey](#47-explicit-named-runner-host-reboot-journey).

### 6.1 Retain the Full Current Collection

Execute collection **inside the existing `mvmctl-030-test`**, from the root of the clean, exact qualified source checkout.
Set the actual candidate paths and retain the qualification environment, including its cache/config, asset mirror and
any imported-kernel binding. Here `MVM_BINARY` is the runner's installed candidate, not the outer controller:

```bash
export MVM_TEST_VM=mvmctl-030-test
export MVM_TEST_QUALIFICATION_HOST=mvmctl-030-test
export MVM_BINARY=/usr/local/bin/mvm
export MVM_CANDIDATE_BINARY=/absolute/path/to/qualified-candidate/mvm
python3 scripts/run-system-tests.py --collect-release-nodes /absolute/path/to/collection-task
```

The candidate artifact must be a distinct file with the same trusted bytes/version as the installed runner binary.
Use an existing, empty, operator-owned `0700` directory at an absolute canonical path. Collection is standalone:
no domain/tier selectors or preparation/test flags. It collects the entire registered node set without running fixtures
and retains the binding, literal node list, native output and sealed `terminal.json`. Successful completion means the
full current registry was collected, not that any test passed. Preserve those files unchanged.

### 6.2 Record the Exact Selected Remainder

Create an explicit selected-node JSON file with exactly these keys. Replace the illustrative values with the actual
SHA-256 of the retained `terminal.json` bytes and **every** remaining exact selector in the selected domains/tiers:

```json
{
  "schema_version": 1,
  "collection_terminal_sha256": "<actual literal SHA-256 of collection-task/terminal.json>",
  "nodes": ["tests/system/cli/test_cli.py::TestHelpCommand::test_help_root"]
}
```

The selector above illustrates the format, not a complete `cli` selection. Preserve parametrized node IDs exactly.
The accepted set is the full selected domains minus nodes proved by verified completed receipts (and an explicitly
accepted kernel carry), excluding the separately proved host-reboot nodes. Unknown, missing, duplicate or already-proved
nodes are rejected; an empty remainder requires no new run. A file-level or test-count summary is not node proof.

Run the selected batch inside the same named-runner/source/candidate context, using a new empty private task directory:

```bash
python3 scripts/run-system-tests.py cli \
  --record-evidence /absolute/path/to/new-matrix-task \
  --collection-task /absolute/path/to/collection-task \
  --selected-node-file /absolute/path/to/selected-nodes.json
```

Add `--completed-matrix-task /absolute/path/to/completed-task` once per actual current receipt used in the subtraction.
The three selection/evidence flags above are required together; do not combine them with `--release-qualification`,
`--rebuild` or `--skip-volume-check`. Tier 3 selections still require `--host-direct` and an explicitly authorized clean
qualification host; retain applicable preparation prerequisites. Preparation-only `--image` does not accept
`--record-evidence`.

A recorded batch counts only when its literal native, JSON, JUnit and node-phase reports agree on the exact selected
nodes, all pass, and cleanup closes successfully. Skips, deselections, failures, collection errors, xfails/xpasses,
missing or extra nodes are not accepted evidence. Preserve the complete task unchanged for verification.

### 6.3 Join Verified Receipts Read-Only

Run the finalizer from the exact clean qualified source, with the same candidate artifact and the **original unchanged
outer controller** configured as `MVM_BINARY`. Do not inherit the inner runner's `MVM_BINARY`. Supply the retained full
collection, every completed matrix task and the closed PRE/POST host-reboot task:

```bash
MVM_BINARY=/absolute/path/to/original-outer-controller \
MVM_CANDIDATE_BINARY=/absolute/path/to/qualified-candidate/mvm \
python3 scripts/system_test_evidence.py \
  --qualified-source /absolute/path/to/qualified-source \
  --collection-task /absolute/path/to/collection-task \
  --matrix-task /absolute/path/to/completed-matrix-task \
  --host-reboot-task /absolute/path/to/closed-host-reboot-task
```

Repeat `--matrix-task` for additional completed batches. The full current registry remains authoritative: the verifier
requires exact node coverage without gaps or overlaps and revalidates source/candidate bindings, literal report seals
and the unchanged outer-controller reboot proof. It does not rebuild, install, execute tests or obtain a fresh reboot
observation. Exit zero with `verdict: "PASS"` means this evidence join passed; `INCOMPLETE` is not qualification success.
Neither result alone declares the final release passed or waives other release gates and approval.

Optionally supply `--official-kernel-carry /absolute/path/to/original-sealed-kernel-task` to both selected execution and
finalization. This verifier accepts only its one sealed historical eight-case proof, the exact original candidate bytes
and version, and unchanged relevant source hashes. A changed binary or relevant input is rejected: there is no
retargeting to a newer artifact, and carried nodes remain historical proof rather than a new runtime pass.

---

## 7. Reference

- [docs/system-test-architecture.md](../system-test-architecture.md) — Three-tier architecture (primary reference)
- [docs/RC_QA.md](../RC_QA.md) — Release gates, evidence collection, regression criteria
- [docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md](HOW_AGENTS_WRITE_SYSTEM_TESTS.md) — How to write L0/L1/L2 tests
