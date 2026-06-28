# Agent Upgrade — Version-Check on Connect

## Problem

The vsock agent binary is compiled at build time, embedded via `//go:embed`, and injected into the VM rootfs at creation time. Running VMs never receive agent updates. A bug fix in the agent (e.g., the `handleFTPush` infinite loop) only applies to newly created VMs. Existing VMs keep the old binary until destroyed and recreated. An automatic upgrade mechanism on connect ensures all VMs transparently receive agent updates without user intervention.

## Architecture

Every vsock connection performs a version check. If the VM's agent is older than the host's embedded agent, the upgrade happens automatically — pushing the new binary and restarting the agent. All operations (exec, shell, cp) benefit transparently.

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

**Key design decision: upgrade happens inside the core layer, but DB lock management lives in the API layer callbacks.** This keeps the core layer agnostic to the storage backend while providing crash-safe concurrency control. The core layer fires callbacks; the API layer wires them to repository calls.

## Entry point

The upgrade is triggered transparently from `ensureAgent()` in `internal/core/vsock/client.go`. This method is called at the start of every `Exec()`, `Shell()`, and file transfer operation (`FTCopyToVM`, `FTCopyFromVM`, `FTCopyVMToVM`).

The API layer creates the `vsock.Client` with `OnUpgradeStarted` and `OnUpgradeCompleted` callbacks. These callbacks are wired to the vsock repository's `SetUpgradeLock()` and `ClearUpgradeLock()` methods, which manage the `vm_vsock_config.upgrading` flag in the database.

## Happy path

### 1. Dial and handshake

`ensureAgent()` calls `dialAndHandshake()` which connects to the Firecracker vsock UDS, sends `CONNECT <port>\n`, and reads the `OK <host-port>` response.

### 2. Version probe

The host sends a `"version"` request type to the agent. The agent responds with its embedded version string (injected via ldflags). The host compares using `version.Compare(hostVersion, agentVersion)` from `internal/lib/version/`.

### 3. Upgrade if host is newer

If the host version is greater than the agent version:

1. **Set upgrade lock**: The `OnUpgradeStarted` callback is invoked, which sets `upgrading=1` in the `vm_vsock_config` table. The SQL UPDATE is conditional on `upgrading=0` — if another process beat us, zero rows are affected and the operation is rejected.

2. **Push new binary**: `upgradeAgent()` writes the embedded binary to a temp file on the host, then uses `FTCopyToVM()` (with `skipVersionCheck=true` to avoid circular calls) to transfer it to `/usr/bin/mvm-vsock-agent.new` inside the VM.

3. **Replace and restart**: The client executes the upgrade shell command on the agent:
   ```
   cp /usr/bin/mvm-vsock-agent /usr/bin/mvm-vsock-agent.bak 2>/dev/null || true;
   mv /usr/bin/mvm-vsock-agent.new /usr/bin/mvm-vsock-agent && chmod 0755 /usr/bin/mvm-vsock-agent &&
   ( sleep 2 && systemctl restart mvm-vsock-agent ) &
   ```

   The 2-second delay is critical: it allows the exec response frame to be sent before the old agent is killed. Without the delay, the host would see a connection error instead of a clean success.

4. **Wait and retry**: The client waits 3 seconds for the agent to restart, then retries from step 1.

5. **Confirm upgrade**: On the retry, the version probe confirms the agent version matches or exceeds the host version. `OnUpgradeCompleted` is invoked, clearing the lock and persisting the new version.

### 4. Return connection

After a successful version check (no upgrade needed or upgrade completed), the connection is returned to the caller for the actual operation.

## DB schema

Three columns on `vm_vsock_config` support the upgrade:

| Column | Type | Purpose |
|--------|------|---------|
| `agent_version` | TEXT | Last known agent version |
| `upgrading` | INTEGER | Boolean: upgrade in progress |
| `upgrade_started_at` | TIMESTAMP | For stale lock detection |

## Repository methods

Three new methods on the vsock repository:

- `SetUpgradeLock` — atomically sets `upgrading=1` where `upgrading=0`. Returns error if zero rows affected.
- `ClearUpgradeLock` — resets `upgrading` and `upgrade_started_at`.
- `UpdateAgentVersion` — persists the new agent version after a successful upgrade.

## Version comparison

Uses `version.Compare()` from `internal/lib/version/compare.go`. Returns a positive int when host > agent, negative when host < agent, zero when equal. Only upgrades when host > agent — downgrades are never attempted.

The version is injected via ldflags (`-X mvmctl/internal/lib/version.BuildVersion=...`). The `VersionString()` method returns `"0.0.0"` as a default when `BuildVersion` is empty. The comparison handles git-style hashes with `.` delimiters.

**Critical edge case: `BuildVersion` vs `VersionString`.** The version injected
via ldflags (`-X mvmctl/internal/lib/version.BuildVersion=<version>`) is what
the host and agent binaries actually carry. `VersionString()` returns `"0.0.0"`
as a default when `BuildVersion` is empty. The comparison function
`ParseSemverInts` cannot parse git-style hashes (e.g., `"80bf5256-dirty"`) —
it returns an empty slice, and `SemverGreater` treats empty slices as older
than any valid semver. This means the host must always use
`version.BuildVersion` (set via ldflags), not `version.VersionString()`.

## Failure modes

### Concurrent upgrade detection

The DB-based lock has two levels of guard:

1. **Before dial** — the API layer loads the vsock config and checks `upgrading`. If set and not stale (< 60s), returns `vsock.upgrade_in_progress` immediately. No connection attempt.

2. **During upgrade** — `ensureAgent` invokes `OnUpgradeStarted`, which sets `upgrading=1` via a conditional UPDATE (`WHERE upgrading=0`). If zero rows affected, another process holds the lock.

### Stale lock recovery

If `upgrading=1` but `upgrade_started_at` is older than 60s, the API layer clears the lock and proceeds. This handles the case where the upgrading process crashed mid-upgrade.

### Upgrade exec failure

If the upgrade shell command fails (binary not found, permission denied, etc.), the client attempts to restore the previous agent from backup (`agent.bak`). If the restoration also fails, a log entry is made but the connection error is returned to the caller.

### Circular call prevention

`upgradeAgent` creates subsidiary `Client` instances with `skipVersionCheck=true`. This bypasses `ensureAgent` entirely and dials directly. Without this flag, `upgradeAgent` would call `ensureAgent`, which would detect a version mismatch and try to upgrade again — infinite loop.

## Key files

| File | Purpose |
|------|---------|
| `internal/core/vsock/client.go` | `ensureAgent()` — dial + version probe + upgrade loop. `upgradeAgent()` — push binary, replace, restart. `probeVersion()` — request/response version exchange |
| `internal/core/vsock/protocol.go` | `requestTypeVersion`, `responseTypeVersion` constants |
| `internal/core/vsock/repository.go` | `SetUpgradeLock()`, `ClearUpgradeLock()`, `UpdateAgentVersion()` |
| `internal/lib/version/compare.go` | `version.Compare()` — semver-style comparison |
| `internal/service/vsockagent/cmdlistener.go` | Agent-side: handles `"version"` request type, responds with embedded version |
| `internal/service/vsockagent/agent.go` | Agent's `vsockConn.Close()` uses `SHUT_RDWR` for reliable vsock shutdown |
| `pkg/api/operation.go` | `newVsockClient()` helper — loads config, checks locks, constructs client with callbacks |
| `pkg/errs/codes.go` | `CodeVsockUpgradeInProgress` — concurrent upgrade rejection error |

## Agent-side changes

The agent gains a new protocol request type `"version"`. When the host sends
this request, the agent responds with its embedded version string (injected via
ldflags). This is handled in the existing connection dispatch loop in
`cmdlistener.go` — no new goroutines or infrastructure. The agent also gains a
`--version` flag for manual verification.

**Why a request-response cycle instead of a banner on CONNECT?** The CONNECT
handshake is already a fixed protocol. Adding version data there would change
the handshake format. A new request type keeps the handshake unchanged and
makes the version probe optional — the connection is fully usable without it.

## Build changes

The agent binary is built with the same ldflags version injection as the host
binary:

```
-X mvmctl/internal/lib/version.BuildVersion=${version}
```

This is the only change to the build script. The agent's `//go:embed` and
compression pipeline remain unchanged.

## Client struct additions

The `vsock.Client` struct gains fields to support the upgrade flow:

- `AgentVersion` — cached after successful probe, used by the API layer for
  display.
- `upgradeInProgress` — set during upgrade, used after retry to distinguish
  "we just upgraded" from "first connect".
- `skipVersionCheck` — internal flag to bypass `ensureAgent` for the upgrade
  push itself (prevents circular upgrade loops).
- `OnUpgradeStarted` / `OnUpgradeCompleted` — callbacks wired by the API layer
  to manage the DB lock.

These are not generic extension points — each field has exactly one purpose in
the upgrade flow.

## API layer integration

The API layer (`pkg/api/cp.go`, `pkg/api/exec.go`) uses a helper
`newVsockClient` that:

1. Loads the vsock config from the DB.
2. Checks for stale or active upgrade locks.
3. Constructs a `Client` with `OnUpgradeStarted` and `OnUpgradeCompleted`
   callbacks wired to the vsock repository's `SetUpgradeLock()` and
   `ClearUpgradeLock()`.
4. Returns the configured client.

This helper is used by exec, shell, and cp operations — all three benefit from
transparent upgrade without duplicating the setup logic.

## Alternatives considered

| Alternative | Rejected because |
|-------------|-----------------|
| **Explicit `mvm vm upgrade` command** | Requires user action; VMs can run outdated agents indefinitely |
| **Pre-built AMI-style VM images** | VMs are ephemeral; there is no image pipeline to bake into |
| **Agent auto-update daemon** | Adds complexity inside the guest; requires agent to reach an update server; duplicates the host's embedded agent logic |
| **Shared memory + signal** | Firecracker does not share memory between host and guest; vsock is the only IPC channel |
| **File lock instead of DB lock** | Does not survive crashes; not visible across processes; cannot do stale detection |
| **Version banner on CONNECT** | Changes the handshake protocol; makes version probe mandatory for all connections |
| **Separate migration file** | Not warranted pre-v1.0; schema is still settling |

## Design decisions

**Automatic upgrade over explicit command.** The agent version is a property of the host binary, not the VM. An explicit `mvm vm upgrade` command would require users to remember to run it. Automatic upgrade on connect eliminates this class of bug. The downside — a ~3-5s latency spike on first connect after host deployment — is acceptable because subsequent operations use the new agent immediately.

**Version request-response over header on CONNECT.** Adding version data to the CONNECT handshake would change the handshake format. A new request type keeps the handshake unchanged and makes the version probe an optional step — the connection is fully usable without it.

**DB lock over file lock.** The DB lock survives process crashes (no stale `.lock` files), is visible across concurrent `mvm` processes, and enables early rejection before any vsock dial attempt.

**Delayed restart over immediate restart.** The `systemctl restart` command is delayed by 2 seconds using `(sleep 2 && systemctl restart) &`. Without the delay, the agent is killed before the exec response frame (the result of the upgrade command) is sent back to the host.
