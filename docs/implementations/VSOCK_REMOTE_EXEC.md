# VM-to-VM Exec: Opt-In Relay, Authority, and Audit

## Current status

The API-owned VM-to-VM relay is implemented behind an explicit `mvm exec --allow-vm-exec` opt-in. It composes the
following components:

- directional `mvm policy exec` desired-state CRUD;
- the closed, bounded `MVMEXEC1` request and response protocol;
- authenticated non-root execution in the destination guest;
- a dedicated, one-request source session between the host and source guest;
- a closed, one-request destination session over an already selected connection;
- a process-bound local source broker and `mvm-agent remote` client in the source guest;
- separate ordinary host-to-VM exec/copy and opt-in VM-to-VM paths;
- a production-composed root-owned capacity, active-pair, peer-binding, and absolute-stop authority behind the retained
  sidecar; and
- a typed fixed-schema command-redacted audit sink.

The relay performs directional policy enforcement, retained-sidecar admission, bounded payload forwarding,
cancellation, and command-redacted audit finalization. It requires root-admitted active source and destination
identities from the schema-5 lifecycle, now composed by typed root launch. A transitional launch or SQLite status
value cannot supply that authority.
The implementation-only update added no tests and ran no build, tests, or qualification. Positive end-to-end behavior
is not qualified.
The required no-IP-network case is not yet representable through public VM creation: `VMCreateRequest.resolveNetwork`
in `pkg/api/inputs/vm_create.go` requires a managed network, and `vm create` has no network-omission mode. The exec grant
does not look up network placement, but that is not evidence that a no-IP VM can be launched. Add an admitted
networkless VM lifecycle before claiming the no-IP case or its Python L2 test complete. The closed placement and
root-owned loopback-only namespace contract is [ADR-0020](../adr/0020-networkless-vm-placement.md); the ADR does not
claim a runnable VM or runtime qualification. The Firecracker config generator has only the no-NIC/no-`ip=` primitive;
public placement, root namespace lifecycle, restart, and snapshot wiring remain missing.
The legacy VM-wide trust flag is removed from the fresh schema, model, configuration, create path, results,
persistence, and environment specifications; it is not converted into a directional policy.

## Required user flow

An authorized non-interactive source command uses this shape:

```text
mvm policy exec create source-vm destination-vm app
mvm exec source-vm --allow-vm-exec -- mvm-agent remote --user app destination-vm -- command arg
```

The outer `mvm exec` command may run in the source guest as root or as a non-root account. The destination target passed
to `--user` is mandatory and must be non-root. Network placement is irrelevant: the directional exec policy is
required for same-network, cross-network, and no-IP-network VM pairs.

The opt-in outer command and its one remote request share a five-minute absolute lifetime. The ordinary `--timeout`
flag remains a connection/probe timeout and cannot extend that bound.

## Architecture

The design uses two independent host-to-source connections. It never multiplexes a guest request onto the ordinary
exec response stream.

```text
host API relay (explicit --allow-vm-exec)
  |
  | dedicated source transport: MVMSRC01 bootstrap, then one MVMEXEC1 request/response
  v
source guest agent
  | pending source-session ID
  |
  | ordinary host exec connection carries only that ID
  v
one source command process tree
  |
  | AF_UNIX /var/run/mvm-agent.sock, admitted by peer credentials + pidfds + ancestry
  v
mvm-agent remote --user <non-root> <destination> -- <command>
```

The source-session ID is a random correlation handle, not an authorization credential. It may cross the ordinary exec
request, but the agent token never enters the guest command's environment, arguments, filesystem, or local socket
request. The destination token remains host-only and appears only in a host-built destination request.

Ordinary `Client.Exec`, shell, and copy operations do not open or inspect a source session. Only the named
`ExecWithSourceSession` path can attach an already-open source-session ID to a non-TTY exec request. Unknown ordinary
exec response types are protocol errors; there is no generic host-frame callback.

### 1. Dedicated source transport

`internal/core/vsock.SourceSession` accepts an already authority-admitted connection and the source guest's canonical
agent token. It does not resolve a VM, consult SQLite, dial a user-provided socket, authorize a policy, or allocate
runtime capacity.

`Open` generates a cryptographically random 16-byte lowercase hexadecimal session ID, writes an authenticated
`MVMSRC01` bootstrap, and requires an exact ready response for that ID. The source guest authenticates the agent token
in constant time before it registers the ID. Duplicate IDs, malformed frames, and unsupported schemas fail closed.

The host session then accepts exactly one closed source request and emits the bounded response stream. After
`ReadRequest` succeeds, `WaitCancel` may claim exactly one control reader bound to the accepted request ID and accepts
only its matching `cancel` frame. Malformed, non-cancel, wrong-ID, and context-cancelled partial input closes the
transport fail-closed. Explicit `Close` and context cancellation unblock a partial control read. The cancel reader sets
only a read deadline while concurrent response operations set only write deadlines, preserving full-duplex output.
A terminal `result` or `error` closes the physical transport so the guest must observe EOF. Session and per-operation
contexts retain their effective deadlines, and `Close` is idempotent and repeats its first cleanup result.

The opt-in `pkg/api` relay consumes this primitive together with the retained root authority.

### 2. Binding the source command

After the dedicated transport is ready, the host relay starts one ordinary non-TTY source command with
`ExecWithSourceSession`. That exec frame contains only the session ID. A normal `Exec` leaves the field empty and stays
on the existing behavior.

The source guest consumes a pending session only once, resolves the requested outer command account, and starts the
outer shell in a new session/process group. Go obtains its pidfd atomically at process creation. The agent verifies the
pidfd, stable PID/start-time identity, and liveness before publishing the binding.

Context cancellation kills the outer command's process group with `SIGKILL`; the command also has a two-second
`WaitDelay`. This is a process-group guarantee, not a per-command cgroup guarantee, and it does not claim control of a
descendant that deliberately escapes the group.

### 3. Local source broker admission

The guest agent exposes `/var/run/mvm-agent.sock` with mode `0666` so either an authorized root or non-root outer source
command can reach it. Filesystem possession does not grant authority. Before decoding a local request, the agent:

1. requires an AF_UNIX stream connection;
2. obtains `SO_PEERCRED` and `SO_PEERPIDFD` during the same descriptor control operation;
3. requires close-on-exec pidfds and verifies their `/proc/self/fdinfo` PID identity;
4. rejects exited and zombie processes through pidfd polling and liveness checks;
5. requires the local peer UID/GID to match the bound outer command account;
6. reads the peer-to-root parent chain twice, including PID, parent PID, and start time, with a depth limit of 64; and
7. accepts exactly one live descendant of exactly one unclaimed source binding.

The broker fails closed if `SO_PEERPIDFD` or any required identity/liveness observation is unavailable. Imported or
custom guest kernels must therefore provide this socket capability; a numeric PID from `SO_PEERCRED` alone is not
sufficient.

Local admission is capped at eight concurrent attempts. The request must arrive within two seconds and within the
source transport's earlier absolute deadline. No local client can select a session ID or token in its request.

### 4. One-request relay inside the source guest

After admission, the local client sends one strict `SourceRequest` containing a random request ID, destination selector,
non-root target user, and command. The broker hands read ownership of the dedicated transport from its disconnect
monitor to the relay, forwards that one request, and copies the closed response stream back to the local client.

The first disconnect closes both sides and releases the pending/bound session and both process pins. A source-host
disconnect therefore tears down the broker relay. Cancellation of the ordinary source exec closes its transport and
kills the outer command process group. Agent shutdown closes every pending and bound source session before waiting for
its listeners.

The `mvm-agent remote` client preserves binary stdout/stderr and returns the remote result exit code only after the
terminal frame and physical EOF. It prints only generic local errors and does not expose the session ID, token,
command, or internal process cause in an error.

### 5. Destination execution

The host relay resolves and authorizes the source request, builds a separate `DestinationRequest`, and opens a
new connection to the destination guest. Only this host-built request contains the destination agent token.

The destination agent accepts only exact `MVMEXEC1` magic on its existing vsock port, authenticates the token before an
execution effect, resolves an exact non-root account to numeric credentials and a clean absolute home, and runs
`/bin/sh -c` with fixed environment and process-group cancellation. Ordinary host exec/copy prefixes remain on their
existing protocol.

### 6. Closed destination transport

`internal/core/vsock.DestinationSession` consumes one already selected destination connection. It does not dial or
resolve a socket path, VM, agent token, policy, or runtime authority. Its only `Open` attempt first validates one
host-built `DestinationRequest`, writes it, and creates a strict response reader bound to that request ID and the wire's
combined-output limit.

Exactly one sequential reader accepts binary `stdout` and `stderr`; it withholds both `Result` and `Error` until a
second read proves physical EOF. After a successful open, at most one exact request-bound `Cancel` can be written while
the response reader remains active. Read and write deadlines are independent and directional, but both remain bounded
by the fixed deadline selected at open. An earlier opening-context deadline wins over the five-minute maximum, and an
absolute lifetime timer closes even an idle session. Operation-context cancellation and explicit `Close` interrupt
partial reads and writes. Protocol or transport failure closes the connection; cleanup is close-once and repeats its
cached first result.

The API relay owns destination dialing, policy lookup, root admission, payload forwarding, and audit. The destination
session owns only its bounded one-request transport.

### 7. Root-owned capacity and active-pair authorities

The private `internal/service/jailer` capacity authority stores strict schema-1 reservations at
`/var/lib/mvmctl/allocations/exec/<32-lowercase-hex>.json`. Each one-link record binds its root-generated reservation ID,
the non-root owner UID, exact source and destination VM IDs plus launch generations, current host boot ID, and exact
creation/expiration times from `CLOCK_BOOTTIME`. Managed directories are exact `root:root` mode `0700`; records are
exact `root:root` mode `0600` with a 4-KiB bound.

The never-unlinked `/run/mvmctl/authority/capacity.lock` makes scan, stale cleanup, ceiling comparison, and no-replace
publication one serialized transaction across CLI processes and users. The store fails closed on any unknown,
ambiguous, unsafe, or corrupt entry before pruning canonical records. It admits and removes only safe crashed temporary
files, prior-boot records, and expired records, then syncs the directory. Exclusive temporary creation, stable metadata,
file sync, checked close, no-replace rename, parent sync, and checked reverse cleanup preserve explicit
`record_created`, `record_deleted`, `records_pruned`, `durability_uncertain`, and `cleanup_required` evidence.

The retained sidecar stops at the reservation's creation boottime plus `vmexecwire.MaxExecutionDuration`; setup and
suspend consume that fixed budget. The record expires after a separate four-second grace for forced-process cleanup and
terminal delivery.

The private active-pair authority takes the instance index once, admits two distinct same-owner `active` records, opens
and retains their authorities separately, and locks the VMs in canonical lexical ID order. With the index released, it
retains the shared roots separately from both active leases, reopens both exact recorded process sets through pidfds,
and proves both complete active identities plus final pidfd locator/liveness. It reserves root-global capacity only
after those proofs, then rechecks the boot ID, both complete identities, and both final pidfd locator/liveness proofs
before exposing an immutable admission bound to the owner, source/destination launch generations, reservation, boot,
and fixed relay-stop deadline.

The lease releases exactly once in reverse authority order: capacity, destination/source process capabilities, active
leases in descending lexical VM-ID order, then the pair roots. The retained sidecar consumes these authorities under a
fixed host-global ceiling of eight and admits its exact source and destination connection descriptors. The API relay
calls the sidecar while retaining ownership of its original connections.

### 8. Typed command-redacted audit sink

`internal/lib/logging.VMExecAuditSink` is a separate typed boundary for one VM-to-VM execution event. It writes one
complete fixed schema-1 JSON object per line to the dedicated user-owned `vm-exec-audit.jsonl` file in the mvmctl user
cache (`~/.cache/mvmctl` by default). Each record contains only `operation=vm_exec`, the request ID, source and
destination VM IDs, non-root target user, UTC start time, bounded duration, a closed result, and the
result-consistent optional exit code. It does not contain the command, agent token, or process cause.

The sink serializes record, poison, and close transitions across copied values. A short or failed write poisons the sink
so a later record cannot be appended to an uncertain partial JSON line; validation, write, and cleanup failures are
typed and returned. Construction rejects an effective-root caller because the user-cache pathname is not retained
descriptor-relatively. The file remains user-modifiable and therefore is observability, not tamper-proof root
authority.

The API relay constructs the sink and records terminal outcomes. `Record` deliberately observes the context it
receives; the relay derives a fresh bounded context through `context.WithoutCancel` for terminal audit finalization
and surfaces audit failure with the relay result. An unresolved destination denial uses a separate bounded,
command-redacted audit shape because it cannot claim a resolved destination VM identity.

## Wire contracts and limits

| Boundary | Contract |
|---|---|
| Source bootstrap | Exact `MVMSRC01`, version 1, big-endian length, 1-512-byte strict JSON body |
| Bootstrap identity | Canonical lowercase UUIDv4 agent token plus random 32-lowercase-hex source-session ID |
| Source/destination request | Exact `MVMEXEC1`, version 1, at most 64 KiB per strict JSON frame |
| Request identity | Random 32-lowercase-hex ID on request, output, terminal, and cancel frames |
| Target | Destination selector at most 64 bytes; mandatory non-root user at most 32 bytes |
| Command | Valid UTF-8, no unsafe controls, 1-32 KiB |
| Output | Binary-safe stdout/stderr frames, 8 MiB combined |
| Lifetime | Destination open selects an absolute deadline no later than five minutes; idle time consumes it |
| Terminal | Exactly one `result` or `error`, followed by physical EOF |
| Control | One matching `cancel` frame; no generic extension frame |

Strict JSON decoding rejects missing, unknown, duplicate, case-colliding, trailing, incorrectly typed, and invalid
values. A wire or runtime limit is enforced independently at each boundary that consumes it.

## Composition and remaining qualification

The implemented relay composes these boundaries and typed launch now supplies schema-5 active records. Remaining
lifecycle recovery work and system qualification are separate from the relay implementation:

1. `pkg/api` owns the relay because it is the only layer allowed to coordinate VM, vsock, policy, and runtime domains.
2. Root authority admits the exact active source connection/vsock identity and supplies the authority-owned source FD;
   SQLite status, a guest-provided source ID, the socket path, and process-local semaphores are not authority.
3. Runtime authorization enforces owner, exact directional source/destination VM identity, and exact non-root target
   user before the destination is contacted.
4. Production composition selects an explicit positive global ceiling, constructs and invokes the implemented private
   pair authority, binds its immutable owner/source/destination launch-generation admission to the admitted source FD,
   reserves capacity before relay effects, enforces the fixed relay-stop deadline, and releases the lease with its
   terminal/cleanup evidence.
5. The relay uses `DestinationSession` to forward bounded response payload and exact cancellation under the same frame,
   command, output, duration, and EOF limits.
6. The API relay invokes the implemented typed audit sink for every terminal outcome. Cancellation and timeout paths
   use a fresh bounded context derived through `context.WithoutCancel`, and every audit failure is surfaced or joined
   with the relay result.
7. Python L2 tests prove positive same-network, cross-network, and no-IP-network execution plus missing/reverse policy,
   foreign owner, stale identity, root target, malformed wire, limit, cancellation, and cleanup denials.

No successful end-to-end execution or release-readiness claim follows from the relay implementation alone. In
particular, root active identity cannot be inferred from user state or bypassed to exercise an older launch path.

## Removed legacy design

v0.3.0 no longer uses the legacy `remote_vm` relay, shared `activeConn`, `OnHostFrame`, or
`internal/vsockhandler`. A guest cannot inject a new operation into an ordinary exec response stream. The VM-wide
remote-exec field, configuration default, create flags/input/result, repository persistence, and environment-spec
property are also removed. Fresh databases use schema version 6 without the column. Schema-version 3, 4, or 5
databases that still contain it are rejected before pending-migration inspection or migration application and remain
unchanged, even if every stored bit is false. v0.3.0 provides no conversion from that trust mesh to directional policy.
