# Implementation Plan: Safe Jailer and Privileged-Operations Architecture

**Status:** Approved — implementation in progress
**Review range:** `f8ffa28b9d74cac2c9e15fa07a18389d51a3263e..ee733232a2dc23c1edb372022a2add71af36f44c`
**Release decision:** Do not push, merge, or describe the current Jailer implementation as production-ready.

## Overview

Replace the user-writable `mvm` sudo target with one root-owned system installation and an early privileged dispatcher;
make every privileged operation typed, owner-bound, descriptor-relative, and race-safe; then repair Jailer lifecycle,
snapshot, hotplug, cgroup, and firewall
semantics. The ordinary CLI continues to run as an unprivileged user. Persistent user state remains in the existing
per-user location for this change. Only trusted releases, privileged ownership records, chroots, cgroups, and ephemeral
locks live in system locations.

The last five commits should be treated as a prototype branch. Remediation should land as reviewable commits on top of
the current work. History may be curated only after the implementation and L2 verification pass, and only after the
existing dirty worktree has been preserved.

## Scope

### Required before release

- Replace every passwordless-sudo entry that can execute user-controlled code or arbitrary system-tool arguments.
- Establish an independent trust anchor for Firecracker/Jailer release installation.
- Remove path validation followed by privileged pathname reopen.
- Give each privileged VM operation an authenticated owner, exact release, process identity, and per-VM lock.
- Put launch mounts in a private namespace and post-launch mounts in the running VM's verified mount namespace.
- Make create, start, stop, remove, snapshot, restore, and volume hotplug crash-consistent.
- Make routed-policy reconciliation fail closed, including resource deletion and IP reuse.
- Execute the prepared L2 tests plus adversarial privilege, race, and fault-injection coverage.
- Rewrite documentation only after the final behavior passes the release gates.

### Explicitly separate

- Moving the user database and durable user state out of `~/.cache/mvmctl` is desirable but is not part of this
  privilege remediation. It requires a separate ADR, compatibility migration, and rollback plan.
- PID and lock files may move to `/run`; the canonical database, images, snapshots, and VM data must not.
- Network namespaces remain a separate feature unless required to make the corrected Jailer lifecycle work.

## Threat Model

### Principals

- A member of the `mvm` group may manage only resources authorized for that Unix UID.
- An `mvm` group member is untrusted from the privileged dispatcher's perspective and must not gain arbitrary root
  execution.
- The normal `mvm` executable, its environment, stdin, arguments, cache, manifests, and database are user-controlled.
- The root-owned system binary, its privileged dispatcher, directory chain, host policy, release records, and privileged
  instance records are trusted.
- Downloaded release archives are untrusted until verified inside the privileged boundary.

### Protected assets

- Host root filesystem, processes, mount namespaces, networking, kernel modules, sysctls, and cgroup hierarchy.
- Other users' VM data and privileged instance records.
- Exact Firecracker/Jailer release identity and installed bytes.
- Default-deny firewall behavior and IP-identity binding.
- Recoverability after process failure, CLI interruption, or host reboot.

### Security invariants

1. The normal CLI never becomes the passwordless root executable.
2. Sudo permits only the early privileged mode of root-owned `/usr/local/bin/mvm`; raw `ip`, `nft`, `iptables`,
   `modprobe`, and `sysctl` access is removed.
3. The privileged dispatcher exposes named, typed operations. It never accepts a generic command, arbitrary argv, raw
   cgroup keys, or an unchecked host path.
4. Every privileged request is bound to `SUDO_UID`, a root-owned instance record, and a fixed VM/release identity.
5. A user-controlled digest is never a trust anchor. The privileged dispatcher obtains expected release integrity data from
   root-owned policy or a fixed, independently fetched official source.
6. User paths are opened once with descriptor-relative, no-symlink resolution. Privileged code operates on pinned
   descriptors and never validates a path and later reopens it.
7. PID actions use a verified PID plus start time and cgroup membership, preferably a pidfd. PID reuse must fail closed.
8. Launch and cleanup are serialized by a root-owned per-VM lock. Cleanup never recursively traverses a possible mount.
9. All post-spawn failures terminate the registered process before deleting DB, VM, jail, or cgroup state.
10. Removing policy intent or a resource cannot make its IP reusable until the deny/allow kernel state is reconciled.

## Target Architecture

```text
one root-owned /usr/local/bin/mvm
    |
    |-- normal CLI mode runs unprivileged
    |
    | typed internal request through sudo
    v
early privileged dispatcher          before app.Initialize/Cobra/user config
    |
    +-- release operations ---------- fixed origin/root policy, bounded archive parser
    +-- VM launch/lifecycle --------- owner record, pidfd/start time, per-VM lock
    +-- mount operations ------------ openat2 dirfds, private/setns mount namespace
    +-- network/firewall operations -- typed models, atomic backend batches
    +-- host operations ------------- named module/sysctl actions only
    |
    +-- /var/lib/mvmctl ------------- trusted releases, instance ownership, chroot dirs
    +-- /run/mvmctl ----------------- ephemeral locks/handshakes
    +-- /sys/fs/cgroup/mvmctl ------- kernel-enforced per-VM limits

~/.cache/mvmctl                       existing unprivileged DB and VM artifacts
```

The privileged mode is compiled into the same `mvm` artifact but is dispatched directly from `cmd/mvm/main.go` before
`app.Initialize()`, Cobra, the user database, configuration, or plugins are loaded. It has a deliberately small import
graph and cannot dispatch back into ordinary `mvm` commands. It verifies at startup that the executable and directory
chain are root-owned and not group/world writable. Upgrading `/usr/local/bin/mvm` requires an explicit administrator
action; a user-level update cannot replace it.

## State Placement

| State | Location for this remediation | Ownership and lifetime |
|---|---|---|
| User SQLite DB, images, kernels, volumes, snapshots, VM directories | Existing `MVM_CACHE_DIR`, normally `~/.cache/mvmctl` | User-owned, persistent |
| Trusted Firecracker/Jailer releases and release manifest | `/var/lib/mvmctl/binaries/...` | Root-owned, persistent |
| Privileged VM ownership/release records | `/var/lib/mvmctl/instances/<uid>/<vm-id>.json` | Root-owned, persistent, minimal metadata |
| Jailer chroot directories | `/var/lib/mvmctl/jailer/...` | Root-owned lifecycle state |
| Per-VM locks and launch handshakes | `/run/mvmctl/<uid>/...` | Root-owned, ephemeral |
| Cgroup state | `/sys/fs/cgroup/mvmctl/<vm-id>` | Kernel state, runtime only |
| `/run/mvm` | Inside the jail only | Bind-mounted jail-visible view, not host canonical state |

The privileged instance record contains only the owning UID, VM ID, exact release hashes/IDs, process identity, and
cleanup generation. It is not a second copy of the user VM specification.

## Key Design Decisions

### Root-owned single binary and early privileged dispatcher

Install the release artifact through an explicit administrative bootstrap to `/usr/local/bin/mvm` as `root:root`, mode
`0755`. Normal users execute that same file without sudo. The sudoers drop-in permits only its internal privileged entry
point, which is selected before normal initialization. Each privileged operation has its own typed request and
validation. Unknown actions, extra fields, and unsupported protocol versions fail closed.

The code continues to follow the existing layer and file split. `cmd/mvm/main.go` performs only the early branch;
`internal/service/privileged/` owns the envelope, identity checks, and fixed dispatch; Jailer, loopmount, network, and
firewall effects remain in capability-specific `internal/service/{name}/` packages; shared values live in
`internal/lib/model/`; domain logic remains in `internal/core/{domain}/`; and `pkg/api/` remains the sole cross-core
orchestrator. No generic privileged backend or raw command method is introduced.

The final state removes the existing raw privileged binaries from sudoers. Merely replacing `mvm *` is insufficient:
several allowed system tools can execute or materially alter arbitrary host state when given attacker-selected arguments.

### Release trust

The normal CLI may download the archive for progress and caching, but the privileged dispatcher must independently
obtain the expected
digest from a fixed official URL that it constructs from validated version/architecture, or from root-owned signed
metadata. It must not accept both an archive and its authority from the caller.

Installation uses a root-owned temporary directory, a bounded tar reader, exact member allowlists, and rejection of
links, devices, traversal, duplicate entries, oversized payloads, and unexpected files. After verification, root-side code
atomically renames and fsyncs the exact pair plus a root-owned release manifest. A forced replacement of a referenced
release is prohibited.

If Firecracker provides a stable verifiable signature/provenance mechanism, prefer it. Otherwise the initial model may
trust the fixed HTTPS release origin and its independently fetched checksum, with that limitation recorded explicitly.

### Safe path and mount handling

Use `openat2`/`openat` through `golang.org/x/sys/unix` with `RESOLVE_BENEATH`, `RESOLVE_NO_SYMLINKS`, and
`RESOLVE_NO_MAGICLINKS`, followed by `fstat` owner/type/mode checks. Keep directory descriptors alive through the
operation. Fixed basenames are derived from validated IDs; no privileged write uses a caller-selected basename.

The privileged launch path first creates a private mount namespace and marks it private, then creates all pre-launch bind mounts
and execs Jailer. These mounts do not leak into the host namespace and disappear when the VM namespace dies. Live
snapshot/volume operations pin their source descriptor, verify the registered Firecracker process, enter its mount
namespace in a short-lived privileged child, and mount onto a descriptor-pinned target. Mounts should use `nodev`, `nosuid`,
and `noexec` whenever the resource permits.

### Process and cleanup lifecycle

Create a root-owned instance record before the first privileged side effect. Record PID, start time, cgroup path, exact
release, and cleanup generation before reporting launch success. Use pidfds where supported and always revalidate start
time plus cgroup membership.

Every create/restore path persists a provisional user DB row before spawn. After `Spawn`, a lifecycle handle is retained
until the DB reports `running`. Any later failure calls a typed `AbortLaunch`: TERM, bounded wait, KILL of the verified
process if necessary, cgroup-empty verification, namespace/chroot cleanup, and instance-record finalization.

Cleanup is per-VM locked and fail closed. It never calls `RemoveAll` while mounts may exist. Startup and pre-launch
reconciliation inspect root-owned instance records, registered PIDs, cgroups, and chroot directories so interrupted
operations can be recovered without trusting the user database.

### Exact binary pair

Keep the current binary-domain shape but add typed pair operations rather than generic repository hooks:

- `UpsertTrustedPair(ctx, firecracker, jailer)` in one DB transaction.
- A uniqueness invariant for active `(type, version, architecture)` records.
- `jailer_binary_id` on snapshots and all restored/migrated VMs.
- Root-owned release hashes checked against both user DB records before every launch.
- No ignored repository errors during pair install, default selection, or removal.

### Firewall policy reconciliation

Make chain/jump initialization return a result and include it in reconciliation. Build one complete desired managed
ruleset and apply it atomically through `iptables-restore` or an nftables batch. Only after the kernel update succeeds may
intent/derived rows be committed or resource identity/IP be released. If the DB commit fails, restore the prior kernel
generation and return a joined error. Startup reconciliation recompiles from authoritative intent.

## Dependency and Implementation Order

```text
ADR + threat contract
    |
    v
root-owned system install + early privileged protocol
    |
    +--> Jailer/release operations --> fd-safe mounts --> lifecycle/recovery --> snapshot/hotplug
    |
    +--> loopmount/network/firewall/host privileged migration --> remove raw sudoers entries
    |
    +--> exact-pair DB migration
    |
    +--> fail-closed policy reconciliation
    v
L1 negative/fault tests --> L2 real-host matrix --> documentation sweep --> release
```

See `tasks/todo.md` for task-level acceptance criteria, ownership, dependencies, and verification.

## Checkpoints

### Checkpoint A — architecture approved

- ADR defines the privileged-dispatch TCB, ownership model, release trust, state layout, and migration.
- Privileged operations are enumerated; no generic execution extension exists.
- Threat cases become named tests before production code changes.
- Human approves the plan and ADR.

### Checkpoint B — no user-controlled root entry point

- Sudoers contains only the internal privileged entry point of root-owned `/usr/local/bin/mvm`.
- Privileged mode cannot invoke the general CLI or raw caller-selected commands.
- Jailer, loopmount, network/firewall, and host operations use typed privileged endpoints.
- Self-authored archives/checksums and mutable-binary replacement tests fail safely.

### Checkpoint C — lifecycle parity

- Create/start/stop/reboot/remove, live attach/detach, snapshot create, and snapshot restore pass L1 path assertions.
- Injected failures after every spawn stage leave no live registered PID, mount, cgroup, or orphaned ownership record.
- Legacy VMs/snapshots migrate or fail with an actionable error; exact pair identity is persistent.

### Checkpoint D — real-host release gate

- Full repository CI passes in the documented order.
- L2 Jailer/cgroup/policy tests pass on the runner VM.
- Multi-user, symlink-race, PID-reuse, kill-at-stage, reboot-recovery, and IP-reuse tests pass.
- VMM memory headroom and any I/O envelope are measured and documented.
- The final documentation audit contains no stale state, sudo, cgroup, service-count, or path claims.

## Verification Strategy

### L0/L1

- Parser/authorization tables for every privileged action, including unknown/extra input.
- Release archive abuse cases and independent-digest failure.
- Descriptor-relative path resolution under concurrent rename/symlink replacement.
- PID start-time/cgroup mismatch and per-VM lock contention.
- Exact Firecracker API paths for normal launch, snapshot create/restore, and hotplug.
- Failure injection after spawn, relay creation, socket readiness, DB writes, snapshot load, and cleanup.
- Firewall parent-jump, batch-apply, DB-commit, policy-remove, VM-remove, and IP-reuse failures.
- Structured cgroup-setting fixture restoration with restoration assertions.

### L2

- Run exclusively as an unprivileged `mvm` group member.
- Confirm a user-owned `mvm` binary is rejected by passwordless sudo and the system installation is immutable.
- Attempt a caller-authored Firecracker/Jailer archive and checksum; installation must fail.
- Repeated create/kill/reconcile cycles show zero leaked mounts, cgroups, PIDs, or chroot state.
- Snapshot and live-volume parity pass inside the private mount namespace.
- A second Unix user cannot inspect, mount, signal, clean, or remove the first user's VM/release references.
- Firewall failure followed by immediate address reuse never grants stale access.
- Measure VMM overhead across supported Firecracker versions before accepting a default headroom.

### Repository CI

```bash
go mod tidy && git diff --exit-code
test -z "$(gofmt -l .)"
golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/ 2>&1 | grep . \
  && echo "violations found" && exit 1 || true
go generate ./internal/service/agent/...
go vet ./...
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
```

## Migration and Rollback

1. Preserve the current base/head commit IDs and all unrelated dirty work before changing history.
2. Add forward-only migrations for exact-pair fields and uniqueness. Never rewrite an already-applied user DB in place.
3. In an authenticated administrator session, remove the insecure project-managed legacy sudoers rule, then run the
   trusted release artifact's exact `host install-system` route. Installation is atomic at root-owned
   `/usr/local/bin/mvm` and is deliberately separate from `host init`. After all callers use typed privileged actions,
   `sudo /usr/local/bin/mvm host init` validates the marker-only policy with `visudo` and atomically replaces the old
   rule. On failure it keeps the previous configuration and reports the unsafe state.
4. An unsupported privileged protocol refuses the operation and prints the explicit administrator upgrade command.
5. Existing VMs with an unambiguous exact pair are backfilled. Ambiguous/unmatched VMs remain stopped and return a
   migration error; they are never launched with an arbitrary pair.
6. Do not remove legacy paths until the new privileged dispatcher has reconciled root records and the L2 upgrade test succeeds.
7. Before release, optionally curate/squash the five prototype commits plus remediation only with explicit approval.

## Documentation Update Matrix

Documentation is the final implementation phase, not a substitute for missing guarantees.

Documentation accuracy is a release invariant. ADR-0016 and the threat contract are written before code so the target
is explicit; all current-behavior documentation is reconciled only after implementation and L2 evidence exist. A stale
or aspirational statement in an operator, architecture, testing, or troubleshooting document blocks release just like a
failing test.

| Document | Required correction |
|---|---|
| `docs/adr/0016-*.md` | New decision superseding the unsafe wildcard sudo boundary and defining privileged-mode/state/trust semantics |
| `docs/adr/0002-*.md`, `0005-*.md`, `0015-*.md` | Mark superseded portions and link to ADR-0016; remove false safety claims |
| `CONTEXT.md` | Correct domain count, privileged boundary, service pattern, state ownership, subprocess exceptions |
| `AGENTS.md`, `docs/STANDARDS.md` | Add privileged-dispatch import/ownership rules and Jailer service; retain typed-method/no-generic-extension constraints |
| `README.md`, `docs/DEPENDENCIES.md` | Administrator system install/upgrade, sudo behavior, trusted-release limitations |
| `docs/RUNTIME.md` | Correct service count, state paths, private mount namespace, process/cleanup flow |
| `docs/RESOURCE_MANAGEMENTS.md` | Exact cgroup path, derived policy, measurement status, I/O policy |
| `docs/TROUBLESHOOTING.md` | System-binary ownership/version checks, reconcile commands, stale instance/chroot/cgroup diagnosis |
| `docs/roadmaps/TENANT_MICROVM.md` | Mark milestones incomplete until all release gates pass |
| `docs/system-test-architecture.md`, coverage matrix | Add privilege/race/recovery/multi-user L2 cases and correct fixture cleanup |
| `CHANGELOG.md` | Describe behavior only after it is verified and releasable |

### Documentation definition of done

- Every changed command, path, ownership, state, lifecycle, and recovery claim is traced to final code and an executed
  test or inspection.
- Documents distinguish current behavior, migration behavior, known limitations, and future work; planned guarantees
  are never written in the present tense.
- Installation, upgrade, rollback, troubleshooting, and recovery commands are executed on the L2 runner.
- Repository-wide searches cover the legacy `~/.local/bin/mvm` sudo target, raw sudo commands, `/var/run`/`/run` state
  claims, both cgroup path variants, old service/domain counts, direct-Firecracker fallback, and snapshot host paths.
- ADR statuses and cross-links agree; superseded decisions remain available for historical context.
- The architect reads the final code diff before approving documentation, and QA independently verifies operator-facing
  procedures against the release candidate.

The untracked `docs/implementations/MEMORY_RECLAIM.md` currently contains the obsolete
`/sys/fs/cgroup/mvmctl/firecracker/<vm-id>` path. Preserve it as user work during implementation and correct it only
when its owner chooses to include it in the documentation sweep.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Privileged mode becomes a generic root RPC | Critical | Early dispatch, enumerated typed methods, minimal imports, abuse-case tests, no raw argv/path/cgroup API |
| Filesystem race remains after validation | Critical | Pinned dirfds/openat2, fixed basenames, concurrent rename/symlink L2 tests |
| Mounts leak or live hotplug remains invisible | High | Private launch namespace; verified `setns` for post-launch mounts; mount-leak assertions |
| PID reuse signals an unrelated host process | Critical | Root record + start time + cgroup + pidfd verification before every signal/setns |
| Root/user state diverges | High | Minimal root ownership record, explicit generations, startup reconciliation, fault injection |
| Release checksum channel is insufficient | High | Independent privileged fetch now; record limitation; adopt signatures/provenance when verifiably available |
| Privilege migration breaks existing installs | High | Atomic system-binary/sudoers install, protocol negotiation, upgrade L2 test, fail-closed diagnostics |
| Scope expands into unrelated XDG migration | Medium | Keep user state placement unchanged; separate ADR and project after security release |

## Open Approval Points

1. **Approved:** keep one binary and install it root-owned at `/usr/local/bin/mvm`, with early privileged dispatch.
2. **Approved:** remove every raw privileged system binary from the `mvm` sudoers entry, not only `mvm *`.
3. **Approved:** privileged code constructs the official Firecracker URL and independently fetches the checksum, with
   signed provenance adopted if the upstream release channel provides a stable verifiable mechanism.
4. **Approved:** XDG state separation is a follow-up and not part of this remediation.
