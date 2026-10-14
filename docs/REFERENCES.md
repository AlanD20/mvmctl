# Command Reference & Configuration

This document provides detailed reference for all `mvm` commands, configuration options, and environment variables.

---

## Table of Contents

- [Command Reference](#command-reference)
- [Selectors](#selectors)
- [mvm init](#mvm-init)
- [mvm host](#mvm-host)
- [mvm kernel](#mvm-kernel)
- [mvm image](#mvm-image)
- [mvm bin](#mvm-bin)
- [mvm vm](#mvm-vm)
- [mvm snapshot](#mvm-snapshot)
- [mvm console](#mvm-console)
- [mvm network](#mvm-network)
- [mvm policy](#mvm-policy)
- [mvm key](#mvm-key)
- [mvm config](#mvm-config)
- [mvm cache](#mvm-cache)
- [mvm logs](#mvm-logs)
- [mvm exec](#mvm-exec)
- [mvm ssh](#mvm-ssh)
- [mvm cp](#mvm-cp)
- [mvm volume](#mvm-volume)
- [mvm env](#mvm-env)
- [Configuration](#configuration)
- [Cloud-Init](#cloud-init)
  - [How Net Mode Works](#how-net-mode-works)
  - [Net Mode Security Architecture](#net-mode-security-architecture)
- [Environment Variables](#environment-variables)
- [Cache Directory Structure](#cache-directory-structure)

---

## Command Reference

**Root command flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--verbose` | persistent | Enable verbose output |
| `--debug` | persistent | Enable debug mode |
| `--json` | persistent | Emit one schema-versioned response for a supported finite command |
| `--version` | root | Show version and exit |

`--json` applies to reads and mutations. The response has `schema_version: 1`, `status` (`success`, `error`, or
`partial`), command-specific `data`, and `error`. An error preserves its code, class, message, optional operation and
entity, and details. Batch data preserves each result and its structured error. A partial response retains completed
work and exits unsuccessfully; callers must inspect both status and data before retrying. Read results now live under
`data`, so scripts consuming the old bare list or object must be updated. Array-valued resource listings, such as
`key ls`, `vm ls`, and `vm ps`, remain arrays under `data`; an empty result is `[]`, not `null`.

Streaming and interactive commands reject structured output with `cli.output.unsupported`: `exec`, `ssh`, `logs`,
completion, `run`, the `init` wizard, and console attachment. Finite console state and kill commands support it.
Arguments after `--` belong to the guest command and do not select host JSON output.

### Selectors

Resource commands (`inspect`, `rm`, `pull`, etc.) accept **selectors** — flexible
identifiers that the system resolves to a specific resource. Each resource domain
defines its own set of selectors, tried in priority order. The general resolution
pattern is:

1. Try the highest-priority selector.
2. If it matches exactly one resource, return it.
3. If not found, fall through to the next priority.
4. On any other error (DB error, ambiguous match), propagate immediately.

| Domain | Selectors (tried in priority order) |
|--------|-------------------------------------|
| **kernel** | `type:version` → absolute path → ID prefix → type name → relative path |
| **image** | `type:version` → type name → display name → ID prefix |
| **binary** | `type:version` → ID prefix → type name |
| **vm** | Name → IP address → MAC address → ID prefix |
| **network** | Name → ID prefix |
| **volume** | Name → ID prefix |
| **key** | Name → fingerprint prefix → `.pub` file path |
| **snapshot** | Name → ID prefix |

**Notes:**
- **ID prefix** — Every resource is assigned a unique SHA-based ID at creation time.
  Any unique prefix resolves to the resource (minimum length enforced by the DB).
- **VM IP/MAC** — If the input contains `.`, it is treated as an IP lookup. If it
  contains `:`, it is treated as a MAC lookup. Both fail immediately on mismatch
  (no fallthrough to ID prefix).
- **Key fingerprint** — The resolver tries both bare input and `SHA256:`-prefixed
  variants for fingerprint matching.
- **`type:version` shorthand** — The colon-separated format works for kernel, image,
  and binary resources. The type portion alone resolves to the latest version.
- Each resource section below documents its specific selectors with usage examples.

### `mvm init`

First-time operator setup wizard. Coordinates host admission, a default network, cache initialization, and a
Firecracker/Jailer release pair. Kernel, image, and SSH-key setup are separate commands. The approved v0.3.0 launch
contract requires the exact matching Firecracker/Jailer 1.17.0 pair; affected runtime qualification remains pending.

```
mvm init [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--non-interactive` | Use defaults, skip all prompts | `false` |
| `--skip-host` | Skip host init step | `false` |
| `--skip-network` | Skip default network creation | `false` |
| `--binary-version <version>` | Download this exact Firecracker version when no local pair exists | empty (interactive choice or latest in non-interactive mode) |

`--binary-version` does not replace an existing default pair. To select the v0.3.0 launch pair when another release is
already installed, run `mvm bin pull 1.17.0 --default` and check `mvm bin ls` before creating a VM.

---

### `mvm host`

Host configuration commands for one-time, machine-global setup.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm host install-system` | — | Administrator-only early bootstrap. Atomically installs the running trusted artifact as root-owned `/usr/local/bin/mvm` without loading user cache, configuration, database, or Cobra state. |
| `mvm host init` | `--operator USER` (direct root only), `--json` | Early administrator setup with a root-owned journal, operator enrollment, and compiled host changes. |
| `mvm host status` | `--json` | Show current host configuration state vs expected |
| `mvm host info` | `--refresh`, `--json` | Show host hardware, limits, and VM capacity projection |
| `mvm host clean` | `-f, --force` | Observe the caller's root VM inventory, then remove its locally registered networks through typed root authority. Refuses running VMs or incomplete observation; does not force-stop VMs or reset shared host settings. |
| `mvm host reset` | `--force`, `--operator USER` (direct root only), `--json` | Early administrator rollback under retained global quiescence. Requires marker-only sudoers and compatible journals; refuses live or ambiguous state rather than force-stopping VMs. Admission remains disabled afterward. |

`host install-system` accepts only the exact command shown: no flags or extra arguments, including `--json`,
`--debug`, or `--verbose`. It runs before ordinary CLI initialization.

`host info` reads a caller-owned capacity cache, detecting and saving capacity on the first read and again with
`--refresh`. It does not infer administrator setup from that cache. Structured `host info --json` omits `setup`;
`host status --json` reports `data.state: null` rather than claiming root admission from the legacy SQLite bit.

`host status` verifies the fixed managed sudoers policy through authenticated root observation when it cannot
confirm absence directly. Permission denial does not mean the policy is missing. Confirmed absence reports
`data.sudoers_exists: false`; unsafe policy metadata or content, unavailable observation, and a missing or invalid
observer response return an error instead of a successful status result.

The interactive `mvm init` wizard can offer explicit administrator setup even when the operator cannot yet observe
the policy.
It reports that initial state as unverified, then requires successful host-policy and group verification after setup.
If the current session lacks the enrolled group, log out and back in or run `newgrp mvm` before retrying.

On a clean host, run `sudo /usr/local/bin/mvm host init`; sudo selects its invoking non-root account. In a direct-root
session, use `/usr/local/bin/mvm host init --operator USER`. Sudo invocation rejects `--operator`. The early route runs
before user configuration, cache, SQLite, and ordinary service initialization. Its journal beneath
`/var/lib/mvmctl/host` preserves original global state and mutation progress. Separate root-owned
`operator-<uid>.json` records preserve each enrolled operator's prior membership. Multiple operators are supported;
an interrupted initial global transaction must first be recovered by its original operator. Existing transitional
wildcard sudo grants cannot establish administrator authentication: that sudo entry
refuses them and directs the administrator to direct root with an explicit operator. Init does not install the binary.

Host initialization uses a schema-2 administrator journal and enables privileged admission only after host setup and
operator enrollment complete. Old schema-1 administrator journals require a clean installation; they are not adopted.
Missing, disabled, or unsafe admission state blocks ordinary privileged requests.

Generated sudoers permits only the fixed system binary's `__mvm_privileged_v1` marker. Install/init/publication reject
older wildcard and raw-tool policies. No host policy activation or runtime qualification was performed in this pass.
Administrator reset is implemented for compatible recorded state: it refuses live or ambiguous ownership, restores
recorded settings through retryable receipts, and leaves admission disabled afterward or after a restoration error.
It does not force-stop VMs, erase authority history, or turn old unowned state into managed state.

The early parser supports JSON boolean forms, help, and argument boundaries. Host clean/reset are not a supported
legacy-state removal procedure in this development build.

---

### `mvm kernel`

Kernel management — list, pull, remove, inspect, set default, and import kernels.

#### Selectors

The kernel resolver tries selectors in priority order:

1. **`type:version`** — e.g. `official:6.19.9`, `firecracker:v1.15`
2. **Absolute path** — path starting with `/` that exists on disk, e.g. `/home/user/vmlinux`
3. **ID prefix** — unique prefix of the kernel SHA ID
4. **Type name** — matches the kernel type (resolves to latest version), e.g. `official`
5. **Relative path** — path relative to CWD that exists on disk, e.g. `./vmlinux-custom`

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm kernel ls` | `--json`, `-r, --remote`, `--no-cache`, `--long` | List cached kernels (or available remote kernels with `--remote`) |
| `mvm kernel pull` | `[type:version]`, `--type`, `--version`, `--default, -d`, `--jobs`, `--keep-build-dir`, `--clean-build`, `--config`, `--features` | Pull or build a kernel. Supports `type:version` shorthand (e.g. `official:6.19.9`) |
| `mvm kernel import` | `NAME`, `PATH`, `--version`, `--default, -d` | Register a vmlinux file as a kernel in the database |
| `mvm kernel default` | `SELECTOR` | Set a kernel as the default |
| `mvm kernel rm` | `[SELECTORS]...`, `-f, --force` | Remove one or more kernels |
| `mvm kernel inspect` | `SELECTOR`, `--json` | Show detailed information about a kernel |

**`pull` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `[type:version]` | (positional) type:version shorthand e.g. `official:6.19.9` | — |
| `--type TYPE` | Kernel type: `firecracker` or `official` | — |
| `--version VERSION` | Kernel version | (latest) |
| `--default, -d` | Set as default after fetch | `false` |
| `--jobs N` | Parallel build jobs (official only) | `0` (not explicitly set) |
| `--keep-build-dir` | Keep build directory (official only) | `false` |
| `--clean-build` | Skip cache and force clean build (official only) | `false` |
| `--config PATH` | Custom kernel config file to apply as a fragment | — |
| `--features TEXT` | Comma-separated kernel features (e.g. `kvm`, `nftables`, `tuntap`, `btrfs`). Use `all` or `*` to enable every feature defined in the selected kernel spec. | — |
| `--skip-checksum` | Skip SHA256 checksum verification | `false` |

```
# List available remote kernel versions
mvm kernel ls --remote

# Pull the official kernel at version 6.19.9
mvm kernel pull official:6.19.9

# Pull with custom config fragment
mvm kernel pull --type official --version 6.19.9 --config /path/to/config.fragment
```

---

### `mvm image`

Image management — download, list, inspect, import, and manage VM images.

#### Selectors

The image resolver tries selectors in priority order:

1. **`type:version`** — e.g. `ubuntu:24.04`, `alpine:3.21`
2. **Type name** — matches the image type (resolves to latest version), e.g. `ubuntu`
3. **Display name** — matches the display name, e.g. `Ubuntu 24.04 LTS`
4. **ID prefix** — unique prefix of the image SHA ID

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm image ls` | `--json`, `-r, --remote`, `--no-cache`, `--type`, `--long` | List cached images (or available remote images with `--remote`) |
| `mvm image pull` | `[SELECTOR]`, `--type`, `--version`, `--force, -f`, `--no-cache`, `--default, -d`, `--skip-optimization`, `--disable-detector` | Download an image by selector |
| `mvm image import` | `NAME`, `VM_SELECTOR` or `PATH`, `--format`, `--root-partition`, `--default, -d`, `--force, -f`, `--skip-optimization`, `--disable-detector` | Import a local image file or create a base image from a VM's rootfs |
| `mvm image default` | `SELECTOR` | Set the default image for VM creation |
| `mvm image rm` | `[SELECTORS]...`, `--force, -f` | Remove cached images by selector |
| `mvm image warm` | `[SELECTOR]`, `--all, -a` | Pre-decompress image to ready pool for fast VM creation (warms all images if omitted) |
| `mvm image inspect` | `SELECTOR`, `--json` | Show detailed information about an image |

**`pull` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `-t, --type TEXT` | Image type from images.yaml (e.g. `ubuntu`, `debian`, `firecracker`) | (first matching) |
| `--version VERSION` | Version override | (latest) |
| `--force, -f` | Re-download even if cached | `false` |
| `--default, -d` | Set as default after fetch | `false` |
| `--no-cache` | Skip cached version listing and fetch live from upstream | `false` |
| `--skip-optimization` | Skip filesystem optimization | `false` |
| `--disable-detector NAME` | Comma-separated detectors to disable: `type`, `label`, `size`, `filesystem`, `all` | — |

**`import` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--format FORMAT` | Image format: `qcow2`, `raw`, `tar-rootfs`, or `auto` | `auto` |
| `--root-partition N` | Root partition number (e.g. 1, 2) | `0` (auto-detect) |
| `--version VERSION` | Set image version | — |
| `--default, -d` | Set as default after import | `false` |
| `--force, -f` | Overwrite existing | `false` |
| `--skip-optimization` | Skip OS cache cleanup (deblob), keep plain ext4 | `true` |
| `--disable-detector NAME` | Comma-separated detectors to disable: `type`, `label`, `size`, `filesystem`, `all` | — |

**Importing from a VM:** `mvm image import NAME VM_SELECTOR` reads the VM's current protected root disk through
the enrolled, typed root dispatcher, not its original operator-visible staging file. A running VM is paused for
the copy and resumed before extraction; already paused or stopped sources retain their state. This produces a
disk image, not a memory or attached-data-volume snapshot. The best-effort guest `sync` hint does not guarantee
application transaction consistency.

Temporary independent copies live under root-controlled `/var/lib/mvmctl/vm-rootfs-export-data/<operator-uid>`
with operator read access. Export copying skips source-reported holes while preserving the logical disk length
and contents. Allow space on that filesystem as well as the selected managed cache, where protected
VM disks and final images remain. Typed cleanup releases the temporary copy after extraction and before final
publication; copy, state-recovery or cleanup failures remain explicit rather than falling back to staging.

An ext4 disk copied while the source VM is paused can still require filesystem journal replay. With LoopMount,
import runs fixed unprivileged `/usr/sbin/e2fsck -p -E journal_only` on a new disposable inspection copy before
the unchanged root read-only OS inspection. This does not modify the protected VM disk, root-controlled export
or extracted source, and it is not general filesystem repair. Journal preparation failure stops the import;
it does not enable a writable inspection mount or discard the journal. See the
[LoopMount dependencies](DEPENDENCIES.md#e1-loop-mount-default).

If an interrupted import leaves no public image row, `mvm image rm --force EXACT_64_CHARACTER_IMAGE_ID` can
release its independent export. The read-only `<image-id>.copy` marker in the operator's export directory identifies
the ID; it is not deletion authority. This recovery requires one full lowercase hexadecimal ID and confirmed
repository absence: names, prefixes, multiple selectors and database errors cannot authorize it. A live exporter
reports busy. Copy removal does not clear uncertain source-VM recovery; that condition is reported separately.
Use the typed command, not manual deletion. An open reader can retain unlinked bytes, so successful cleanup does
not promise immediate physical reclamation.

The authoritative export, cleanup and VM-only journal-preparation paths are implemented. Earlier local Go CI and
the journal fix's focused Go/API/vet checks passed separately. The real-agent candidate built from clean source
`1a2506e6` and its normal installation inside the named QA runner passed exact binary-identity and preservation
checks; the outer controller was not replaced. One installed run completed import and restored its source to
Running before a safety guard interrupted clone boot. Clone-content, other source states and interruption/fault
journeys remain release-qualification gates; collection or this partial runtime evidence is not a full pass. See
[ADR-0018](adr/0018-isolated-vm-execution-and-snapshot-authority.md#vm-to-image-import-from-authoritative-backing).

Typed image optimization supports deblob and fstab repair, ext-family offline shrink, and durable Btrfs-to-ext4
conversion as a separate transaction before shrink. XFS supports inspection, provisioning, deblob, and fstab repair
at existing capacity; resize/shrink requests fail before mutation.

Interrupted optimization can resume from exact retained staging intent and resource identities. A checked root result
is required before publication, which requires atomic exchange and preserves the original until durable completion.
Unknown or changed remnants remain for recovery rather than being adopted or recursively removed. These paths are
implemented but unverified; unprivileged archive construction remains separate.

```
# List available remote image types
mvm image ls --remote

# Pull Ubuntu 24.04
mvm image pull ubuntu:24.04

# Import a local qcow2 image
mvm image import my-image /path/to/image.qcow2
mvm image import base-img my-vm
```

---

### `mvm bin`

Firecracker runtime management — install, list, select, and remove Firecracker/Jailer release pairs. Root authority
fetches or accepts the optional exact mirror archive, verifies it, and atomically publishes the pair in the trusted
release store. Source builds and manually supplied binary paths are unsupported.

The approved VM launch pair is exactly 1.17.0. Binary-management support for an older audited release does not
authorize VM launch with it. Release updates review and qualify the latest stable matching official pair before
changing the pin; the installed runtime does not automatically follow upstream `latest`. See
[runtime guidance](RUNTIME.md#vm-launch-and-resource-control).

#### Selectors

The binary resolver tries selectors in priority order:

1. **`type:version`** — e.g. `firecracker:1.15.0`
2. **ID prefix** — unique prefix of the binary SHA ID
3. **Type name** — matches the binary type (resolves to latest semver), e.g. `firecracker`

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm bin ls` | `-r, --remote`, `--limit`, `--json`, `--long` | List local (and optionally remote) Firecracker versions |
| `mvm bin pull` | `<version>`, `--default, -d`, `--force, -f` | Install one root-authoritative release |
| `mvm bin default` | `SELECTOR` | Set a binary as the active default |
| `mvm bin rm` | `<exact-version>...`, `-f, --force` | Remove exact releases; force hides local advisories only |

```
# List local and remote versions
mvm bin ls --remote

# Install and select the required VM launch pair
mvm bin pull 1.17.0 --default

# Remove the exact architecture-scoped release pair
mvm bin rm 1.15.0
```

---

### `mvm vm`

VM lifecycle management — create, start, stop, reboot, pause, resume, remove, list, and inspect VMs.

#### Selectors

The VM resolver tries selectors in priority order:

1. **Name** — exact VM name, e.g. `my-vm`, `test-runner`
2. **IP address** — input contains `.`, e.g. `10.88.0.5` (fails immediately on mismatch, no fallthrough)
3. **MAC address** — input contains `:`, e.g. `06:00:ac:10:88:05` (fails immediately on mismatch, no fallthrough)
4. **ID prefix** — unique prefix of the VM SHA ID

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm vm create` | `NAME [flags]` | Create and start a new Firecracker microVM |
| `mvm vm start` | `[SELECTORS]...` | Start one or more stopped VMs |
| `mvm vm stop` | `[SELECTORS]...`, `-f, --force` | Stop one or more running VMs |
| `mvm vm reboot` | `[SELECTORS]...`, `-f, --force` | Reboot one or more VMs |
| `mvm vm pause` | `[SELECTORS]...` | Pause one or more running VMs |
| `mvm vm resume` | `[SELECTORS]...` | Resume one or more paused VMs |
| `mvm vm rm` | `[SELECTORS]...`, `-f, --force` | Remove one or more VMs |
| `mvm vm ls` | `--json`, `--long` | List all VMs |
| `mvm vm ps` | `--json` | List running VMs (active processes) |
| `mvm vm inspect` | `SELECTOR`, `--json` | Show detailed information about a VM |

If creation was interrupted before the VM reached the caller database, `mvm vm rm --force <full-id>` can request
cleanup through the retained root authority. This recovery form accepts exactly one full 32-character lowercase
hexadecimal ID, not a name or ID prefix; existing registered selectors retain their usual behavior and name precedence.
Root authorization and ownership checks still apply: a missing or inadmissible root record is not permission to
delete files. On success, the result reports `caller_artifacts_retained: true`; caller-owned source files, the VM
directory and staging artifacts are preserved. This is not a filesystem orphan sweep.

VM launch and process control use typed root lifecycle transactions and schema-6 instance records separating operator
ownership from per-VM execution credentials. PID values in
output are display projections, never signaling authority. List observes its complete VM selection before filtering
status and propagates observation failures; inspect/get also propagate them. Paused display state requires an observed
live process. Stop preserves endpoint reservations and traffic grants for restart; remove invalidates them through
root before local deletion. Without `--force`, stop requests guest shutdown through an identity-bound Firecracker API
connection and waits on the exact process before authoritative cleanup.

Typed launch now includes console relay composition and raw data volumes with release-derived device limits,
descriptor-only mounts, and no new storage quotas. PCI launch and x86 nested configuration are implemented for the
pinned release; ARM nested launch is rejected. This development implementation is unverified. Typed live-volume
operations and snapshot capture/inspect/remove are integrated. Snapshot restore source composition now uses the
captured topology and independent protected disks; the named privileged route is integrated. Release qualification remains pending.

**`vm create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `NAME` | VM name (required, positional) | — |
| `--image IMAGE` | Image name, type:version (e.g. `ubuntu:24.04`), short ID, or path to .ext4 file | auto-detected |
| `--kernel KERNEL` | Kernel short ID or path to vmlinux file | auto-detected |
| `--vcpu N` | vCPU count | from config |
| `--mem, --memory SIZE` | Memory (e.g. `512M`, `1G`, `4096`) | from config |
| `--disk-size, -s SIZE` | Disk size (e.g. `512M`, `1G`) | from config |
| `--ip ADDRESS` | Guest IP | auto-assigned |
| `--mac ADDRESS` | Custom MAC address | auto-generated |
| `--network, --net NAME` | Named network | from config |
| `--no-network` | Explicitly create without a managed network, NIC, guest IP, or MAC; use vsock exec rather than SSH | `false` |
| `--volume, -v TEXT` | Attach volume(s) to the VM | none |
| `--ssh-key NAME_OR_PATH` | SSH public key (name from cache or file path) | default keys |
| `--user USER` | Default SSH user | from config |
| `--cloud-init-mode MODE` | Cloud-init mode: `inject`, `iso`, `net`, `off` | `off` (effective) |
| `--writeback` | Use writeback cache mode for drives (guest fsync honored) | `false` |
| `--nocloud-net-port N` | Reserved NoCloud TCP port; custom ports are rejected | `8000` |
| `--cloudinit-config PATH` | Path to custom cloud-init config file | — |
| `--no-pci` | Disable PCI device support | from config |
| `--nested-virt` / `--disable-nested-virt` | Enable/disable nested virtualization (requires PCI) | from config |
| `--cpu-template PATH` | Path to CPU template JSON file | — |
| `--enable-logging` / `--disable-logging` | Enable/disable Firecracker logging | from config |
| `--enable-metrics` / `--disable-metrics` | Enable/disable Firecracker metrics | from config |
| `--lsm-flags FLAGS` | Linux Security Module kernel cmdline flags | from config |
| `--boot-args ARGS` | Kernel boot arguments | from config |
| `--console` | Enable serial console relay | `false` |
| `--vsock-port N` | Vsock port for the guest agent | `1024` |
| `--writeback` | Use writeback cache mode for drives | from config |
| `--skip-cleanup` | Keep resources on failure for debugging | `false` |
| `--force, -f` | Skip confirmation prompts | `false` |
| `--count, -c N` | Create N VMs in batch | `1` |
| `--atomic` | All-or-nothing batch: roll back all VMs if any creation fails | `false` |
| `--skip-deblob` | Skip debloat operations on rootfs | `false` |

**Examples:**

```
# Create a single VM with default settings
mvm vm create my-vm

# Create with specific image, kernel, and resources
mvm vm create my-vm --image ubuntu:24.04 --kernel official:6.19.9 --vcpu 2 --mem 2G --disk-size 20G

# Create 3 VMs in batch
mvm vm create cluster-node --count 3

# Create a networkless VM for vsock-only access
mvm vm create offline-worker --no-network --cloud-init-mode off
```

`--no-network` is an explicit choice, not the result of omitting `--network`. It conflicts with `--network`,
`--ip`, `--mac`, and NoCloud network mode. Focused installed-candidate runner cases passed networkless full
capture/restore with an attached raw volume and guest exec, plus create/stop/start; the full system suite and
networked policy matrix remain unqualified.

---

### `mvm snapshot` (alias: `ss`)

The listed snapshot surface is implemented but not release-qualified. Capture/inspect/remove use root-controlled
storage and exclusive protected rootfs/data-volume capture. Restore copies disks into independent protected backing,
retains immutable memory backing, and preserves captured drive IDs and network identity. The public API reserves the
captured IP before root restore; conflicts fail, and an uncertain root outcome keeps the reservation for recovery.
Focused installed-candidate networkless and same-network managed restore journeys passed in the named runner; the
full snapshot fault matrix, interruption reconciliation, and release QA remain open (see ADR-0018).

Those earlier restore results do not qualify the 1.17.0 pair. Create fresh 1.17.0 snapshots and qualify capture and
restore with the same matching release. Captured versions are preserved; no cross-version conversion or compatibility
is promised. See [runtime guidance](RUNTIME.md#vm-launch-and-resource-control).

Create, list, inspect, restore, and remove VM snapshots.

#### Selectors

The snapshot resolver tries selectors in priority order:

1. **Name** — exact snapshot name, e.g. `daily-backup`
2. **ID prefix** — unique prefix of the snapshot SHA ID

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm snapshot create` | `VM_SELECTOR`, `--name`, `--pause` | Snapshot a running VM |
| `mvm snapshot ls` | `--json` | List all snapshots |
| `mvm snapshot inspect` | `SELECTOR`, `--json` | Show detailed information about a snapshot |
| `mvm snapshot restore` | `SELECTOR`, `NAME`, `--network`, `--resume`, `--count N` | Restore one or more VMs from a snapshot |
| `mvm snapshot rm` | `SELECTOR`, `--force, -f` | Remove a snapshot |

`snapshot create` writes the full **Snapshot request ID** to stderr before trusted capture, including with `--json`.
Keep that ID until the command completes. If the command is interrupted, `snapshot ls` may omit a root-published
capture; after the root operation settles, use `mvm snapshot inspect <full-id> --json` to check it and
`mvm snapshot rm <full-id> --force` to remove an unwanted artifact. Do not edit the root-controlled files directly.
`snapshot ls` does not discover unindexed root captures, so losing the ID still requires administrator investigation.

---

### `mvm console`

VM serial console access without SSH. Uses a PTY relay subprocess.
Accepts VM selectors (see [mvm vm](#mvm-vm) for resolution order).

```
mvm console VM_SELECTOR [flags]
```

| Flag | Description |
|------|-------------|
| `VM_SELECTOR` | VM name, ID, IP, or MAC address (positional, required) |
| `--state` | Show console relay state without attaching |
| `--kill` | Kill the console relay process |

Press `Ctrl+X` then `D` to detach from an active console session.

---

### `mvm network`

Named bridge network management.

#### Selectors

The network resolver tries selectors in priority order:

1. **Name** — exact network name, e.g. `sys-test-net`, `default`
2. **ID prefix** — unique prefix of the network SHA ID

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm network create` | `[NAME]`, `--subnet`, `--ipv4-gateway`, `--no-nat`, `--default, -d` | Create a named bridge network without prompting |
| `mvm network rm` | `[SELECTORS]...`, `-f, --force` | Remove one or more networks |
| `mvm network ls` | `--json, --long` | List all networks |
| `mvm network inspect` | `[SELECTOR]`, `--json` | Show network details, IP leases, and active firewall rules |
| `mvm network default` | `[SELECTOR]` | Set a network as the default for VM creation |
| `mvm network sync` | `[SELECTORS]...`, `--json` | Sync firewall rules between database and host |

NAT intent is enabled or disabled; `--nat-gateways` and caller-selected uplink names are removed. Root selects the
eligible host uplink. Typed root network creation applies its firewall generation before raising the bridge, and
removal uses exact ownership and reference checks with a durable receipt. Core/API network mutations consume these
transactions. A pending desired network can be retried only with matching inputs. If no root network record exists,
removal can acknowledge cancellation only after root proves the complete bridge/topology/firewall state absent; it
performs no speculative kernel deletion. Cancellation tombstones prevent reuse of that network ID. Old-boot removal
receipts also require fresh absence proofs. These paths have not been qualified. NoCloud now uses typed fixed-slot
host-service grants and no longer uses the legacy tracker.

---

### `mvm policy`

Policy management separates TCP/UDP traffic intent from directional VM-to-VM execution grants. The former flat
`mvm policy create/ls/inspect/rm/sync` commands are replaced by the typed families below.

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm policy traffic create` | Exactly one of `--from-network NETWORK` or `--from-vm VM`; `--to-vm VM`, `--protocol tcp\|udp`, `--port PORT\|START-END` | Apply a root-authorized traffic grant, then project it locally |
| `mvm policy traffic ls` | `--json` | List both source families with current resource names |
| `mvm policy traffic inspect` | `POLICY_ID`, exactly one of `--from-network` or `--from-vm`, `--json` | Inspect intent in the selected family |
| `mvm policy traffic rm` | `POLICY_IDS...`, exactly one of `--from-network` or `--from-vm`, `-f, --force` | Revoke the root grant or prove its absence before deleting the local projection |
| `mvm policy exec create` | `SOURCE_VM DESTINATION_VM TARGET_USER` | Persist one directional execution grant for an exact non-root destination user |
| `mvm policy exec ls` | `--json` | List directional execution policies with current VM names |
| `mvm policy exec rm` | `POLICY_IDS...`, `-f, --force` | Remove one or more execution policies by exact ID or unambiguous prefix |

Traffic creation applies the grant through root before updating SQLite; removal requires root revocation or exact
absence first. List and inspect show the local projection. Root success followed by a projection failure is reported
for repair/retry and does not undo the root outcome. Policies may bind exact active or cleaned root endpoints. Stopping
a VM preserves endpoint reservations and grants for restart; removing it invalidates them. A grant to a stopped
endpoint does not create a live network interface.

Execution grants are directional, independent of IP-network placement, and require an exact non-root target user.
The opt-in API relay now reads and enforces them at runtime. It also requires root-admitted active VM identities;
creating a grant alone cannot make a transitional VM launch eligible. End-to-end qualification remains pending.

---

### `mvm key`

SSH key management.

#### Selectors

The key resolver tries selectors in priority order:

1. **Name** — exact key name, e.g. `mykey`, `builder-key`
2. **Fingerprint prefix** — unique prefix of the SHA256 fingerprint (bare input and `SHA256:`-prefixed variants are both tried)
3. **`.pub` file path** — path to a `.pub` file on disk, e.g. `./id_ed25519.pub`

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm key ls` | `--json, --long` | List all SSH keys |
| `mvm key import` | `NAME`, `PATH`, `--default, -d`, `-f, --force` | Import an existing public key to the cache |
| `mvm key create` | `NAME`, `--algorithm, -a`, `--bits`, `--comment`, `--out`, `--default, -d`, `-f, --force` | Generate a new SSH keypair |
| `mvm key rm` | `[SELECTORS]...`, `-f, --force` | Remove one or more SSH keys |
| `mvm key inspect` | `[SELECTOR]`, `--json` | Inspect an SSH key |
| `mvm key default` | `[SELECTORS]...`, `--clear` | Set default SSH keys, or clear with `--clear` |
| `mvm key export` | `[SELECTOR]`, `[PATH]`, `-f, --force` | Export a keypair to a directory |

---

### `mvm config`

Configuration management for overridable settings.

| Command | Description |
|---------|-------------|
| `mvm config get CATEGORY [KEY]` | Get a configuration value |
| `mvm config set CATEGORY KEY VALUE` | Set a configuration value |
| `mvm config ls` | List all overridable settings and current values (alias: `list`) |
| `mvm config reset [CATEGORY] [KEY]` | Reset overrides to defaults. Use `--all, -a` for global reset; `--force, -f` to skip confirmation |

---

### `mvm cache`

Cache management.

| Command | Description |
|---------|-------------|
| `mvm cache init` | Initialize all cache resources |
| `mvm cache prune [RESOURCE]` | Prune cache resources: `vm`, `network`, `image`, `kernel`, `binary`, `misc` |
| `mvm cache clean` | Completely clean all cache: prune everything, host clean, remove cache dir |

**`cache prune` flags:**

| Flag | Description |
|------|-------------|
| `--all, -a` | Remove ALL items including running VMs, default network, protected assets |
| `--dry-run` | Show what would be removed without removing |
| `--force, -f` | Skip confirmation prompts |

Cache cleanup uses typed VM/network operations and no longer offers to rerun the ordinary CLI through sudo or
checks for a raw `ip` executable. It no longer performs arbitrary unmounts through the removed raw provisioner.
Inspection and cleanup failures propagate instead of being reported as empty cache or successful cleanup.
`mvm run provision` is removed.

Omit `RESOURCE` to get an error unless `--all` is passed. Each per-resource prune subcommand (`vm`, `network`, `image`, `kernel`, `binary`, `misc`) prompts for confirmation unless `--force` is set.

**`cache clean` flags:**

| Flag | Description |
|------|-------------|
| `--dry-run` | Show what would be removed without removing |
| `--force, -f` | Skip confirmation prompts |

---

### `mvm logs`

View VM logs.
Accepts VM selectors (see [mvm vm](#mvm-vm) for resolution order).

```
mvm logs VM_SELECTOR [flags]
```

| Flag | Description |
|------|-------------|
| `VM_SELECTOR` | VM name, ID, IP, or MAC address (positional, required) |
| `--os` | Show Firecracker OS log instead of boot log |
| `--lines, -n N` | Number of log lines to show |
| `--follow, -f` | Follow log output in real-time |

---

### `mvm exec`

Execute a command inside a VM via the vsock guest agent. If no command is provided, starts an interactive shell session.
Accepts VM selectors (see [mvm vm](#mvm-vm) for resolution order).

```
mvm exec [VM_SELECTOR] [-- <command>...] [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `VM_SELECTOR` | VM name, ID, MAC, or IP (positional, required) | — |
| (args after `--`) | Command to execute (omit for interactive shell) | — |
| `--user, -u TEXT` | User to run the command as | `root` |
| `--timeout, -t N` | Vsock agent connect/probe timeout in seconds | `0` (no timeout) |
| `--no-sync` | Skip final `sync()` after command | `false` |
| `--allow-vm-exec` | Permit this non-interactive source command to request policy-authorized VM-to-VM execution | `false` |
| `--port, -p N` | Vsock port for the guest agent | `1024` |

**Examples:**

```
# Interactive shell
mvm exec my-vm

# Run a command
mvm exec my-vm -- ls -la /etc

# With timeout
mvm exec my-vm --timeout 30 -- apt-get update

# As a different user
mvm exec my-vm --user ubuntu
```

An explicit source command can request execution on another VM with:

```bash
mvm policy exec create source-vm destination-vm app
mvm exec source-vm --allow-vm-exec -- mvm-agent remote --user app destination-vm -- command arg
```

The outer source command may run as root or a non-root account; the destination user must be non-root. The relay
requires the exact directional grant and root-admitted active source/destination identities, applies bounded wire and
capacity limits, and writes command-redacted audit records in the user cache. The opt-in outer command and its one
remote request share an absolute five-minute lifetime; `--timeout` still controls connection/probe time. Ordinary
exec, shell, and copy do not enable this source session. The source guest kernel must provide `SO_PEERPIDFD`; the
stock upstream `6.1.155` kernel used by the cached Firecracker CI selection lacks it. Select a capable source kernel
explicitly; see [kernel capability requirements](KERNEL.md#reference). This does not change the default kernel.

If an admitted atomic pidfd proves that an opted-in source command exited before publication, the same binding is
still reserved, unclaimed, and unused, and cleanup succeeds without cancellation, the unused source session closes
and ordinary stdout, stderr, and exit status are preserved. Metadata, reservation, cancellation, and cleanup errors
remain fail-closed. This completion publishes no remote-execution authority.
Qualification of the corrected source-publication and kernel-selection paths remains pending.

---

### `mvm ssh`

Open an SSH session into a VM, or execute a command.
Accepts VM selectors (see [mvm vm](#mvm-vm) for resolution order).

```
mvm ssh VM_SELECTOR [flags]
```

| Flag | Description |
|------|-------------|
| `VM_SELECTOR` | VM name, ID prefix, IP, or MAC address (positional, required) |
| `--user, -u USER` | SSH user (default: from user config) |
| `--key PATH` | SSH private key file or directory of keys |
| `--cmd, -c CMD` | Command to execute |
| `--timeout, -t SECONDS` | SSH connection timeout in seconds |

---

### `mvm cp`

Copy files between host and microVMs using vsock binary frame protocol (no SSH, no guest dependencies).

```
mvm cp [OPTIONS] SOURCE... DESTINATION
```

| Flag | Description | Default |
|------|-------------|---------|
| `SOURCE...` | Source path(s); use `vm_name:/path` for VM paths (at least 2 args) | — |
| `DESTINATION` | Destination path; last positional arg | — |
| `--force, -f` | Overwrite existing destination files | `false` |
| `--no-sync` | Skip final sync() after transfer (faster but risks data loss on VM stop) | `false` |

**Examples:**

```
# Copy local files to VM
mvm cp ./myfile.txt my-vm:/root/

# Copy file from VM to local
mvm cp my-vm:/var/log/syslog ./syslog

# Copy between VMs
mvm cp vm1:/data/file.txt vm2:/data/
```

**Notes:**
- Symlinks are followed: a symlink to a file or directory is copied as the target's content under the symlink's name.
- Broken symlinks and special files (sockets, FIFOs, devices) inside a copied directory are skipped with a warning.

---

### `mvm volume`

Persistent raw data disk management. Creation always uses `volumes/<volume-id>.raw`; `volume create --format` is
removed, including `--format raw`. Read-only, shareable, and writeback options are preserved. QCOW2 image import and
conversion remain supported through `mvm image`.

Existing non-raw volume records remain available for inspection and removal, but cannot be attached, resized, or used
by VM create, start, reboot, or restore. Every selected volume must resolve successfully. Persisted VM volume IDs must
resolve exactly; a matching name or ID prefix cannot replace a missing referenced volume.

#### Selectors

The volume resolver tries selectors in priority order:

1. **Name** — exact volume name, e.g. `data`, `asset-mirror`
2. **ID prefix** — unique prefix of the volume SHA ID

| Command | Flags | Description |
|---------|-------|-------------|
| `mvm volume create` | `NAME`, `SIZE` | Create a new persistent raw volume |
| `mvm volume rm` | `[SELECTORS]...`, `-f, --force` | Remove volumes; `--force` permits an attachment only when its VM is stopped |
| `mvm volume ls` | `--json, --long` | List all volumes |
| `mvm volume inspect` | `SELECTOR`, `--json` | Show detailed information about a volume |
| `mvm volume resize` | `SELECTOR`, `SIZE` | Grow a volume; an attached VM must be stopped, but detaching is unnecessary |
| `mvm volume attach` | `VM_SELECTOR`, `VOLUME_SELECTOR` | Attach a volume to a VM |
| `mvm volume detach` | `VM_SELECTOR`, `VOLUME_SELECTOR` | Detach a volume from a VM |

**`volume create` flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `NAME` | Volume name (required) | — |
| `SIZE` | Volume size, e.g. `10G`, `512M` (required) | — |
| `--read-only`, `--readonly`, `--ro` | Mount volume as read-only | writable |
| `--shareable, -s` | Allow volume to be attached to multiple VMs | `false` |
| `--writeback` | Use writeback cache mode | `false` |

---

### `mvm self-update`

Check for and apply binary updates from GitHub Releases.

| Command | Description |
|---------|-------------|
| `mvm self-update` | Check + apply if newer for a user-owned artifact; the system installation requires administrator bootstrap |
| `mvm self-update check` | Check only, print available version |
| `mvm self-update apply` | Apply to a user-owned artifact. `--force` re-installs the same version |

For a user-owned artifact, downloads the latest release matching the host architecture, verifies its SHA256 checksum,
and atomically swaps that artifact. Apply refuses canonical `/usr/local/bin/mvm`; install a newly downloaded trusted
artifact with `sudo <new-mvm-binary> host install-system`. `check` remains available from the system installation.

---

### `mvm env`

Declarative environment workflow management. Define and provision full VM environments from a YAML spec.

| Command | Description |
|---------|-------------|
| `mvm env apply [spec-path]` | Apply an environment spec (alias: `up`). Defaults to `mvmctl.yaml` then `mvmctl.yml`. Supports `--env KEY=VAL` for exec step overrides. |
| `mvm env ls` | List applied environments (alias: `list`) |
| `mvm env diff [spec-path]` | Show what would change (spec vs state) |
| `mvm env destroy [wf-id\|path]` | Tear down exactly what was provisioned (alias: `down`) |

See [ENV_SPEC_REFERENCE.md](ENV_SPEC_REFERENCE.md) for the full YAML spec reference.

---

## Configuration

### Configuration Priority (highest overrides lowest)

1. CLI flags (highest priority)
2. `MVM_*` environment variables (e.g. `MVM_LOG_LEVEL`, `MVM_CACHE_DIR`)
3. SQLite database (`~/.cache/mvmctl/mvmdb.db`) — canonical store for user overrides
4. Built-in fallbacks (`internal/infra/constants.go`)

### Kernel Defaults

Built-in fallbacks for kernel operations (defined in `internal/infra/constants.go` under `defaults.kernel`):

| Key | Default | Description |
|-----|---------|-------------|
| `version` | `6.19.9` | Default version for `--type official` |
| `build_jobs` | `nil` | Parallel build jobs (`nil` = all available cores) |
| `remote_list_limit` | `5` | Max remote versions to list per type |
| `remote_list_cache_ttl` | `14400` | Cache TTL in seconds (4 hours) for remote version listings |

### Image Defaults

| Key | Default | Description |
|-----|---------|-------------|
| `import_format` | `auto` | Default import format when no `--format` specified |
| `remote_list_limit` | `5` | Max remote versions to list per type |
| `remote_list_cache_ttl` | `3600` | Cache TTL in seconds (1 hour) for remote version listings |

### Network Defaults

| Key | Default | Description |
|-----|---------|-------------|
| `name` | `net` | Default bridge name |
| `subnet` | `172.27.0.0/24` | Default subnet CIDR |
| `nat_enabled` | `true` | Default NAT/masquerade setting |

---

## Cloud-Init

The effective default is `--cloud-init-mode off` (no cloud-init). When SSH keys are provided without an explicit mode, the system enables direct key injection regardless of mode.

### Modes

| Mode | Flag | Description |
|------|------|-------------|
| **inject** | `--cloud-init-mode inject` | Direct injection into rootfs via the active provisioner backend (loop-mount or GuestFS) |
| **net** | `--cloud-init-mode net` | Serves cloud-init files via HTTP (nocloud-net) |
| **iso** | `--cloud-init-mode iso` | Generates a cloud-init ISO via `cloud-localds`, or uses a provided custom ISO path |
| **off** | `--cloud-init-mode off` | Skips cloud-init entirely (default) |

The typed LoopMount VM path preserves missing-account creation, selected SSH keys, cloud-init injection, and automatic
deblob. A new non-root account uses a free UID/private GID in 1000–60000, a locked password, `/home/<name>`, and `/bin/sh`;
existing accounts are retained. Injected seeds contain fixed `meta-data` and `user-data` leaves plus optional
`network-config`. Requests cannot select arbitrary host paths or guest commands.

Some distro preparation is installed as fixed guest-boot work, including SSH setup, Arch package-key initialization,
and Btrfs balance. Successful host provisioning means those actions were queued, not that the guest has run them.
The implementation is unverified. Typed image conversion/recovery is implemented; unprivileged archive construction
remains separate.

### How Net Mode Works

The net mode delivers cloud-init configuration via HTTP. Steps below apply to the net mode only.

1. **HTTP Server**: A NoCloud batch binds the managed network gateway on fixed TCP port `8000`; custom ports are rejected.
2. **Firewall Grant**: Root derives the owned gateway service slot from the network identity. Binding must succeed before access is granted.
3. **Kernel Command Line**: The VM boots with `ds=nocloud;seedfrom=http://GATEWAY_IP:8000/`
4. **Configuration Delivery**: cloud-init fetches `meta-data`, `user-data`, and `network-config` via HTTP
5. **Cleanup**: Normal shutdown revokes the grant while retaining the listener socket, then releases the port.

### Net Mode Security Architecture

The security model applies to the net cloud-init mode only:

- One live NoCloud batch occupies each network gateway listener. A bind collision fails; it neither adopts nor kills the existing listener.
- The typed privileged request selects only the owned network, never a custom address, port, protocol, or service.
- The firewall grant represents the fixed gateway TCP `8000` slot; it does not authenticate the current listener process.
- An interrupted process can leave that reserved-slot grant for a subsequent invocation or network teardown. Live behavior remains unqualified.

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MVM_CACHE_DIR` | Override the managed cache directory | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override config directory | `~/.config/mvmctl` |
| `MVM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARN`, `ERROR`) | `WARN` |
| `MVM_ASSET_MIRROR` | Local artifact mirror; a new Firecracker/Jailer install still fetches the official checksum sidecar for root verification | (not set) |
| `MVM_WARM_POOL` | Warm image pool backend (`disk` for cache-backed; otherwise under `/tmp/mvmctl-<uid>/ready`, on tmpfs only when `/tmp` is tmpfs) | (not set) |
| `MVM_ESCALATED` | Set by sudo wrapper to indicate privilege escalation | `1` |
| `MVM_TEMP_DIR` | Override the per-operator temporary work directory, warm-pool location, and official-kernel scratch (`<value>/kernel-build`) | Managed cache `tmp/` for ordinary temporary work; official-kernel build uses embedded `build_dir` when unset or empty |
| `MVM_SUDO_RESTART` | Set internally when re-running with sudo for host init | (not set) |

Generic image/kernel downloads look up mirror entries by URL filename, not full origin. Configured checksums are
verified, but Firecracker CI kernel downloads have no upstream SHA-256 configured. Different CI releases can publish
different bytes under the same filename (for example, `vmlinux-6.1.155`); those entries are not interchangeable.
Use separate mirror directories for colliding origins and keep the matching artifact in each. A mirror hit without
an expected checksum does not prove origin or CI-release identity. This does not relax the official checksum
verification required for root-owned Firecracker/Jailer installation.

---

## Cache Directory Structure

```
~/.cache/mvmctl/
├── bin/                  # Source-build and non-launch binary workspace
├── kernels/              # vmlinux kernel images
├── images/               # Root filesystem images (.ext4, .btrfs, .img, .raw, .ext4.zst, .btrfs.zst)
├── volumes/              # Persistent disk volume files
├── vms/                  # Per-VM state
│   └── <vm-sha>/         # VM directories named by SHA256 hash
│       ├── rootfs.img
│       ├── cloud-init.iso            # ISO mode only
│       ├── firecracker.json
│       ├── firecracker.log
│       ├── firecracker.console.log
│       ├── firecracker.metrics       # Metrics enabled only
│       └── cloud-init/               # Generated seed inputs
├── workflows/            # Workflow state persistence
├── nocloudnet/           # nocloud-net batch server dirs and logs
├── .mvm-protected/       # Root-controlled store; not caller-editable
│   └── snapshots/<snapshot-id>/
│       ├── capture.json              # Root-authenticated artifact record
│       ├── capture-progress.json     # Interrupted-capture outcome
│       ├── rootfs.img
│       ├── kernel
│       ├── memory                    # Full snapshots only
│       ├── vmstate                   # Full snapshots only
│       └── volume-<drive-id>.raw     # Each captured data volume, when present
├── logs/                 # Application log files
├── firecracker-src/      # Firecracker git clone (for building from source)
├── mvmdb.db              # SQLite database (canonical asset state)
├── mvmctl.log            # Main application log
├── timing.log            # Performance/timing log
├── audit.log             # Rotating operation log (10MB, 3 backups)
└── ...
```

The VM and snapshot leaf names above are compiled and are not configurable. `MVM_CACHE_DIR` still selects the managed
cache root. Caller-facing console outputs and display-only PID mirrors use
`/run/mvmctl/runtime/<uid>/<vm-id>`; the Firecracker API and vsock sockets use the isolated execution identity's
private `/run/mvmctl/vmm-runtime/<uid>/<vm-id>` directory, mounted at `/vmm-runtime` inside the jail. The API is
available through the authenticated root broker, not a public `api_socket_path` field. The root transaction pins
individual persistent resources; it does not mount the whole caller-owned VM directory.

The v0.3 privileged path accepts a managed cache root on ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or ZFS.
FUSE, remote, overlay/stacked, and automount cache roots are unsupported. The descriptor-pinned cache foundation is
implemented. Its private base-resource preparation also replaces admitted caller configuration bytes with a root-built
sealed memfd consumed by typed privileged launch and descriptor-only mounting. These paths have not been qualified.

Canonical launch assets and jail state are outside the user cache:

```text
/var/lib/mvmctl/
├── binaries/<version>/   # Root-owned exact Firecracker/Jailer release pair
└── jailer/firecracker/<vm-id>/root/  # Per-VM jail and bind mounts
```
