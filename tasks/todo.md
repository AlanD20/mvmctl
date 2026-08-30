# v0.3.0 Release Tasks

This checklist implements `tasks/plan.md`. Go production and L0/L1 work belongs to the `engineer`; Python
`tests/system/` and release verification belongs to the `qa-engineer`; ADRs, plans, and documentation belong to the
`architect`. The architect reviews the actual diff after every implementation slice.

## Phase 0: Preserve and establish the target

### Task 0: Preserve prototype history and unrelated work

**Owner:** architect
**Status:** Complete

- [x] Preserve the prototype base/head and `review/jailer-prototype` reference.
- [x] Move local `main` back to `origin/main` without losing release work.
- [x] Keep all user-modified and untracked files outside release commits.
- [x] Work on `0.3.0-release` without force-pushing or rewriting shared history.

### Task 1: Record the clean-break architecture

**Owner:** architect
**Status:** Complete
**Dependencies:** Task 0

- [x] ADR-0016 defines the root-owned single-binary privileged dispatcher.
- [x] ADR-0005 is marked superseded for v0.3.0 while its transitional implementation remains accurately labeled.
- [x] ADR-0017 defines mandatory per-VM netns, nftables-only enforcement, and typed `traffic` / `exec` policies.
- [x] ADR-0009 and ADR-0014 are marked superseded for v0.3.0.
- [x] CNI and the old optional namespace proposal are marked rejected/superseded.
- [x] The release plan records exact authority interfaces, lifecycle states, lock order, errors, and QA gates.
- [x] Update ADR-0016 and operator-facing planning text to require clean installation rather than legacy adoption.
- [x] Review all architecture diffs against `CONTEXT.md`, `docs/STANDARDS.md`, and existing interfaces.
- [x] Commit only the architecture baseline, plan, and changelog files after diff verification.

### Task 2: Verify the implemented system installer through Python

**Owner:** qa-engineer
**Status:** Complete
**Dependencies:** Implemented system installer

- [x] Add an installed-release-candidate fixture that stages the candidate outside `/usr/local/bin`.
- [x] Invoke `sudo <candidate> host install-system` and then `sudo /usr/local/bin/mvm host init`.
- [x] Prove canonical path, root ownership, exact mode, safe ancestors, hash equality, and atomic replacement.
- [x] Prove malformed install invocation, symlink target, partial failure, and system self-update refusal.
- [x] Register the focused Python domain in the orchestrator and coverage matrix.
- [x] Run collection and the focused domain on the prepared runner.

Qualification evidence: the candidate `0.3.0-rc.1` was installed and exercised only inside disposable nested-virt
runner VMs. The focused `system_install` domain passed all 10 cases on 2026-08-23. The outer host controller remained
byte-for-byte unchanged, and the pre-existing user VM was not modified.

The final marker-only sudoers assertion is deferred until Task 14; current sudoers remains transitional.

## Phase 1: Root authority and trusted resources

### Task 3: Complete the early privileged protocol foundation

**Owner:** engineer
**Status:** Shared codec, caller transport, and fd-0 receiver implemented; trusted-release actions registered;
LoopMount client/protocol, private rootfs authority, canonical resource locks, private lock adapter, and inactive
receiver-transaction foundations implemented without receiver registration; remaining capabilities stay pending
**Dependencies:** Task 1

- [x] Reserved marker selected before Cobra and application initialization.
- [x] Unsupported versions fail closed.
- [x] Strict bounded JSON rejects unknown, duplicate, case-fold duplicate, trailing, oversized, and malformed input.
- [x] Caller identity, active `mvm` group, environment, and system executable are verified before dispatch.
- [x] Add a strict versioned request/response codec with 64 KiB frames, duplicate/unknown/depth rejection, matching
  actions, typed read-only success/error outcomes separated from protocol failures, and bounded `DomainError`
  code/class/entity/partial-state detail preservation.
- [x] Add the fd-0 Unix socketpair control transport with fixed `/usr/bin/sudo -n -- /usr/local/bin/mvm` invocation,
  concurrent upload/response, request and response half-close/actual EOF, CLOEXEC, cancellation, bounded reaping and
  buffers, early-rejection handling, and private process dependencies; do not extend `CommandRunner`, `RunCmdOpts`,
  `SpawnConfig`, or `FakeRunner`.
- [x] Transport failures distinguish pre-start failures from started, outcome-unknown failures; stdout, stderr, and
  generic subprocess text are diagnostic only and never response authority.
- [ ] Give each capability package a private minimal `Exchange` interface and dedicated fake when its first client is
  added. The client must decode only EOF-qualified responses, report malformed or missing final responses as started
  and outcome-unknown, and let a valid typed result win over upload, exit, or bounded-reaping diagnostics.
- [ ] Keep the action switch closed until each capability has its own typed handler and abuse tests.

### Task 4: Add private root-owned VM instance authority

**Owner:** engineer
**Status:** Complete; Tasks 6 and 8 consume the prepared authority lease
**Dependencies:** Tasks 1 and 3

**Interface:**

- [x] Implement private `lockReleaseSlot(ctx, slot) (*releaseSlotLease, error)` before release identity resolution.
- [x] Implement private `releaseSlotLease.registerLaunch(ctx, caller, registration) (*launchLease, error)` with
  ownership transfer only after durable registration.
- [x] Implement private `LockRegistered(ctx, caller, vmID) (*registeredLease, error)`.
- [x] Implement private `BeginCleanup(ctx, caller, vmID) (*cleanupLease, error)`.
- [x] Implement context-first `cleanupLease.Complete(ctx)` and checked lease release.
- [x] Implement private `releaseSlotLease.requireUnreferenced(ctx, release)` for exact-identity removal/replacement.
- [x] Expose no generic storage, callbacks, raw paths, actions, or state strings.

**Authority and lifecycle:**

- [x] Persist strict versioned records under `/var/lib/mvmctl/instances/<uid>/<vm-id>.json`.
- [x] Store only owner UID, VM ID, lifecycle, exact release identity, process identity, cgroup path, and cleanup generation.
- [x] Implement `absent|cleaned -> registered -> cleaning -> cleaned`; failed cleanup remains recoverable.
- [x] Keep a `cleaned` ownership tombstone; foreign UIDs never acquire the VM ID.
- [x] Establish the global VM-ID claim while holding the global index lock and scanning pinned numeric UID directories.
- [x] Reject foreign, duplicate, corrupt, unreadable, unsafe, or inconsistent records without disclosing the owner UID.

**Filesystem and locking:**

- [x] Use descriptor-relative no-follow traversal and retain required root/record/lock FDs.
- [x] Verify root ownership, exact file types, and non-writable ancestors.
- [x] Use exact `0700` managed directories and `0600` record/lock files.
- [x] Implement the Task 4 lock prefix: release -> index -> VM.
- [x] Use cancellable nonblocking flock retry; never unlink lock files.
- [x] Atomically replace records using exclusive temp, bounded write, ownership/mode, fsync, renameat, and directory fsync.
- [x] Return `record_replaced` / `durability_uncertain` DomainError details for post-rename failure.

**TDD and verification:**

- [x] RED/GREEN strict codec and pure lifecycle-transition tests.
- [x] RED/GREEN descriptor store, atomic-write, and context-aware lock tests.
- [x] RED/GREEN typed authority behavior and release-reference tests.
- [x] Real concurrent two-UID first-launch race proves exactly one durable owner.
- [x] Tests cover every unsafe ancestor, symlink level, owner/mode/type mismatch, EEXIST race, partial I/O, fsync/close/
  rename failure, cancellation, stale lock reuse, corrupt record, and temp cleanup.
- [x] Focused race test, format, golines, vet, and Go tests pass.
- [x] Architect reviews actual interface, state schema, lock order, imports, and error details before commit.
- [x] Key the release lock by canonical `(version, architecture)` store slot rather than binary hashes; prove two
  different identities for one slot serialize.
- [x] Prove competing identities serialize on the slot, transfer occurs only after durable registration, pre-transfer
  failures retain a releasable lease, and a mismatched slot writes no record or acquires index/VM locks.
- [ ] Task 6 proves this lease is acquired before root manifest/hash resolution and supplies only the manifest identity.

**Expected files:** fixed root constants; private instance types, codec, descriptor store, lock, authority, and focused tests
under `internal/service/jailer/`. No handler, transport, CLI, API, core-domain, or network changes.

### Task 5: Add descriptor-pinned managed user-resource access

**Owner:** engineer
**Dependencies:** Task 4

- [ ] Preserve custom `MVM_CACHE_DIR` through one typed managed-cache locator; pin it from `/`, verify caller ownership,
  safe components/mode/mount topology, and reject every raw effect path.
  - [x] Add the private canonical locator and descriptor lease: component-wise `openat2`, synchronized ownership/mode/
    identity inspection, a closed local-filesystem policy on the final cache root, unique/legacy/unavailable mount-ID
    typing, identity-checked re-pin, and checked reverse cleanup.
  - [ ] Carry the locator and closed base launch selection through the privileged request, remove the transitional raw
    effect paths, and consume the private base launch-resource lease. The private foundation is not yet a production
    handler.
- [ ] Bind the pinned cache device/inode/mount identity into the root-owned instance record and require later operations
  to re-pin the same identity.
- [ ] Land canonical producer names before descriptor pinning:
  - [x] ID-based `volumes/<volume-id>.<raw|qcow2>` storage.
  - [x] Content-addressed `kernels/<kernel-id>` storage with receiver-owned staging; kernel pull inputs cannot select an
    output directory, name, or path.
  - [x] Freeze persistent VM producer basenames as `rootfs.img`, `cloud-init.iso`, `firecracker.json`,
    `firecracker.log`, `firecracker.console.log`, and optional `firecracker.metrics`; copy custom cloud-init ISO sources
    into the fixed managed leaf, rebuild relaunch paths instead of trusting SQLite path columns, and reject alternate
    manifest basenames in the transitional Jailer receiver.
  - [ ] Make persistent outputs caller-owned `0600` one-link regular files and truncate them only after durable launch
    registration. The current launch still creates/truncates outputs before registration.
  - [x] Freeze API/vsock/console socket and PID mirror basenames and remove their filename configuration/relay flags.
  - [ ] Relocate those ephemeral leaves beneath `/run/mvmctl/runtime/<uid>/<vm-id>`; the root-controlled parent and
    pinned caller-owned VM directory must replace the current whole-cache-VM-directory bind.
  - [x] Freeze snapshot producer/restore leaves as `rootfs.img`, `memory`, and `vmstate`; derive restore and remove paths
    from the validated snapshot ID rather than SQLite file-path columns.
  - [ ] Remove the transitional `phantom-rootfs.img` symlink through the Task 7 namespace overlay and add the managed
    ID-scoped image-provisioning staging subtree.
- [ ] Define operation-specific non-empty/maximum-size and access-mode policies before coding; a caller- or DB-provided
  expected size is an equality check, not authorization for an unbounded resource.
  - [x] Define base VM launch admission: rootfs 128 MiB through 16 TiB with exact bounded size equality, config 1 byte
    through 1 MiB, kernel 1 byte through 1 GiB, and optional cloud-init ISO 1 byte through 256 MiB. All are caller-owned,
    one-link regular files with resource-specific owner access and no group/world write or special bits.
  - [ ] Define snapshot restore memory/state, volume count/format/capacity, writable snapshot creation, persistent output,
    and image-provisioning policies in their owning increments. QCOW2 physical length must not be confused with virtual
    capacity or inspected by an unrestricted root subprocess.
- [x] Implement the private base launch-resource lease for fixed `vms/<vm-id>/rootfs.img`,
  `vms/<vm-id>/firecracker.json`, optional `vms/<vm-id>/cloud-init.iso`, and `kernels/<kernel-id>`: component traversal
  beneath the pinned cache, `NO_XDEV`, synchronized ownership/type/link/mode/size checks, descriptor transfer, reverse
  uncancelled cleanup, primary `DomainError` preservation, and policy-valid continuous replacement races. This lease is
  intentionally not wired to a privileged action or launch.
- [x] Make caller `firecracker.json` admission-only evidence: repeat the 1-byte-through-1-MiB bound before allocation,
  require stable positioned reads and strict semantic equality with root-built typed launch intent, consume the source
  descriptor, and retain only a root-owned `0444` fully sealed memfd with verified readback and checked cleanup.
- [ ] Consume the sealed configuration through Task 7's descriptor-only mount transaction. The private capability has
  no live privileged action or launch consumer yet, and current public launches remain transitional.
- [ ] Extend open-once beneath/no-symlink/no-magic-link traversal to snapshot, volume, output, and image-provisioning
  resources under their separately defined policies.
- [ ] Verify the remaining resource classes' caller ownership, file type, fixed basenames, size/capacity, and allowed
  access mode on pinned descriptors.
- [ ] Ensure every remaining resource consumer uses its admitted descriptor without pathname reopen.
- [ ] Keep future resource leases private to the owning service package, expose no generic FD/path accessor, and release
  retained descriptors in reverse order with checked `context.WithoutCancel` cleanup.
- [ ] Preserve primary `DomainError` code, class, entity, and details for close/fsync/cleanup failures in every remaining
  resource lease.
- [ ] Add policy-valid continuous symlink/rename replacement races for every remaining user-owned ancestor and leaf.
- [ ] Task 7 consumes same-package Jailer leases; Task 13 consumes a separate private loopmount image lease. Neither
  consumer may reconstruct or accept `/proc/self/fd` paths from the request.

### Task 6: Implement independent trusted release authority

**Owner:** engineer
**Dependencies:** Task 4

- [ ] Store exact releases under `/var/lib/mvmctl/binaries/<architecture>/<version>` with fixed Firecracker, Jailer,
  and strict root-owned manifest leaves.
- [ ] Privileged code constructs the fixed official release/checksum locations from validated version and architecture.
  - [x] Freeze the official origin, tag, archive, checksum-sidecar, archive-root, exact Firecracker/Jailer member, and
    trusted-store leaf derivation. No derived value is accepted from the caller.
  - [x] Implement and verify the private typed source derivation for both architectures and canonical prerelease tags;
    reject non-canonical or path-like slots before constructing any source value.
- [ ] Root obtains the checksum independently through a dedicated bounded HTTPS-only source with proxies disabled. It
  may consume an exact-length bounded archive stream supplied by the unprivileged client for mirror efficiency, but it
  accepts no caller path, URL, or checksum and never opens or trusts the user asset mirror as authority.
  - [x] Freeze the one-request timeout, transport, redirect, status, body-size, and exact checksum-sidecar grammar
    policy. The ordinary downloader and its proxy/cache/retry behavior are outside this trust boundary.
  - [x] Implement and verify the private checksum authority and typed archive digest: source-integrity revalidation,
    proxy-free bounded HTTPS over fresh HTTP/1 with HTTP/2 disabled, one closed redirect, exact status/body/grammar
    checks, cancellation, and checked cleanup.
  - [x] Compose the implemented zero-payload root-origin archive fetch into private end-to-end release installation
    without relaxing source, transport, stream, or staging policy.
- [x] Implement the strict bounded gzip/PAX/GNU-tar parser: reject traversal, links, devices, sparse files, duplicates,
  unexpected or missing members, malformed headers and metadata, concatenated/trailing input, and size/count overflow;
  route only exact Firecracker/Jailer bytes through the private selected-writer seam.
  - [x] Validate the parser against all eight mirrored x86_64 release archives.
- [ ] Validate the complete reviewed upstream member allowlist while extracting only Firecracker and Jailer; validate
  their ELF class/machine without executing downloaded code.
  - [x] Freeze the exact audited x86_64 contract for versions 1.10.1, 1.14.2, 1.14.3, 1.14.4, 1.15.0, 1.15.1,
    1.16.0, and 1.16.1: complete order-independent 24-member allowlists, lower/uppercase CPU-template variants, GNU
    local-PAX plus regular-file structure, closed PAX keys, exact modes, one gzip member, 32-MiB decompressed and 8-MiB
    member bounds, and strict tar termination.
  - [ ] Audit an actual aarch64 archive and freeze its architecture-specific member and format contract; source
    derivation and ELF support do not authorize extraction, and x86_64 packaging must not be inferred.
  - [x] Freeze the exact bounded ELF64 header-admission policy for x86_64 and aarch64.
  - [x] Implement and verify the private ELF header validator: exact source identity and 64-byte header length, closed
    identity/type/machine/program-header policy, no untrusted allocation, and no executable loading or execution.
  - [x] Bind ELF admission to the bounded actual file size and reject a truncated declared program-header table.
  - [x] Back the parser's selected-writer seam with concrete anonymous root-owned Firecracker/Jailer staging descriptors;
    extract with positioned writes, bind exact size and full-file SHA-256 to closed ELF admission, apply exact mode
    `0755`, fsync, and verify final identity, metadata, and zero offset without publishing a path.
  - [x] Exercise the complete selected-executable staging, extraction, and finalization path against all eight cached
    audited x86_64 archives.
- [ ] Exact Firecracker/Jailer bytes and a root manifest install atomically and durably.
  - [x] Create or open only the fixed write-side `mvmctl/binaries` store components relative to pinned safe ancestors;
    enforce exact root ownership/mode and fsync newly observed child and parent directories before staging bytes.
  - [x] Create a private anonymous `O_TMPFILE|O_EXCL` archive-stage lease on the pinned trusted-store filesystem with
    exact empty, unlinked, root-owned `0600` metadata and no pathname, memory, or alternate-filesystem fallback.
  - [x] Admit one exact caller-supplied 1-byte through 128-MiB stream into that stage with bounded positioned writes,
    authoritative SHA-256 comparison, exact EOF, fsync, stable metadata, zero offset, and fail-closed poisoning.
  - [x] Freeze the strict schema-v1 manifest contract, 4 KiB record bound, typed SHA-256 fields, and 120-byte through
    64 MiB executable-size policy; the fixed store leaf is `manifest.json`.
  - [x] Implement and verify the private manifest codec and release-identity derivation.
  - [x] Pin and validate the fixed root/architecture/version directory chain without following links or reopening paths.
  - [x] Read `manifest.json` once through the pinned slot; verify exact metadata, bounded bytes, stable descriptor
    identity, strict decoding, and exact slot equality.
  - [x] Pin and verify the fixed `firecracker` and `jailer` leaves through the retained slot descriptor: exact metadata
    and manifest size, bounded positioned full-file hashing, closed ELF admission, pre/post-read stability, unchanged
    offsets, exact descriptor retention, cancellation, and checked reverse cleanup.
  - [x] Compose the release-slot lease, pinned store and version directory, verified manifest identity, and exact
    executable descriptors into one private prepared installed-release lease with strict reverse checked cleanup.
  - [x] Create the architecture/version slot, stage and write the strict manifest, and publish or replace the complete
    three-file release descriptor-relatively and atomically; the anonymous executable stages remain unpublished until
    this transition succeeds.
    - [x] Create and durably verify the fixed architecture directory under the exact active slot lease; admit every
      matching exact-name candidate before deletion, fail closed on unsafe state, and safely remove only fixed leaves
      with cancellation-independent resumable cleanup and ordered directory fsyncs, never recursive fallback.
    - [x] Stage and write the strict manifest anonymously; link it and the finalized anonymous executables into one
      exact root-owned mode-`0700` reserved candidate with production-only `AT_EMPTY_PATH`; fsync each linked file,
      then the candidate and architecture directories; close every writable descriptor; and re-admit the complete
      candidate through the installed-release verifier. Cover discard, recovery, cancellation, and partial-failure
      edges.
    - [x] Require exact three-leaf shared admission before idempotent canonical-manifest comparison; conflict on a
      different complete release and fail closed on unsafe state; publish an absent slot with descriptor-relative
      `renameat2(RENAME_NOREPLACE)` and no fallback; transition to installed before parent fsync and close-only cleanup;
      report `release_installed`/`durability_uncertain`; and cover pre-commit, commit, parent-fsync, close,
      cancellation, and reserved-name binding faults.
    - [x] Implement explicit replacement only after complete old-release admission and unreferenced-identity proof;
      commit with `renameat2(RENAME_EXCHANGE)` and retire only fixed old leaves after the first parent fsync.
    - [x] Complete the replacement/exchange fault matrix for reference-scan, commit, parent-fsync, retirement, and final
      fsync edges; verify one complete old/new release plus exact partial-state details, never a partial installed
      directory.

The completed recovery foundation above covers grammar, metadata, safe fixed-leaf subsets, ordering, durability, and
the reserved-name device/inode rechecks required before leaf cleanup and directory removal.

- [x] Referenced release removal/replacement holds the release lease and fails closed on unreadable records.
- [ ] Complete L1 fault injection for typed privileged integration. Private release removal now proves that after every
  reference-scan, cancellation, syscall, fsync, and cleanup failure, each canonical slot contains one complete
  three-file release or is absent, never a partial release.

**Ordered Task 6 slices:**

- [x] 1. Freeze the zero-payload root-fetch and atomic-removal contracts in ADR-0016 and the release plan. Review the
  documentation diff, paths, links, line length, stale wording, and completed-checkbox set without claiming either
  behavior is implemented or changing the changelog.
- [x] 2. Implement the private zero-payload root-origin archive fetch. Revalidate the receiver-derived source and use
  one retrieval attempt and redirect chain: one initial HTTPS GET plus at most one redirect GET, with no retry. Disable
  HTTP/2 and use fresh HTTP/1 connections because Go's HTTP/2 transport may retry bodyless GETs internally. Use TLS
  1.2+, no proxy/cache/compression/keepalive, at most one live connection, 5-second phase timeouts, 16 KiB headers, and
  a fixed five-minute maximum further bounded by the caller context/deadline. Restrict the redirect to exact no-port
  `release-assets.githubusercontent.com` and reapply only fixed safe headers. Require HTTP 200 and one effective
  validated 1-byte-through-128-MiB `Content-Length` after Go `net/http` processing before stage mutation: rely on
  `net/http` to reject malformed/conflicting duplicates and deduplicate identical duplicates, and reject missing or
  chunked length or any still-exposed multiple values without a raw HTTP parser. Stream once into
  exact-length/digest/EOF/fsync admission with no path, memory, replay, ordinary downloader, or root mirror access.
  Any started receive failure or body-close failure after an otherwise successful receive poisons the stage, leaving
  only checked `Release` valid. Hermetic L1 must cover source, request, redirect, header, status, length, stream,
  timeout, cancellation, digest, stage-state, and body-close edges. Run focused format, golines, vet, tests, and race
  tests.
- [x] 3. Compose one private end-to-end install method from source/checksum authority, caller-stream or root-fetch
  archive admission, strict extraction/finalization, and candidate assembly. Treat `allow replacement` as permission,
  not replace-only: an absent slot installs, an identical complete release remains unchanged, and a differing complete
  release replaces only with permission and exact reference proof. Return a closed `installed`, `replaced`, or
  `unchanged` outcome plus exact fully re-admitted manifest-derived metadata. Precommit error returns a zero result;
  installed/replaced outcome and metadata remain with postcommit error; unchanged and metadata are retained only after
  candidate cleanup completes, while an earlier candidate cleanup failure returns zero and requires retry. A later outer
  slot-lease release failure retains unchanged and metadata alongside the error. Preserve primary `DomainError` and
  commit details through checked reverse cleanup. Keep the manifest as the single metadata source, derive identity when
  needed without redundant storage, and never reopen the root-owned mode-`0700` store from the normal process. L1 covers
  both body modes, both permission values across absent/identical/differing canonical states, exact results, every
  handoff and commit boundary, cancellation, and cleanup fault. Add no transport, CLI, API, path, mirror, or
  legacy-downloader dependency.
- [x] 4. Implement only `releaseAuthority.removeInstalled(ctx, slot) (removed bool, err error)`: acquire and retain the
  slot lease before existing-only traversal. Return unchanged for safe store/architecture absence. Within an existing
  architecture, inspect canonical first: if safely absent, recover then return unchanged without reference scan; if
  present, fully admit and identity-bind it before recovery, so corrupt canonical state causes zero recovery mutation.
  Recover only after successful admission, then pass the exact identity to `requireUnreferenced`.
  - [x] Commit only by moving canonical to a fresh validated reserved name through descriptor-relative
    `renameat2(RENAME_NOREPLACE)` with at most eight total name/rename attempts: the first plus seven retries. Only
    `EEXIST` is retryable; every name-generation, grammar, cancellation, binding, and other rename failure stops.
  - [x] Make removal committed/close-only immediately after rename. Use uncancelled first parent fsync, best-effort
    admission close and fixed `manifest.json`, `jailer`, `firecracker` unlink, old-directory fsync, reserved-binding
    recheck, `rmdir`, and final parent fsync. Preserve the primary error and exact `release_removed`,
    `durability_uncertain`, and `retired_release_retained` details.
  - [x] Classify removal-owned rename/fsync/unlink/`rmdir` failures as `CodeBinaryRemoveFailed`/`ClassInternal`.
    Preserve every shared admission/reference `DomainError` code, class, entity, details, and cause through cleanup and
    annotation.
  - [x] Correct recovery in this same slice: retain every admitted reserved directory device/inode, reopen and recheck
    its exact name immediately before leaf unlink and again before `rmdir`, admit all matching entries before mutation,
    accept only safe subsets of the three fixed leaves, and never delete a replacement name or use `RemoveAll`.
  - [x] L1 covers absence at every level, corrupt canonical with zero recovery mutation, exact reference identity,
    referenced/corrupt authority, same-filesystem and both binding races, invalid names, success on attempt eight,
    exhaustion after eight `EEXIST` results, immediate non-`EEXIST` failure, cancellation, every syscall/fsync/close/
    unlink/`rmdir` edge, exact error classification, combined metadata, close-only release, and crash/retry. Canonical
    remains a complete three-file release or absent. A future `--force` bypasses only a local untrusted-SQLite warning
    and is never sent to root.
- [x] 5. Build the receiver foundation over the implemented caller transport and strict wire codec in two audit-friendly
  substeps. The fixed five-second request-header read deadline, five-minute install archive-payload read deadline, and
  five-second response-write deadline each use an earlier current context deadline when one exists. Final error
  delivery may use `context.WithoutCancel` only with the absolute deadline established before cancellation was detached.
  These limits are non-configurable security policy. Neither substep reopens resolved caller upload/response-EOF/reap
  behavior.
  - [x] 5a. The production entry owns fd 0 directly. Admit it only as a blocking `AF_UNIX`/`SOCK_STREAM`, set and verify
    `CLOEXEC` and require a positive peer PID from `SO_PEERCRED`. Match its UID/GID to the sudo caller and enforce the
    same exact action grammar in caller and receiver. Only structurally admitted channels receive bounded early framed
    errors. Complete the frame before `SHUT_WR`; do not drain a rejected closed action. Use raw
    `SendmsgN(MSG_DONTWAIT|MSG_NOSIGNAL)` with bounded
    absolute-deadline `POLLOUT` waits so a duplicate descriptor's later status-flag mutation cannot bypass the response
    deadline. Remove the obsolete standalone EOF-delimited JSON decoder. L1 faults and real socketpair tests cover
    descriptor identity/type/status, CLOEXEC ordering, peer mismatch, early rejection, deadline/write/half-close,
    process-independent EOF, status mutation, cancellation, and cleanup. Independent audit findings were corrected
    before commit.
  - [x] 5b. Add unregistered closed install/remove request and result values plus directly tested typed handlers.
    - [x] Put exact `jailer-release-install` / `jailer-release-remove` constants and the closed internal `ReleaseSlot`,
      install/remove requests, string outcome, manifest-derived install result, executable metadata, and Boolean remove
      result in `internal/service/jailer`. Carry no path, URL, caller hash, force, reference override, generic action,
      reader field, or decoder hook.
    - [x] Add authority-owning `TrustedReleaseServer`. Named `Install` takes
      `(ctx, request, payload io.Reader, payloadLength uint64)`. Privileged imports Jailer and supplies the private
      reader; the named Jailer client imports system/privilegedwire, never privileged.
    - [x] Zero install/remove require EOF before effect. Nonzero install streams once through existing Jailer
      archive-stage length/EOF admission. Permit staging during read but no extraction/publication before EOF. Add no
      128 MiB buffer, second stage, `LimitReader`, replay, registry, or generic dispatcher.
    - [x] Make each handler own exactly one success-or-`DomainError` response plus `SHUT_WR` and preserve original
      details. Slice 6 must register them without outer `run` double-framing a handler-owned transport error.
    - [x] Replace the unused read-deadline path with raw `Recvmsg(MSG_DONTWAIT)` plus absolute `POLLIN` polling. Start
      the five-second header deadline immediately before decode; after a nonzero install header, start a fresh
      five-minute stream/EOF reader deadline. Each reader carries context/deadline and rechecks cancellable context at a
      compiled maximum 100 ms interval. No individual cancellable poll waits longer than that bound. Pass original
      context to Jailer; the payload deadline does not limit post-EOF work. Zero/remove retain the header deadline for
      EOF. Install accepts at most 128 MiB, remove accepts zero, and fd 0 is never closed.
    - [x] Establish the five-second response deadline immediately before each delivery, not at invocation start. Compute
      the final-error deadline before `context.WithoutCancel`; a long valid effect does not consume the write budget.
    - [x] Before success, validate outcome, exact valid request slot, lowercase 64-character hashes, size policy, and
      nested values. Add invalid fake-effect results; slice 6 repeats semantic validation after decode.
    - [x] L1 covers strict fields/types/null/duplicate/case conflicts, action/magic/schema, truncation/oversize,
      install zero/nonzero/oversize/truncated/trailing, remove payload/trailing, blocked header/payload cancellation,
      status mutation, zero/remove effects after EOF, nonzero publication after EOF, every outcome/remove Boolean,
      invalid effect results, `DomainError` details, response short/error/half-close/no-double-frame, and unknown actions
      remaining no-read after exact trusted-release registration.
- [x] 6. Add Jailer's private minimal `Exchange`, dedicated fake, named typed install/remove client methods, and exact
  action registrations. Distinguish a handler-owned response transport failure from an unhandled action so outer `run`
  never writes a second frame. Use the existing EOF-qualified response authority and remote-result precedence over
  uploader/exit/reap diagnostics; capability tests confirm those resolved semantics, preserve partial-state details,
  and expose no generic call/decoder, executable/argv/environment, path/URL/checksum, force, or reference-bypass input.
- [ ] 7. Switch the public binary API to root release authority, keep SQLite rows display-only and untrusted, and delete
  the legacy downloader/path-based privileged mutation path without compatibility code. Test projection mismatch and
  local `--force` warning behavior. Do not update SQLite after root postcommit error; retry/idempotency obtains root
  truth and repairs projection. Preserve root error details and search for every removed legacy route.
- [ ] 8. Audit a real aarch64 archive and freeze its version/member/format contract before enabling extraction. Record
  evidence and pass mirror-backed parser/staging tests; source and ELF support alone remain fail-closed.
- [ ] 9. Extend the existing registered Python system-test domains and coverage matrix, then qualify the release build
  only inside disposable nested-virt runner VMs with `MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror`. Cover installed-CLI
  install/idempotency/replacement/removal, referenced denial, crash/retry, reboot, cross-user abuse, malformed
  transport, and zero leaked release/runtime resources before the later full CI and release gates.

### Task 7: Add private mount and process identity primitives

**Owner:** engineer
**Foundation dependency:** Task 4
**Full acceptance dependencies:** Tasks 5, 6, and 8

- [ ] Extend the strict instance record once with owner GID, host boot ID, mount-namespace device/inode, and Task 5's
  pinned cache identity; bump the schema version and reject old/partial records.
- [ ] Launch a blocked Firecracker child in a private mount namespace and an optional blocked dropped-caller console
  relay in the host mount namespace; pin their pidfds and the Firecracker namespace FD, persist the complete record
  before externally mutable effects, create/truncate fixed outputs, mark propagation private, mount the pinned runtime
  directory and individual persistent resources, including the sealed root-built Firecracker configuration, publish
  display-only PID mirrors, then release Firecracker only after named mounts and relay readiness succeed. The relay
  receives inherited descriptors and never reopens a caller path or retains the VM namespace.
- [ ] Verify PID, boot ID, start ticks, all UID/GID identities, supplementary groups, exact cgroup, namespace inode,
  process liveness, and pinned executable hash against the Task 6 release manifest before signal or `setns`.
- [ ] Require pidfd support for every mutating operation, signal only with `pidfd_send_signal`, wait through the pidfd,
  and fail host capability admission instead of falling back to raw PID signaling.
- [ ] Reopen and pin `/proc/<pid>/ns/mnt` for each verified live operation; do not persist a mount-namespace handle that
  would retain mounts after process exit.
- [ ] Lock the OS thread for `setns`, pin/restore/verify the original host namespace, and terminate the short-lived
  privileged process if restoration fails rather than returning a contaminated thread to the Go runtime.
- [ ] Live snapshot/volume operations enter only the verified VM mount namespace.
- [ ] Cleanup parses and verifies mount state, reaps the pidfd-verified relay, unmounts only receiver-derived fixed
  leaves, requires cgroup emptiness, unlinks only the five runtime leaves, and removes descriptor-relative allowlisted
  directories bottom-up only when empty; never recursively traverse a caller-writable tree or one containing a mount.
- [ ] L1 fault tests cover source/target replacement, PID mismatch, process exit, setns, mount, unmount, and cleanup.

## Phase 2: Final Jailer and networking paths

### Task 8: Wire typed Jailer lifecycle actions and remove the legacy service

**Owner:** engineer
**Dependencies:** Tasks 4-7

- [ ] Add distinct typed install, launch, abort, cleanup, snapshot exposure, volume exposure/removal, and release removal
  actions to the privileged dispatcher.
- [ ] Preserve existing named Jailer caller methods; replace their transport without adding a generic action method.
- [ ] Persist `registered` authority before the first privileged launch effect and retain the lease through exec.
- [ ] Every post-effect failure invokes verified typed abort and leaves recoverable authority.
- [ ] Remove public `mvm run jailer` and its Cobra/service dispatch.
- [ ] Search finds no ordinary CLI route to a privileged Jailer handler.
- [ ] Go L1 and Python T2 Jailer/authority tests prove cross-UID denial, record-before-effect, exact process identity,
  cgroup/chroot cleanup, and zero leaks.

### Task 9: Add root network authority and mandatory per-VM namespaces

**Owner:** engineer
**Dependencies:** Tasks 4, 7, and 8

- [x] Add the private topology-preparation foundation: derive owner/network and owner/VM names, require a non-forgeable
  held-VM-lock capability before the network lock, retain exact mutation progress, and expose authority only from the
  exact committed state while both leases and all creation markers remain present. Rollback is cancellation-independent;
  a classified lock-acquisition `DomainError` keeps its code/class/entity/details, and failed lock cleanup adds exact
  `cleanup_required` recovery detail without replacing it. No production locker/effect adapter or live-topology handoff
  is wired yet.
- [ ] Persist minimal root-owned network identity and serialize network/VM topology mutations.
- [ ] Acquire the network lock only after the applicable VM launch/registered lease; the full order is
  `release -> global index -> VM -> network -> firewall`.
- [ ] Derive namespace/interface names from UID and immutable IDs; accept no raw name, path, or `ip` argv.
- [ ] Create namespace, local bridge, TAP, and veth pair with links down.
- [ ] Attach only the verified host veth to the verified managed bridge.
- [ ] Pass the pinned root-owned namespace handle to Jailer with `--netns`.
- [ ] Record namespace inode and exact device identities for verification/reconciliation.
- [ ] Typed rollback and cleanup are idempotent and ownership-bound.
- [ ] Python T2 proves every Firecracker process is in its own namespace and no namespace/link leaks after failure.

### Task 10: Replace both firewall backends with one atomic nftables engine

**Owner:** engineer
**Dependencies:** Tasks 3 and 9

- [x] Pin `github.com/google/nftables v0.3.0` and add the private typed compiler for fixed bridge/inet tables and
  chains, raw-priority anti-spoof/unsupported-L3 guards,
  same-network/routed/host-service/default-deny/conditional-egress/NAT rules, generation userdata, and
  overlap/unusable-IPv4 rejection. It performs no kernel or privileged effect.
- [x] Add deterministic input-order coverage and bounded allocation-growth evidence for a 1,000-VM, 1,000-grant
  generation.
- [x] Derive SHA-256 generation identity from the versioned canonical closed projection of normalized intent and exact
  compiled managed tables/chains/rules/order/userdata. The caller supplies no ID; explicit empty input emits no tables
  or rules. Cover all fields of exactly the eight emitted v0.3.0 expression structs, reject typed-nil/unknown
  expressions, stream digest encoding through a fixed 64-KiB buffer with surfaced errors, require schema audit/version
  bump for module or expression changes, and retain the representative 1,000-VM 250,000-allocation regression ceiling.
- [ ] Add the never-unlinked root-owned `/run/mvmctl/authority/firewall.lock` after the network lock and the strict
  committed/applying `/var/lib/mvmctl/firewall/generation.json` record bound to boot ID, host netns identity, empty
  state, and exact before/target generations.
- [ ] Define empty as both reserved tables absent; never create empty tables or delete ambiguous, foreign, partial,
  missing-record, corrupt-record, wrong-boot, wrong-netns, or mismatched fixed-name state.
- [ ] Resolve the v0.3.0 inspection gaps before activation: table/chain values have no ownership userdata and
  structured `GetRules` can omit unknown expressions. Explicitly accept the root/`CAP_NET_ADMIN` threat-model boundary
  for record-bound authority and detect unknown raw expressions instead of silently adopting them.
- [ ] Complete the isolated-netns compatibility spike: prove required expression round-trips, explicit namespace
  isolation, lasting-connection cancellation/checked close, valid delete/create replacement, late-invalid-batch
  atomicity, both-table empty removal, recovery inspection, and representative 1,000-VM runtime within a reviewed
  budget.
- [ ] Add typed privileged nftables application under `internal/service/firewall/`; caller cannot pass syntax, files,
  includes, commands, or library/netlink types across the privileged wire.
- [ ] Keep `github.com/mdlayher/netlink` transitive only; do not add a second low-level adapter or a production CLI
  fallback.
- [ ] Install bridge-family anti-spoofing and same-network filtering.
- [ ] Install routed, VM-to-host, established reply, required system service, egress, and NAT rules.
- [ ] Apply each complete generation with one `Conn.Flush` transaction before links become usable.
- [ ] Propagate every initialization, parent-chain, ordering, and batch failure.
- [ ] Remove iptables renderer/repository/tests, backend enum/selector/config, production `nft` subprocess/parser, raw
  sudo rule, and obsolete documentation; do not require `libnftnl` or `nftables.service` at runtime.
- [ ] Probe required kernel nftables, bridge-family, connection-tracking, and NAT capabilities directly rather than
  using executable lookup, `nft --version`, or parsed CLI diagnostics.
- [ ] Complete L1 with kernel-application atomic failure, cancellation, reconciliation, and runtime scale. L2 may use
  the `nft` CLI only as an independent read-only oracle and prove the installed product has no production CLI
  dependency.

### Task 11: Replace service-access policy with typed traffic policies

**Owner:** engineer
**Dependencies:** Task 10

- [x] Add the typed SQLite allow-intent foundation for source-network and exact-source-VM grants, including immutable
  identity fields, TCP/UDP and port-range constraints, foreign-key cascades, duplicate rejection, and one atomic
  cross-table VM invalidation transaction. This foundation is not yet a public or enforced policy path.
- [ ] Remove `ServiceAccessPolicy`, routed-only CLI/API/schema/compiler, and same-network rejection.
- [ ] Add separate typed source-network and source-VM traffic input/resolution methods under `mvm policy traffic`.
- [ ] Persist immutable source/destination identities, TCP/UDP, and exact port/range only.
- [ ] Compile source-network and exact source-VM policy for same-network bridge and cross-network routed paths.
- [ ] Default-deny all other managed VM-to-VM traffic without breaking replies, required system traffic, or egress.
- [ ] Resource/policy removal applies safe kernel state before releasing identity or IP.
- [ ] Python T2 proves exact allows, wrong source/target/protocol/port denial, same/cross behavior, reboot sync, and IP reuse.

### Task 12: Replace remote-exec flags with typed exec policies

**Owner:** engineer
**Dependencies:** Tasks 4, 8, and 9

- [x] Add the directional exact source-VM, destination-VM, and non-root-user SQLite allow-intent foundation with exact
  authorization lookup, duplicate/foreign-key constraints, and a dedicated authorization failure code. Relay
  authentication, public inputs, and enforcement are not wired yet.
- [ ] Remove `allow_remote_exec` from schema, model, config, CLI, environment specs, results, tests, and docs.
- [ ] Add directional exact source-VM -> target-VM -> non-root-user `mvm policy exec` intent.
- [ ] Refactor the relay so it no longer imports multiple core domains; API resolves shared identities.
- [ ] Authenticate source from the active host session and root-owned instance/vsock identity.
- [ ] Enforce request/frame/output/duration/concurrency bounds and redact full commands from audit logs.
- [ ] Reject root target, VM-to-host execution, missing/reverse policy, foreign owner, stale target, and identity mismatch.
- [ ] Python T2 proves same-network, cross-network, and no-IP-network exec behavior plus all denials and limits.

## Phase 3: Finish the privilege boundary and lifecycle

### Task 13: Migrate loopmount and supported host mutations

**Owner:** engineer
**Status:** Pending — typed LoopMount client/protocol, private rootfs authority, and inactive receiver-transaction
foundations are implemented; the shared authority filesystem, its separate Jailer and LoopMount capability families,
canonical VM/image locks, and the private service lock adapter are implemented and reviewed; mount/effects, `Server`
activation, receiver registration, backend/public cutover, legacy removal, and host-mutation migration remain pending
**Dependencies:** Tasks 3, 5, and 6

- [ ] Move provisioning/mount operations behind typed descriptor-pinned actions.
  - [x] Add exactly three fixed LoopMount actions and named client methods for VM provisioning, image inspection, and
    image optimization, using one private minimal exchange seam and no generic call/decoder/command/path surface.
  - [x] Add closed typed VM/image requests, plans, and results with grow-only VM resize, exact request-bound results,
    closed filesystem/conversion choices, and explicit username, SSH-key, agent-token/port, resource-size, and result
    bounds.
  - [x] Require both process start and actual response EOF for result authority; preserve an EOF-qualified remote
    `DomainError`, and classify every non-authoritative pre/post-start transport outcome with stable `process_started`
    and `outcome_unknown` details.
  - [x] Add private descriptor-pinned VM rootfs and image-staging leases with canonical cache traversal, fixed resource
    selection, caller ownership, stable identity/mode/size admission, local-filesystem and no-cross-mount policy,
    checked reverse cleanup, and deterministic before/after-open replacement races for every caller-owned component.
  - [x] Add the inactive private `Server` transaction coordinator with action-specific effect dependencies and the
    `rootfsResourceLocks.Acquire` lease seam. Validate request/caller before authority; unwind effect, rootfs, then lock
    with `context.WithoutCancel`; derive closed mutation/durability evidence; preserve the primary `DomainError`; and
    require action-specific cleanup authority before success.
  - [x] Admit a VM grow replay only at the exact old or target rootfs size, reject every third size before effects, and
    hand the actually admitted stable size to `rootfsEffects.ProvisionVM` through its private lease.
  - [x] Establish the Jailer portion of the ephemeral authority split: exact `root:root` mode-`0711`
    `/run/mvmctl`, exact `root:root` mode-`0700` `/run/mvmctl/authority`, and Jailer index/release/UID/VM-lock state
    beneath it. Keep `/var/lib/mvmctl/instances` unchanged; verify rather than repair existing managed objects; cover
    modes, symlinks, concurrent creation, creation faults, scans, and lock placement in L1. This creates no runtime
    output tree and activates no public action.
  - [x] Extract the initializer and Jailer locks into the narrow, cycle-free `internal/lib/authorityfs` leaf module.
    Expose only closed Jailer, UID, and lock capabilities with canonical typed keys; share lifecycle state through
    pointers so copied values release exactly once; cache the first checked idempotent release result; preserve children
    and acquired locks across parent release; and migrate Jailer through private error-mapping adapters.
  - [x] Implement exclusive, never-unlinked LoopMount locks beneath the shared layout through a separate closed
    LoopMount `internal/lib/authorityfs` capability family. Use root-owned mode-`0600`
    `loopmount/vm-<32hex>.lock` and `loopmount/image-<64hex>.lock` leaves. Key only by authenticated UID, resource family,
    and canonical ID. Exclude cache locator, sizes, filesystem, action, and plan; inspection and optimization share the
    image lock. Prove descriptor-safe creation/admission and checked idempotent release. LoopMount must not import
    Jailer or create a second pathname initializer. The reviewed implementation also preserves unrelated-resource
    concurrency on one parent capability while another key is contended.
  - [x] Bind the inactive `rootfsResourceLocks` seam through a private service adapter that opens only the authenticated
    UID capability, maps only the closed VM/image selection to canonical keys, retains partial acquisition for cleanup,
    maps cancellation/deadline acquisition failures as retryable, and releases lock before parent authority exactly
    once. No production `Server` constructor, receiver handler, dispatcher entry, backend/public path, or privileged
    mount effect uses this adapter. Network, firewall, and `/run/mvmctl/runtime` consumers remain pending.
  - [x] Split the inactive effect authority so `rootfsEffects` contains exactly `ProvisionVM` and `InspectImage`, while
    private `imageOptimizeEffects` contains only `OptimizeImage`, with dependency validation scoped to each action. The
    optimize seam has no production implementation or construction, receiver handler, or dispatcher registration, and
    the entire `Server` remains inert.
  - [ ] Implement the private mount/loop foundation. Use a private-propagation mount namespace, direct loop, mount,
    unmount, descriptor, sync, and detach syscalls, bounded partition discovery, autoclear, and read-only inspection.
    Permit only fixed host filesystem probes/grow tools; reject caller paths, `/proc` FD passthrough, chroot, guest
    executables, generic operations, PID scan/kill, and `/tmp` workspaces.
  - [ ] Implement read-only `InspectImage` first. Consume the admitted descriptor, share the image lock with future
    optimization, mount `ro,nosuid,nodev,noexec`, read only bounded fixed OS-release leaves, prove byte-for-byte image
    stability, and return only unchanged or outcome-unknown evidence.
  - [ ] Implement fixed `ProvisionVM` content and grow convergence. Support only hostname/hosts, DNS, existing-account
    SSH/sudo, fixed SSH service, embedded-agent, and fstab content. Converge grow from the exact old or target size and
    prove modified, durability-uncertain, and outcome-unknown mappings plus cancellation-independent cleanup.
  - [ ] Add only provision and inspect receiver handlers and exact central-dispatch entries for the already-separated
    `rootfsEffects` seam. `OptimizeImage` remains without a production implementation or construction, handler, or
    registration until durable shrink and replacement replay authority exists.
  - [ ] Before public cutover, obtain an explicit scope decision for every unsupported legacy behavior: missing-user
    creation, unpinned cloud-init copying, deblob/package commands, recursive guest cleanup, guest-code execution,
    partition-table growth, shrink, conversion, and replacement. No cutover may silently reduce public behavior.
  - [ ] After the scope gate, cut the production backend/API to the approved actions and remove public
    `mvm run provision`, raw wire/sudo operations, arbitrary unmount, PID scanning/killing cleanup, and the legacy
    `Provisioner`/`Op` path. Repository searches must prove that no legacy wrapper remains.
- [ ] Move required module/sysctl/host mutations behind exact compiled allowlists.
- [ ] Keep `host init`, `host reset`, group membership, and sudoers changes as explicit administrator operations; do not
  route authorization-boundary changes through the passwordless operator dispatcher.
- [ ] Replace `HostInit`'s `any` / `map[string]any` result with one typed result and one consistent interaction channel.
- [ ] Make administrator host setup/reset fail closed on every mutation and state-write error; remove ignored group,
  sysctl, KVM, firewall, repository, and ownership-repair failures.
- [ ] Dispatch administrator `host init` / `host reset` before ordinary application initialization, with a fixed
  allowlisted environment; never open user SQLite or honor user-controlled cache/config/temp roots first.
- [ ] Make `host clean` and `host reset` fail closed when running-VM discovery, network discovery, teardown, or restore
  fails; never interpret an unreadable inventory as an empty one.
- [ ] Make the init coordinator propagate sudo, dispatch, network-sync, cache, guestfs, binary, and state-write
  failures; success/readiness must require every non-skipped required step.
- [ ] Store the administrator mutation journal in root-owned state and never let user SQLite or a recursive cache
  `chown` authorize or steer a root host mutation.
- [ ] Move any remaining bridge/link/firewall caller to the typed privileged modules.
- [ ] Repository search finds no unauthorized raw sudo callsite.
- [ ] L1 abuse tests cover arbitrary path, PID, mount, module, sysctl, network, and shell/argv attempts.

### Task 14: Install the final marker-only sudoers policy

**Owner:** engineer with qa-engineer L2 verification
**Dependencies:** Tasks 8-13

- [ ] Generate only the exact root-owned versioned privileged dispatcher entry for the `mvm` group.
- [ ] Remove wildcard `/usr/local/bin/mvm *` and raw `ip`, `iptables`, `nft`, `modprobe`, and `sysctl` grants.
- [ ] Validate with `visudo -c` before atomic activation; preserve prior policy on failure.
- [ ] Python system tests prove every removed command and `sudo /usr/local/bin/mvm help` fail while supported ordinary
  unprivileged CLI workflows succeed.

### Task 15: Complete reconciliation, admission, and lifecycle parity

**Owner:** engineer
**Dependencies:** Tasks 8-14

- [ ] Add root-authoritative startup/pre-launch reconciliation for records, processes, cgroups, namespaces, links,
  chroots, mounts, and nftables generations.
- [ ] Reconcile observed runtime status before `mvm vm ls`, `mvm vm ps`, and `mvm vm inspect` filter or render. Use a
  bounded typed root-authority batch, make a host boot-ID mismatch immediately non-live, update SQLite only as a
  `stopped` projection, classify a same-boot missing/mismatched process as `crashed`, and never perform a per-VM sudo,
  Firecracker API, SSH, or guest-agent liveness tick. Change the list API to return an error rather than stale rows when
  authority cannot complete the observation.
- [ ] Benchmark 1,000 authority records in L1 and enforce the reviewed regression budget without one goroutine, process,
  or control connection per VM.
- [ ] Add root-owned global capacity reservation so separate CLI processes and users cannot over-admit the host.
- [ ] Make create/start/stop/remove/reboot/snapshot/restore/volume paths transactionally abortable and retryable.
- [ ] Build one complete typed relaunch state for create/start/reboot/snapshot restore. Preserve a configured vsock CID,
  let root derive the fixed runtime UDS leaf, retain agent port/token only in the normal process, distinguish valid
  absence from lookup failure, and clean an old fixed socket only after verified process absence.
- [ ] Preserve exact release identity and jail-visible paths across snapshot restore and live hotplug.
- [ ] Harden cgroup readiness, membership, values, and process-aware cleanup.
- [ ] Remove process-local semaphore admission and user-DB-as-root-authority designs.
- [ ] L1/L2 fault injection covers kill at every effect stage, reboot, PID reuse, partial cleanup, and repeated reconcile.
- [ ] L1 proves relaunch emits the vsock device and L2 proves `mvm exec` after stop/start, reboot, host reboot plus start,
  and snapshot restore. L2 also proves `ls`/`ps` stop reporting stale running state after a host reboot.

### Task 16: Complete global structured JSON output

**Owner:** engineer
**Dependencies:** Stable final CLI results from Tasks 8-15

- [ ] Add one global structured-output contract for reads and mutations.
- [ ] Remove conflicting per-command rendering branches where the global contract replaces them.
- [ ] Preserve DomainError code/class/details and per-item partial results.
- [ ] Python CLI/invariant tests cover success, validation failure, partial batch failure, and stable machine-readable output.

## Phase 4: Release qualification and documentation

### Task 17: Run staged Python system-test qualification

**Owner:** qa-engineer
**Dependencies:** Applicable implementation task for each domain

- [ ] Extend existing Jailer, cgroup, network, nftables, policy, exec, invariant, and full-journey tests instead of
  replacing them with a parallel harness.
- [ ] Add only the focused installer/authority/netns/fault tests missing from existing domains.
- [ ] Remove direct raw-sudo product mutations from tests; use the installed CLI.
- [ ] Use constrained users with only the product’s intended `mvm` privilege.
- [ ] Fix exact configuration restoration and make all teardown failures visible.
- [ ] Run each relevant domain twice and assert zero leaked resources.
- [x] Require explicit host-direct consent before selecting Tier 3 or `--all`, and validate it before any runner
  mutation; developer-host iterative runs are limited to T1/T2.
- [x] Require an explicit release-qualification mode with exact root-owned `/usr/local/bin/mvm`, safe ancestors, and
  descriptor-pinned candidate/controller version and SHA-256 identity before any host resource preparation.
- [ ] Refuse Tier 3 release evidence when the dedicated outer host has pre-existing active VMs, then verify every
  selected resource belongs to the qualification run before cleanup.
- [ ] Make cleanup and post-domain absence checks part of the domain result; preserve both primary and cleanup failures,
  and never advance tiers while a timed-out worker can still mutate state.
- [x] Reject unknown, duplicate, mixed, zero-domain, and empty-file domain selections before probes or mutation.
- [x] Parse strict bounded pytest outcomes for T1/T2/T3 and require non-empty collection, matching exit status, every
  selected item passed, and zero errors, collection errors, deselections, skips, XFAIL, or XPASS; missing/malformed
  reports and cleanup failures fail the domain while preserving pytest output and the reason.
- [x] Ensure the full matrix selects every required domain exactly once, including `exec`, and does not run
  `test_vm_fresh_env.py` twice under aliases.
- [x] Make mirror validation reject pull errors and HTTP fallback; only a verified local-mirror read is a cache hit.
- [ ] Run the complete T1/T2/T3 matrix plus clean-host installation and reboot/reconciliation qualification.
- [ ] Add the installed-CLI reboot lifecycle journey: stale `running` state is reconciled before list filtering, then
  stop/start and reboot both restore SSH and host-to-guest `mvm exec` without a daemon or leaked vsock socket.
- [ ] Update `tests/system/COVERAGE_MATRIX.md` and attach commands/results to the release report.

### Task 18: Run repository CI in exact order

**Owner:** engineer and qa-engineer
**Dependencies:** Tasks 3-17

- [ ] Add a deterministic CI architecture check that rejects production imports between sibling `internal/core/*`
  domains. The check must distinguish a package importing itself from one domain importing another.
- [ ] `go mod tidy && git diff --exit-code`
- [ ] `test -z "$(gofmt -l .)"`
- [ ] `golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/`
- [ ] `go generate ./internal/service/agent/...`
- [ ] `go vet ./...`
- [ ] `go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic`
- [ ] No critical/high review finding, skipped release gate, hidden teardown error, or unexplained coverage regression.

### Task 19: Reconcile and independently verify all documentation

**Owner:** architect with qa-engineer verification
**Dependencies:** Tasks 17 and 18

- [ ] Update `CONTEXT.md`, standards, ADR links/statuses, README, dependencies, runtime, resource management,
  troubleshooting, improvements, roadmap, system-test architecture, coverage matrix, release QA guide, and changelog.
- [ ] Remove stale iptables, service-access, `allow_remote_exec`, host-global TAP, legacy sudo, migration, state-path,
  cgroup-path, command, and service-count claims.
- [ ] Distinguish implemented behavior, verified limitations, and future work.
- [ ] Architect traces every security/operator claim to final code and test evidence.
- [ ] QA executes every documented install, initialization, policy, recovery, and troubleshooting procedure.
- [ ] Human approves the final diff and release decision before tagging v0.3.0.

## Release completion

The goal is complete only when Tasks 0-19 are satisfied, the final worktree contains no unintended changes, all release
commits are reviewable and working, CI and Python system-test evidence are recorded, documentation matches verified
behavior, and the user approves the release candidate.
