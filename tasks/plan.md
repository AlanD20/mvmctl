# v0.3.0 Clean-Break Security Release Plan

**Status:** Approved — implementation in progress
**Branch:** `0.3.0-release`
**Release gate:** Draft only until every CI and Python system-test requirement below passes

## Outcome

Release v0.3.0 as a clean installation with one unprivileged public CLI and one root-owned system copy of the same
`mvm` binary. All privileged effects must enter through a versioned typed dispatcher, bind to root-owned resource
authority, and fail closed under multi-user, path-race, process-reuse, partial-failure, and crash conditions.

Firecracker must always run through Jailer with cgroup v2 and one network namespace per VM. nftables becomes the only
firewall backend. Managed traffic is default-deny for both same-network and cross-network VMs. IP connectivity and
VM-to-VM vsock execution use separate typed `traffic` and `exec` policies.

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
- Private root-owned VM ownership/lifecycle records with strict codecs, descriptor-relative atomic storage, exact
  release references, global VM-ID claims, and release/index/VM locking.
- ADR-0016 for the single-binary privilege boundary.

The action switch is intentionally still empty and the sudoers policy remains transitional. The routed policy, dual
firewall backends, host-global TAP topology, public `mvm run jailer` / `mvm run provision` services, and
`allow_remote_exec` trust mesh must not survive the final release.

## Approved Architecture

### Trust and state

| State | Location | Authority |
|---|---|---|
| User configuration, SQLite DB, and VM artifacts | Existing `MVM_CACHE_DIR` | Untrusted for root authorization |
| Trusted Firecracker/Jailer pairs | `/var/lib/mvmctl/binaries` | Root-owned persistent |
| VM ownership/lifecycle records | `/var/lib/mvmctl/instances/<uid>` | Root-owned persistent |
| Network ownership/topology records | `/var/lib/mvmctl/networks/<uid>` | Root-owned persistent |
| Global capacity allocations | `/var/lib/mvmctl/allocations` | Root-owned persistent |
| Locks, launch handshakes, namespace handles | `/run/mvmctl` | Root-owned ephemeral |
| Jailer chroots | `/var/lib/mvmctl/jailer` | Root-owned lifecycle state |
| Cgroups | `/sys/fs/cgroup/mvmctl/<vm-id>` | Kernel runtime state |

Durable user state does not move wholesale to `/run` or `/var/run`. XDG state separation remains a separate project.

### Module and layer rules

- `cmd/mvm/main.go` only recognizes early modes and routes them.
- `internal/service/privileged/` owns the envelope, authenticated caller, executable verification, and fixed action
  switch. It imports no API or core domain and exposes no raw command/argv/path interface.
- Capability modules under `internal/service/{jailer,network,firewall,loopmount,host}/` own typed privileged effects.
- `pkg/api/` remains the sole orchestrator of multiple core domains.
- Core domains never import sibling `internal/core/*` packages.
- Public validation remains in `pkg/api/inputs/`; privileged receivers enforce trust-boundary invariants.
- Every side-effecting function takes `context.Context` first and returns `pkg/errs.DomainError`.
- Each new capability uses named typed methods matching existing service conventions. No generic backend, operation,
  selector registry, callback-under-lock, raw firewall expression, raw cgroup key, or raw netns action is permitted.

### VM authority interface

The first implementation slice is private to `internal/service/jailer`. It uses unexported typed values and these
operations:

```go
func (a *instanceAuthority) RegisterLaunch(
    ctx context.Context,
    caller instanceCaller,
    registration launchRegistration,
) (*launchLease, error)

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

func (a *instanceAuthority) LockUnreferencedRelease(
    ctx context.Context,
    release releaseIdentity,
) (*releaseLease, error)

func (l *launchLease) Release(ctx context.Context) error
func (l *registeredLease) Release(ctx context.Context) error
func (l *cleanupLease) Release(ctx context.Context) error
func (l *releaseLease) Release(ctx context.Context) error
```

Each lease retains its pinned directory and lock descriptors and provides a checked context-first release operation.
There is no public `Get`, `Put`, `Delete`, `Run`, raw path, arbitrary state string, or callback interface.

The durable lifecycle is intentionally small:

```text
absent  ── RegisterLaunch ──> registered
cleaned ── RegisterLaunch ──> registered
registered ─ BeginCleanup ──> cleaning
cleaning ─── BeginCleanup ──> cleaning
cleaning ───── Complete ─────> cleaned
```

`registered` means launch identity is durable; it does not claim the process is healthy. Process health is derived by
verifying PID, start ticks, cgroup membership, release, and namespace identity. Failed cleanup remains `cleaning`.
`cleaned` is a persistent ownership tombstone and may be relaunched only by the same UID.

The global lock order is:

```text
release lock -> global index lock -> VM lock -> network lock
```

Operations may acquire a suffix or subset but never an earlier class while holding a later class. Global VM-ID claims
are established by descriptor-enumerating all numeric UID record directories while holding the index lock. A foreign,
duplicate, corrupt, unreadable, or inconsistent claim fails closed. Lock files are never unlinked.

`RegisterLaunch` establishes the durable record before the first network or launch effect and returns with the VM lock
held. A later typed network step may therefore acquire only the later network lock while retaining that launch lease.
Task 4 owns release, index, and VM locking; it does not import network code or predeclare a network implementation.

Atomic record replacement uses a random exclusive temporary file, bounded writes, root ownership and exact modes, file
`fsync`, checked close, `renameat`, and parent-directory `fsync`. Pre-rename failure preserves the old record.
Post-rename failure returns details that distinguish `record_replaced` and `durability_uncertain`; the caller starts
no VM effects after such an error.

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

The public policy families are:

- `mvm policy traffic`: TCP/UDP from a source network or exact source VM to an exact destination VM port/range.
- `mvm policy exec`: directional exact source VM to exact destination VM and required non-root target user.

Traffic policy never grants vsock execution. Exec policy never grants IP connectivity. Same-network placement grants
nothing implicitly.

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
root network authority --> per-VM netns/veth/TAP --> nftables-only generation
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
   host restrictions, egress, atomic failure, reboot reconciliation, and nftables-only host readiness.
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
- same-network and cross-network default deny;
- typed `traffic` and `exec` policies;
- removal of `allow_remote_exec`;
- deferral of CNI and speculative multi-VMM support; and
- documentation plus Python system-test coverage as release-critical artifacts.
