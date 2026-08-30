# v0.3.0 Clean-Break Security Release Plan

**Status:** Approved — implementation in progress
**Branch:** `0.3.0-release`
**Release gate:** Draft only until every CI and Python system-test requirement below passes

## Outcome

Release v0.3.0 as a clean installation with one unprivileged public CLI and one root-owned system copy of the same
`mvm` binary. All privileged effects must enter through a versioned typed dispatcher, bind to root-owned resource
authority, and fail closed under multi-user, path-race, process-reuse, partial-failure, and crash conditions.

Firecracker must always run through Jailer with cgroup v2 and one network namespace per VM. nftables becomes the only
firewall backend and is programmed directly through a reviewed, pinned `github.com/google/nftables` version rather than
the `nft` CLI. Managed traffic is default-deny for both same-network and cross-network VMs. IP connectivity and VM-to-VM
vsock execution use separate typed `traffic` and `exec` policies.

The release does not migrate, adopt, backfill, or preserve legacy databases, runtime state, policies, remote-exec flags,
Jailer directories, cgroups, TAP devices, or firewall rules. It detects ambiguous legacy host state and refuses to
continue without silently deleting it.

## Implemented Baseline

- Canonical Firecracker Jailer launch with no direct fallback.
- Exact Firecracker/Jailer pairing in VM and snapshot paths.
- Persisted and verified cgroup v2 CPU, memory, swap, and PID envelopes.
- Transitional routed cross-network service-access policy and default deny.
- Atomic root-owned installation at `/usr/local/bin/mvm`.
- Early reserved privileged dispatch before Cobra and ordinary application initialization.
- Strict bounded request parsing, sudo caller identity, group authorization, environment sanitization, and executable
  identity verification.
- Strict version-1 length-framed request/response codec with matching actions, read-only typed outcomes distinct from
  protocol failures, and bounded safe `DomainError` preservation.
- Private root-owned VM ownership/lifecycle records with strict codecs, descriptor-relative atomic storage, exact
  release references, global VM-ID claims, and release/index/VM locking.
- Private Jailer ephemeral-authority isolation with exact `root:root` mode-`0711` `/run/mvmctl` and exact `root:root`
  mode-`0700` `/run/mvmctl/authority`. Jailer index/release/UID/VM-lock state now lives beneath that tree while
  `/var/lib/mvmctl/instances` remains unchanged. Existing managed objects are verified rather than repaired, with L1
  mode, symlink, concurrent-creation, creation-fault, scan, and lock evidence. The cycle-free
  `internal/lib/authorityfs` leaf module now owns the initializer and closed Jailer/UID/lock capabilities; copied values
  share lifecycle state, and checked idempotent release caches its first result without retry. Its separate LoopMount,
  Network, and Firewall families provide canonical resource locks to their owning services. The firewall family is
  consumed only by an inert generation authority, and the LoopMount provision/inspect actions are registered only in
  the private receiver; no `/run/mvmctl/runtime` tree, live network topology, public LoopMount backend cutover, or
  firewall kernel effect exists yet.
- Private managed-cache locator and lease foundation with descriptor-relative component traversal, synchronized
  owner/mode/identity checks, an explicit local-filesystem policy, retained descriptors, and identity-checked re-pin.
- Private base VM launch-resource selection and lease for fixed rootfs, Firecracker configuration, kernel, and optional
  cloud-init leaves, with descriptor-relative no-cross-mount opening, synchronized admission checks, descriptor
  transfer, and checked reverse cleanup. Caller configuration is strict admission evidence for root-built typed intent;
  only a root-owned `0444` sealed memfd survives successful preparation.
  The locator and base selection are not yet carried by a privileged action, cache identity is not bound into the
  instance record, and the root-side leases and sealed configuration are not consumed by launch.
- Typed SQLite allow-intent foundations for source-network and exact-source-VM traffic grants plus directional,
  exact-user VM-to-VM execution grants. Their repositories enforce exact identities, foreign keys, duplicate
  constraints, and atomic cross-table VM invalidation. Public inputs, enforcement compilation, relay authorization,
  and the clean-break removal of the transitional policy path remain pending.
- Private topology preparation that gates exported authority on the exact committed state, retains the network lock and
  namespace pin through rollback, and preserves a classified lock-acquisition `DomainError` plus cleanup state.
- Private typed nftables compiler pinned to `github.com/google/nftables v0.3.0`, with fixed bridge/inet tables and
  chains, raw-priority ingress guards, same-network/routed/host-service/default-deny/conditional-egress/NAT rules,
  compiler-derived canonical SHA-256 generation identity, strict digest-bound userdata, explicit empty output,
  resolved-IPv4 rejection, and deterministic bounded 1,000-VM L1 evidence. It performs no kernel or privileged effect.
- Named typed LoopMount client/protocol methods for VM provisioning and image inspection/optimization, with grow-only
  VM resize, bounded typed values, and strict process-start/response-EOF authority. A private rootfs authority pins the
  caller-owned cache and fixed VM/image rootfs through retained descriptors, enforces local-filesystem, identity, mode,
  size, and no-cross-mount policies, and has deterministic before/after-open replacement-race evidence for every
  caller-owned component. A private server now splits effects between `rootfsEffects`, containing exactly
  `ProvisionVM` and `InspectImage`, and private `imageOptimizeEffects`, containing only `OptimizeImage`. It validates
  dependencies per action and adds a per-resource lock seam, ordered cancellation-independent cleanup, closed
  mutation/durability evidence, and old-or-target VM grow replay admission with actual admitted-size handoff. The
  reviewed private lock adapter binds the seam to `authorityfs` using only the authenticated caller UID and canonical
  VM/image identity. An inert deterministic mount-source policy accepts only pathless receiver-derived probes, at most
  16 partitions, and either a matching raw filesystem with no partitions or exactly one matching partition; malformed,
  duplicate, and ambiguous layouts fail closed. A pure loop-device contract fixes control identity `10:237`, derives
  block-major-`7` devices from exact 20-bit minors, and builds only an admitted-FD `AUTOCLEAR|PARTSCAN` configuration
  with `READ_ONLY` for read-only access. A private inert capability now performs identity-rechecked fixed-device opens,
  bounded free-device allocation and atomic configure, plus checked cancellation-independent release without exposing
  paths or descriptors. Stable bounded sysfs partition admission and fixed `/usr/bin/blkid` probing bind the selected
  source to exact pre/post-probe identity. An inert read-only inspection adapter uses a private mount namespace, fixed
  `ro,nosuid,nodev,noexec` mount, exact source/mount/image revalidation, bounded OS-release reads, stability proof, and
  reverse cleanup with preserved primary errors. A private `ProvisionVM` adapter consumes the same admitted
  capabilities for descriptor-bound fixed guest writes and ext4/Btrfs grow convergence. Both adapter factories are
  used by the production provision/inspect server. Their exact receiver handlers and early-dispatch entries are
  registered with the embedded-agent accessor bound at the composition root. Live-kernel qualification,
  public/backend cutover, legacy-route removal, and every optimize receiver path remain pending.
- Private strict schema-3 Jailer record/codec and pure lifecycle transitions. Consuming
  `preparedRelease.registerLaunching` derives the release identity internally and transfers the pinned store, slot lock,
  executable admission, and VM lock into a durable launch lease after record replacement. The prepared value and launch
  lease use pointer-shared close-once cleanup state. Production child/pidfd launch, activation, abort/cleanup,
  network/firewall lease transfer, observation, reconciliation, API wiring, and system tests remain pending.
- Private firewall generation authority with a strict committed/applying record and codec, descriptor-relative
  exclusive store at `/var/lib/mvmctl/firewall/generation.json`, and never-unlinked global
  `/run/mvmctl/authority/firewall.lock`. Acquisition requires earlier network authority and admits only the current boot
  and host network namespace. It has no initial kernel inspection, raw unknown-expression decoder, record replacement,
  `Conn.Flush`, recovery, privileged action, public consumer, or system tests.
- ADR-0016 for the single-binary privilege boundary.

Only the exact trusted-release install/remove actions are registered in the privileged switch, and the sudoers policy
remains transitional. The routed policy, dual firewall backends, host-global TAP topology, public `mvm run jailer` /
`mvm run provision` services, and `allow_remote_exec` trust mesh must not survive the final release.

## Approved Architecture

### Trust and state

| State | Location | Authority |
|---|---|---|
| User configuration, SQLite DB, and VM artifacts | Existing `MVM_CACHE_DIR` | Untrusted for root authorization |
| Trusted Firecracker/Jailer pairs | `/var/lib/mvmctl/binaries/<architecture>/<version>` | Root-owned persistent |
| VM ownership/lifecycle records | `/var/lib/mvmctl/instances/<uid>` | Root-owned persistent |
| Managed-network owner/bridge identity records; future VM topology | `/var/lib/mvmctl/networks/<uid>` | Root-owned persistent; current schema 1 is not VM-topology authority |
| Firewall generation record | `/var/lib/mvmctl/firewall/generation.json` | Root-owned persistent; exact committed/applying kernel authority |
| Global capacity allocations | `/var/lib/mvmctl/allocations` | Root-owned persistent |
| Locks and launch handshakes | `/run/mvmctl/authority/<uid>` | Root-owned `0700` ephemeral; no persistent mount-namespace handles |
| LoopMount locks | Per-UID authority `loopmount/` | Root-owned `0600`; exclusive; never unlinked during the boot |
| Global firewall lock | `/run/mvmctl/authority/firewall.lock` | Root-owned `0600`, ephemeral, and never unlinked |
| VM sockets and PID mirrors | `/run/mvmctl/runtime/<uid>/<vm-id>` | Root-controlled parent, caller-owned `0700` VM directory; ephemeral and non-authoritative |
| Jailer chroots | `/var/lib/mvmctl/jailer` | Root-owned lifecycle state |
| Cgroups | `/sys/fs/cgroup/mvmctl/<vm-id>` | Kernel runtime state |

Durable user state does not move wholesale to `/run` or `/var/run`. XDG state separation remains a separate project.

Implementation status: `internal/lib/authorityfs` now owns the descriptor-relative initializer for exact
`/run/mvmctl` and `/run/mvmctl/authority` modes. Jailer uses its private adapters over the module's closed index,
release, UID, and VM-lock capabilities. Separate LoopMount, Network, and Firewall families provide canonical locking
to their owning service seams. LoopMount provision/inspect and firewall generation authority now consume their exact
families, but the module does not create the runtime-output tree; public LoopMount cutover, network topology, and
firewall kernel application remain pending.

### Module and layer rules

- `cmd/mvm/main.go` only recognizes early modes and routes them.
- `internal/service/privileged/` owns the envelope, authenticated caller, executable verification, and fixed action
  switch. It imports no API or core domain and exposes no raw command/argv/effect-path interface. One typed managed-cache
  locator may identify the caller's persistent namespace; every effect below it is selected by typed IDs and fixed
  receiver-derived basenames.
- `internal/lib/privilegedwire/` owns only the strict length-framed request/response codec and bounded `DomainError`
  normalization. `internal/lib/system/` owns the fd-0 socketpair subprocess transport. Neither package exposes a generic
  privileged effect or action dispatcher.
- `internal/lib/authorityfs/` owns the fixed descriptor-relative ephemeral authority tree and only closed named
  capability families. It imports no service, core domain, API, or CLI package and exposes no raw descriptor, path, or
  generic lock-name surface.
- Capability modules under `internal/service/{jailer,network,firewall,loopmount,host}/` own typed privileged effects.
- `pkg/api/` remains the sole orchestrator of multiple core domains.
- Core domains never import sibling `internal/core/*` packages.
- Public validation remains in `pkg/api/inputs/`; privileged receivers enforce trust-boundary invariants.
- Every side-effecting function takes `context.Context` first and returns `pkg/errs.DomainError`.
- Each new capability uses named typed methods matching existing service conventions. No generic backend, operation,
  selector registry, callback-under-lock, raw firewall expression, raw cgroup key, or raw netns action is permitted.

### Privileged control transport

The normal and root processes communicate over one full-duplex Unix stream socket duplicated onto the privileged
process's file descriptor 0 through `sudo -n`. The root process marks it close-on-exec immediately. Standard output and
standard error remain outside the authority channel, and launch/console traffic uses a separate typed relay. The system
runner owns concurrent upload/response I/O so an early root rejection cannot deadlock behind a large archive write.
Root requires an `AF_UNIX` `SOCK_STREAM` descriptor and Linux `SO_PEERCRED` UID/GID equal to the authenticated sudo
caller. The positive peer PID is recorded only for audit and never substitutes for typed receiver authorization. The
production receiver owns `os.Stdin` directly and accepts no alternate reader from the privileged entry point.

The concrete transport always runs
`/usr/bin/sudo -n -- /usr/local/bin/mvm __mvm_privileged_v1 <fixed-action>`. It exposes no executable, argv, environment,
cwd, timeout, callback, decoder, or extra-FD option, never uses `PATH`/`os.Executable`, and never bypasses sudo for a root
caller. It remains separate from `CommandRunner`, `RunCmdOpts`, `SpawnConfig`, and `FakeRunner`; capability packages use
private minimal exchanger interfaces and a dedicated fake in tests.

Requests use fixed `MVMREQ01` magic, a 32-bit network-order JSON-header length capped at 64 KiB, a 64-bit network-order
payload length, the strict schema-version-1 header, and the exact optional payload. The header repeats the fixed argv
action and must match it. An action is 1 through 64 ASCII bytes, starts with an ASCII alphanumeric byte, and thereafter
contains only ASCII alphanumeric bytes, `.`, `_`, or `-`; caller and receiver enforce the same grammar. Only typed
release install accepts a payload: zero bytes or at most 128 MiB. The caller half-closes after the declared bytes. Root
requires exact length and EOF before extraction/canonical publication; anonymous staging may occur while reading.
Zero-payload operations require EOF before effects. Root may reject authentication/header failure without draining.
The request-header read deadline is a fixed five seconds; the install archive-payload read deadline is a fixed five
minutes.
Responses use `MVMRES01`, a 32-bit length capped at 64 KiB, and one strict matching-action success/result or
error/`DomainError` envelope. Each delivery establishes its fixed five-second absolute deadline immediately before the
write, so a long valid effect does not consume the response budget; an earlier context deadline wins. Final error
delivery computes the deadline before `context.WithoutCancel` and never gains a fresh or unbounded interval. These are
compiled, non-configurable security policy.

A delivered envelope is the final effect record even if process reaping later reports an anomaly; root half-closes its
socket write side after the complete frame, so response EOF does not depend on process exit. The codec exposes a
read-only decoded success/error outcome separately from protocol failure, so callers cannot confuse a valid root
`DomainError` with an untrusted malformed frame. The transport reports whether actual socket EOF was observed; in-memory
EOF over copied bytes cannot promote a response. A capability client decodes EOF-qualified bytes with a non-cancelled
context and lets a valid success or valid remote error win over upload `EPIPE`, exit, or later reap diagnostics. No
domain effect or fallible outcome check is allowed after the response. Missing, malformed, truncated, mismatched,
oversized, or non-EOF responses from a started process report `process_started: true` and `outcome_unknown: true`; a
pre-start failure reports both false. Stderr and exit text are never parsed as authority.

The implemented receiver rejects an initially nonblocking fd 0. Its response path uses raw `SendmsgN` with
`MSG_DONTWAIT|MSG_NOSIGNAL` and bounded `POLLOUT` waits against the established absolute deadline. This preserves the
deadline and suppresses `SIGPIPE` even when a duplicate descriptor mutates shared `O_NONBLOCK` state after admission.
Only structurally admitted channels with a valid version-1 action receive an early framed error. The complete frame is
written before `SHUT_WR`; malformed invocation and admission failures are unframed, and rejected closed actions are not
drained.

### VM authority interface

The final interface remains private to `internal/service/jailer`. Schema 3 uses unexported typed values and these
operations:

```go
func (a *releaseAuthority) prepareInstalled(
    ctx context.Context,
    slot releaseSlot,
) (*preparedRelease, error)

func (p *preparedRelease) registerLaunching(
    ctx context.Context,
    caller instanceCaller,
    intent launchIntent,
) (*launchLease, error)

func (l *launchLease) activateVerifiedProcess(ctx context.Context) (*activeLease, error)

func (l *launchLease) Abort(ctx context.Context) (*cleanupLease, error)
func (l *activeLease) BeginCleanup(ctx context.Context) (*cleanupLease, error)

func (l *releaseSlotLease) requireUnreferenced(
    ctx context.Context,
    release releaseIdentity,
) error

func (a *instanceAuthority) LockActive(
    ctx context.Context,
    caller instanceCaller,
    vmID string,
) (*activeLease, error)

func (a *instanceAuthority) ObserveOwnedBatch(
    ctx context.Context,
    caller instanceCaller,
    vmIDs []string,
) ([]instanceObservation, error)

func (a *instanceAuthority) BeginCleanup(
    ctx context.Context,
    caller instanceCaller,
    vmID string,
) (*cleanupLease, error)

func (l *cleanupLease) Complete(ctx context.Context) error

func (p *preparedRelease) Release(ctx context.Context) error
func (l *launchLease) Release(ctx context.Context) error
func (l *activeLease) Release(ctx context.Context) error
func (l *cleanupLease) Release(ctx context.Context) error
func (l *releaseSlotLease) Release(ctx context.Context) error
```

Each lease retains its pinned directory and lock descriptors and provides a checked context-first release operation.
There is no public `Get`, `Put`, `Delete`, `Run`, raw path, arbitrary state string, or callback interface.

The schema-3 durable lifecycle is intentionally small:

```text
absent  ── registerLaunching ──> launching ── activateVerifiedProcess ──> active
cleaned ── registerLaunching ──> launching
launching ──── Abort ────> cleaning
active    ── BeginCleanup ──> cleaning
launching|active|cleaning ── recovery BeginCleanup ──> cleaning
cleaning  ───── Complete ─────> cleaned
```

`launching` means only that exact pre-effect intent is durable. It stores owner UID/GID, VM ID, cache identity, exact
release identity, host boot ID, launch/cleanup generations, the blocked child PID/start locator, and an explicit
`absent` relay or the blocked relay PID/start locator. `active` adds receiver-observed final credentials with an empty
supplementary-group set, final mount namespace and jail root, and
Jailer's copied Firecracker identity after it is sealed and hashed against the release. A present relay adds its full
receiver-observed caller credentials, empty groups, host namespace/root, and exact root-owned `/usr/local/bin/mvm`
executable metadata and digest; `console.pid` remains display-only. Activation also verifies the
exact derived cgroup and the separate closed topology capability's network namespace. Failed cleanup remains
`cleaning`. `cleaned` is a persistent same-UID ownership tombstone, carries no launch-specific GID, cache, release, or
process identity, and may be relaunched only by that UID. Mount identity is deliberately not retained across cleanup
because it is valid for only one mount lifetime.

Implementation status: main now contains the private strict schema-3 record/codec and pure lifecycle transitions. A
prepared release can consume its manifest-derived identity and pinned store/slot/executable authority into a durable
`launching` lease that retains the VM lock; the prepared value and launch lease are pointer-shared and close once. No
production VM lifecycle consumes that record yet. Child/pidfd launch, `activateVerifiedProcess`, typed abort/cleanup,
observation, and reconciliation remain pending.

The global lock order is:

```text
release lock -> global index lock -> VM lock -> network lock -> firewall lock
```

Operations may acquire a suffix or subset but never an earlier class while holding a later class. Global VM-ID claims
are established by descriptor-enumerating all numeric UID record directories while holding the index lock. A foreign,
duplicate, corrupt, unreadable, or inconsistent claim fails closed. Lock files are never unlinked.

Release locks are keyed by the canonical store slot `(version, architecture)`, not by caller-observed binary hashes, and
are acquired before root manifest resolution. Reference matching remains against the full release identity. Competing
identities for one store slot must therefore serialize before install, replace, remove, launch, or reference inspection.

Task 6 acquires the release-slot lease before it reads a manifest or computes a launch identity. Its prepared launch
value retains that lease together with the verified manifest and pinned Firecracker/Jailer descriptors. Only that value
may call its consuming `registerLaunching`; it derives the exact release identity internally rather than accepting
hashes from the normal user process. Registration acquires the index and VM locks, establishes durable `launching`
before the first network or launch effect, and transfers the pinned admission, store/directory descriptors, and release
lock into the returned launch lease. Pre-rename failure leaves the prepared value live. Post-rename failure returns a
non-nil launch lease plus `record_replaced` and any `durability_uncertain` evidence so typed abort retains authority.
Task 4 owns release, index, and VM locking; it does not import network code or predeclare a network implementation.

The launch seam uses a child blocked by an inherited private two-way handshake in an initial private mount namespace
and, when enabled, a blocked console relay running as the dropped caller in the host mount namespace. After opening each
pidfd, the parent sends a fresh challenge and requires acknowledgement from the still-blocked child before accepting
PID-derived evidence. It then persists only `launching` intent. Before Jailer can execute, reconciliation under the
held release/VM locks proves `/var/lib/mvmctl/jailer/firecracker/<vm-id>` absent; it removes only an exactly admitted,
non-mounted stale fixed tree after process absence, and ambiguous state is unavailable. It creates/truncates fixed
persistent outputs, creates the runtime directory, marks propagation private, and installs named mounts through pinned
descriptors. The relay receives only inherited PTY, pinned log, and pinned runtime-directory descriptors and never
retains the VM namespace.

The launch lease retains VM, network, and firewall leases. It creates all links down, applies the complete atomic
nftables generation, verifies it freshly, and only then raises and reverifies links. An unknown firewall outcome keeps
links down and both children blocked. Only after those effects and exact relay readiness does the parent recheck the
fresh jail destination and release the child to Jailer with the pinned netns. Jailer 1.16.1 copies
Firecracker, joins netns/cgroup, unshares and pivots into a new final mount namespace, drops credentials, and execs its
copy. The initial namespace therefore cannot be recorded as the final namespace, and the trusted-store inode cannot be
recorded as `/proc/<pid>/exe`. `activateVerifiedProcess(ctx)` consumes the launch lease's retained prepared release and
uses bounded context-aware `/proc/<pid>/exe` observation with a 10-second budget and 25-millisecond minimum interval,
while the original pidfd supplies stable task identity and exit detection. Pidfds do not notify `exec`; an exited pidfd
ends observation. It then opens the fixed jail root and final proc objects, seals the jail root and copy to
`root:root 0755` below `root:root 0700` ancestors, hashes the copy, rechecks complete VM and relay identity, and commits
`active`. Jailer never receives `--daemonize` or `--new-pid-ns`.

Here, "first launch effect" means the first externally mutable effect; allocating a handshake-blocked child and its
initial namespace is preparation. Mutating process operations require pidfds and never fall back to `kill(pid)`. Live
mount operations reopen and verify the final `/proc/<pid>/ns/mnt`; a persistent mount-namespace handle is prohibited
because it would retain the namespace's mounts after process exit.

All composite leases use pointer-shared mutex-protected state, close/unlock exactly once across copies and concurrent
release, ignore cancellation during cleanup, and cache the first cleanup result. They expose no raw path, descriptor,
callback, or generic state mutation.

`launchLease.Abort` and `activeLease.BeginCleanup` consume their source directly into a cleanup lease, transferring
release/VM locks, pidfds, and all mount/network/firewall resources without unlock/reacquire. Authority-level
`BeginCleanup` is only fresh recovery admission. Cleanup and reconciliation re-admit a present relay through pidfd plus
its state-appropriate exact locator or full identity and never signal an ambiguous or reused PID.

The persistent VM outputs are fixed caller-owned `0600` one-link regular files: `firecracker.log`,
`firecracker.console.log`, and, when enabled, `firecracker.metrics`. They are truncated once per launch after durable
`launching` registration and retained after stop or crash. Direct Firecracker output supports bounded readers and
one-launch retention but not a truthful hard live-size cap; such a cap would require a separately supervised FIFO
collector. API, vsock, and console sockets plus the two PID mirrors are fixed ephemeral leaves beneath
`/run/mvmctl/runtime/<uid>/<vm-id>`. Root retains the directory descriptor through setup and rollback but never treats
its caller-writable contents as authority. The cache VM directory is never mounted wholesale. Cleanup unlinks only the
fixed runtime leaves and removes the directory only when empty; it never recursively deletes caller-writable contents.

Atomic record replacement uses a random exclusive temporary file, bounded writes, root ownership and exact modes, file
`fsync`, checked close, `renameat`, and parent-directory `fsync`. Pre-rename failure preserves the old record.
For activation, pre-rename failure leaves the launch lease live. Once the active rename occurs, in-memory ownership
first transfers to a non-nil active lease; a later failure returns that lease with a `DomainError` carrying
`record_replaced=true` and any `durability_uncertain=true`. The caller cleans through active authority, never stale
launching authority. Registration follows the corresponding prepared-to-launch contract above.

### Trusted release authority

Task 6 adds a private `releaseAuthority` in `internal/service/jailer`. Its later install request contains only a
validated release version, architecture, and `allow replacement` permission; removal carries only the same validated
slot. Replacement permission is not a replace-only mode. Root constructs the fixed official checksum and archive URLs
and obtains the checksum independently through a dedicated bounded HTTPS-only client with proxies disabled. The
implemented checksum client disables HTTP/2 and uses fresh HTTP/1 connections because Go's HTTP/2 transport may retry
bodyless GETs internally and violate the one-retrieval-attempt contract. For mirror efficiency, the normal-user client
may stream an exact-length bounded archive body through the typed install transport; root hashes it against the
independently fetched checksum before parsing it. A zero-payload install instead makes one receiver-derived root-origin
archive request under the separate closed transport contract in ADR-0016. Root never opens a caller path or accepts a
caller URL or checksum, and `MVM_ASSET_MIRROR` supplies caller-streamed bytes rather than root authority.

Slice 5b introduced the initially unregistered wire contract in `internal/service/jailer`, which owns the exact action
constants `jailer-release-install` and `jailer-release-remove`; `ReleaseSlot`; install request with
`AllowReplacement`; remove request; a typed string outcome closed to `installed|replaced|unchanged`; install result with
the exact slot, archive SHA-256, and each executable's SHA-256 and size; and a remove result containing only `Removed`.
These values carry no path, URL, caller hash, force, reference override, generic action, reader, or decoder field. One
concrete `TrustedReleaseServer` owns authority. Its named install takes
`(ctx, request, payload io.Reader, payloadLength uint64)`. Privileged imports Jailer and supplies the private raw
reader; the implemented named Jailer client imports only system/privilegedwire, never privileged. Slice 6 registered
only these two exact actions.

Zero install/remove require EOF before effect. Nonzero install streams once through Jailer-owned archive-stage
length/EOF admission; staging may occur during read but canonical publication cannot occur before EOF. There is no
128 MiB buffer, second stage, `LimitReader`, replay, registry, or generic dispatcher. Each handler owns one response and
`SHUT_WR`; handled/unhandled disposition prevents outer double framing.

Readers carry operation context and absolute deadline and use raw `Recvmsg(MSG_DONTWAIT)` plus `POLLIN`. No cancellable
poll waits longer than the compiled 100 ms maximum, so cancellation does not wait for the full read deadline. Header gets
five seconds; nonzero payload gets a new five-minute reader deadline; zero/remove retain the header deadline for EOF.
Original context reaches Jailer and the payload deadline does not constrain post-EOF work. Validate outcome, exact
request slot, lowercase hashes, sizes, and nested values before success; slice 6 repeats after decode. Remove bool is
closed. Response timing, original `DomainError`, fd ownership, and no-double-frame rules remain as above.

Extraction validates the reviewed complete upstream member allowlist and extracts only Firecracker and Jailer.

The implemented zero-payload root fetch revalidates the receiver-derived source before one retrieval attempt and
redirect chain: one initial HTTPS GET plus at most one redirect GET, with no retry. It disables HTTP/2 and uses fresh
HTTP/1 connections because Go's HTTP/2 transport may retry bodyless GETs internally. Its dedicated TLS-1.2-or-newer
transport has no proxy, cache, compression, or keepalive path and permits one live connection. It applies 5-second
dial/TLS/header timeouts, a 16 KiB header limit, and a fixed five-minute maximum further bounded by the caller context
and its deadline. The redirect may target only exact host `release-assets.githubusercontent.com` without a port, user
information, or fragment. The opaque signed query is allowed, but only the fixed safe headers are reapplied; no
authorization or cookie is carried. HTTP 200 and one effective validated `Content-Length` from 1 byte through 128 MiB
after Go `net/http` processing are required before stage mutation. `net/http` rejects malformed or conflicting
duplicates and deduplicates identical duplicates; mvmctl rejects missing or chunked length and any still-exposed
multiple values. No raw HTTP parser is required.
The response is streamed once into the existing anonymous archive stage and admitted through its exact-length
positioned-write, independent-digest, EOF, fsync, stable-metadata, and zero-offset checks. Any failure after receive
starts poisons the stage. A body-close failure after an otherwise successful receive also poisons it, so only checked
`Release` remains valid. Header, status, and length rejections happen before mutation. There is no temporary path,
memory copy, replay, ordinary downloader, or root asset-mirror access.

The private fetch and anonymous-stage admission are implemented with hermetic L1 coverage. Private end-to-end install
composition is also implemented; typed privileged, API, and CLI wiring remain pending.

The frozen v0.3 extraction contract accepts only audited x86_64 archives for `1.10.1`, `1.14.2`, `1.14.3`, `1.14.4`,
`1.15.0`, `1.15.1`, `1.16.0`, and `1.16.1`. Each must contain the exact order-independent 24-member set recorded in
ADR-0016, encoded as one GNU local-PAX header plus one GNU regular-file header per logical member. The parser accepts
only the closed `mtime` plus optional `uid`/`gid` PAX grammar, exact modes and names, canonical octal
mode/uid/gid/size/mtime fields, the canonical checksum form, NUL-only unused device fields, one gzip member, at most
32 MiB decompressed, at most 8 MiB per logical member, exactly two tar end blocks, and only decompressed zero padding
through gzip EOF. It rejects every unexpected extension, member, type, duplicate, malformed checksum, non-NUL device
field, overflow, concatenated gzip member, and trailing byte. Archive metadata is never authority. Source and ELF
support for `aarch64` does not imply archive support; aarch64 extraction remains fail-closed until its own archive audit
is recorded.

Only the two selected executables may enter private root-owned staging descriptors, and nothing may be published until
the complete archive and both ELF files validate. The strict bounded gzip/PAX/GNU-tar parser and its private
selected-writer seam are implemented and have been validated against all eight mirrored x86_64 release archives. That
writer seam now owns concrete anonymous root-owned Firecracker/Jailer descriptors. Extraction uses bounded positioned
writes; finalization binds exact size and full-file SHA-256 to closed ELF admission, applies exact mode `0755`, fsyncs
both files, and re-verifies final identity, metadata, and zero offset. Unit and fault tests cover the lifecycle, and the
complete path has passed against all eight cached audited x86_64 archives. Both stages remain anonymous and unpublished.
The fixed architecture-directory and recovery slice is implemented under the exact active release-slot lease. It
admits every matching reserved candidate before deletion, fails closed on malformed or unsafe state, and uses
cancellation-independent, resumable fixed-leaf cleanup with candidate and architecture directory fsyncs after the
corresponding namespace mutations. Its required reserved-name device/inode rechecks remain pending and must land with
removal before privileged wiring. Strict manifest staging and complete candidate assembly are also implemented. The
canonical manifest is positioned-written and fsynced in an anonymous root-owned descriptor; production links it and
the finalized anonymous Firecracker/Jailer pair into one exact root-owned mode-`0700` reserved candidate using only
`AT_EMPTY_PATH`. The linked files, candidate directory, and architecture directory are fsynced in that order; every
writable descriptor is checked closed before exact shared re-admission. Candidate discard, recovery, cancellation, and
partial-failure edges have fault-injection coverage. Both private publication transitions are now implemented. An
existing canonical version must contain exactly the three fixed leaves and pass shared strict manifest, full-file hash,
and ELF admission before an identical canonical manifest returns unchanged without a reference scan. A different
complete release conflicts on absent-only publication, and unsafe or corrupt state fails closed. An absent slot commits
with descriptor-relative `renameat2(RENAME_NOREPLACE)` and no fallback. Explicit replacement fully admits the old
release, uses the active release-slot lease to prove its exact identity unreferenced, and rechecks both directory
bindings. It commits only through descriptor-relative `renameat2(RENAME_EXCHANGE)` without an absent-install fallback.
The candidate becomes installed or replaced before post-commit fsync and close-only cleanup. Replacement retirement
unlinks only the three fixed old leaves through the retained old-directory descriptor and fsyncs that directory. It
then rechecks the reserved-name binding, removes that directory, and fsyncs the architecture directory again. Later
errors preserve the primary `DomainError` and report the committed state, uncertain durability, and retained old
release precisely. Fault injection covers referenced-release and corrupt-authority rejection, name-binding races,
cancellation boundaries, commit syscalls, both parent fsyncs, every retirement step, cleanup, recovery, and combined
error metadata. The private end-to-end method composes independent checksum authority, caller-stream or root-fetch
archive admission, strict extraction/finalization, candidate assembly, and absent/identical/replacement selection. It
returns only a closed outcome and fully re-admitted manifest metadata, preserves committed results through post-commit
errors, and returns a zero result for precommit failure. The named typed client and two privileged actions are wired,
but the public API/CLI still uses the legacy binary path. The aarch64 audit, public-authority cutover, L2 qualification,
and remaining release integration work are still pending.

The strict root-owned manifest stores schema version, release slot, archive hash, and each executable's hash and size.
The store is exactly `/var/lib/mvmctl/binaries/<architecture>/<version>/{firecracker,jailer,manifest.json}`. Binaries are
validated as ELF for the selected architecture without execution. Descriptor-relative atomic install, exchange, and
removal use only fixed leaves. The canonical slot is always one complete three-file release or absent. Post-commit
errors report `release_installed`, `release_replaced`, `release_removed`, `durability_uncertain`, and
`retired_release_retained` details without replacing the primary error identity.

Publication holds the release-slot lease and uses a root-owned `0700` candidate directory beneath the pinned
architecture directory. Its reserved slot-scoped name is
`.mvm-release-<64-lowercase-slot-digest>-<32-lowercase-random-hex>.tmp`; recovery examines only that exact slot prefix,
admits only a subset of the three fixed safe leaves, and performs fixed-leaf unlink plus `rmdir`, never recursive
removal. Empty verified architecture directories may remain. The finalized executable stages and strict manifest are
linked into the candidate, fsynced, and re-admitted as one complete release before commit.

`allow replacement` is permission rather than a replace-only mode: an absent slot installs normally, an exact
canonical-manifest match returns unchanged, and a differing complete release is exchanged only with permission after
complete old-release admission and exact unreferenced-identity proof. Corrupt state fails closed. Absent install uses
descriptor-relative `renameat2(RENAME_NOREPLACE)`, replacement uses `renameat2(RENAME_EXCHANGE)`, and neither has a
non-atomic fallback. Rename/exchange success is the observable commit point. Subsequent errors annotate the committed
state and durability; replacement retirement removes only the old fixed leaves and reserved directory after the first
parent fsync and never rolls back the new release.

The private result contains a closed `installed`, `replaced`, or `unchanged` outcome and the exact release slot, archive
digest, and executable digests and sizes from full shared re-admission. The strict manifest is the single metadata
source; no redundant stored identity is required, and identity may be derived when needed. A precommit error returns a
zero result. A committed installed/replaced result and metadata remain available alongside any postcommit error.
Unchanged and its metadata become authoritative only after candidate-transaction cleanup completes; an earlier
candidate cleanup failure returns a zero result and requires retry. A later outer slot-lease release failure retains
unchanged and its manifest metadata alongside the error. The normal-user process consumes this result rather than
reopening the root-owned mode-`0700` store.

Removal is private and deliberately deeper than its implementation details:

```go
func (authority *releaseAuthority) removeInstalled(
    ctx context.Context,
    slot releaseSlot,
) (removed bool, err error)
```

It accepts only a validated slot and acquires that slot's release lease before any store read. Existing-only traversal
creates nothing; a safely missing store or architecture is unchanged without a reference scan. Within an existing
architecture, removal inspects the canonical version before recovery. A safely absent version triggers recovery and
then returns unchanged without a reference scan. An existing version first passes exact-three-leaf shared admission,
same-filesystem and directory-identity binding; corrupt canonical state therefore causes zero recovery mutation. Only
after that admission does removal recover reserved remnants, then pass the exact manifest-derived identity to
`requireUnreferenced`. Corrupt store or reference authority fails closed.

Commit moves the complete canonical version directory to one newly generated exact slot-scoped reserved name through
descriptor-relative `renameat2(RENAME_NOREPLACE)`. Generated names are grammar-checked. The first attempt plus at most
seven retries permits eight total name/rename attempts; only `EEXIST` is retryable. Canonical name binding and
cancellation are rechecked immediately before each attempt. There is no force, reference bypass, plain-rename, copy,
per-file, or recursive fallback. Rename success immediately makes the removal target committed and close-only.

Uncancelled post-commit work fsyncs the architecture, closes the old admission while continuing safe best-effort
cleanup, verifies the reserved name binding, removes only `manifest.json`, `jailer`, and `firecracker` through the
retained old-directory descriptor, fsyncs that directory, reverifies the reserved binding, removes the directory, and
fsyncs the architecture again. Post-commit errors preserve the primary `DomainError` and distinguish removed state,
uncertain durability, and a retained retired directory exactly as ADR-0016 specifies.

Removal-owned rename, fsync, unlink, and `rmdir` failures use `CodeBinaryRemoveFailed`/`ClassInternal`. Shared admission
and reference `DomainError` values preserve their original code, class, entity, details, and cause through cleanup and
post-commit annotation.

The removal slice also corrects shared recovery before any privileged action can use it. Every admitted reserved
directory records device/inode identity, and recovery reopens and rechecks the exact name immediately before leaf unlink
and before `rmdir`. It still admits all matching entries before mutation, accepts only safe subsets of the three fixed
leaves, and never recursively deletes content. A crash before commit leaves the complete canonical release; a crash
after commit leaves the canonical slot absent and an identity-bound safe remnant for retry. When canonical is absent,
the retry recovers first and then reports unchanged; when canonical exists, it is admitted before recovery. `--force`
may later suppress only an untrusted SQLite warning in the ordinary process; it is never carried to root or allowed to
bypass root references.

Private atomic removal and the recovery binding correction are implemented with L1 fault coverage for safe absence,
admission and references, bounded collision retry, every commit/cleanup boundary, exact post-commit details,
close-only target state, device/inode replacement races, crash/retry, and slot-lock retention through final durability.

Launch uses a private prepared value that owns the release-slot lease, verified manifest, pinned release directory, and
pinned executable descriptors. It alone supplies release hashes to instance registration. The privileged transport must
return a strict versioned response envelope before release install/remove is wired, because generic subprocess errors
cannot preserve the partial-state details required for safe retries.

```go
func (a *releaseAuthority) prepareInstalled(ctx context.Context, slot releaseSlot) (*preparedRelease, error)
func (p *preparedRelease) Release(ctx context.Context) error
```

Preparation and checked release are implemented first. The later launch slice adds only the typed ownership transfer
that it actually needs; it does not expose raw descriptors, paths, caller-supplied hashes, or a generic operation hook.

The trusted-release work proceeds in this order. Steps 1 through 6 are complete. Steps 7 through 9 remain an acceptance
plan and are not implemented:

1. **Freeze the fetch and removal contracts.** ADR-0016 and the task ledger must record the exact trust inputs, commit
   points, crash states, error details, and prohibited fallbacks. Acceptance is a documentation diff review with no
   implementation or changelog claim; verification checks links, referenced paths, line length, stale wording, and the
   unchanged completed-checklist set.
2. **Implement the private root-origin fetch.** The dedicated zero-payload archive client now streams its final accepted
   response directly into the anonymous archive stage. It enforces the exact source, transport, redirect, response,
   size, stream, and checked-close policy above. Hermetic L1 tests cover request, redirect, header, status, length,
   short/long body, digest mismatch, cancellation, timeout, receive-stage poisoning, post-receive close poisoning, and
   no pre-admission mutation.
3. **Compose private end-to-end install.** One private release-authority method selects caller-stream or root-fetch
   input, independently fetches checksum authority, admits the anonymous archive, extracts and finalizes both
   executables, assembles the strict candidate, and applies `allow replacement` as permission: absent installs,
   identical complete releases remain unchanged, and differing complete releases require permission plus exact
   reference proof. Acceptance requires a closed outcome and exact fully re-admitted manifest-derived metadata, with a
   zero result on precommit failure, committed installed/replaced result plus metadata alongside postcommit error, and
   unchanged plus metadata only after successful candidate cleanup. Candidate cleanup failure before unchanged
   completion requires retry; a later outer slot-lease release failure retains unchanged plus metadata alongside the
   error. Preserve the first `DomainError` and commit details through checked reverse cleanup. The manifest is the
   single metadata source; derive identity when needed, add no redundant stored identity, and never reopen the
   root-owned mode-`0700` store from the normal process. L1 covers both body modes, all intent/canonical-state
   outcomes, exact result metadata, every handoff, commit boundary, cancellation, and cleanup fault. Add no wire, CLI,
   API, mirror-path, or legacy-downloader dependency.
4. **Implement private removal and recovery correction.** Add only `releaseAuthority.removeInstalled(ctx, slot)` under
   the exact lease, canonical admission before recovery, no-replace commit, eight-attempt bound, close-only state,
   fixed-leaf retirement, and error classification above. In the same slice, bind recovery names to admitted directory
   device/inode before leaf unlink and `rmdir`. Acceptance is the full absence, admission-order, reference, collision,
   non-retryable error, binding-race, syscall, fsync, cleanup, combined-error, and crash/retry L1 matrix. It must also
   prove that every canonical slot is one complete three-file release or absent.
5. **Add the receiver transport foundation.** Build on the implemented caller process transport and strict wire codec.
   Land it as two separately reviewable substeps without opening the action catalog:
   - **5a. Admit and answer on fd 0 — implemented.** The production entry owns fd 0, requires blocking
     `AF_UNIX`/`SOCK_STREAM`, sets and verifies `CLOEXEC`, requires a positive peer PID from `SO_PEERCRED`, matches its
     UID/GID to the sudo caller, and uses the exact shared action grammar. Only structurally admitted channels receive
     bounded early framed errors.
     Raw `SendmsgN(MSG_DONTWAIT|MSG_NOSIGNAL)` with absolute-deadline `POLLOUT` waits remains safe after shared
     status-flag mutation; the full frame precedes `SHUT_WR`. Closed actions are not drained, and the obsolete
     standalone EOF-delimited JSON decoder is removed. L1 fault tests and real socketpair tests cover ordering,
     admission, peer mismatch, deadline/write/half-close failures, cancellation, no-drain behavior,
     process-independent EOF, cleanup, and status-flag mutation. Independent audit findings were corrected before
     commit.
   - **5b. Add the approved closed release handlers without registration — implemented.** Jailer now owns the exact
     action constants, closed request/result values, and authority-owning `TrustedReleaseServer`. Named install takes
     the request plus one `io.Reader`/length; privileged supplies it. Zero/remove require EOF before effect. Nonzero
     install streams through Jailer-owned length/EOF admission without buffer, second stage, `LimitReader`, or
     publication before EOF.
     Readers cap each cancellable poll wait at 100 ms; payload deadline limits only reads, not post-EOF work. Install
     accepts at most 128 MiB and remove accepts zero. A fresh five-second response deadline starts immediately before
     delivery; the handler preserves one result or original `DomainError`, half-closes, and never closes fd 0.
     Success semantics and ADR-0016's direct L1 abuse matrix are covered, including blocked read cancellation, invalid
     effect results, phase deadlines, transport failures, and all closed outcomes. Both actions remained unknown/no-read
     until step 6 completed the typed client capability; they are now exact registered actions.
   The already-resolved caller upload, response-EOF, and reap behavior is not reopened in either substep.
6. **Add typed client methods and actions — implemented.** Jailer has one private minimal `Exchange` interface, named
   install/remove methods, a dedicated fake, and exact privileged action registrations. Dispatch distinguishes a handler-owned
   response transport failure from an unhandled action so outer `run` never writes a second frame. The client uses the
   existing EOF-qualified
   response authority and remote-result precedence over upload/reap diagnostics. Capability tests confirm those resolved
   semantics while preserving partial-state details and exposing no generic privileged call, decoder hook, executable,
   argv, environment, path, URL, checksum, force, or reference-bypass input.
7. **Switch API authority and remove the legacy path.** Treat SQLite binary data only as an untrusted user-facing
   projection, route public install/remove/list selection through the typed release client, and delete the old root
   downloader/path-based binary mutation route rather than preserving compatibility. Do not update SQLite after a root
   postcommit error; retry/idempotency obtains root truth and repairs the projection. Add
   repository/API tests for projection mismatch and local `--force` warning behavior, searches proving no legacy
   privileged path remains, and public result/error tests that retain root partial-state details.
8. **Audit aarch64 packaging.** Inspect a real aarch64 release archive and freeze its exact member/format/version
   contract before enabling extraction. Acceptance requires independently recorded evidence and mirror-backed
   parser/staging tests. Source derivation and ELF support alone remain fail-closed and do not count as architecture
   support.
9. **Qualify through Python system tests.** Extend the existing registered system-test domains and coverage matrix,
   build a release candidate, install it only inside disposable nested-virt runner VMs, and set
   `MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror` for asset workflows. Acceptance covers installed-CLI install,
   idempotency, replacement, referenced denial, removal, crash/retry, reboot, cross-user abuse, malformed transport, and
   zero leaked release/runtime resources; full release signoff still requires the later complete CI and QA gates.

### Network topology and policy

ADR-0017 is the authoritative design:

```text
host managed bridge
└── host veth
    └── per-VM network namespace
        ├── namespace veth
        ├── namespace-local bridge
        └── TAP opened by jailed Firecracker
```

Namespace paths and interface names are derived from UID and immutable IDs. The privileged transaction creates links
down, retains VM/network/firewall leases, installs and freshly verifies the complete atomic nftables generation and
anti-spoofing rules, then brings the exact links up and reverifies them. Only afterward may it pass the pinned namespace
handle to the blocked Jailer child.

The private topology-preparation foundation now derives owner/network-bound managed bridges and owner/VM-bound links,
enforces the VM-before-network lock seam, records mutation-plus-error identities, and retains the network lock and
namespace pin through rollback-safe cleanup. `Topology()` returns authority only from the exact committed state with
both leases and all namespace/local-bridge/TAP/veth creation markers present. Building, cleaning, cleaned,
missing-lease, and partial-rollback states return no authority. A classified `DomainError` from lock acquisition
survives successful rollback exactly; failed lock cleanup preserves its code, class, entity, prior details, and cause
while adding `cleanup_required=true`. The foundation deliberately exposes no production constructor or live-topology
release.

The private strict schema-1 managed-network record/codec/store and inert registration authority are implemented. Root
persists only owner, network, current boot, and exact derived managed-bridge name/ifindex/host-namespace identity at
`/var/lib/mvmctl/networks/<uid>/<network-id>.json`, beneath exact root-owned mode-`0700` directories with a mode-`0600`
one-link record. The 4-KiB strict codec rejects missing/unknown/duplicate/case-colliding/trailing structure. Publication
uses descriptor-relative stable admission, exclusive temporary creation, file sync, checked close, no-replace rename,
and parent sync; a post-rename sync failure returns the record with `record_created` and `durability_uncertain`.
Registration holds the global ownership-index then per-network lock, scans all owners to reject cross-UID duplicates,
and cleans per-network lock, lock/store UID capabilities, store root, index lock, then lock root in exact reverse order.
Cleanup is cancellation-independent, close-once, and repeats cached results while preserving primary error identity.

This remains an inert identity foundation. No production path calls registration; no real VM-lock lease is transferred
into registration or topology preparation; and no namespace/link/firewall kernel effect consumes the record. VM-lock
transfer, netlink effects, firewall/link activation, durable per-VM topology lifecycle, global capacity authority,
production construction, Jailer handoff, public cutover, and live qualification remain pending.

The private root-side compiler in `internal/service/firewall/` pins `github.com/google/nftables v0.3.0` and receives
only mvmctl-owned typed resolved intent. It builds fixed bridge and inet tables/chains. Raw-priority bridge prerouting
holds source MAC/IPv4/ARP anti-spoofing and unsupported-L3 guards; bridge forward holds same-network policy. Inet
forward/input/postrouting cover routed policy, established replies, host services, default deny, and conditional
egress/NAT, with neither uplink egress nor NAT when masquerade is false. It rejects overlapping managed prefixes and
unusable IPv4 authorities and orders output deterministically. The caller supplies no generation identity. Explicit
empty input emits no reserved tables or rules.

Generation identity is SHA-256 over a versioned canonical closed projection of normalized intent and the exact managed
tables, chains, rules, expressions, order, and userdata schema. A fixed digest placeholder breaks userdata
self-reference before the derived digest and rule identity are bound into every rule. The projection covers every
field of exactly the emitted `expr.Meta`, `expr.Masq`, `expr.Bitwise`, `expr.Cmp`, `expr.Ct`, `expr.Payload`,
`expr.Range`, and `expr.Verdict` structs from pinned `github.com/google/nftables v0.3.0`; typed-nil and unknown
expressions fail closed. Digest encoding streams through a fixed 64-KiB buffer into SHA-256 and returns canonical
validation, encoding, or finalization failures. Module or expression-set changes require a schema audit and
digest-version bump. The isolated representative 1,000-VM benchmark was roughly 23–28 ms, 10 MB, and 179,000
allocations, with a 250,000-allocation L1 ceiling.

This compiler creates no `nftables.Conn` and calls no `Flush`. A separate implemented private foundation provides the
strict committed/applying generation record and codec, its exclusive descriptor-relative store, the global firewall
lock, same-boot/host-netns admission after earlier network authority, and cached close-once cleanup. Initial kernel
inspection, raw unknown-expression detection, application-time record replacement, actual empty-generation
deletion/application, namespace execution, privileged registration/application, public policy cutover, legacy `nft`
CLI removal, and L2 qualification remain pending. The final adapter must submit one complete generation through one
`Conn.Flush` call; until then there is no kernel effect or atomicity evidence.

Before any fixed-name deletion, the final adapter must hold the never-unlinked host-global
`/run/mvmctl/authority/firewall.lock` and admit the strict root-owned
`/var/lib/mvmctl/firewall/generation.json` record. The record binds committed/applying state, boot ID, host network
namespace identity, empty state, and exact before/target generation identities. The compiler now derives the canonical
generation digest, but it is not kernel ownership or deletion authority. Applied empty state means both reserved tables
are absent; the compiler's empty output does not delete or apply them. Missing, corrupt, mismatched, partial, or foreign
fixed-name state is never adopted or deleted.

The pinned library exposes rule userdata but no table/chain ownership metadata, and structured `GetRules` can omit
unknown expression types. Root-record-bound exact state can become authority only within an explicitly accepted threat
model for other root/`CAP_NET_ADMIN` actors and an inspection path that detects unknown raw expressions. A fresh
explicitly namespace-bound lasting connection performs one replacement batch; a fresh connection inspects the result.
Cancellation or a flush failure gets one bounded recovery inspection under the same lock and namespace pin, never a
retry on the used connection. Real-kernel isolated-namespace tests must still prove delete/create and late-invalid-batch
atomicity, both-table empty removal, namespace isolation, exact expression inspection, cancellation/close behavior, and
the reviewed 1,000-VM budget before activation.

The final production path must not execute or parse the `nft` CLI, use `libnftnl`, depend on `nftables.service`, or
retain a CLI fallback. Host readiness must probe kernel capabilities directly. A disposable QA runner may use `nft`
only as an independent read-only oracle.

The public policy families are:

- `mvm policy traffic`: TCP/UDP from a source network or exact source VM to an exact destination VM port/range.
- `mvm policy exec`: directional exact source VM to exact destination VM and required non-root target user.

Traffic policy never grants vsock execution. Exec policy never grants IP connectivity. Same-network placement grants
nothing implicitly.

### LoopMount privilege foundation

`internal/service/loopmount` now defines exactly three fixed actions and named client methods: `ProvisionVM`,
`InspectImage`, and `OptimizeImage`. Their closed VM/image requests, plans, and results carry a managed-cache locator,
typed resource identity, admitted size, filesystem, and observed operation fields rather than a rootfs path, command,
shell, mount target, or generic operation list. VM resize is grow-only relative to the admitted rootfs size, no-resize
results must preserve that exact size, and image results are bound to the request's identity, filesystem, and size
semantics. Guest usernames, SSH keys, agent port/token, resource sizes, and result counts have compiled limits.

The private minimal exchange seam accepts an operation result only from a started process with actual response EOF. A
valid EOF-qualified remote `DomainError` wins over later transport diagnostics. Missing, malformed, mismatched,
non-EOF, impossible pre-start, or semantically invalid results use the stable `CodeProcessError` classification with
the action and exact `process_started` / `outcome_unknown` details.

The private rootfs authority pins the canonical cache locator, typed mount identity, fixed VM or image descendant chain,
and exact `rootfs.img` through retained descriptors. It enforces the closed local-filesystem set at the cache root,
no-cross-mount descendant traversal, caller ownership, one-link regular-file type, safe modes, stable identity, exact
bounded size, and action-specific access. It retains the image staging descriptor for future descriptor-relative
replacement, closes the complete chain in checked reverse order, and has deterministic before/after-open replacement
tests for every caller-owned component.

The private inert mount-source policy consumes only receiver-derived block identities and filesystem values, never a
path or device name. It accepts no more than 16 partitions. A raw source is selected only when there are no partitions
and its filesystem exactly matches the request; a partitioned layout requires exactly one matching partition. Invalid,
reused, or duplicate block identities, duplicate or out-of-range partition numbers, raw-plus-partition ambiguity, and
missing or multiple matches fail closed. Implemented receiver discovery enumerates fixed `/sys/class/block` within
65,536 entries and 16 partitions, retains descriptors, proves stable partition membership and block identity, probes
only through fixed `/usr/bin/blkid` with bounded output and accepted exit states, and binds the selected pathless source
to the exact probe. No caller path, device name, raw descriptor, or tool selection crosses the seam.

The private inert loop-device kernel contract fixes `/dev/loop-control` to character-device identity `10:237` and
derives `/dev/loop<N>` only for `N` from `0` through `2^20-1`. The derived device must be a block device with major `7`
and minor exactly `N`. Its `unix.LoopConfig` carries the exact admitted rootfs descriptor with only
`LO_FLAGS_AUTOCLEAR|LO_FLAGS_PARTSCAN`, adding `LO_FLAGS_READ_ONLY` only for read-only access. Every other field remains
zero; direct I/O and legacy setup are excluded. Invalid descriptors, access, indices, or identities fail closed through
the private LoopMount `DomainError` path. A private inert attach/release capability opens only the fixed control and
derived loop leaves with `O_RDWR|O_CLOEXEC|O_NOFOLLOW`, rechecks their identities with `fstat`, obtains candidates via
`LOOP_CTL_GET_FREE`, and retries configure-time `EBUSY` at most 16 times. It applies one atomic `LOOP_CONFIGURE` from the
admitted rootfs descriptor/access and never issues `LOOP_CLR_FD` after configure failure. A successful lease exposes
no path or descriptor accessor and releases through checked, idempotent, cancellation-independent `LOOP_CLR_FD` plus
close, with `AUTOCLEAR` as crash safety. A configured lease remains available for cleanup if control close fails.

The implemented `InspectImage` adapter locks its OS thread, creates a private propagation mount namespace, and
mounts only at fixed `/run/mvmctl/rootfs` with `ro,nosuid,nodev,noexec`. It revalidates the exact source, mountpoint,
filesystem, and backing image; reads only bounded fixed `etc/os-release` or `usr/lib/os-release` leaves; proves the
admitted/statx snapshot stable; and unwinds root, mount, source, partitions, loop, mountpoint, and backing authority in
reverse.
It returns `unchanged` only after complete cleanup/stability and otherwise preserves the primary `DomainError` with
bounded cleanup evidence and `outcome_unknown`. The production provision/inspect server constructs this adapter for its
exact private receiver action; live-kernel qualification and public cutover remain pending.

The private `Server` transaction foundation splits its effects between `rootfsEffects`, which contains exactly
`ProvisionVM` and `InspectImage`, and private `imageOptimizeEffects`, which contains only `OptimizeImage`. Provisioning
and inspection validate the rootfs effect dependency; optimization validates its separate effect dependency; all three
validate the shared rootfs authority and narrow `rootfsResourceLocks.Acquire` lease seam. The coordinator validates the
request and caller before authority acquisition, retains a partially acquired lock lease for checked release, admits
the fixed rootfs, invokes one effect, and validates the request-bound result. The private service adapter now provides
the concrete resource-lock implementation over `authorityfs`. The production provision/inspect server injects it and
registers only the exact VM-provision and image-inspect receiver actions. The optimize seam has no production
implementation or construction, receiver handler, or dispatcher registration.

Every action requires its own effect cleanup capability and unwinds effect cleanup, rootfs authority, then resource
lock with `context.WithoutCancel`. Closed action-specific effect states derive mandatory `rootfs_modified`,
`image_replaced`, `durability_uncertain`, and `outcome_unknown` evidence; missing, invalid, or uncertain states cannot
authorize success. Cleanup details are bounded and preserve the primary `DomainError` identity.

For a requested VM grow, admission accepts only the exact pre-grow size or exact target size. It rejects a third size
before effect invocation and hands the stable actually admitted size to `rootfsEffects.ProvisionVM` through the private
rootfs lease. The implemented adapter distinguishes first execution from a converged replay without reopening a caller
path. `OptimizeImage` remains blocked from registration until durable shrink and replacement replay authority is
implemented.

The implemented private seam split isolates activated private provisioning/inspection actions from deferred
optimization. Exclusive locks use the shared privileged descriptor-safe authority layout at
`/run/mvmctl/authority/<uid>/loopmount/{vm-<32hex>,image-<64hex>}.lock`. They are root-owned mode `0600`, keyed only by
authenticated UID, family, and canonical ID, and never unlinked during the boot. Inspection and future optimization
share the image lock. `internal/lib/authorityfs` now owns the top-level tree and separate closed Jailer and LoopMount
families. The implemented LoopMount family pins the authenticated nonzero UID and fixed `loopmount` directory, admits
only the canonical VM/image keys, verifies existing lock files without repair, and retains independent-resource
concurrency even while another key is contended. Its private service adapter implements only the transaction
coordinator's resource-lock seam, including partial-acquisition cleanup, retryable cancellation/deadline mapping, and
lock-before-parent checked release. Review corrected an initial parent-capability serialization bug before this record
was updated. LoopMount does not import Jailer or duplicate a pathname initializer.

The implemented private `ProvisionVM` adapter uses the private namespace/loop/source foundation plus only fixed host
filesystem grow tools. It consumes admitted capabilities directly and never reopens or passes a caller path,
synthesizes a `/proc` FD path, executes a guest binary or chroot, accepts generic operations, scans or kills PIDs, or
stages beneath `/tmp`. All guest reads and writes remain descriptor-bound beneath the admitted mounted root. Fixed
`/usr/bin/blkid` probing has a five-second cap and the fixed ext-family/Btrfs grow tools have a five-minute cap, with an
earlier caller deadline always winning. Ext4/Btrfs evidence proves convergence from the exact old or target size; the
adapter returns closed modified/durability-uncertain/outcome-unknown evidence.

The implemented provisioning subset is fixed host-side hostname/hosts, DNS, existing-account SSH/sudo, fixed SSH
service, embedded agent, and fstab content. It persists the root-only agent token at `/etc/mvmctl/agent.token` and
enables exactly one admitted init system: systemd through `sysinit.target`, or OpenRC through its default runlevel.
Missing-user creation, unpinned cloud-init copying, deblob/package commands, recursive guest cleanup, guest-code
execution, partition-table growth, shrink, conversion, and replacement remain unsupported. No public cutover may
silently reduce behavior: an explicit scope decision must accept each reduction or keep the old public workflow until
a safe typed replacement exists.

The canonical resource locks, private service adapter, read-only inspection effect, and fixed `ProvisionVM` effect are
now composed behind exact private receiver handlers and dispatcher entries. There is still no live-kernel
qualification, backend/public cutover, optimize action, or legacy-route removal. The current public path still uses
`mvm run provision`, raw wire operations and sudo, including arbitrary unmount/cleanup. Task 13 proceeds in the slices
recorded in `tasks/todo.md`; handler registration alone does not authorize the final marker-only sudo policy before
cutover and legacy removal.

### Daemonless status reconciliation and relaunch parity

`mvm vm ls`, `mvm vm ps`, and `mvm vm inspect` must report observed runtime state rather than replaying a stale SQLite
status. Before status filtering or rendering, the API obtains a bounded batch observation from the root VM authority.
The receiver verifies boot ID and the complete active process identity. Its closed observation outcomes are `running`,
`stopped`, `crashed`, and `unavailable`. An absent/cleaned record or host boot-ID mismatch projects `stopped`; a
same-boot missing, dead, or mismatched active process projects `crashed`; permission, I/O, corrupt-record,
missing-capability, and incomplete inspection outcomes are `unavailable`. Stopped and crashed are successful
observations. Any unavailable item fails the complete list/inspect operation rather than returning partial or stale
rows. The user database may be updated after the authoritative observation, but it never decides whether a privileged
process is live.

An interrupted `launching` record is reconciled before projection. Boot mismatch cleans without signaling. Missing PID
or start mismatch never signals a current task. The exact same task already running the final copied Firecracker may
finish idempotent sealing and commit active only when the optional relay also proves its state-appropriate exact
identity. The exact same task still in Jailer, another transient exec, or a mismatched exec is terminated only through
its verified pidfd, and a present relay only through its separately verified pidfd. Inability to classify either task
returns unavailable and performs no guessed signal or deletion; `console.pid` is never authority.

This is a local metadata operation, not a heartbeat. The implementation must not start one sudo process, open one
control connection, or launch one goroutine per VM. L1 benchmarks cover 1,000 authority records and set a regression
budget from the reviewed baseline. L2 proves correctness after a real host reboot. Guest readiness remains an explicit
probe performed by commands such as `mvm exec` and `mvm ssh`, not by list output.

All relaunch paths use one complete typed relaunch state. Start, reboot, and snapshot restore must load the persisted
vsock relation when present and distinguish a valid absence from a repository failure. The root launch request contains
only the validated CID and derives the fixed runtime UDS leaf. The normal process retains the agent port and token for
later agent client probes. Receiver-owned cleanup removes a stale fixed socket only after process authority proves no
live instance owns it. L1 verifies the generated Firecracker config. L2 runs `mvm exec` after stop/start, reboot, host
reboot plus start, and snapshot restore.

The 2026-08-26 read-only reproduction isolated the current defect: `VMStart` and `VMReboot` omit the `vsock` relation,
while `vmRespawnFirecracker` emits a vsock device only when `VMItem.Vsock` is populated. The restarted Firecracker config
therefore had no vsock entry, its old host UDS remained stale, and the active guest agent had no host endpoint. This
evidence defines the regression test. It is not approval for a best-effort one-line enrichment patch.

## Dependency Order

```text
architecture + Python installed-CLI harness
    |
    v
root VM authority + descriptor-safe root records/locks
    |
    +--> descriptor-pinned user resources --> private mount namespace
    |
    +--> independently trusted release installation
    |
    v
typed Jailer lifecycle actions + remove public legacy Jailer service
    |
    v
root network authority --> per-VM netns/veth/TAP --> direct Go nftables generation
    |                                               |
    |                                               v
    |                                    traffic policy replacement
    |
    +--> exact exec policy + bounded vsock relay
    |
    v
loopmount + host mutation migration --> marker-only sudoers
    |
    v
reconciliation + admission + snapshot/volume/cgroup parity
    |
    v
full CI + Python system-test qualification + documentation signoff
```

The final typed Jailer path is built directly from the safe substrates. The project will not add owner checks to the
legacy public transport merely to remove that transport later.

Independent trusted release installation precedes the final Jailer action and final sudoers rule. Descriptor-safe
resource access precedes every privileged handler that consumes user-owned paths. The marker-only sudoers policy is
installed only after every supported privileged caller has migrated.

## Increment and Commit Contract

Each commit must:

1. implement one complete bounded behavior;
2. add the applicable test before production behavior;
3. leave the repository compiling and existing tests passing;
4. pass focused format, golines, vet, and Go tests;
5. include only its owned files and preserve unrelated dirty work; and
6. receive architect review of actual interfaces, structs, imports, and effects before the next slice.

Documentation and changelog changes land with the architecture or behavior they describe. They distinguish implemented
behavior from pending release blockers. No document may describe an unverified target in the present tense.

## Verification and QA Signoff

Go L0/L1 tests prove pure logic, strict codecs, descriptor behavior, lock races, state transitions, renderers, and
failure propagation. They support but do not replace release qualification.

The Python suite under `tests/system/` is the authority for operator-visible behavior. It must exercise the installed
release-candidate binary through the public CLI:

- stage the candidate outside `/usr/local/bin`;
- run `sudo <candidate> host install-system`;
- run `sudo /usr/local/bin/mvm host init`;
- perform product mutations only through `/usr/local/bin/mvm` as constrained users;
- use runner administrator access only for fixed read-only observations and controlled fault injection; and
- aggregate teardown errors and assert a zero-leak baseline after every mutating topology.

Required staged Python gates:

1. **System installation (T1):** atomic install, ownership/mode/ancestor checks, hash equality, invocation routing,
   self-update refusal, and install failure safety.
2. **VM authority (T2):** record-before-effect, multi-user denial, strict privileged input, process identity, release
   reference, corrupt-state refusal, cleanup, and raw-sudo denial once marker-only policy lands.
3. **Jailer/cgroup/mount (T2/T3):** canonical launch, exact pair, namespace visibility, snapshot/volume parity,
   PID-reuse and kill-stage recovery.
4. **Network/traffic (T2):** netns membership, topology ownership, spoof denial, same/cross default deny, exact allows,
   host restrictions, egress, atomic failure, reboot reconciliation, direct-kernel host readiness, and an independent
   read-only nftables-state oracle.
5. **Exec (T2):** same/cross/no-network behavior, directionality, missing policy, non-root target, source identity,
   timeout/output/frame/concurrency bounds, command redaction, and target cleanup.
6. **Clean release (T2 plus clean host):** clean-install refusal of legacy state, full user journeys, repeated fault
   suite, reboot/reconcile, and zero leaked process, namespace, link, cgroup, mount, socket, or nftables state.

The existing system-test architecture, domain map, fixtures, orchestrator, coverage matrix, and release QA guide must be
updated as these gates land. Directly copying the binary into `/usr/local/bin` is not evidence that the installer works.

## CI Gate

```bash
go mod tidy && git diff --exit-code
test -z "$(gofmt -l .)"
golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/
go generate ./internal/service/agent/...
go vet ./...
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
```

After CI, QA runs the applicable `scripts/run-system-tests.py` domains using the installed release candidate. Final
signoff requires the complete relevant L1/L2/L3 matrix and a clean-host run, not only test collection or mocked commands.

## Explicit Deferrals

- XDG user-state relocation.
- CNI and configurable root-executed network plugin chains.
- Multiple VMM support and speculative VMM-driver abstractions.
- Snapshot hot pools, device-mapper clones, and network pre-allocation.
- Resource batch grouping and root-volume boot.

These features may reuse the v0.3.0 authority and namespace foundations later. They do not justify generic extension
points in this release.

## Human Decisions

The user has approved:

- a clean installation with no backwards-compatibility implementation;
- one root-owned system binary and no helper/daemon;
- removal of raw sudo and the legacy public privileged services;
- mandatory per-VM network namespaces;
- nftables-only enforcement and removal of iptables;
- direct kernel nftables programming through a reviewed, pinned Go module with no production CLI fallback;
- same-network and cross-network default deny;
- typed `traffic` and `exec` policies;
- removal of `allow_remote_exec`;
- deferral of CNI and speculative multi-VMM support; and
- documentation plus Python system-test coverage as release-critical artifacts.
