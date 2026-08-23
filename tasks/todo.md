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
**Status:** In progress
**Dependencies:** Implemented system installer

- [ ] Add an installed-release-candidate fixture that stages the candidate outside `/usr/local/bin`.
- [ ] Invoke `sudo <candidate> host install-system` and then `sudo /usr/local/bin/mvm host init`.
- [ ] Prove canonical path, root ownership, exact mode, safe ancestors, hash equality, and atomic replacement.
- [ ] Prove malformed install invocation, symlink target, partial failure, and system self-update refusal.
- [ ] Register the focused Python domain in the orchestrator and coverage matrix.
- [ ] Run collection and the focused domain on the prepared runner.

The final marker-only sudoers assertion is deferred until Task 14; current sudoers remains transitional.

## Phase 1: Root authority and trusted resources

### Task 3: Complete the early privileged protocol foundation

**Owner:** engineer
**Status:** Foundation implemented; action catalog pending later tasks
**Dependencies:** Task 1

- [x] Reserved marker selected before Cobra and application initialization.
- [x] Unsupported versions fail closed.
- [x] Strict bounded JSON rejects unknown, duplicate, case-fold duplicate, trailing, oversized, and malformed input.
- [x] Caller identity, active `mvm` group, environment, and system executable are verified before dispatch.
- [ ] Keep the action switch closed until each capability has its own typed handler and abuse tests.

### Task 4: Add private root-owned VM instance authority

**Owner:** engineer
**Status:** Ready for implementation
**Dependencies:** Tasks 1 and 3

**Interface:**

- [ ] Implement private `RegisterLaunch(ctx, caller, registration) (*launchLease, error)`.
- [ ] Implement private `LockRegistered(ctx, caller, vmID) (*registeredLease, error)`.
- [ ] Implement private `BeginCleanup(ctx, caller, vmID) (*cleanupLease, error)`.
- [ ] Implement context-first `cleanupLease.Complete(ctx)` and checked lease release.
- [ ] Implement private `LockUnreferencedRelease(ctx, release) (*releaseLease, error)`.
- [ ] Expose no generic storage, callbacks, raw paths, actions, or state strings.

**Authority and lifecycle:**

- [ ] Persist strict versioned records under `/var/lib/mvmctl/instances/<uid>/<vm-id>.json`.
- [ ] Store only owner UID, VM ID, lifecycle, exact release identity, process identity, cgroup path, and cleanup generation.
- [ ] Implement `absent|cleaned -> registered -> cleaning -> cleaned`; failed cleanup remains recoverable.
- [ ] Keep a `cleaned` ownership tombstone; foreign UIDs never acquire the VM ID.
- [ ] Establish the global VM-ID claim while holding the global index lock and scanning pinned numeric UID directories.
- [ ] Reject foreign, duplicate, corrupt, unreadable, unsafe, or inconsistent records without disclosing the owner UID.

**Filesystem and locking:**

- [ ] Use descriptor-relative no-follow traversal and retain required root/record/lock FDs.
- [ ] Verify root ownership, exact file types, and non-writable ancestors.
- [ ] Use exact `0700` managed directories and `0600` record/lock files.
- [ ] Implement the Task 4 lock prefix: release -> index -> VM.
- [ ] Use cancellable nonblocking flock retry; never unlink lock files.
- [ ] Atomically replace records using exclusive temp, bounded write, ownership/mode, fsync, renameat, and directory fsync.
- [ ] Return `record_replaced` / `durability_uncertain` DomainError details for post-rename failure.

**TDD and verification:**

- [ ] RED/GREEN strict codec and pure lifecycle-transition tests.
- [ ] RED/GREEN descriptor store, atomic-write, and context-aware lock tests.
- [ ] RED/GREEN typed authority behavior and release-reference tests.
- [ ] Real concurrent two-UID first-launch race proves exactly one durable owner.
- [ ] Tests cover every unsafe ancestor, symlink level, owner/mode/type mismatch, EEXIST race, partial I/O, fsync/close/
  rename failure, cancellation, stale lock reuse, corrupt record, and temp cleanup.
- [ ] Focused race test, format, golines, vet, and Go tests pass.
- [ ] Architect reviews actual interface, state schema, lock order, imports, and error details before commit.

**Expected files:** fixed root constants; private instance types, codec, descriptor store, lock, authority, and focused tests
under `internal/service/jailer/`. No handler, transport, CLI, API, core-domain, or network changes.

### Task 5: Add descriptor-pinned managed user-resource access

**Owner:** engineer
**Dependencies:** Task 4

- [ ] Open user cache/VM/image/kernel/volume/snapshot resources once with beneath/no-symlink/no-magic-link resolution.
- [ ] Verify caller ownership, file type, fixed basenames, size, and allowed access mode on pinned descriptors.
- [ ] Never validate a path and later reopen it by pathname.
- [ ] Return DomainError partial-state details for close/fsync/cleanup failures.
- [ ] Race tests continuously replace every user-owned ancestor and leaf with symlinks/renames.

### Task 6: Implement independent trusted release authority

**Owner:** engineer
**Dependencies:** Tasks 4 and 5

- [ ] Privileged code constructs the fixed official release/checksum locations from validated version and architecture.
- [ ] Caller-provided archive bytes are not their own authority.
- [ ] Bounded extraction rejects traversal, links, devices, duplicates, unexpected members, and size/count overflow.
- [ ] Exact Firecracker/Jailer bytes and a root manifest install atomically and durably.
- [ ] Referenced release removal/replacement holds the release lease and fails closed on unreadable records.
- [ ] L1 failure injection proves an old complete pair or no pair, never a partial trusted release.

### Task 7: Add private mount and process identity primitives

**Owner:** engineer
**Dependencies:** Tasks 4 and 5

- [ ] Unshare a private mount namespace before launch mounts and mark propagation private.
- [ ] Verify PID, start ticks, cgroup, owner, release, and namespace inode before signal or setns.
- [ ] Use pidfd when available and fail closed on PID reuse.
- [ ] Live snapshot/volume operations enter only the verified VM mount namespace.
- [ ] Cleanup never recursively traverses a tree that may contain a mount.
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

- [ ] Add typed privileged nftables generation/application; caller cannot pass syntax, files, includes, or commands.
- [ ] Install bridge-family anti-spoofing and same-network filtering.
- [ ] Install routed, VM-to-host, established reply, required system service, egress, and NAT rules.
- [ ] Apply complete generations atomically before links become usable.
- [ ] Propagate every initialization, parent-chain, ordering, and batch failure.
- [ ] Remove iptables renderer/repository/tests, backend enum/selector/config, raw sudo rule, and documentation.
- [ ] Host readiness requires nftables bridge-family support.

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
- [ ] Add root-owned global capacity reservation so separate CLI processes and users cannot over-admit the host.
- [ ] Make create/start/stop/remove/reboot/snapshot/restore/volume paths transactionally abortable and retryable.
- [ ] Preserve exact release identity and jail-visible paths across snapshot restore and live hotplug.
- [ ] Harden cgroup readiness, membership, values, and process-aware cleanup.
- [ ] Remove process-local semaphore admission and user-DB-as-root-authority designs.
- [ ] L1/L2 fault injection covers kill at every effect stage, reboot, PID reuse, partial cleanup, and repeated reconcile.

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
- [ ] Run the complete T1/T2/T3 matrix plus clean-host installation and reboot/reconciliation qualification.
- [ ] Update `tests/system/COVERAGE_MATRIX.md` and attach commands/results to the release report.

### Task 18: Run repository CI in exact order

**Owner:** engineer and qa-engineer
**Dependencies:** Tasks 3-17

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
