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
  It is not yet carried by a privileged action or bound into the instance record.
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

Task 6 adds a private `releaseAuthority` in `internal/service/jailer`. Its public privileged request contains only a
validated release version, architecture, and explicit replacement intent. Root constructs the fixed official checksum
and archive URLs and obtains the checksum independently through a dedicated bounded HTTPS-only client with proxies
disabled. For mirror efficiency, the normal-user client may stream an exact-length bounded archive body through the
typed install transport; root hashes it against the independently fetched checksum before parsing it. If no body is
supplied, root may fetch the fixed archive itself. Root never opens a caller path or accepts a caller URL or checksum,
and `MVM_ASSET_MIRROR` supplies bytes rather than authority. Extraction validates the reviewed complete upstream member
allowlist and extracts only Firecracker and Jailer.

The strict root-owned manifest stores schema version, release slot, archive hash, and each executable's hash and size.
The store is exactly `/var/lib/mvmctl/binaries/<architecture>/<version>/{firecracker,jailer,release.json}`. Binaries are
validated as ELF for the selected architecture without execution. Descriptor-relative atomic install, exchange, and
removal use only fixed leaves and preserve an old complete release or expose a new complete release, never a partial
pair. Post-commit errors report `release_installed`, `release_replaced`, `release_removed`, `durability_uncertain`, and
`retired_release_retained` details without replacing the primary error identity.

Launch uses a private prepared value that owns the release-slot lease, verified manifest, pinned release directory, and
pinned executable descriptors. It alone supplies release hashes to instance registration. The privileged transport must
return a strict versioned response envelope before release install/remove is wired, because generic subprocess errors
cannot preserve the partial-state details required for safe retries.

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
