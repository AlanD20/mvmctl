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
**Status:** Codec and transport implemented; capability clients and action catalog pending later tasks
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
- [ ] Root obtains the checksum independently through a dedicated bounded HTTPS-only source with proxies disabled. It
  may consume an exact-length bounded archive stream supplied by the unprivileged client for mirror efficiency, but it
  accepts no caller path, URL, or checksum and never opens or trusts the user asset mirror as authority.
- [ ] Bounded extraction rejects traversal, links, devices, duplicates, unexpected members, and size/count overflow.
- [ ] Validate the complete reviewed upstream member allowlist while extracting only Firecracker and Jailer; validate
  their ELF class/machine without executing downloaded code.
- [ ] Exact Firecracker/Jailer bytes and a root manifest install atomically and durably.
- [ ] Referenced release removal/replacement holds the release lease and fails closed on unreadable records.
- [ ] L1 failure injection proves an old complete pair or no pair, never a partial trusted release.

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
