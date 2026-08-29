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
- Private managed-cache locator and lease foundation with descriptor-relative component traversal, synchronized
  owner/mode/identity checks, an explicit local-filesystem policy, retained descriptors, and identity-checked re-pin.
- Private base VM launch-resource selection and lease for fixed rootfs, Firecracker configuration, kernel, and optional
  cloud-init leaves, with descriptor-relative no-cross-mount opening, synchronized admission checks, descriptor
  transfer, and checked reverse cleanup.
  The locator and base selection are not yet carried by a privileged action, cache identity is not bound into the
  instance record, and the root-side leases are not consumed by launch.
- ADR-0016 for the single-binary privilege boundary.

The action switch is intentionally still empty and the sudoers policy remains transitional. The routed policy, dual
firewall backends, host-global TAP topology, public `mvm run jailer` / `mvm run provision` services, and
`allow_remote_exec` trust mesh must not survive the final release.

## Approved Architecture

### Trust and state

| State | Location | Authority |
|---|---|---|
| User configuration, SQLite DB, and VM artifacts | Existing `MVM_CACHE_DIR` | Untrusted for root authorization |
| Trusted Firecracker/Jailer pairs | `/var/lib/mvmctl/binaries/<architecture>/<version>` | Root-owned persistent |
| VM ownership/lifecycle records | `/var/lib/mvmctl/instances/<uid>` | Root-owned persistent |
| Network ownership/topology records | `/var/lib/mvmctl/networks/<uid>` | Root-owned persistent |
| Global capacity allocations | `/var/lib/mvmctl/allocations` | Root-owned persistent |
| Locks and launch handshakes | `/run/mvmctl/authority/<uid>` | Root-owned `0700` ephemeral; no persistent mount-namespace handles |
| VM sockets and PID mirrors | `/run/mvmctl/runtime/<uid>/<vm-id>` | Root-controlled parent, caller-owned `0700` VM directory; ephemeral and non-authoritative |
| Jailer chroots | `/var/lib/mvmctl/jailer` | Root-owned lifecycle state |
| Cgroups | `/sys/fs/cgroup/mvmctl/<vm-id>` | Kernel runtime state |

Durable user state does not move wholesale to `/run` or `/var/run`. XDG state separation remains a separate project.

### Module and layer rules

- `cmd/mvm/main.go` only recognizes early modes and routes them.
- `internal/service/privileged/` owns the envelope, authenticated caller, executable verification, and fixed action
  switch. It imports no API or core domain and exposes no raw command/argv/effect-path interface. One typed managed-cache
  locator may identify the caller's persistent namespace; every effect below it is selected by typed IDs and fixed
  receiver-derived basenames.
- `internal/lib/privilegedwire/` owns only the strict length-framed request/response codec and bounded `DomainError`
  normalization. `internal/lib/system/` owns the fd-0 socketpair subprocess transport. Neither package exposes a generic
  privileged effect or action dispatcher.
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
caller. The positive peer PID is recorded only for audit and never substitutes for typed receiver authorization.

The concrete transport always runs
`/usr/bin/sudo -n -- /usr/local/bin/mvm __mvm_privileged_v1 <fixed-action>`. It exposes no executable, argv, environment,
cwd, timeout, callback, decoder, or extra-FD option, never uses `PATH`/`os.Executable`, and never bypasses sudo for a root
caller. It remains separate from `CommandRunner`, `RunCmdOpts`, `SpawnConfig`, and `FakeRunner`; capability packages use
private minimal exchanger interfaces and a dedicated fake in tests.

Requests use fixed `MVMREQ01` magic, a 32-bit network-order JSON-header length capped at 64 KiB, a 64-bit network-order
payload length, the strict schema-version-1 header, and the exact optional payload. The header repeats the fixed argv
action and must match it. Only typed release install accepts a payload: zero bytes or at most 128 MiB. The caller
half-closes after the declared bytes. Root requires exact length and EOF before payload-dependent effects and before all
zero-payload effects, but may respond to pre-effect authentication/header failure without draining a claimed payload.
Responses use `MVMRES01`, a 32-bit length capped at 64 KiB, and one strict matching-action success/result or
error/`DomainError` envelope. A delivered envelope is the final effect record even if process reaping later reports an
anomaly; root half-closes its socket write side after the complete frame so response EOF does not depend on process exit.
The codec exposes a read-only decoded success/error outcome separately from protocol failure, so callers cannot confuse a
valid root `DomainError` with an untrusted malformed frame. The transport reports whether actual socket EOF was observed;
in-memory EOF over copied bytes cannot promote a response. A capability client decodes EOF-qualified bytes with a
non-cancelled context and lets a valid success or valid remote error win over upload `EPIPE`, exit, or later reap
diagnostics. No domain effect or fallible outcome check is allowed after the response. Missing, malformed, truncated,
mismatched, oversized, or non-EOF responses from a started process report `process_started: true` and
`outcome_unknown: true`; a pre-start failure reports both false. Stderr and exit text are never parsed as authority.

### VM authority interface

The first implementation slice is private to `internal/service/jailer`. It uses unexported typed values and these
operations:

```go
func (a *instanceAuthority) lockReleaseSlot(
    ctx context.Context,
    slot releaseSlot,
) (*releaseSlotLease, error)

func (l *releaseSlotLease) registerLaunch(
    ctx context.Context,
    caller instanceCaller,
    registration launchRegistration,
) (*launchLease, error)

func (l *releaseSlotLease) requireUnreferenced(
    ctx context.Context,
    release releaseIdentity,
) error

func (a *instanceAuthority) LockRegistered(
    ctx context.Context,
    caller instanceCaller,
    vmID string,
) (*registeredLease, error)

func (a *instanceAuthority) BeginCleanup(
    ctx context.Context,
    caller instanceCaller,
    vmID string,
) (*cleanupLease, error)

func (l *cleanupLease) Complete(ctx context.Context) error

func (l *launchLease) Release(ctx context.Context) error
func (l *registeredLease) Release(ctx context.Context) error
func (l *cleanupLease) Release(ctx context.Context) error
func (l *releaseSlotLease) Release(ctx context.Context) error
```

Each lease retains its pinned directory and lock descriptors and provides a checked context-first release operation.
There is no public `Get`, `Put`, `Delete`, `Run`, raw path, arbitrary state string, or callback interface.

The durable lifecycle is intentionally small:

```text
absent  ── registerLaunch ──> registered
cleaned ── registerLaunch ──> registered
registered ─ BeginCleanup ──> cleaning
cleaning ─── BeginCleanup ──> cleaning
cleaning ───── Complete ─────> cleaned
```

`registered` means launch identity is durable; it does not claim the process is healthy. Process health is derived by
verifying host boot ID, PID, start ticks, all UID/GID identities and supplementary groups, cgroup membership, pinned
mount-namespace identity, and the executable hash against the exact release. Failed cleanup remains `cleaning`.
`cleaned` is a persistent ownership tombstone and may be relaunched only by the same UID.

The global lock order is:

```text
release lock -> global index lock -> VM lock -> network lock
```

Operations may acquire a suffix or subset but never an earlier class while holding a later class. Global VM-ID claims
are established by descriptor-enumerating all numeric UID record directories while holding the index lock. A foreign,
duplicate, corrupt, unreadable, or inconsistent claim fails closed. Lock files are never unlinked.

Release locks are keyed by the canonical store slot `(version, architecture)`, not by caller-observed binary hashes, and
are acquired before root manifest resolution. Reference matching remains against the full release identity. Competing
identities for one store slot must therefore serialize before install, replace, remove, launch, or reference inspection.

Task 6 acquires the release-slot lease before it reads a manifest or computes a launch identity. Its prepared launch
value retains that lease together with the verified manifest and pinned Firecracker/Jailer descriptors. Only that value
may call `releaseSlotLease.registerLaunch`; it supplies the exact release identity from the root-owned manifest rather
than accepting hashes from the normal user process. Registration then acquires the index and VM locks, establishes the
durable record before the first network or launch effect, and transfers the held release lock into the returned launch
lease. A mismatch or registration failure performs no external effect and leaves a checked, releasable slot lease.
Task 4 owns release, index, and VM locking; it does not import network code or predeclare a network implementation.

The mount-namespace launch seam uses a blocked Firecracker child in a private mount namespace and, when enabled, a
blocked console relay running as the dropped caller in the host mount namespace. The parent pins both pidfds and the
Firecracker namespace descriptor, persists the complete launch identity, and only then creates/truncates fixed
persistent outputs and creates the per-VM runtime directory. It enters the Firecracker namespace, marks propagation
private, mounts the runtime directory at `/run/mvm/runtime` and each persistent resource through its pinned descriptor,
publishes display-only PID mirrors, releases and verifies the relay, and releases Firecracker to exec. The relay receives
only inherited PTY, pinned log, and pinned runtime-directory descriptors; it never reopens a caller path or retains the
VM namespace. Here, "first launch effect" means the first externally mutable effect; allocating blocked processes and
the namespace is preparation. Mutating process operations require pidfds and never fall back to `kill(pid)`. Live mount
operations reopen and verify `/proc/<pid>/ns/mnt`; a persistent mount-namespace handle is prohibited because it would
retain the namespace's mounts after process exit.

The persistent VM outputs are fixed caller-owned `0600` one-link regular files: `firecracker.log`,
`firecracker.console.log`, and, when enabled, `firecracker.metrics`. They are truncated once per launch after durable
registration and retained after stop or crash. Direct Firecracker output supports bounded readers and one-launch
retention but not a truthful hard live-size cap; such a cap would require a separately supervised FIFO collector. API,
vsock, and console sockets plus the two PID mirrors are fixed ephemeral leaves beneath
`/run/mvmctl/runtime/<uid>/<vm-id>`. Root retains the directory descriptor through setup and rollback but never treats
its caller-writable contents as authority. The cache VM directory is never mounted wholesale. Cleanup unlinks only the
fixed runtime leaves and removes the directory only when empty; it never recursively deletes caller-writable contents.

Atomic record replacement uses a random exclusive temporary file, bounded writes, root ownership and exact modes, file
`fsync`, checked close, `renameat`, and parent-directory `fsync`. Pre-rename failure preserves the old record.
Post-rename failure returns details that distinguish `record_replaced` and `durability_uncertain`; the caller starts
no VM effects after such an error.

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
caller URL or checksum, and `MVM_ASSET_MIRROR` supplies caller-streamed bytes rather than root authority. Extraction
validates the reviewed complete upstream member allowlist and extracts only Firecracker and Jailer.

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
composition and typed privileged, API, and CLI wiring remain pending.

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
error metadata. This remains a private, unwired substrate. Private end-to-end install composition, the aarch64 audit,
caller/privileged-transport wiring, actual release removal, L2 qualification, and remaining release integration work
are still pending.

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

The trusted-release work proceeds in this order. Steps 1 and 2 are complete; steps 3 through 9 remain an acceptance
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
   The receiver requires fd 0 to be `AF_UNIX`/`SOCK_STREAM`, authenticates `SO_PEERCRED` against the sudo caller, sets
   `CLOEXEC`, reads one framed request through declared payload and EOF, writes one framed response, and half-closes its
   write side. Then add closed install/remove request values and handlers with exact payload policy and action matching.
   Receiver tests cover descriptor type, peer mismatch, framing/EOF, response half-close, unknown or extra fields,
   action mismatch, payload on removal, and early rejection. The already-resolved caller upload, response-EOF, and reap
   behavior is not reopened, and the action catalog remains closed until the release capability is complete.
6. **Add typed client methods and actions.** Give Jailer one private minimal `Exchange` interface, named install/remove
   methods, a dedicated fake, and exact privileged action registrations. The client uses the existing EOF-qualified
   response authority and remote-result precedence over upload/reap diagnostics. Capability tests confirm those resolved
   semantics while preserving partial-state details and exposing no generic privileged call, decoder hook, executable,
   argv, environment, path, URL, checksum, force, or reference-bypass input.
7. **Switch API authority and remove the legacy path.** Treat SQLite binary data only as an untrusted user-facing
   projection, route public install/remove/list selection through the typed release client, and delete the old root
   downloader/path-based binary mutation route rather than preserving compatibility. Acceptance includes
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
down, installs the complete nftables generation and anti-spoofing rules, then brings links up and passes a pinned
namespace handle to Jailer.

The root-side adapter lives in `internal/service/firewall/` and receives only mvmctl-owned typed intent. It uses a
reviewed, pinned `github.com/google/nftables` version to submit one complete generation through one `Conn.Flush` call.
Neither its types nor `github.com/mdlayher/netlink` types cross the privileged wire or enter the network domain. The
latter may remain a transitive dependency only; mvmctl does not maintain a second low-level netlink adapter.

Production does not execute or parse the `nft` CLI, use `libnftnl`, depend on `nftables.service`, or retain a CLI
fallback. Host readiness probes kernel capabilities directly. The `nft` CLI may be present in disposable QA runners as
an independent read-only state oracle, never as a production dependency or sudo target. Before integration, a focused
compatibility spike must prove every expression and family required by ADR-0017, atomic invalid-batch failure,
namespace-scoped connections, structured inspection, cancellation/deadline behavior, and a representative 1,000-VM
generation within a reviewed performance budget.

The public policy families are:

- `mvm policy traffic`: TCP/UDP from a source network or exact source VM to an exact destination VM port/range.
- `mvm policy exec`: directional exact source VM to exact destination VM and required non-root target user.

Traffic policy never grants vsock execution. Exec policy never grants IP connectivity. Same-network placement grants
nothing implicitly.

### Daemonless status reconciliation and relaunch parity

`mvm vm ls`, `mvm vm ps`, and `mvm vm inspect` must report observed runtime state rather than replaying a stale SQLite
status. Before status filtering or rendering, the API obtains a bounded batch observation from the root VM authority.
The receiver verifies boot ID and the complete recorded process identity. A host reboot therefore classifies every old
process identity as non-live without waiting for a VM, Firecracker API, SSH, or agent timeout. The user database may be
updated after the authoritative observation, but it never decides whether a privileged process is live. A boot-ID
mismatch projects `stopped`; a missing or mismatched process in the current boot projects `crashed`. The list API must
return an error when authority cannot complete the observation, not silently return stale rows.

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
