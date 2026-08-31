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
LoopMount client/protocol, private rootfs authority, canonical resource locks, private lock adapter, and exact
provision/inspect receiver actions implemented and registered; remaining capabilities stay pending
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
**Status:** Complete as the private strict schema-3 record/registration foundation; Task 7 now also has private pidfd
activation and cleanup/recovery authority, while production composition and reconciliation remain in Tasks 7-8 and 15
**Dependencies:** Tasks 1 and 3

**Interface:**

- [x] Implement private `lockReleaseSlot(ctx, slot) (*releaseSlotLease, error)` before release identity resolution.
- [x] Implement consuming private `preparedRelease.registerLaunching(ctx, caller, intent) (*launchLease, error)` with
  manifest-derived release identity and ownership transfer only after durable registration.
- [x] Implement private `LockActive(ctx, caller, vmID) (*activeLease, error)` over strict schema-3 authority.
- [x] Implement private `BeginCleanup(ctx, caller, vmID) (*cleanupLease, error)`.
- [x] Implement context-first `cleanupLease.Complete(ctx)` and checked lease release.
- [x] Implement private `releaseSlotLease.requireUnreferenced(ctx, release)` for exact-identity removal/replacement.
- [x] Expose no generic storage, callbacks, raw paths, actions, or state strings.

**Authority and lifecycle:**

- [x] Persist strict schema-3 records under `/var/lib/mvmctl/instances/<uid>/<vm-id>.json`; reject schema 1/2 and every
  unknown, duplicate, case-colliding, missing, trailing, or lifecycle-inconsistent shape.
- [x] Store the exact owner/generation fields, `launching` intent, and state-appropriate receiver-observed `active`
  identity defined by ADR-0016; validate release identity against the manifest-derived hashes.
- [x] Implement pure `absent|cleaned -> launching -> active`, `launching|active -> cleaning -> cleaned`, and idempotent
  `cleaning` transitions; production effects and transition consumers remain pending.
- [x] Keep a `cleaned` ownership tombstone; foreign UIDs never acquire the VM ID.
- [x] Establish the global VM-ID claim while holding the global index lock and scanning pinned numeric UID directories.
- [x] Reject foreign, duplicate, corrupt, unreadable, unsafe, or inconsistent records without disclosing the owner UID.
- [x] Keep the schema-3 foundation private: no production VM lifecycle consumes it, and v0.3 migrates no old record.

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
- [x] Task 6 acquires the slot lease before root manifest/hash resolution; consuming registration supplies only the
  admitted manifest identity and transfers the prepared authority into the launch lease.
- [x] Make prepared-release and launch-lease cleanup pointer-shared, mutex-protected, cancellation-independent, and
  close-once across copies, with the first cleanup result cached. Other composite lifecycle leases remain Task 7 work.

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
- [x] 7. Switch the public binary API to root release authority while keeping SQLite rows display-only and untrusted.
  - [x] **Slice 1:** add architecture to binary rows and stable root-result display IDs; scope active member/default
    uniqueness and list/prefix/type-version/latest/default resolution to architecture; pair Jailer migration rows only
    within the Firecracker architecture; and add atomic exact-slot pair replacement/removal with same-version
    cross-architecture isolation and rollback evidence.
  - [x] **Slice 2:** route public pull/remove/list selection through the typed release client and reconcile the
    projection only from authoritative results. Test projection mismatch and local `--force` warning behavior. Do not
    update SQLite after a root postcommit error; retry/idempotency obtains root truth and repairs projection while
    preserving root error details.
  - [x] **Slice 3:** delete the legacy downloader/source-build/filesystem-removal and path-based Jailer mutation routes
    without compatibility code and search for every removed legacy route.
- [ ] 8. Audit a real aarch64 archive and freeze its version/member/format contract before enabling extraction. Record
  evidence and pass mirror-backed parser/staging tests; source and ELF support alone remain fail-closed.
- [ ] 9. Extend the existing registered Python system-test domains and coverage matrix, then qualify the release build
  only inside disposable nested-virt runner VMs with `MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror`. Cover installed-CLI
  install/idempotency/replacement/removal, referenced denial, crash/retry, reboot, cross-user abuse, malformed
  transport, and zero leaked release/runtime resources before the later full CI and release gates. Rewrite the stale
  binary tests and `scripts/fresh_env.py` calls that still use removed `--version`/`--git-ref` syntax.

### Task 7: Add private mount and process identity primitives

**Owner:** engineer
**Status:** Private blocked-process, pidfd, Jailer-destination, activation, and cleanup/recovery authority implemented;
production child launch/composition, complete effects, privileged actions, reconciliation, and qualification pending
**Foundation dependency:** Task 4
**Full acceptance dependencies:** Tasks 5, 6, and 8

- [ ] Activate the implemented strict schema 3 throughout the production lifecycle while rejecting every old/partial
  record. Persist
  `absent|cleaned -> launching -> active`, `launching|active -> cleaning -> cleaned`, and idempotent `cleaning`.
  `launching` contains owner UID/GID, VM ID, pinned cache identity, exact release identity, host boot ID,
  launch/cleanup generations, the blocked child PID/start locator, and an explicit absent relay or blocked relay
  PID/start locator. `active` adds receiver-observed final credentials, empty supplementary groups, final mount
  namespace, jail root, copied executable, and, when present, the relay's caller credentials, empty groups, host
  namespace/root, and exact root-owned `/usr/local/bin/mvm` executable identity. Activation also
  verifies exact derived cgroup membership and the closed topology capability's network namespace. `cleaned` retains
  only the same-UID tombstone and generations, with no launch-specific GID, cache, release, or process identity.
  - [x] The strict record/codec, lifecycle-shape validation, pure transitions, and manifest release validation are
    implemented. The private process verifier can now commit `active`, but production activation and final live
    cgroup/topology verification keep this item open.
- [ ] Launch a child blocked by an inherited EOF-fails-closed two-way handshake in an initial private mount namespace
  and an optional blocked dropped-caller console relay in the host mount namespace. Open each pidfd, issue a fresh
  parent challenge, and require the still-blocked child to acknowledge it before accepting PID-derived evidence;
  persist `launching` before externally mutable effects, create/truncate fixed outputs, mark propagation private, mount
  the pinned runtime directory and individual persistent resources including the sealed root-built Firecracker
  configuration, and publish display-only PID mirrors. The relay receives only inherited descriptors and never reopens
  a caller path or retains the VM namespace. Its `console.pid` is never authority.
  - [x] Implement the private authority half for already-created process sources: cancellation-aware challenge and
    acknowledgement, EOF-fails-closed release gates, close-on-exec pidfd admission, exact PID/start derivation, optional
    relay separation, pinned receiver-executable verification, and ownership transfer into durable `launching`.
    Production spawning, initial namespace, outputs, mounts, mirrors, and action composition remain pending.
- [x] Make `preparedRelease.registerLaunching` consume its pinned admission/store/slot resources, derive release
  identity internally, and transfer them into the launch lease. Make `activateVerifiedProcess(ctx)` consume only that
  retained prepared release. Pre-rename failure keeps the source lease; post-rename failure returns the non-nil target
  lease with exact `record_replaced` and any `durability_uncertain` evidence.
  - [x] Consuming registration and prepared-to-launch ownership transfer are implemented. Private activation consumes
    only the retained prepared authority, and post-active-rename failures return the active lease for cleanup.
- [ ] Before Jailer execution, prove `/var/lib/mvmctl/jailer/firecracker/<vm-id>` absent/fresh under the retained locks.
  Remove only an exactly admitted non-mounted stale fixed tree after process absence; replacement, unexpected entries,
  mounts, or incomplete evidence fail unavailable. Recheck absence immediately before release.
  - [x] Implement descriptor-pinned exact-ancestor and mount-identity admission for an already-absent destination,
    recheck absence immediately before the blocked child is released, then bind the exact Jailer-created root and copy
    after handoff. Stale-tree admission/removal and production invocation remain pending.
- [ ] Retain VM, network, and firewall leases; create links down; apply and freshly verify the complete atomic nftables
  generation; then raise and reverify links. Release the child only after named mounts, firewall-protected topology,
  and exact relay readiness succeed. Invoke Jailer 1.16.1
  without `--daemonize` or `--new-pid-ns`; the original PID/start/pidfd must survive Jailer and Firecracker exec. Do not
  trust Jailer's PID file or a user-visible PID mirror.
- [ ] Observe `/proc/<pid>/exe` with a bounded context-aware 10-second budget and 25-millisecond minimum interval until
  it is Jailer's copied Firecracker. Retain the original pidfd for stable identity and nonblocking exit detection; it
  does not notify `exec`, and observed exit ends the loop. Then descriptor-relatively require the host jail root
  and `/proc/<pid>/root` to be the same device/inode and the jail `firecracker` leaf to equal `/proc/<pid>/exe`. Seal the
  jail root and copied executable to exact `root:root` mode `0755` beneath exact `root:root` mode-`0700` ancestors, hash
  the copy against Task 6's release manifest, and only then atomically commit `active`. Never compare its inode with the
  trusted-store Firecracker inode.
  - [x] Implement the private bounded verifier with strict `/proc` parsing, descriptor-pinned observations, a 10-second
    budget and 25-millisecond minimum interval, pidfd exit checks, exact root/copy binding, sealing, manifest hashing,
    and active-record replacement. Production topology/cgroup authority and live final verification remain pending.
- [ ] Recheck PID, boot ID, start ticks, all UID/GID identities, the exact empty supplementary-group set, cgroup, final
  namespace/root/executable identity, process liveness, and release binding before signal or `setns`. The network
  namespace identity remains owned by the closed topology capability rather than a raw path/fd field.
  - [x] The private activation verifier repeats these checks and compares the observed network namespace only with an
    owner-bound topology-selected identity retained by the launch lease. Production topology selection, `setns`, and
    live qualification remain pending.
- [ ] Make launch, active, cleanup, release-slot, authority-root, and UID-directory leases pointer-shared,
  mutex-protected, close-once across copies/concurrent release, cancellation-independent during cleanup, and repeat the
  cached first cleanup result.
  - [x] Extend pointer-shared close-once/cached-result ownership to blocked and recovered processes, prepared and bound
    Jailer destinations, active leases, and cleanup leases. Later output, mount, network, and firewall resources keep
    the complete item open.
- [ ] Add consuming `launchLease.Abort(ctx)` and `activeLease.BeginCleanup(ctx)` transitions that transfer held release,
  VM, pidfd, mount, network, and firewall resources into cleanup without unlocking/reacquiring the VM. Keep
  authority-level `BeginCleanup` only for fresh recovery admission.
  - [x] Implement consuming abort/active-cleanup transfer for the currently retained prepared release, VM lock, pidfds,
    Jailer destination, and topology-selected namespace identity. Mount, network, and firewall lease transfer remains
    pending.
- [ ] Require pidfd support for every mutating operation, signal only with `pidfd_send_signal`, observe exit through the
  pidfd, and fail host capability admission instead of falling back to raw PID signaling. Re-admit a present relay by
  exact locator and complete final identity after a crash; never signal an ambiguous or reused PID.
  - [x] The private authority signals only with `pidfd_send_signal`, waits/reaps through the pidfd, and treats `POLLIN`,
    post-reap `POLLHUP`, or both as exited. It retains retry authority on failure. Fresh same-boot cleanup reopens exact
    PID/start locators and requires complete active identities and executable digests; a boot mismatch opens or signals
    no pidfd. Production capability admission and full reconciliation remain pending.
- [ ] Reopen and pin `/proc/<pid>/ns/mnt` for each verified live operation; do not persist a mount-namespace handle that
  would retain mounts after process exit.
- [ ] Lock the OS thread for `setns`, pin/restore/verify the original host namespace, and terminate the short-lived
  privileged process if restoration fails rather than returning a contaminated thread to the Go runtime.
- [ ] Live snapshot/volume operations enter only the verified VM mount namespace.
- [ ] Cleanup parses and verifies mount state, reaps the pidfd-verified relay, unmounts only receiver-derived fixed
  leaves, requires cgroup emptiness, unlinks only the five runtime leaves, and removes descriptor-relative allowlisted
  directories bottom-up only when empty; never recursively traverse a caller-writable tree or one containing a mount.
  - [x] Private cleanup terminates and reaps the optional relay before Firecracker, releases retained process and Jailer
    destination descriptors, and writes `cleaned` only afterward. Wait/reap failure leaves locks, resources, and the
    `cleaning` record available for retry; mount/runtime/cgroup cleanup remains pending.
- [ ] L1 fault tests cover source/target replacement, PID mismatch, process exit, setns, mount, unmount, and cleanup.
  - [x] Focused L1 evidence covers gate cancellation/EOF, pidfd and locator mismatch, liveness/exit/HUP classification,
    bounded executable observation, every identity recheck, active post-rename ownership, full-identity recovery,
    boot-mismatch no-signal behavior, terminate/wait/reap retry, destination substitution, and close-once cleanup.
- [ ] L1 crash-window tests cover pre-record handshake EOF, every `launching` effect stage, partial final-copy sealing,
  post-rename durability uncertainty, `launching -> active`, and repeated `cleaning` recovery.

## Phase 2: Final Jailer and networking paths

### Task 8: Wire typed Jailer lifecycle actions and remove the legacy service

**Owner:** engineer
**Dependencies:** Tasks 4-7

- [ ] Add distinct typed install, launch, abort, cleanup, snapshot exposure, volume exposure/removal, and release removal
  actions to the privileged dispatcher.
- [ ] Preserve existing named Jailer caller methods; replace their transport without adding a generic action method.
- [ ] Persist schema-3 `launching` authority before the first privileged launch effect, retain its release/VM locks and
  original pidfd through Jailer exec, and commit `active` only after the final copy/root/namespace identity is sealed and
  verified.
- [ ] Every post-effect failure invokes verified typed abort and leaves recoverable authority.
- [ ] Remove public `mvm run jailer` and its Cobra/service dispatch.
- [ ] Search finds no ordinary CLI route to a privileged Jailer handler.
- [ ] Go L1 and Python T2 Jailer/authority tests prove cross-UID denial, record-before-effect, exact process identity,
  cgroup/chroot cleanup, and zero leaks.

### Task 9: Add root network authority and mandatory per-VM namespaces

**Owner:** engineer
**Status:** Private namespace target authority plus complete down-only bridge/veth/TAP effects implemented but not
production-composed; route/address, link-up, firewall, Jailer, persistence, public cutover, and qualification remain
pending
**Dependencies:** Tasks 4, 7, and 8

- [x] Add the private topology-preparation foundation: derive owner/network and owner/VM names, require a non-forgeable
  held-VM-lock capability before the network lock, retain exact mutation progress, and expose authority only from the
  exact committed state while both leases and all creation markers remain present. Rollback is cancellation-independent;
  a classified lock-acquisition `DomainError` keeps its code/class/entity/details, and failed lock cleanup adds exact
  `cleanup_required` recovery detail without replacing it. No production locker/effect adapter or live-topology handoff
  is wired yet.
- [x] Implement the strict schema-1 root-owned managed-network record/codec/store and private registration authority.
  Persist only owner, network, current boot, and exact derived bridge identity beneath
  `/var/lib/mvmctl/networks/<uid>`; serialize global claims and each network; preserve post-rename authority evidence;
  and release acquired capabilities in exact reverse order with cached close-once cleanup. The registration entry point
  remains private and unreferenced.
- [x] Add the closed per-VM namespace target capability and private create/remove effect. Create the exact root-owned
  `/run/mvmctl/authority/<uid>/netns/<vm-id>` placeholder, retain its directory/leaf/mount identity, create the network
  namespace on a disposable locked OS thread, publish it with `open_tree`/`move_mount`, verify host-namespace
  restoration, and remove only the exact mount and placeholder with explicit uncertainty evidence.
- [x] Add and compose the private typed link adapter behind the complete `TopologyEffects` boundary. Create and inspect
  the exact namespace-local bridge, host/namespace veth pair, and TAP down; attach only identity-checked endpoints; and
  delete only exact retained identities. Bind each rtnetlink request to an explicit retained namespace descriptor.
- [x] Create TAPs only after entering the retained VM namespace on a disposable locked thread. Open fixed
  `/dev/net/tun`, use exact `TUNSETIFF` TAP/no-PI/exclusive flags and `TUNSETOWNER`, set no group, and reverify owner,
  absent group, alias, ifindex, type, and down/non-persistent state before final persistence. The outer link effect
  reverifies the persistent TAP.
- [ ] Compose the private service/effect in production. No current constructor call, Jailer handoff, public action, or
  live-kernel qualification exists.
- [ ] Transfer the applicable Jailer VM-lock lease into network registration and serialize each topology mutation under
  both VM and network authority. No current network record authorizes a VM topology.
- [ ] Acquire the network lock only after the applicable VM launching/active lease; the full order is
  `release -> global index -> VM -> network -> firewall`.
- [ ] Derive namespace/interface names from UID and immutable IDs; accept no raw name, path, or `ip` argv.
- [x] Create namespace, local bridge, TAP, and veth pair with links down in the private topology service.
- [x] Attach only the verified host veth to the verified managed bridge in the private topology service.
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
- [x] Add the never-unlinked root-owned `/run/mvmctl/authority/firewall.lock` after the network lock and the strict
  committed/applying `/var/lib/mvmctl/firewall/generation.json` record bound to boot ID, host netns identity, empty
  state, and exact before/target generations.
- [x] Add the bounded strict generation codec, descriptor-relative exclusive record store, earlier-network-authority
  acquisition seam, same-boot/host-netns admission, and pointer-shared cached close-once cleanup. This is inert and
  creates no initial record from kernel state.
- [x] Add evidence-bound atomic replacement for an admitted generation record. Re-admit the exact expected value and
  file identity immediately before rename, read back the replacement, retain exact post-rename/durability evidence,
  and block another mutation after an ambiguous committed outcome. This mutates only the record, not nftables.
- [x] Add the inert bounded raw-netlink snapshot transport/collector and strict decoder. It chooses only the exact
  managed tables, fixed chains, rules, sets, objects, and flowtables; brackets collection with generation reads; shares
  one 32-MiB/20,000-message budget across fresh request transports; and rejects unknown expressions, forbidden object
  classes, malformed replies, and generation drift.
- [x] Add the retained exact-host-netns snapshot transport factory after earlier network and generation authority agree.
  Pin and recheck `/proc/self/ns/net`, then create each fresh strict `NETLINK_NETFILTER` connection through that
  descriptor with checked close-once cleanup. No production application/recovery path obtains the factory or invokes
  the collector, and no real-kernel result is qualified.
- [ ] Define empty as both reserved tables absent; never create empty tables or delete ambiguous, foreign, partial,
  missing-record, corrupt-record, wrong-boot, wrong-netns, or mismatched fixed-name state.
- [ ] Resolve the remaining v0.3.0 inspection gates before activation: table/chain values have no ownership userdata.
  Explicitly accept the root/`CAP_NET_ADMIN` threat-model boundary and compose the raw collector and retained factory
  into root-record-bound qualified initial inspection/adoption without silently adopting ambiguous state.
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
**Status:** Public directional exec-policy CRUD, the closed bounded wire, and destination-agent non-root execution are
implemented; source session, API-owned relay, runtime policy/root capacity authority, legacy flag removal, and Python
qualification remain pending, so VM-to-VM execution remains fail-closed
**Dependencies:** Tasks 4, 8, and 9

- [x] Add the directional exact source-VM, destination-VM, and non-root-user SQLite allow-intent foundation with exact
  authorization lookup, duplicate/foreign-key constraints, and a dedicated authorization failure code. Relay
  authentication and enforcement are not wired yet.
- [x] Sever the legacy `remote_vm` authorization bypass. Return one terminal unauthorized response before source
  lookup, request parsing, destination lookup, vsock dial, relay, or command logging while preserving ordinary host
  execution and all copy paths.
- [ ] Remove `allow_remote_exec` from schema, model, config, CLI, environment specs, results, tests, and docs.
- [x] Expose directional exact source-VM -> destination-VM -> non-root-user intent through typed public API methods and
  `mvm policy exec create|ls|rm`. Derive a deterministic ID from exact VM identities and target user, reject same-VM
  and root-target creation, keep resolution independent of network placement, and isolate list/removal to this family.
  This is desired-state CRUD only and does not authorize a runtime relay.
- [x] Add `internal/lib/vmexecwire` with fixed `MVMEXEC1`/version-1 64-KiB framing and strict closed source and
  destination request schemas. Keep the canonical lowercase UUIDv4 agent token host-only in the destination request;
  require a 32-lowercase-hex request identity and non-root target; bound commands at 32 KiB, combined output at 8 MiB,
  and duration at five minutes; accept only `stdout`/`stderr`, one terminal `result`/`error` plus EOF, and matching
  `cancel`. This package performs no policy, relay, or execution effects.
- [ ] Wire the protocol into the closed source-session and API-owned relay listener. `internal/vsockhandler` must not
  resolve multiple core domains; the API resolves shared identities only after root runtime admission.
- [ ] Authenticate source from the active host session and root-owned instance/vsock identity, then resolve and enforce
  the exact directional policy at runtime.
- [x] Add the destination-agent process side for the strict host-built request. Require the configured canonical token
  before effects; resolve the exact target to a nonzero numeric UID, numeric groups, and a clean absolute home; run
  `/bin/sh -c` with explicit credentials and a fixed environment; stream through the wire limits; apply effective
  request/execution/terminal deadlines; kill the process group on timeout/cancel; and redact command, token, and cause.
  This is L1 evidence only and does not claim per-command cgroup cleanup for descendants that leave the process group.
- [ ] Compose that destination capability only behind the authenticated source session and API-owned relay. Reject
  VM-to-host execution, missing/reverse policy, foreign owner, stale target, and every identity mismatch before the
  host constructs the destination request.
- [ ] Enforce the same wire limits at the relay, add Task 15 root-owned concurrent capacity, and add command-redacted
  source/destination/timing/result audit records.
- [ ] Python T2 proves same-network, cross-network, and no-IP-network exec behavior plus all denials and limits.

## Phase 3: Finish the privilege boundary and lifecycle

### Task 13: Migrate loopmount and supported host mutations

**Owner:** engineer
**Status:** Pending — the production provision/inspect server, exact receiver handlers, and central-dispatch entries now
activate the typed private LoopMount effects. Live-kernel qualification, public/backend cutover, legacy removal,
optimization, unsupported-behavior scope decisions, and host-mutation migration remain pending
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
  - [x] Add the private `Server` transaction coordinator with action-specific effect dependencies and the
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
  - [x] Bind the `rootfsResourceLocks` seam through a private service adapter that opens only the authenticated
    UID capability, maps only the closed VM/image selection to canonical keys, retains partial acquisition for cleanup,
    maps cancellation/deadline acquisition failures as retryable, and releases lock before parent authority exactly
    once. The production provision/inspect server uses this adapter; backend/public cutover remains pending.
  - [x] Split the effect authority so `rootfsEffects` contains exactly `ProvisionVM` and `InspectImage`, while
    private `imageOptimizeEffects` contains only `OptimizeImage`, with dependency validation scoped to each action. The
    optimize seam has no production implementation or construction, receiver handler, or dispatcher registration.
  - [x] Implement the private mount/loop foundation. Use a private-propagation mount namespace, direct loop, mount,
    unmount, descriptor, sync, and detach syscalls, bounded partition discovery, autoclear, and read-only inspection.
    Permit only fixed host filesystem probes/grow tools; reject caller paths, `/proc` FD passthrough, chroot, guest
    executables, generic operations, PID scan/kill, and `/tmp` workspaces.
    - [x] Define the inert deterministic mount-source policy over pathless receiver-derived probes. Accept at most 16
      partitions; select raw only with no partitions and an exact requested-filesystem match; otherwise require exactly
      one matching partition. Reject malformed probes, reused or duplicate identities, duplicate partition numbers,
      raw-plus-partition ambiguity, and missing or multiple matches. This implements no discovery, ioctl, mount,
      namespace, effect, or `Server` activation.
    - [x] Define the inert pure loop-device kernel contract: fixed `/dev/loop-control` character-device identity
      `10:237`; derived `/dev/loop<N>` block major `7` with exact 20-bit minor `N`; and an exact admitted-FD
      `unix.LoopConfig` with `AUTOCLEAR|PARTSCAN`, `READ_ONLY` only for read-only access, and every other field zero.
      Exclude legacy setup and direct I/O; fail closed with a private `DomainError`. The pure constructor performs no
      I/O; the private attach/release capability below consumes it.
    - [x] Implement the private inert loop-device attach/release capability. Open only fixed control and derived device
      leaves with `O_RDWR|O_CLOEXEC|O_NOFOLLOW`; recheck exact identities with `fstat`; select candidates through
      `LOOP_CTL_GET_FREE`; and bound configure-time `EBUSY` to 16 attempts. Apply one atomic `LOOP_CONFIGURE` from the
      admitted rootfs descriptor/access, never issue `LOOP_CLR_FD` after configure failure, and return a configured
      lease with a control-close error so cleanup authority is retained. Expose no path or descriptor accessor. Use
      checked, idempotent, cancellation-independent `LOOP_CLR_FD` plus close on release, with `AUTOCLEAR` for crash
      safety.
    - [x] Implement stable bounded partition admission from fixed sysfs and fixed `/usr/bin/blkid` filesystem probing.
      Retain exact block/source capabilities, bind the selected source to pre/post probe identity, bound entries,
      partitions, stdout/stderr, and exit states, and expose no caller path, device name, raw descriptor, or tool
      selection.
  - [x] Implement the read-only `InspectImage` adapter. Consume the admitted capability, share the image lock with
    future optimization, enter a private-propagation mount namespace on a locked OS thread, mount only at fixed
    `/run/mvmctl/rootfs` with `ro,nosuid,nodev,noexec`, revalidate exact source/mount/image identity, read only bounded
    fixed OS-release leaves, prove the admitted/statx snapshot stable, and unwind in checked reverse order. Return only
    unchanged or outcome-unknown evidence while preserving the primary error. The exact private receiver action now
    constructs it; live-kernel QA and public cutover remain pending.
  - [x] Implement the private `ProvisionVM` adapter with descriptor-bound guest reads/writes; fixed
    hostname/hosts, DNS, existing-account SSH/sudo, SSH service, embedded-agent, and fstab content; root-only persistent
    `/etc/mvmctl/agent.token`; and exactly one admitted systemd `sysinit.target` or OpenRC default-runlevel enablement.
    Use only fixed ext-family/Btrfs grow tools, prove ext4/Btrfs convergence from the exact old or target size, cap fixed
    `/usr/bin/blkid` at five seconds and grow tools at five minutes with an earlier caller deadline winning, and preserve
    closed mutation/durability outcomes plus cancellation-independent reverse cleanup. The exact private receiver action
    now constructs it; live-kernel QA and public cutover remain pending.
  - [x] Add only provision and inspect receiver handlers and exact central-dispatch entries for the already-separated
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
  bounded typed root-authority batch with closed `running`, `stopped`, `crashed`, and `unavailable` outcomes. Make an
  absent/cleaned record or host boot-ID mismatch `stopped`, classify a same-boot missing/dead/mismatched active process
  as `crashed`, and treat permission, I/O, corrupt-record, missing-capability, or incomplete inspection as
  `unavailable`. Update SQLite only after a complete authoritative result and never perform a per-VM sudo, Firecracker
  API, SSH, or guest-agent liveness tick. Any unavailable item fails the read instead of returning partial/stale rows.
- [ ] Reconcile interrupted `launching` records exactly: boot mismatch cleans without signaling; PID/start mismatch
  never signals; the same task with the exact final copied Firecracker may complete idempotent sealing and commit
  `active` only when the optional relay proves its state-appropriate identity; the same task still in
  Jailer/transient/mismatched exec is terminated only through its pidfd; a present relay is signaled only through its
  separately verified pidfd. Unavailable identity causes no guessed kill or deletion, and `console.pid` is never
  authority. Resume `cleaning` idempotently.
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

- [x] Add a deterministic CI architecture check that rejects production imports between sibling `internal/core/*`
  domains while allowing same-domain subpackages. It scans non-test Go files, reports stable repository-relative
  violations, has fixture coverage for both outcomes, and runs as `go test ./internal/architecture -count=1` before the
  rest of CI. This marks only the gate implementation; the complete CI run below remains pending.
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
