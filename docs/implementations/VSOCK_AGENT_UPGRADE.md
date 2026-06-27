# Agent Upgrade — Version-Check on Connect

## Problem

The vsock agent binary is compiled at build time, embedded via `//go:embed`,
and injected into the VM rootfs at creation time. Running VMs never receive
agent updates. A bug fix in the agent (e.g., the `handleFTPush` infinite loop)
only applies to newly created VMs. Existing VMs keep the old binary until
destroyed and recreated.

## Solution

Every vsock connection performs a version check. If the VM's agent is older
than the host's embedded agent, upgrade automatically — pushing the new binary
and restarting the agent. All operations (exec, shell, cp) benefit transparently.

## Architecture

```
┌──────────────────────────────────────────────┐
│              API Layer (pkg/api/)             │
│                                               │
│  Creates vsock Client with callbacks:         │
│  OnUpgradeStarted  → set DB lock              │
│  OnUpgradeCompleted → clear DB lock + persist │
│                                               │
│  client.Exec/Shell/FTCopyToVM(...)            │
└───────────────────┬──────────────────────────┘
                    │
┌───────────────────▼──────────────────────────┐
│           Core Layer (internal/core/)          │
│                                               │
│  ensureAgent(ctx):                            │
│    1. dial raw UDS + CONNECT handshake        │
│    2. probe version via protocol request      │
│    3. if host > agent:                        │
│       a. mark upgrade in progress             │
│       b. invoke OnUpgradeStarted callback     │
│       c. push binary via FTCopyToVM           │
│       d. exec replace + delayed restart       │
│       e. retry from step 1                    │
│       f. on version match: invoke             │
│          OnUpgradeCompleted                   │
│    4. cache agent version on client           │
│    5. return raw connection                   │
└──────────────────────────────────────────────┘
```

**Key design decision: upgrade happens inside the core layer, but DB lock
management lives in the API layer callbacks.** This keeps the core layer
agnostic to the storage backend while still providing crash-safe concurrency
control. The core layer fires callbacks; the API layer wires them to
repository calls.

## Why Automatic Upgrade (Not Explicit Command)

The agent version is fundamentally a property of the *host binary*, not the
VM. When you deploy a new `mvm` binary with an updated embedded agent, every
VM you connect to after that should pick it up. An explicit `mvm vm upgrade`
command would require users to remember to run it — creating a window where
the VM runs an outdated agent. Automatic upgrade on connect eliminates this
class of bug.

The only downside is a small latency spike on first connect (≈3-5s for
binary push + restart + retry). This is acceptable: subsequent operations
use the new agent immediately, and the latency is limited to the first
operation after host deployment.

## Version Comparison

Uses semantic versioning via `version.Compare(hostVersion, agentVersion)` from
`internal/lib/version/compare.go`. Returns a positive int when host > agent,
negative when host < agent, and zero when equal. Only upgrades when host > agent —
downgrades are never attempted (a future `mvm` binary might pin an older
agent, but that's a separate concern handled by the comparison logic).

**Critical edge case: `BuildVersion` vs `VersionString()`.** The version
injected via ldflags (`-X version.BuildVersion=...`) is what the host and
agent binaries actually carry. The `VersionString()` method returns `"0.0.0"`
as a default when `BuildVersion` is empty. The comparison function
`version.Compare` handles git-style hashes (e.g.
`"80bf5256-dirty"`) by splitting on `.` and comparing numerically where
possible, falling back to string comparison for non-numeric segments. This
means the host must always use `version.BuildVersion` (set via ldflags), not
`version.VersionString()`.

## Upgrade Lock

DB-based lock on `vm_vsock_config.upgrading`. Two-level guard:

1. **Before dial** — API layer loads the vsock config and checks
   `upgrading`. If set and not stale (< 60s), returns an error immediately.
   No connection attempt, no timeout.

2. **During upgrade** — `ensureAgent` invokes `OnUpgradeStarted`, which
   sets `upgrading=1` in the DB. The SQL UPDATE is conditional on
   `upgrading=0` — if another process beat us to it, zero rows are
   affected and the operation is rejected with an error.

**Why DB lock instead of file lock?** The DB lock survives process crashes
(no stale `.lock` files), is visible across concurrent `mvm` processes
(not just within one process), and enables early rejection before any
vsock dial attempt.

**Stale lock recovery.** If `upgrading=1` but `upgrade_started_at` is
older than 60s, the API layer clears the lock and proceeds. This handles
the case where the upgrading process crashed mid-upgrade.

## Agent-Side Changes

The agent gains a new protocol request type `"version"`. When the host
sends this request, the agent responds with its embedded version string
(also injected via ldflags). This is handled in the existing connection
dispatch loop — no new goroutines or infrastructure.

The agent also gains a `--version` flag for manual verification, used
during development and testing.

**Why a request-response cycle instead of a banner on CONNECT?** The
CONNECT handshake is already a fixed protocol (send token, expect "OK").
Adding version data there would change the handshake format. A new request
type keeps the handshake unchanged and makes the version probe an optional
step — the connection is fully usable even without it.

## Build Changes

The agent binary is built with the same ldflags version injection as the
host binary:
```
-X mvmctl/internal/lib/version.BuildVersion=${version}
```

This is the only change to the build script. The agent's `//go:embed` and
compression pipeline remain unchanged.

## Restart Timing

The `systemctl restart` command is delayed by 2 seconds using a
backgrounded `(sleep 2 && systemctl restart) &`. This is **critical**:
without the delay, the agent is killed before the exec response frame
(the result of the upgrade command) is sent back to the host. The host
would see a connection error instead of a clean success.

## `ensureAgent` (Replaces `waitForAgent`)

The existing `waitForAgent` method was a simple retry loop: dial UDS →
CONNECT handshake → return connection. The new `ensureAgent` adds version
probe + upgrade loop before returning the connection.

The loop:
1. Dial + handshake (same as before).
2. Probe agent version (new `"version"` request).
3. If host version > agent version: lock, push binary, replace, restart,
   retry from step 1.
4. If versions match after a previous upgrade attempt: clear lock,
   persist version, return connection.
5. If any step fails: close connection, sleep, retry (bounded by probe
   timeout).

**Circular call prevention.** `upgradeAgent` uses a `skipVersionCheck`
flag on the Client. This bypasses `ensureAgent` entirely and dials
directly. Without this flag, `upgradeAgent` would call `ensureAgent`
which would detect a version mismatch and try to upgrade again —
infinite loop.

## `Client` Struct Additions

The Client gains fields to support the upgrade flow:
- `AgentVersion` — cached after successful probe (used by API layer for
  display).
- `upgradeInProgress` — set during upgrade, used after retry to
  distinguish "we just upgraded" from "first connect".
- `skipVersionCheck` — internal flag to bypass `ensureAgent`.
- `OnUpgradeStarted` / `OnUpgradeCompleted` — callbacks wired by the API
  layer to manage the DB lock.

These are not generic extension points — each field has exactly one
purpose in the upgrade flow. The Client struct remains a concrete type
with typed fields, not a generic callback registry.

## DB Schema Changes

Three columns added to `vm_vsock_config`:

| Column | Type | Purpose |
|---|---|---|
| `agent_version` | `TEXT` | Last known agent version |
| `upgrading` | `INTEGER` | Boolean: upgrade in progress |
| `upgrade_started_at` | `TIMESTAMP` | For stale lock detection |

These are added directly to `001_initial_schema.sql` (no separate
migration file — project is pre-v1.0).

## Repository Methods

Three new methods on the vsock repository:

- `SetUpgradeLock` — atomically sets `upgrading=1` where `upgrading=0`.
  Returns error if zero rows affected (lock already held or VM not found).
- `ClearUpgradeLock` — resets `upgrading` and `upgrade_started_at`.
- `UpdateAgentVersion` — persists the new agent version after a
  successful upgrade.

These follow the same pattern as existing repository methods: raw SQL
via `sqlx`, context propagation, error wrapping via `errs.Wrap`.

## API Layer Integration

The API layer (`pkg/api/cp.go`, `pkg/api/vm.go`) uses a helper
`newVsockClient` that:
1. Loads the vsock config from the DB.
2. Checks for stale/active upgrade locks.
3. Constructs a Client with `OnUpgradeStarted` / `OnUpgradeCompleted`
   callbacks.
4. Returns the configured client.

This helper is used by exec, shell, and cp operations — all three
benefit from transparent upgrade without duplicating the setup logic.

## Error Code

A new error code `CodeVsockUpgradeInProgress` is added to `pkg/errs/`.
Used when a concurrent upgrade is rejected before dialing.

## Trade-offs and Alternatives Considered

| Alternative | Rejected because |
|---|---|
| **Explicit `mvm vm upgrade` command** | Requires user action; VMs can run outdated agents indefinitely |
| **Pre-built AMI-style VM images** | Our VMs are ephemeral; there's no image pipeline to bake into |
| **Agent auto-update daemon** | Adds complexity inside the guest; requires the agent to reach an update server; duplicates host's embedded agent logic |
| **Shared memory + signal** | Firecracker does not share memory between host and guest; vsock is the only IPC channel |
| **File lock instead of DB lock** | Doesn't survive crashes; not visible across processes; can't do stale detection |
| **Version banner on CONNECT** | Changes the handshake protocol; makes version probe mandatory for all connections |
| **Separate migration file** | Not warranted pre-v1.0; schema is still settling |
