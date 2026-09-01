# VM-to-VM Exec: Isolated Source and Destination Foundations

## Current status

VM-to-VM command execution is intentionally unavailable while the v0.3.0 authority path is completed. The following
foundations are implemented and covered by Go L0/L1 tests:

- directional `mvm policy exec` desired-state CRUD;
- the closed, bounded `MVMEXEC1` request and response protocol;
- authenticated non-root execution in the destination guest;
- a dedicated, one-request source session between the host and source guest;
- a process-bound local source broker and `mvm-agent remote` client in the source guest; and
- strict separation between ordinary host-to-VM exec/copy and the dormant VM-to-VM path.

There is no production API-owned relay listener, root active-runtime/capacity admission, runtime exec-policy
enforcement, or audit path yet, and no positive Python L2 system test has qualified the end-to-end behavior. The
legacy VM-wide trust flag is removed from the fresh schema, model, configuration, create path, results, persistence,
and environment specifications; it is not converted into a directional policy. No current host path constructs an
authorized destination request.

## Required user flow

Once the remaining host relay is implemented, an authorized source command will use this guest-side shape:

```text
mvm exec source-vm -- mvm-agent remote --user app destination-vm -- command arg
```

The outer `mvm exec` command may run in the source guest as root or as a non-root account. The destination target passed
to `--user` is mandatory and must be non-root. Network placement is irrelevant: the final directional exec policy is
required for same-network, cross-network, and no-IP-network VM pairs.

## Architecture

The design uses two independent host-to-source connections. It never multiplexes a guest request onto the ordinary
exec response stream.

```text
host API relay (pending)
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

The host session then accepts exactly one closed source request and emits the bounded response stream. A terminal
`result` or `error` closes the physical transport so the guest must observe EOF. Session and per-operation contexts set
effective deadlines; cancellation closes the connection to interrupt partial reads and blocked writes. `Close` is
idempotent and repeats its first cleanup result.

This type is deliberately dormant until `pkg/api` receives a root-admitted active source identity and constructs the
relay.

### 2. Binding the source command

After the dedicated transport is ready, the future host relay starts one ordinary non-TTY source command with
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

The future host relay must resolve and authorize the source request, build a separate `DestinationRequest`, and open a
new connection to the destination guest. Only this host-built request contains the destination agent token.

The destination agent accepts only exact `MVMEXEC1` magic on its existing vsock port, authenticates the token before an
execution effect, resolves an exact non-root account to numeric credentials and a clean absolute home, and runs
`/bin/sh -c` with fixed environment and process-group cancellation. Ordinary host exec/copy prefixes remain on their
existing protocol.

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
| Lifetime | Five-minute fixed maximum for one source/destination execution |
| Terminal | Exactly one `result` or `error`, followed by physical EOF |
| Control | One matching `cancel` frame; no generic extension frame |

Strict JSON decoding rejects missing, unknown, duplicate, case-colliding, trailing, incorrectly typed, and invalid
values. A wire or runtime limit is enforced independently at each boundary that consumes it.

## Remaining host authority

The positive path must not be enabled until all of these release blockers are complete:

1. `pkg/api` owns the relay because it is the only layer allowed to coordinate VM, vsock, policy, and runtime domains.
2. Root authority admits the exact active source instance/vsock identity; SQLite status, a guest-provided source ID,
   the socket path, and process-local semaphores are not authority.
3. Runtime authorization enforces owner, exact directional source/destination VM identity, and exact non-root target
   user before the destination is contacted.
4. Task 15 supplies root-owned global concurrent-capacity reservation across users and CLI processes.
5. The relay enforces the same frame, command, output, duration, cancellation, and EOF limits and records a
   command-redacted source/destination/user/timing/result audit event.
6. Python L2 tests prove positive same-network, cross-network, and no-IP-network execution plus missing/reverse policy,
   foreign owner, stale identity, root target, malformed wire, limit, cancellation, and cleanup denials.

Until these gates land, the source-session components remain a fail-closed foundation and VM-to-VM execution remains
unavailable.

## Removed legacy design

v0.3.0 no longer uses the legacy `remote_vm` relay, shared `activeConn`, `OnHostFrame`, or
`internal/vsockhandler`. A guest cannot inject a new operation into an ordinary exec response stream. The VM-wide
remote-exec field, configuration default, create flags/input/result, repository persistence, and environment-spec
property are also removed. Fresh databases use schema version 6 without the column. Schema-version 3, 4, or 5
databases that still contain it are rejected before pending-migration inspection or migration application and remain
unchanged, even if every stored bit is false. v0.3.0 provides no conversion from that trust mesh to directional policy.
