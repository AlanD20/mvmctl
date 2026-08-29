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
**Status:** Codec and caller process transport implemented; receiver foundation, capability clients, and action catalog
pending later tasks
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
  - [ ] Compose the implemented zero-payload root-origin archive fetch into private end-to-end release installation
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

The completed recovery foundation above covers grammar, metadata, safe fixed-leaf subsets, ordering, and durability.
The reserved-name device/inode rechecks are deliberately still unchecked below and must land with removal before
privileged wiring.

- [ ] Referenced release removal/replacement holds the release lease and fails closed on unreadable records.
- [ ] Complete L1 fault injection for release removal and typed privileged integration. After every reference-scan,
  cancellation, syscall, fsync, and cleanup failure, each canonical slot contains one complete three-file release or is
  absent, never a partial release.

**Ordered remaining Task 6 slices:**

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
- [ ] 3. Compose one private end-to-end install method from source/checksum authority, caller-stream or root-fetch
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
- [ ] 4. Implement only `releaseAuthority.removeInstalled(ctx, slot) (removed bool, err error)`: acquire and retain the
  slot lease before existing-only traversal. Return unchanged for safe store/architecture absence. Within an existing
  architecture, inspect canonical first: if safely absent, recover then return unchanged without reference scan; if
  present, fully admit and identity-bind it before recovery, so corrupt canonical state causes zero recovery mutation.
  Recover only after successful admission, then pass the exact identity to `requireUnreferenced`.
  - [ ] Commit only by moving canonical to a fresh validated reserved name through descriptor-relative
    `renameat2(RENAME_NOREPLACE)` with at most eight total name/rename attempts: the first plus seven retries. Only
    `EEXIST` is retryable; every name-generation, grammar, cancellation, binding, and other rename failure stops.
  - [ ] Make removal committed/close-only immediately after rename. Use uncancelled first parent fsync, best-effort
    admission close and fixed `manifest.json`, `jailer`, `firecracker` unlink, old-directory fsync, reserved-binding
    recheck, `rmdir`, and final parent fsync. Preserve the primary error and exact `release_removed`,
    `durability_uncertain`, and `retired_release_retained` details.
  - [ ] Classify removal-owned rename/fsync/unlink/`rmdir` failures as `CodeBinaryRemoveFailed`/`ClassInternal`.
    Preserve every shared admission/reference `DomainError` code, class, entity, details, and cause through cleanup and
    annotation.
  - [ ] Correct recovery in this same slice: retain every admitted reserved directory device/inode, reopen and recheck
    its exact name immediately before leaf unlink and again before `rmdir`, admit all matching entries before mutation,
    accept only safe subsets of the three fixed leaves, and never delete a replacement name or use `RemoveAll`.
  - [ ] L1 covers absence at every level, corrupt canonical with zero recovery mutation, exact reference identity,
    referenced/corrupt authority, same-filesystem and both binding races, invalid names, success on attempt eight,
    exhaustion after eight `EEXIST` results, immediate non-`EEXIST` failure, cancellation, every syscall/fsync/close/
    unlink/`rmdir` edge, exact error classification, combined metadata, close-only release, and crash/retry. Canonical
    remains a complete three-file release or absent. A future `--force` bypasses only a local untrusted-SQLite warning
    and is never sent to root.
- [ ] 5. Build the receiver foundation over the implemented caller transport and strict wire codec. Require fd 0
  `AF_UNIX`/`SOCK_STREAM`, authenticate `SO_PEERCRED` against the sudo caller, set `CLOEXEC`, read one framed request
  through payload and EOF, write one framed response, and half-close the write side. Then add closed install/remove
  values and handlers with exact action and payload policy. Abuse-test descriptor type, peer mismatch, framing/EOF,
  response half-close, unknown/extra fields, action mismatch, payload on removal, and early rejection. Do not reopen
  resolved caller upload/response-EOF/reap behavior; keep the action catalog closed until capability completion.
- [ ] 6. Add Jailer's private minimal `Exchange`, dedicated fake, named typed install/remove client methods, and exact
  action registrations. Use the existing EOF-qualified response authority and remote-result precedence over
  uploader/exit/reap diagnostics; capability tests confirm those resolved semantics, preserve partial-state details,
  and expose no generic call/decoder, executable/argv/environment, path/URL/checksum, force, or reference-bypass input.
- [ ] 7. Switch the public binary API to root release authority, keep SQLite rows display-only and untrusted, and delete
  the legacy downloader/path-based privileged mutation path without compatibility code. Test projection mismatch and
  local `--force` warning behavior, preserve root error details, and search for every removed legacy route.
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
  directory and individual persistent resources, publish display-only PID mirrors, then release Firecracker only after
  named mounts and relay readiness succeed. The relay receives inherited descriptors and never reopens a caller path or
  retains the VM namespace.
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

- [ ] Persist minimal root-owned network identity and serialize network/VM topology mutations.
- [ ] Acquire the network lock only after the applicable VM launch/registered lease; the full order is
  `release -> global index -> VM -> network`.
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

- [ ] Run a focused compatibility spike against a reviewed, pinned `github.com/google/nftables` version. Prove every
  required bridge/IP/NAT expression, structured inspection, namespace-scoped connections, cancellation/deadlines,
  atomic invalid-batch failure, and a representative 1,000-VM generation within a reviewed budget.
- [ ] Add typed privileged nftables generation/application under `internal/service/firewall/`; caller cannot pass syntax,
  files, includes, commands, or library/netlink types across the privileged wire.
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
- [ ] L1 proves typed compilation, atomic failure, cancellation, reconciliation, and scale. L2 may use the `nft` CLI only
  as an independent read-only oracle and proves the installed product has no production CLI dependency.

### Task 11: Replace service-access policy with typed traffic policies

**Owner:** engineer
**Dependencies:** Task 10

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
**Dependencies:** Tasks 3, 5, and 6

- [ ] Move provisioning/mount operations behind typed descriptor-pinned actions.
- [ ] Remove public `mvm run provision` and arbitrary unmount behavior.
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
