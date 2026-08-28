# Root-Owned Single-Binary Privileged Dispatch

**Status:** Accepted — implementation in progress
**Date:** 2026-08-20
**See also:** [ADR-0002: Single Go Binary Architecture](0002-single-go-binary-architecture.md),
[ADR-0005: Sudo Privilege Architecture](0005-sudo-privilege-architecture.md),
[ADR-0015: Jailer Is the Canonical Firecracker Launcher](0015-jailer-canonical-launcher.md)

## Context

mvmctl is a Go CLI whose user-facing commands and subprocess services are compiled into one release artifact. The CLI
is intended to run as an unprivileged user while a small set of VM, filesystem, network, firewall, cgroup, and Jailer
operations require root.

The existing installation contract puts `mvm` at user-owned `~/.local/bin/mvm` and generates a passwordless sudoers
entry for that path with wildcard arguments. A member of the `mvm` group can replace the executable and then ask sudo to
run the replacement as root. Root ownership checks performed later by Jailer cannot repair a compromised executable
boundary.

The existing sudoers policy also grants raw argument access to host tools such as `ip`, `iptables`, `nft`, `modprobe`,
and `sysctl`. Those tools expose substantially more host authority than the named mvmctl operations that need them. The
policy therefore does not provide the targeted access claimed by ADR-0005.

Introducing a separately compiled privileged helper would provide a small executable trust boundary, but it would break
the project's single-binary product contract. A safe design must retain one artifact without allowing the ordinary CLI
initialization path or caller-controlled program bytes to execute as root.

## Decision

### One root-owned system installation

Manually built and release-script artifacts are installed at `/usr/local/bin/mvm` as `root:root`, mode `0755`. The
directory chain must be root-owned and not group/world writable. `/usr/bin/mvm` is reserved for a future installation
owned by a distribution package manager.

Normal users execute `/usr/local/bin/mvm` without sudo. The file contains both the public CLI and the internal privileged
dispatcher, preserving one release artifact and one installed executable. Replacing or upgrading the system executable
is an explicit administrator action; a normal user update cannot overwrite it.

Development builds may exist elsewhere, but they are never sudo targets. A development build that needs privileged
operations calls the compatible system installation and fails closed on a privileged-protocol mismatch.

### Administrator bootstrap and system-binary replacement

System installation is selected by the exact `host install-system` command before `app.Initialize()`, Cobra, signal
setup, user configuration, the user database, cache resolution, logging configuration, or service construction. The
bootstrap route clears the inherited environment, installs only fixed root-safe values, and then atomically copies the
running image to `/usr/local/bin/mvm`. Flags and extra arguments are rejected inside the same reserved route and cannot
fall through to the ordinary CLI.

The installer cannot prove whether an already-root process reached it through password-authenticated administrator sudo
or an unintended passwordless rule. It therefore inspects the project-managed sudoers drop-in before touching the
executable and refuses unrecognized active syntax. On a clean host, an administrator runs
`sudo <trusted-mvm-binary> host install-system` and then runs `sudo /usr/local/bin/mvm host init`. Installation is not an
implicit side effect of `host init`.

v0.3.0 does not use this installer to convert, adopt, or preserve a pre-v0.3 installation. Operators must remove the old
managed sudoers rule and clean the old runtime/state layout in an authenticated administrator session before installing
v0.3.0. Later replacement of an already-v0.3-compatible system binary remains an explicit administrator action and
must fail closed when the privileged protocol is incompatible.

Replacing the executable is descriptor-relative, no-follow, temporary-file plus file fsync plus rename plus directory
fsync. Failures after rename report that replacement occurred and whether directory durability is uncertain. A normal
`self-update` may update a user-owned artifact, but it refuses to overwrite the system installation and prints the
administrator bootstrap instruction.

### Early privileged dispatch

`cmd/mvm/main.go` recognizes one reserved, versioned privileged marker before signal setup, `app.Initialize()`, Cobra,
user configuration, the user database, logging configuration, or plugin/service discovery. Once selected, this path can
only enter the privileged dispatcher; it cannot return to or invoke an ordinary CLI command.

The dispatcher:

- requires effective UID 0 and derives the invoking identity from sudo's trusted execution context;
- verifies the system executable and its directory chain are root-owned and not group/world writable;
- verifies that the invoking user is currently authorized by the `mvm` group;
- sanitizes its environment and uses absolute paths for root-side subprocesses;
- accepts only a versioned, bounded, typed request for a named operation;
- rejects unknown actions, extra arguments or fields, invalid identifiers, unsupported versions, and unchecked paths;
- never accepts raw commands, arbitrary argv, shell fragments, raw firewall syntax, or raw cgroup key/value input.

The sudoers entry may contain a wildcard after the reserved marker because operation arguments vary, but that wildcard
can reach only the early dispatcher. It cannot select the public CLI. The dispatcher remains the authoritative
allowlist, and every handler performs exact argument-count and type checks before side effects.

Synchronous actions return one bounded, versioned response envelope on their control channel. Success payloads are
action-specific. Failures preserve the original `DomainError` code, class, entity, and bounded partial-state details so
the normal-user client can make safe retry and recovery decisions. Logs and subprocess exit status are diagnostic only;
they never replace the structured result or turn a malformed/missing response into a generic successful operation.

The control channel is one full-duplex Unix stream socket duplicated onto file descriptor 0 across `sudo -n`. File
descriptor 0 is control-only and is marked close-on-exec before any handler can launch a descendant. Standard output and
standard error are never parsed as authority. VM console input/output must use a separate typed relay owned by the launch
implementation; it cannot reuse the control channel. Using descriptor 0 avoids sudo policy for inherited descriptors,
while the socketpair permits the client to upload a bounded release archive and receive an early rejection concurrently.
Root requires descriptor 0 to be an `AF_UNIX` `SOCK_STREAM` socket and requires its Linux `SO_PEERCRED` UID/GID to match
the authenticated sudo caller. The peer PID is positive and audit-only; it is not an authorization identity.

The transport always executes exactly
`/usr/bin/sudo -n -- /usr/local/bin/mvm __mvm_privileged_v1 <fixed-action>`. It never uses `PATH`, `os.Executable`, the
current user binary, a caller-supplied executable/argv/environment/cwd/timeout, or a root-EUID bypass; the receiver
requires authenticated non-root sudo identity. `internal/lib/system` provides one concrete transport rather than
extending the generic command runner. Each capability package owns a minimal private `Exchange` interface and named
typed public methods; no public generic privileged call/action/decoder hook is introduced. Hermetic tests inject private
process dependencies and use a dedicated fake exchanger without extending `CommandRunner`, `RunCmdOpts`, `SpawnConfig`,
or the existing `FakeRunner`.

Each request starts with fixed `MVMREQ01` magic, a network-order 32-bit JSON-header length, and a network-order 64-bit
payload length. The strict header is at most 64 KiB and contains schema version 1, the action repeated from argv, and the
action-specific body. The repeated action must match. Only release install may carry a payload: either zero bytes or an
exact stream of at most 128 MiB. The caller half-closes its write side after the declared bytes, and root requires EOF so
truncation or trailing bytes fail closed before any payload-dependent effect. Zero-payload operations likewise require
EOF before effects. Authentication, framing, and header failures may return an early error without draining an advertised
payload; the concurrent client treats the resulting uploader `EPIPE` as expected once it has a valid response. Root
applies bounded header and archive deadlines and reads no second request.

Each response starts with fixed `MVMRES01` magic and a network-order 32-bit JSON length followed by at most 64 KiB of
strict schema-version-1 JSON. It contains the matching action, a closed `success` or `error` status, and exactly one typed
result or error. Error envelopes carry code, stable string class, message, operation, entity, and normalized details;
they never serialize wrapped causes, stacks, stderr, or arbitrary Go values. Wire details permit only bounded booleans,
strings, signed integers, and string arrays. Unsupported details are omitted visibly. Root half-closes the control
socket's write side after the complete frame, so the client can validate response EOF independently of process exit. An
error response delivered after all effects and cleanup exits successfully at the process layer. The codec returns a
decoded success/error outcome separately from protocol failure, so a valid root `DomainError` cannot be mistaken for a
malformed frame. A start failure has known no-effect status; any started process with a missing, malformed, truncated,
mismatched, oversized, or non-EOF response returns `CodeProcessError` with `process_started: true` and
`outcome_unknown: true`.

The transport returns bounded raw response bytes only together with an explicit indication that actual socket EOF was
observed; copying a complete-looking prefix into an in-memory reader cannot fabricate authority. The named capability
client decodes an EOF-qualified response with a non-cancelled context and lets either a valid success or valid remote
`DomainError` win over upload `EPIPE`, non-zero exit, or later reap diagnostics. Without one valid envelope, a pre-start
failure reports `process_started: false, outcome_unknown: false`; every post-start failure reports both values as true.

### Authorization roles

The root-owned executable establishes code integrity. The `mvm` Unix group remains the authorization role for
passwordless privileged VM operations. Ordinary non-privileged commands do not require group membership.

Membership in `mvm` authorizes management of mvmctl-owned VM infrastructure; it does not authorize arbitrary host root
commands. The sudoers drop-in grants only the reserved privileged entry point of the root-owned system executable. It
does not grant the public CLI or raw `ip`, `iptables`, `iptables-restore`, `iptables-save`, `nft`, `modprobe`, or `sysctl`
commands.

`host init`, system-binary installation/replacement, group membership changes, sudoers changes, and `host reset` modify the
authorization boundary itself. They remain explicit administrator operations under the administrator's normal sudo
policy and are not exposed through the `mvm` group's passwordless dispatcher.

The binary is never setuid and is not assigned broad Linux file capabilities.

### Typed privileged operation catalog

The initial dispatcher contains only the following named capabilities. Public callers retain domain-specific typed
methods; there is no generic privileged-operation interface.

| Capability | Allowed effect | Required binding |
|---|---|---|
| Trusted release install/remove | Verify and atomically manage an exact Firecracker/Jailer release pair | Fixed origin, version, architecture, root release manifest, active references |
| VM launch/abort/cleanup/reconcile | Create and recover one Jailer process, chroot, cgroup, and private mount namespace | Invoking UID, VM ID, exact release, root instance record, per-VM lock |
| Snapshot expose/remove | Mount one validated snapshot directory in the registered VM namespace | Invoking UID, VM ID, snapshot ID, pinned source/target descriptors |
| Volume expose/remove | Mount one validated volume at one typed drive ID in the registered VM namespace | Invoking UID, VM ID, volume/drive ID, pinned source/target descriptors |
| Image provision | Execute the existing typed provision operations inside one pinned managed image/rootfs | Invoking UID, managed resource identity, pinned root descriptor, bounded operation list |
| Bridge/TAP lifecycle | Create, configure, or remove an mvmctl-owned bridge or TAP | Invoking UID, network/VM identity, validated interface/address model |
| Neighbor flush | Flush neighbors only for an mvmctl-owned bridge | Invoking UID and registered network identity |
| Firewall inspect/reconcile/teardown | Render and atomically apply the complete typed mvmctl ruleset | Invoking UID, typed firewall models, fixed mvmctl tables/chains/comments |

Cgroup preparation and removal are part of VM launch/abort/cleanup rather than a public generic cgroup operation. Module
loading and sysctl mutation are administrator-owned host initialization steps, not operator dispatcher capabilities.

Adding a new privileged capability requires a new typed method, abuse cases, ADR-0016 review, and sudo threat-model
review. “Extensible for future use” is not a reason to add a generic action.

### Module placement and dependency direction

The privileged boundary follows the repository's existing service-subprocess architecture and does not create a second
application architecture:

| Package/file | Responsibility | Must not do |
|---|---|---|
| `cmd/mvm/main.go` | Detect the reserved marker and enter privileged dispatch before normal initialization | Parse domain requests, perform side effects, initialize the public CLI |
| `internal/service/privileged/` | Versioned envelope, bounded parsing, invoking identity, executable verification, and fixed action dispatch | Import `pkg/api`, `internal/cli`, or any `internal/core/*`; expose generic command execution or raw effect paths |
| `internal/service/jailer/` | Typed release, launch, namespace mount, cgroup, process, and cleanup handlers plus normal-user client calls | Orchestrate other core domains or trust user paths after validation |
| `internal/service/loopmount/` | Existing typed provisioning protocol and confined rootfs operations | Accept arbitrary host roots or unrelated unmount/PID targets |
| `internal/service/network/` | Typed root-side bridge, TAP, address, route, and neighbor mutations | Accept raw `ip` argv or own network business orchestration |
| `internal/service/firewall/` | Typed root-side firewall inspection and atomic managed-ruleset application | Accept raw nftables/iptables text from the caller |
| `internal/lib/model/` | Shared typed request/value models used across boundaries | Import services, core domains, API, or CLI |
| `internal/lib/system/` | Subprocess transport and low-level process helpers | Become a generic privileged-operation API or render domain commands |
| `internal/core/{domain}/` | Existing domain logic and typed backend interfaces | Import another core domain or perform cross-domain orchestration |
| `pkg/api/` | Sole cross-core orchestration and public input validation | Implement root-side handlers or bypass typed service methods |

The central dispatcher performs only fixed routing and security-boundary checks. Actual effects remain in
capability-specific service packages and files. Each public client method keeps a domain name and shape, such as
`jailer.Launch`, `jailer.Cleanup`, `network.EnsureTap`, and `firewall.Reconcile`; there is no public
`ApplyOperations([]Operation)` or `RunCommand([]string)` method.

Where an existing leaf package needs privileged effects, it defines the smallest typed interface in the owning layer and
receives a concrete service client through construction. Leaf packages do not gain imports of `pkg/api`, `internal/cli`,
or core domains. The one existing loopmount service import exception is not generalized without a separate architecture
review.

Normal API `Input.Validate`/`Resolve` remains the caller-facing validation layer. Because the privileged dispatcher
receives data from an untrusted process, it independently enforces authorization, ownership, bounds, path confinement,
and system-damage invariants. These are receiver trust-boundary checks, not duplicated business validation.

### Release trust

The caller supplies a validated release version and architecture and may stream an exact-length bounded archive body for
cache efficiency. Privileged release code constructs the permitted Firecracker release and checksum URLs and obtains the
checksum independently from the fixed official origin with a dedicated bounded HTTPS-only client and proxies disabled.
It hashes the complete archive body against that checksum before parsing it. If no body is supplied, root may fetch the
fixed archive itself. Root never opens a caller path or accepts a caller URL or checksum. The normal-user client may read
`MVM_ASSET_MIRROR`, but those streamed bytes are transport, never release authority.

Extraction occurs in a root-owned temporary directory with bounded compressed/decompressed input, a reviewed allowlist
for the complete upstream archive layout, and rejection of path traversal, symlinks, hardlinks, devices, sparse files,
duplicates, unexpected members, and size/count overflow. Only the exact Firecracker/Jailer pair is extracted. Their ELF
class and machine must match the selected architecture without executing downloaded code. The pair and strict root-owned
release manifest are fsynced and atomically renamed into the trusted store. A referenced release cannot be
force-replaced.

ELF admission reads exactly the first 64 bytes of each extracted binary and performs no executable handoff. It requires
the ELF magic, ELF64 class, little-endian encoding, current ident/object versions, System V ABI version 0 with zero ident
padding, `ET_DYN`, a non-zero entry point, a 64-byte ELF header, program headers beginning at byte 64 with 56-byte
entries, and 1 through 64 program headers. Admission also receives the exact full-file size, enforces the 120-byte
through 64 MiB executable policy, and requires the complete declared program-header table to fit within that size. The
machine is exactly `EM_X86_64` (62) for `x86_64` or `EM_AARCH64` (183) for `aarch64`. A future upstream format change
fails closed until this reviewed policy and its tests are updated.

If upstream provides a stable verifiable signature or provenance mechanism, mvmctl should adopt it. Until then, the
fixed HTTPS origin and independently fetched checksum are an explicit supply-chain limitation, not a signature claim.

The receiver derives the source identity from the canonical release slot; none of these values are request fields. The
naming follows Firecracker's
[official binary-installation contract](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md#getting-a-firecracker-binary).
For version `<version>` without a leading `v` and architecture `<architecture>` in `x86_64|aarch64`, the fixed
derivation is:

| Value | Receiver-derived form |
|---|---|
| Official origin | `https://github.com/firecracker-microvm/firecracker/releases/download` |
| Release tag | `v<version>` |
| Archive | `firecracker-v<version>-<architecture>.tgz` |
| Checksum sidecar | `firecracker-v<version>-<architecture>.tgz.sha256.txt` |
| Archive root | `release-v<version>-<architecture>` |
| Firecracker member | `release-v<version>-<architecture>/firecracker-v<version>-<architecture>` |
| Jailer member | `release-v<version>-<architecture>/jailer-v<version>-<architecture>` |
| Trusted-store leaves | `firecracker`, `jailer`, and `manifest.json` |

This table freezes source construction and the two extracted member identities. It does not claim that those are the
only members in the upstream archive: Task 6 separately records and enforces the complete reviewed upstream allowlist
before extraction is accepted. A packaging change must update the reviewed contract and tests; it must not fall back to
basename matching, caller-selected member names, or permissive extraction.

Checksum authority uses a dedicated client rather than the ordinary downloader. It performs one retrieval attempt of
the derived sidecar URL with a 15-second total deadline, 5-second dial/TLS/header timeouts, a 16 KiB response-header
limit, transport compression disabled, TLS 1.2 or newer, no proxy function, no response cache, and at most one redirect.
That redirect must remain HTTPS, contain no user information or fragment, and target exactly
`release-assets.githubusercontent.com`.
The response must be HTTP 200 and no more than 256 bytes whether or not it declares a content length. The accepted body
is exactly `<64 lowercase hexadecimal characters><two ASCII spaces><derived archive name><LF>`. Missing or additional
lines, CRLF, alternate filenames, uppercase digests, GNU binary markers, and other whitespace are rejected.

The fixed `manifest.json` leaf uses schema version 1 and is at most 4 KiB. Its closed JSON schema contains exactly the
canonical release slot, the archive SHA-256 digest, and separate Firecracker and Jailer objects containing a SHA-256
digest and `size_bytes`. All digests are exactly 64 lowercase hexadecimal characters. Each executable size is between
120 bytes (one complete ELF64 header and one program header) and 64 MiB. The current mirrored x86_64 releases use no
more than 3,527,456 bytes for either selected executable, so the upper bound leaves substantial growth room while
preventing a corrupt root manifest from authorizing an unbounded verification pass. An upstream executable exceeding
the bound fails closed until the policy is reviewed; the bound is architecture-neutral and does not substitute for the
still-required aarch64 archive-member audit.

```json
{
  "schema_version": 1,
  "release": {"version": "1.16.1", "architecture": "x86_64"},
  "archive_sha256": "<lowercase-sha256>",
  "firecracker": {"sha256": "<lowercase-sha256>", "size_bytes": 3527456},
  "jailer": {"sha256": "<lowercase-sha256>", "size_bytes": 2181264}
}
```

Decoding rejects unknown, missing, duplicate, case-conflicting, trailing, wrongly typed, oversized, or out-of-policy
values. The executable hashes in a successfully decoded manifest are the sole source of the release identity supplied
to instance registration; the unprivileged request never supplies them.

Trusted-store traversal starts at a pinned `/` descriptor and opens every fixed component with `O_NOFOLLOW` relative to
its retained parent. `/`, `/var`, and `/var/lib` must be root-owned directories without group or world write access.
Managed `/var/lib/mvmctl`, `binaries`, architecture, and version directories are exactly `root:root` mode `0700`.
`firecracker` and `jailer` are one-link root-owned regular files of mode `0755`; `manifest.json` is a one-link
root-owned regular file of mode `0600`. A missing architecture or version directory is an ordinary binary-not-found
result. Once the version directory exists, a missing or unsafe fixed leaf is an incomplete/corrupt trusted release and
fails as `binary.untrusted`, never as an absent release. Reads retain the version-directory descriptor, open each leaf
once, compare pre/post-read descriptor identity and metadata, and do not reconstruct a pathname.

Executable admission opens the fixed `firecracker` and `jailer` leaves with `O_NOFOLLOW|O_NONBLOCK` relative to that
retained version-directory descriptor. The nonblocking open prevents an unsafe special-file leaf from stalling root
before type admission. Each descriptor must identify a one-link `root:root` regular file of exact mode `0755`, with a
size equal to its manifest entry and within the hard executable bound. Verification uses bounded positioned reads from
offset zero with a fixed 32 KiB buffer: it hashes exactly the declared bytes, captures the first 64 bytes for the closed
ELF policy, and probes the next byte so truncation and growth fail closed. Positioned reads leave the descriptor offset
unchanged. A second descriptor stat must match device, inode, type and mode, link count, owner and group, size,
modification time, and change time from the first stat. The service retains those same verified descriptors for the
later launch handoff; it never validates one pathname and executes a reopened pathname. Failure to admit `jailer`
closes an already admitted `firecracker`, and cleanup preserves the primary domain error while reporting any close
failure.

Implementation note (2026-08-29): the private Jailer service derives the exact source identity from a validated
`(version, architecture)` release slot and rejects non-canonical slots before constructing any source value. Its
dedicated checksum authority independently fetches the derived sidecar with the closed transport policy above and
returns a typed archive digest only after exact grammar validation. Forged private source values fail before a request,
and response cleanup preserves the primary `DomainError` metadata. The private ELF admission parser validates the
bounded header shape, selected architecture, exact bounded file size, and complete program-header-table extent without
loading or executing candidate bytes. The private manifest codec enforces the closed schema, record and executable
bounds, typed digests, and manifest-derived instance release identity.
The private read-side store foundation pins and validates the fixed root/architecture/version directory chain, then
reads `manifest.json` once through the retained slot descriptor with stable metadata and exact slot verification. It
then opens the two fixed executable leaves nonblocking, verifies their exact metadata, manifest sizes and full hashes,
closed ELF policy, read bounds, and pre/post-read stability, and retains the same descriptors at offset zero. These
foundations are not yet wired to the privileged request or legacy installer. Bounded archive transport and extraction,
the complete member allowlist, manifest writing, and atomic trusted-store installation remain Task 6 work.

### Paths, process identity, and runtime state

Privileged code treats the normal CLI, arguments, environment, database, cache, manifests, and downloaded content as
untrusted. Managed filesystem objects are opened once through descriptor-relative no-symlink resolution and retained as
pinned descriptors through each operation. Validation followed by pathname reopen is prohibited.

`MVM_CACHE_DIR` remains the user-selected location for persistent user state and large VM artifacts. The privileged
protocol may carry exactly one typed managed-cache locator; it is a namespace locator, not authority for an individual
effect. The receiver requires one canonical absolute path, rejects dot components, NULs, symlinks, magic links, unsafe
owners/modes, and unsupported mount topology, and pins the directory before resolving any resource. All subsequent
inputs are typed IDs, closed format enums, and presence/access intent. A request never supplies a kernel, rootfs,
snapshot, volume, config, PID, socket, log, mount-target, or temporary-file path.

The resource-bearing cache root must use a kernel-backed local filesystem from this closed set: ext2/ext3/ext4, XFS,
Btrfs, F2FS, bcachefs, tmpfs, or ZFS. FUSE, remote filesystems, overlay or other stacked filesystems, and automount cache
roots are rejected. The filesystem allowlist applies to the final pinned cache root, not `/` or unrelated ancestor
mounts. Every ancestor is still opened descriptor-relatively and checked for safe ownership, mode, and automount
topology. Bind, idmapped, shared, or private views of an approved underlying filesystem remain supported because their
authority comes from retained descriptors and exact identity checks, not the pathname or propagation mode.

Identity inspection requests synchronized `statx` metadata. It prefers `STATX_MNT_ID_UNIQUE`, records the identity kind,
and falls back to ordinary mount ID or explicit unavailability on older kernels. A later comparison includes the kind so
ordinary and unique mount identities are never treated as interchangeable.

This exception is deliberately narrower than a generic path interface. Dropping custom cache roots would break the
documented large-filesystem use case, while a persistent root-owned path registry would still need safe pathname
resolution after reboot. Instead, each live instance record binds the pinned cache root's stable identity (device,
inode, and mount identity where available). A later operation fails closed if it cannot re-pin the same cache identity.
Descendants are opened relative to that pinned root with beneath/no-symlink/no-magic-link resolution and retained until
the privileged effect and rollback are complete.

Privileged-visible basenames are canonical and derived by the receiver. Volume storage is keyed by volume ID rather
than user-visible name; the VM rootfs uses fixed `rootfs.img`; kernel and image names use their canonical IDs plus only
enumerated representations; cloud-init, Firecracker runtime, and snapshot leaves use compile-time fixed names. The user
database may describe a resource, but it cannot choose the basename root will open. Snapshot create exposes the snapshot
read-write and writes its fixed `rootfs.img`, `memory`, and `vmstate` leaves. Restore exposes the snapshot read-only and
overlays the new VM's pinned `/rootfs` at `/snapshot/rootfs.img` inside that VM's private mount namespace; no persistent
phantom-rootfs symlink is created.

The v0.3 canonical persistent layout is:

| Resource | Relative to the pinned managed cache root |
|---|---|
| VM directory | `vms/<vm-id>/` |
| VM rootfs | `vms/<vm-id>/rootfs.img` |
| Cloud-init ISO | `vms/<vm-id>/cloud-init.iso` |
| Firecracker configuration | `vms/<vm-id>/firecracker.json` |
| Persistent VM outputs | Fixed `vms/<vm-id>/firecracker.log`, `firecracker.console.log`, and optional `firecracker.metrics` regular-file leaves |
| Volume | `volumes/<volume-id>.<raw|qcow2>` |
| Kernel | `kernels/<kernel-id>` |
| Snapshot | `snapshots/<snapshot-id>/{rootfs.img,memory,vmstate}` |
| Durable image | `images/<image-id>.zst` |
| Image staging | `images/staging/<image-id>/{source.raw,rootfs.img}` |

Base VM launch admission uses a closed receiver policy before any descriptor is handed to the launch implementation.
The request supplies only the VM ID, kernel ID, cloud-init presence, and the expected rootfs size. Root derives every
component and opens it below the pinned cache descriptor with beneath/no-symlink/no-magic-link/no-cross-mount
resolution. The base resource limits are:

| Resource | Required size | Access and mode policy |
|---|---:|---|
| VM rootfs | 128 MiB through 16 TiB, exactly equal to the bounded expected size | Caller-owned, one-link regular file; owner read/write; no group/world write, execute, or special bits |
| Firecracker configuration | 1 byte through 1 MiB | Caller-owned, one-link regular file; owner read; no group/world write, execute, or special bits |
| Kernel | 1 byte through 1 GiB | Caller-owned, one-link regular file; owner read; execute bits permitted; no group/world write or special bits |
| Cloud-init ISO | 1 byte through 256 MiB | Caller-owned, one-link regular file; owner read; no group/world write, execute, or special bits |

The expected rootfs size is an equality check inside the receiver's hard range; it never raises the hard maximum. These
checks authorize only the base launch set. Snapshot restore, volume attachment, snapshot creation, output creation, and
image provisioning each require their own closed policy before their descriptors can join a launch or mutation lease.
In particular, a QCOW2 file's physical length is not its virtual capacity, so a future volume policy cannot treat
`statx` size as QCOW2 capacity or invoke an unrestricted image tool as root to compensate.

Implementation note (2026-08-29): the private Jailer service now implements this base launch selection and resource
lease. It derives the fixed components from typed IDs, opens them relative to the pinned cache descriptor with the
required no-cross-mount resolution, performs synchronized ownership/type/link/mode/size admission, and transfers the
complete descriptor chain into checked reverse cleanup. The privileged request does not yet carry the cache locator and
base selection, the root-owned instance record does not bind the cache identity, and the mount-namespace launch path
does not consume the root-side lease. The lease is not yet responsible for parsing and constraining the Firecracker
configuration contents. Snapshot, volume, output, and image-provisioning policies remain pending.

Sockets and PID mirrors are not persistent cache artifacts. Their host layout is fixed beneath the root-controlled
ephemeral runtime tree:

```text
/run/mvmctl/                         root:root 0711
├── authority/                       root:root 0700
│   └── <uid>/                       root:root 0700
└── runtime/                         root:root 0711
    └── <uid>/                       root:root 0711
        └── <vm-id>/                 caller-uid:caller-gid 0700
            ├── firecracker.api.socket
            ├── vsock.sock
            ├── console.sock
            ├── firecracker.pid
            └── console.pid
```

The root-owned UID parent prevents the caller from replacing the VM runtime directory. Root creates and pins that
directory descriptor-relatively after durable launch registration, verifies its owner, mode, device, and inode, and
retains the descriptor through mount and rollback. The dropped VM process uses umask `0077`. Socket leaves must be
caller-owned sockets with no world access; PID leaves are caller-owned `0600` positive-decimal display mirrors capped
at 32 bytes. Privileged process control never trusts a PID mirror.

The caller can mutate leaves inside its runtime directory, so root treats the directory only as a fixed output area,
never as authority. It is mounted as a directory at `/run/mvm/runtime` inside the private mount namespace so sockets
created after child release remain visible. The user-owned cache VM directory is never mounted wholesale. Persistent
configuration and output files are opened and verified individually, then mounted or inherited through their pinned
descriptors.

The three persistent output leaves are caller-owned `0600` regular files with one link. A launch truncates each enabled
output exactly once, only after the root launch record is durable, and retains the files after stop or crash for
inspection. `firecracker.metrics` is created only when metrics are enabled. Direct Firecracker writes do not provide a
truthful hard live-size cap; v0.3 therefore promises bounded readers and one-launch retention, not live rotation. A
future hard cap requires a separately designed and supervised FIFO output collector.

Warm-cache files are unprivileged accelerators and never root authority. Existing database path columns may remain as
derived display/local metadata during the clean break, but privileged handlers reconstruct these names from typed
identities and never authorize a path from SQLite.

Every privileged VM operation is serialized by a root-owned per-VM lock and authorized by a minimal root-owned instance
record. Process actions verify host boot ID, PID, start ticks, every real/effective/saved/filesystem UID and GID,
supplementary groups, expected cgroup membership, mount-namespace identity, and the pinned executable hash against the
exact release. Mutating operations require a pidfd, use `pidfd_send_signal`, and wait through that pidfd; a host without
the required pidfd support fails capability admission instead of falling back to a racy raw PID signal. PID reuse fails
closed. The receiver enforces its compiled group-drop policy and never permits a retained root supplementary group.

Release launch preparation acquires the `(version, architecture)` release-slot lock before resolving the root manifest.
The prepared value retains the verified manifest, pinned release directory, and pinned Firecracker/Jailer descriptors.
It supplies the full release identity to instance registration itself, then transfers the release lock into the launch
lease only after the owner-bound record is durable. The unprivileged caller never supplies executable hashes.

The launch path starts a blocked Firecracker child in a new private mount namespace and, when enabled, a blocked console
relay as the dropped caller in the host mount namespace. Before either child can perform an externally mutable effect,
the parent pins each pidfd and the Firecracker namespace descriptor, records the complete identity, and retains the
launch lease. Root then creates/truncates the fixed output slots and runtime directory, makes Firecracker mount
propagation private, installs named mounts, publishes the non-authoritative PID mirrors, releases and verifies the relay,
and only then releases Firecracker to exec the exact Jailer/Firecracker pair. The relay receives only inherited PTY,
pinned log, and pinned runtime-directory descriptors; it never reopens a caller path or joins and retains the VM mount
namespace. Process and namespace allocation for a blocked child are not considered an externally mutable launch effect;
mounts, output creation/truncation, cgroup changes, links, and executable handoff are.

Live snapshot and volume operations reopen and verify `/proc/<pid>/ns/mnt` while the process is alive and enter only
that namespace. No persistent mount-namespace handle is retained after the operation, because doing so would retain
every mount after process exit. Cleanup stops and reaps the verified relay, unmounts named resources in reverse order,
unlinks only the five fixed runtime leaves, and removes the runtime directory only when empty. It preserves persistent
outputs and never recursively traverses a caller-writable tree or a tree that may contain a mount. Unexpected runtime
entries leave a bounded recoverable cleanup record instead of authorizing recursive deletion.

State is split by authority and lifetime:

| State | Location | Ownership/lifetime |
|---|---|---|
| User DB and VM artifacts | Existing `MVM_CACHE_DIR`, normally `~/.cache/mvmctl` | User-owned, persistent |
| Trusted release pairs and manifests | `/var/lib/mvmctl/binaries/<architecture>/<version>` | Root-owned, persistent |
| Privileged instance ownership/release records | `/var/lib/mvmctl/instances/<uid>` | Root-owned, persistent, minimal |
| Jailer chroot directories | `/var/lib/mvmctl/jailer` | Root-owned lifecycle state |
| Per-VM locks and launch handshakes | `/run/mvmctl/authority/<uid>` | Root-owned `0700`, ephemeral; no persistent mount-namespace handles |
| VM sockets and PID mirrors | `/run/mvmctl/runtime/<uid>/<vm-id>` | Root-controlled parent, caller-owned `0700` VM directory, ephemeral and non-authoritative |
| Cgroups | `/sys/fs/cgroup/mvmctl/<vm-id>` | Kernel runtime state |
| `/run/mvm` and `/run/mvm/runtime` | Inside the jail | Individually pinned persistent leaves plus the pinned ephemeral runtime directory; not host authority |

Moving durable user state out of `~/.cache/mvmctl` is a separate XDG migration decision. Full state must not move to
`/run` or `/var/run`.

### Failure and reconciliation contract

A root-owned instance record exists before the first externally mutable privileged launch side effect. A blocked
private-namespace child may exist first only while an inherited private handshake makes external mutation impossible.
Every post-spawn failure retains a live lifecycle handle and invokes a typed abort that verifies identity, terminates
the process through its pidfd, checks cgroup emptiness, and cleans namespace/chroot/instance state in order.

Startup and pre-launch reconciliation compare root instance records, processes, cgroups, and chroot directories. They do
not trust the user database as root authorization. Operations are idempotent and either converge to a known state or
return a precise error without deleting potentially mounted user data.

Read-side status is also reconciled. Before `mvm vm ls`, `mvm vm ps`, or `mvm vm inspect` filters or renders a status,
the normal process obtains a bounded, typed batch observation from root authority. Root verifies the recorded host boot
ID, PID, start ticks, process credentials, cgroup, namespace, and executable identity. A host boot-ID mismatch proves the
old process identity is not live without contacting the guest. The read path never opens one sudo process per VM and
never dials the Firecracker API or guest agent as a liveness tick. It may update the user database as a projection only
after it receives a complete authoritative result; SQLite remains non-authoritative. A boot-ID mismatch projects
`stopped`; a missing or mismatched process during the same boot projects `crashed`. Failure to obtain an authoritative
observation fails the read instead of returning stale status.

Every create, start, reboot, and snapshot-restore path prepares the same complete typed relaunch state. If the VM has a
persisted vsock configuration, the root launch request includes only its validated CID; root derives the fixed runtime
UDS leaf. The normal process retains the agent port and token for the post-launch client probe instead of exposing them
to root. A repository failure aborts launch instead of silently omitting the device. Root removes an old fixed vsock
socket only after authority proves that no live registered process owns it.

## Alternatives Considered

### Separate privileged helper binary

This provides the smallest executable trust boundary, but creates a second artifact and contradicts the product's
single-binary contract. Rejected in favor of early dispatch within one immutable system installation.

### User-owned CLI with a sudo command digest

Pinning a digest would make every user update require sudoers regeneration and would retain a user-owned path at the
root boundary. It also leaves the public CLI initialization path exposed to root execution. Rejected.

### Continue wildcard sudo for a root-owned public CLI

Root ownership fixes executable replacement but `mvm *` would still allow every public command to run as root and load
user state under root authority. Rejected. Only the reserved early dispatcher is allowed.

### Setuid or broad file capabilities

Both would expose privilege on every invocation and make the whole public CLI a security boundary. Required operations
also span mount, network, cgroup, and process privileges that do not reduce cleanly to a safe fixed capability set.
Rejected.

### Root daemon, polkit service, or systemd RPC

These can provide a strong authorization boundary but add a long-running service, protocol lifecycle, and additional
deployment artifacts. Rejected for the initial implementation; reconsider only if concurrency or policy needs exceed the
short-lived dispatcher model.

## Consequences

**Benefits:**

- The product retains one compiled and installed executable.
- Passwordless sudo can no longer execute user-selected program bytes or raw host-tool arguments.
- Root execution bypasses user configuration and the ordinary CLI initialization graph.
- The `mvm` group has a precise meaning: authorized mvmctl operator, not unrestricted root.
- Privileged state and user state have explicit owners and recovery responsibilities.

**Costs and limitations:**

- Installation and system-binary replacement require an explicit administrator step.
- Development binaries cannot perform privileged work unless a compatible system binary is installed.
- All raw privileged callsites must be replaced by typed operations before the final sudoers policy is enabled.
- The single executable contains more code than a dedicated helper, so early dispatch and import-graph isolation require
  strict tests and review.
- Multi-user ownership, crash recovery, release verification, namespace mounts, and firewall atomicity add implementation
  and L2 testing cost.

## Clean Installation and Release Gate

ADR-0005 remains a historical description of the pre-v0.3 policy. v0.3.0 provides no compatibility or automated
migration path for that policy, its user state, or its runtime resources. The implementation and release sequence is:

1. Add early privileged dispatch, the exact administrator bootstrap route, and negative tests while the final marker-only
   path is unreachable from sudoers.
2. Replace every supported privileged service/tool call with a typed operation.
3. On a clean host, install the trusted artifact as the root-owned system binary and atomically activate only the
   marker-scoped sudoers drop-in after `visudo` validation.
4. Run installation, replacement, privilege-abuse, multi-user, path-race, PID-reuse, crash-recovery, Jailer, cgroup,
   snapshot, hotplug, and firewall L2 tests through the installed binary.
5. Mark ADR-0005 superseded for v0.3.0, update all current-behavior documentation, and only then describe the new
   boundary as active.

No implementation phase is complete while documentation describes a different executable path, sudo policy, state
location, lifecycle guarantee, or known limitation.

## Related Decisions

- ADR-0002: One release artifact and one installed executable remain the product contract.
- ADR-0005: Pre-v0.3 group-based wildcard sudo policy; superseded when the v0.3.0 release gate passes.
- ADR-0012: Root boundary, namespace, cgroup, and recovery behavior require L2 verification.
- ADR-0015: Jailer remains canonical; its privilege-boundary and cleanup requirements are refined here.
