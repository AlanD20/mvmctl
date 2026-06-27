> **STATUS: Implemented.** Describes embedding a Go guest agent into the single `mvm` binary for vsock-based command execution and interactive shells, bypassing SSH.

# Vsock Exec Agent — `mvm exec`

## Problem

Currently, running commands inside a VM requires SSH:
- SSH needs the networking stack to be up (~1-2s after boot)
- SSH needs `sshd` running inside the guest
- SSH key injection adds ~200ms to pre-boot provisioning
- No interactive shell access before SSH is ready

For the `mvm env` workflow, the `exec:` step type already runs post-boot commands via vsock. But there's a gap: **no way to run commands or get a shell the instant the VM boots**, and no way to do it without a network dependency.

## Solution

A **Go guest agent** embedded into the `mvm` binary via `go:embed`, injected into the rootfs by both provisioner backends at VM creation time, listening on a vsock port for command execution and PTY shell sessions.

### Architecture

```
mvm exec my-vm -- ls -la /etc
       │
       ▼
┌──────────────────────────────────┐
│  API layer (pkg/api/exec.go)      │
│  op.Exec(ctx, input)              │
│    ├── vsock.NewClient(item, probeTimeout) │
│    │   ├── dial Firecracker UDS   │
│    │   ├── CONNECT handshake      │
│    │   └── JSON frame exchange    │
│    │                               │
│  op.VMCreate(ctx, input)          │
│    ├── provcontent.BuildVsockAgentOps()│
│    ├── vsock section in JSON cfg  │
│    └── vsockRepo.Upsert()         │
└──────────┬───────────────────────┘
           │ AF_VSOCK (Firecracker virtio-vsock device)
           ▼
┌──────────────────────────────────┐
│  Guest: mvm-vsock-agent           │
│  • Embedded Go binary             │
│  • Injected by loop-mount/guestfs │
│  • Runs as systemd service        │
│  • Listens on vsock port 1024     │
│  • JSON protocol:                 │
│    - exec (streaming stdout/      │
│      stderr frames)               │
│    - exec-tty (PTY shell)         │
│    - ping (heartbeat)             │
└──────────────────────────────────┘
```

## Domain Structure

```
internal/
├── core/
│   └── vsock/                    ← vsock domain
│       ├── client.go             ← NewClient(item), Exec/Shell/Teardown, ensureAgent, upgradeAgent
│       ├── file_transfer.go      ← FTCopyToVM, FTCopyFromVM, FTCopyVMToVM (binary frame protocol)
│       ├── repository.go         ← GetByVMID, Upsert, DeleteByVMID
│       ├── sqlite.go             ← SQLite implementation
│       ├── resolver.go           ← GetByVMID for enrichment
│       ├── service.go            ← Service: AllocateCID, PersistConfig (intra-domain orchestration)
│       ├── agent.go              ← AgentBinary() wrapper (delegates to vsockagent)
│       └── protocol.go           ← dial UDS, CONNECT, JSON framing (unexported)
├── service/
│   └── vsockagent/               ← guest agent binary + go:embed
│       ├── agent.go              ← Agent struct, Run(), vsock listener, vsockConn with SHUT_RDWR
│       ├── protocol.go           ← JSON frame types, read/write helpers
│       ├── exec.go               ← exec handler with streamingWriter (io.Writer)
│       ├── pty.go                ← exec-tty handler: PTY relay, exit detection, 5s kill timer
│       ├── cmdlistener.go        ← vsock connection dispatch: exec/tty/ping/version/file-transfer
│       ├── file_transfer.go      ← binary frame handler: handleFTPush, handleFTPull
│       ├── build_amd64.go        ← //go:embed agent-linux-amd64.zst (amd64 build tag)
│       ├── build_arm64.go        ← //go:embed agent-linux-arm64.zst (arm64 build tag)
│       └── cmd/
│           └── main.go           ← standalone entry point
└── infra/
    └── provcontent/
        └── content.go            ← add BuildVsockAgentOps() builder method
```

## Archived Decisions (Grill Session)

| Decision | Resolution |
|---|---|
| **Subprocess?** | No — vsock exec is ephemeral (connection lives as long as CLI command). No background process needed. Console relay is long-lived; vsock exec is not. |
| **Agent requires vm domain changes?** | No — agent is just FileOp + ChrootOp injected via provisioner. Same path as SSH keys. |
| **Naming** | `vsock.Client` not `vsock.Controller` — no entity lifecycle state transitions. Matches the concept: protocol-level client that dials UDS, CONNECT handshake, JSON framing. |
| **Protocol location** | Internal to `internal/core/vsock/` — not in `internal/lib/`. No other domain needs to dial vsock. |
| **Model types** | `model.VsockConfigItem` (DB record), `model.VsockConfig` (Firecracker JSON). Follows `Item` suffix convention for DB-persisted entities. |
| **DB table** | `vm_vsock_config` with `FOREIGN KEY (vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE` as safety net. Explicit `DeleteByVMID` for error-handling control. |
| **CID allocation** | Random from range 3–4294967295. Retry on DB collision (unique constraint). No allocator service needed. |
| **Port** | Configurable at VM creation via `--vsock-port` flag. Default 1024. |
| **UDS path** | `<vm_dir>/vsock.sock` — consistent with console's `<vm_dir>/relay.sock`. Absolute, explicit. |
| **Enrichment** | 1:1 reverse relation on VM. `VMRelations["vsock"]` in enricher — `vm.id → vsock_config.vm_id`. Resolver method `get_by_vm_id`. |
| **Teardown** | `vsock.Client.Teardown()` — delete stale socket. DB record deleted explicitly in API layer during VM removal, with CASCADE as fallback. |
| **Lifecycle** | vsock domain has no hooks in vm domain. API layer orchestrates setup in VMCreate, cleanup in VMRemove. |
| **Firecracker version gate** | Not needed — vsock requires Firecracker v1.0+. Minimum supported version in mvmctl is above that. |
| **Agent arch** | Matches host build arch. Build-tagged files (`build_amd64.go`, `build_arm64.go`) embed zstd-compressed binaries. `AgentBinary()` decompresses once via `sync.Once`. Build script cross-compiles both. |
| **FirecrackerClient location** | Stays in `internal/core/vm/` — NOT moved to `internal/lib/firecracker/`. vsock domain doesn't need the HTTP client. |
| **Vsock device config** | Via JSON config file only — vsock section in `FirecrackerVMConfig`. No `PUT /vsock` API call. Firecracker creates the device at boot. |
| **Auth token** | Random UUID generated at VM creation, stored in `VsockConfigItem.Token`. Agent accepts via flag (`-token`) or file (`/var/run/mvm-vsock-agent.token`). `exec`/`exec-tty` require token; `ping` does not. |
| **Agent binaries in git?** | No — built by `scripts/build.sh` before main binary. Not committed to the repo. `go build ./cmd/mvm` alone will fail without a prior agent build. |
| **Exec protocol** | Streaming: agent sends `"stdout"`/`"stderr"` frames as chunks arrive from pipes, final `"result"` on completion. Host reads in a loop with single `json.Decoder`. |
| **Vsock close reliability** | `vsockConn.Close()` calls `shutdown(fd, SHUT_RDWR)` before `close(fd)`. Raw `close(fd)` on virtio-vsock may race with socket teardown and leave host-side UDS open. |
| **Exit hang workaround** | Agent scans input for `exit`/`logout` + Enter and arms a 5s kill timer. If shell hasn't exited by then, SIGKILL forces cleanup. |

## VM Creation Sequence

```
op.VMCreate():

  1. Build provision ops:
     provcontent.BuildVsockAgentOps(agentBinary, port)
       ├── FileOp: /usr/bin/mvm-vsock-agent (agent binary, mode 0755)
       ├── FileOp: /etc/systemd/system/mvm-vsock-agent.service
        ├── FileOp: /etc/init.d/mvm-vsock-agent (OpenRC init script)
        └── ChrootOp: detect init system → enable agent

  2. Run provisioner (loop-mount or guestfs — both consume the same Operation types)

  3. Create Firecracker config JSON (includes vsock section in config file)

  4. Spawn Firecracker process (JSON config includes vsock section —
     Firecracker reads it at boot and creates the virtio-vsock device automatically)

  5. Persist vsock config:
     vsockRepo.Upsert(ctx, VsockConfigItem{
         VmID:     vm.ID,
         GuestCID: randomCID,
         UDSPath:  absUdsPath,
         Port:     port,
         Token:    crypto.UUIDV4(),
     })

  6. InstanceStart (via controller.Start)
```

### Build agent ops lives in `provcontent`

Added as a `Builder` method in `internal/infra/provcontent/content.go` — consumed by both loop-mount and guestfs backends:

```go
func (Builder) BuildVsockAgentOps(agentBinary []byte, port int, token string) []Operation {
    return []Operation{
        FileOp{
            Path: "/usr/bin/mvm-vsock-agent",
            Data: agentBinary,
            Mode: 0755,
        },
        FileOp{
            Path: "/var/run/mvm-vsock-agent.token",
            Data: []byte(token),
            Mode: 0600,
        },
        FileOp{
            Path: "/etc/systemd/system/mvm-vsock-agent.service",
            Data: fmt.Appendf(nil, `[Unit]
Description=MVM VSock Agent
DefaultDependencies=no

[Service]
Type=simple
ExecStart=/usr/bin/mvm-vsock-agent -port %d
Restart=always
RestartSec=2

[Install]
WantedBy=sysinit.target
`, port),
            Mode: 0644,
        },
        FileOp{
            Path: "/etc/init.d/mvm-vsock-agent",
            Data: fmt.Appendf(nil, `#!/sbin/openrc-run

description="MVM VSock Agent"

command=/usr/bin/mvm-vsock-agent
command_args="-port %d"
pidfile=/var/run/mvm-vsock-agent.pid
command_background=true

depend() {
    need localmount
}
`, port),
            Mode: 0755,
        },
        ChrootOp{
            Command: `
if command -v systemctl >/dev/null 2>&1; then
    mkdir -p /etc/systemd/system/multi-user.target.wants 2>/dev/null || true
    ln -sf /etc/systemd/system/mvm-vsock-agent.service /etc/systemd/system/multi-user.target.wants/mvm-vsock-agent.service 2>/dev/null || true
elif rc-update >/dev/null 2>&1; then
    rc-update add mvm-vsock-agent default
else
    echo "mvm: warning - unknown init system, mvm-vsock-agent not auto-enabled"
fi
`,
        },
    }
}
```

## Guest Agent Design

### Protocol

#### Firecracker vsock config

The vsock device is configured via the **Firecracker JSON config file** (not the runtime API). The JSON config is written to disk by `spawner.WriteToFile()` before Firecracker starts. Firecracker reads it at boot and creates the virtio-vsock device automatically — no separate PUT /vsock API call needed.

```json
{"vsock": {"guest_cid": 3, "uds_path": "/path/to/vm-dir/v.sock"}}
```

This is represented as `model.VsockConfig` in the `FirecrackerVMConfig.Vsock` field.

#### Host vsock handshake

After Firecracker creates the UDS, the host dials in using Firecracker's built-in CONNECT protocol:

```
Host → UDS: "CONNECT 1024\n"
Host ← UDS: "OK <host-side-port>\n"    ← connection established, bytes now flow to guest agent
                                         (host-side port is dynamically assigned by Firecracker,
                                          not the same as the requested guest port)
```

Implemented in `protocol.go` — no user-visible configuration needed.

#### JSON exchange (guest agent protocol)

Simple framed JSON messages, one per line (`\n` delimiter):

**Request:**
```json
{"id":"1","type":"exec","command":"ls -la /etc","timeout":10}
{"id":"2","type":"exec-tty","command":"/bin/bash","env":{"TERM":"xterm-256color"}}
{"id":"3","type":"ping"}
```

**Response (streaming exec):**

The `exec` request type streams output as the command runs. Stdout and stderr are sent as separate frames, followed by a final `result` frame with the exit code:

```json
{"id":"1","type":"stdout","data":"total 128\n"}
{"id":"1","type":"stdout","data":"drwxr-xr-x 2 root root 4096 ...\n"}
{"id":"1","type":"stderr","data":"warning: ...\n"}
{"id":"1","type":"result","status":0,"duration_ms":15}
```

The host reads frames in a loop, printing stdout/stderr to the terminal immediately and returning on the `result` frame. This provides real-time output for long-running commands like `apt update`.

**Response (non-streaming exec-tty):**

The `exec-tty` request sends a single TTY acknowledgement frame, then switches to raw bidirectional byte relay over the same connection (no further JSON framing):

```json
{"id":"2","type":"tty"}
```

**Ping:**
```json
{"id":"3","type":"pong"}
```

#### Auth token

Each VM gets a random UUID token at creation time, stored in `VsockConfigItem.Token`. The guest agent reads the token from **both** sources:
- **File (primary):** `/var/run/mvm-vsock-agent.token` written by `BuildVsockAgentOps()`. Used by the systemd/OpenRC service at startup.
- **Flag (debug):** `-token <value>` passed to the agent binary. Overrides the file when set. For debugging and manual agent runs only — the systemd unit does NOT use it.

The `exec-tty` request includes the token:
```json
{"id":"2","type":"exec-tty","command":"/bin/bash","token":"abc-123","env":{"TERM":"xterm-256color"}}
```

If the token doesn't match, the agent rejects with `{"error":"invalid auth token"}`. The host CLI reads the token from the DB and includes it in every exec/exec-tty request. No `ping` auth needed — it's a heartbeat, not a privileged operation.

### Error codes

| Code | When |
|------|------|
| `vsock.not_found` | VM has no vsock config |
| `vsock.connection_failed` | Can't dial Firecracker UDS |
| `vsock.handshake_failed` | CONNECT response is not "OK" |
| `vsock.agent_unreachable` | Agent doesn't respond to ping |
| `vsock.exec_failed` | Agent returned error status |

### Agent structure

```
internal/service/vsockagent/
├── agent.go        # Agent struct, Run(), vsock listener loop, vsockConn (Close uses SHUT_RDWR)
│                   # vsockConn.Close() uses shutdown(SHUT_RDWR) before close(fd) — ensures
│                   # VIRTIO_VSOCK_OP_SHUTDOWN is sent to Firecracker proxy, preventing host-side
│                   # UDS from lingering open.
├── protocol.go     # JSON frame types, read/write helpers, streaming type constants
├── exec.go         # exec handler: streaming via streamingWriter (io.Writer), output frames sent
│                   # as "stdout"/"stderr" types as chunks arrive, final "result" on completion.
├── pty.go          # exec-tty handler: PTY + relay, inactivity kill switch (5s after "exit" sent)
│                   # configurePTY sets ICRNL/ICANON/ECHO/ISIG on slave before shell start.
│                   # On "exit\n" detection, a 5s kill timer force-kills the shell if cmd.Wait()
│                   # doesn't return — prevents hangs when shell doesn't process exit.
├── cmdlistener.go  # vsock listener loop (accept, spawn handler goroutine)
├── build_amd64.go   //go:embed agent-linux-amd64.zst (amd64 build tag)
└── build_arm64.go   //go:embed agent-linux-arm64.zst (arm64 build tag)
```

**PTY handler** (`pty.go`) provides an interactive shell — no password authentication required. The agent runs as root inside the guest and can start any user's shell.

1. Allocate a PTY pair
2. Configure slave termios with `ICRNL | ICANON | ECHO | ECHOE | ISIG | OPOST | ONLCR` for proper interactive shell behavior across all kernel versions
3. Fork `su - <user>` (or `su - root`) with slave as TTY
4. Custom relay loops (not io.Copy) track data flow for "exit" command detection
5. When user types `exit` + Enter, a 5-second kill timer arms. If the shell exits normally, `cmd.Wait()` returns and the connection closes cleanly. If the shell is stuck, the timer fires, kills the shell with SIGKILL, and force-closes the connection.


### Embedding into the binary

```go
// internal/service/vsockagent/build_amd64.go  (//go:build amd64)
// internal/service/vsockagent/build_arm64.go  (//go:build arm64)
package vsockagent

import _ "embed"

//go:embed agent-linux-amd64.zst   // (or agent-linux-arm64.zst on arm64)
var agentBinaryZST []byte

// AgentBinary returns the decompressed agent binary, lazily decompressed
// once via sync.Once on first call.
func AgentBinary() []byte { ... }
```

The agent binary is **zstd-compressed** before embedding, saving ~60% in embedded binary size. Each architecture has its own build-tagged file. The `AgentBinary()` function decompresses once on first call using `sync.Once`.

Build process (in `scripts/build.sh`):

```bash
# Step 1: Cross-compile agent for both supported architectures
GOOS=linux GOARCH=amd64 go build \
  -o internal/service/vsockagent/agent-linux-amd64 \
  -ldflags="-s -w" \
  ./internal/service/vsockagent/cmd/

GOOS=linux GOARCH=arm64 go build \
  -o internal/service/vsockagent/agent-linux-arm64 \
  -ldflags="-s -w" \
  ./internal/service/vsockagent/cmd/

# Step 2: Compress with zstd
zstd -f internal/service/vsockagent/agent-linux-amd64
zstd -f internal/service/vsockagent/agent-linux-arm64

# Step 3: Build host binary (embeds compressed agent for target arch)
go build -o dist/mvm ./cmd/mvm
```

`scripts/build.sh` selects the correct embedded binary via build tags (the host's `GOARCH` determines which `build_*.go` file is compiled). The agent binary is ~3MB statically linked, compressed to ~1MB with zstd — negligible compared to the existing ~30MB `mvm` binary.

## Host Client — `mvm exec`

### CLI interface

```bash
# Run a command (captured output)
mvm exec my-vm -- ls -la /etc

# Interactive shell
mvm exec my-vm

# With timeout
mvm exec my-vm --timeout 30 -- apt-get update

# Specify vsock port
mvm exec my-vm --port 1025 -- /bin/bash

# Interactive shell as a different user
mvm exec my-vm --user ubuntu

# With custom port
mvm exec my-vm --port 1025 -- /bin/bash
```

### Host-side client flow

```
1. Resolve VM → get vsock config from vsockRepo.GetByVMID(vmID)
2. Dial Firecracker vsock UDS (cfg.UDSPath)
3. Send "CONNECT <port>\n" handshake
4. For one-shot exec:
   a. Send JSON exec request (include `user` field if `--user` flag specified)
   b. Streaming read loop using single json.Decoder:
      - "stdout" frame → write to os.Stdout immediately, accumulate
      - "stderr" frame → write to os.Stderr immediately, accumulate
      - "result" frame → return with exit code + accumulated output
5. For interactive shell:
   a. Send JSON exec-tty request (include `user` field if `--user` flag specified)
   b. Set terminal to raw mode
   c. Bidirectional relay: stdin → vsock (with `\r` → `\n` conversion via crFilter), vsock → stdout
```

### Integration points

| File | Change |
|------|--------|
| `internal/cli/exec.go` | New `mvm exec` cobra command (moved from `internal/cli/vm.go`). |
| `pkg/api/exec.go` | New `op.Exec(ctx, input)` method (moved from `pkg/api/vm.go`). |
| `pkg/api/inputs/exec_input.go` | `ExecInput{Identifier, Command, Port, Timeout, User, Env, NoSync}`. |
| `internal/core/vsock/` | Client, Repository, SQLite, Resolver, protocol. `Exec()` uses streaming read loop with `json.Decoder`. `crFilter` converts `\r` → `\n` for raw terminal input. `relayTTY` bidirectional relay for interactive shells. |
| `internal/service/vsockagent/` | Guest agent binary + go:embed. `exec.go` streams output via `streamingWriter` io.Writer (Stdout/Stderr fields). `pty.go` has PTY relay, exit command detection with 5s kill timer, and `configurePTY` for cross-kernel termios. `agent.go` uses `shutdown(SHUT_RDWR)` before `close(fd)` for reliable vsock close. |
| `internal/infra/provcontent/content.go` | Add `BuildVsockAgentOps()`. |
| `internal/enricher/enrich.go` | Add `VMRelations["vsock"]`. |
| `scripts/build.sh` | Add agent cross-compile step. Add `--arch` flag. |
| `internal/lib/db/migrations/` | New migration for `vm_vsock_config` table. |

## Model & Schema

### `model.VsockConfigItem` (DB record)

```go
type VsockConfigItem struct {
    ID               string     `db:"id" json:"id"`
    VmID             string     `db:"vm_id" json:"vm_id"`
    GuestCID         int        `db:"guest_cid" json:"guest_cid"`   // unique constraint
    UDSPath          string     `db:"uds_path" json:"uds_path"`
    Port             int        `db:"port" json:"port"`
    Token            string     `db:"token" json:"token"`
    AgentVersion     string     `db:"agent_version" json:"agent_version"`
    Upgrading        bool       `db:"upgrading" json:"upgrading"`
    UpgradeStartedAt *time.Time `db:"upgrade_started_at,omitempty" json:"upgrade_started_at,omitempty"`
}
```

### `model.VsockConfig` (Firecracker JSON)

```go
type VsockConfig struct {
    GuestCID int    `json:"guest_cid"`
    UDSPath  string `json:"uds_path"`
}
```

### DB schema

Add to existing `internal/lib/db/migrations/001_initial_schema.sql` — does NOT create a new migration file:

```sql
-- VSOCK_CONFIG: Per-VM vsock device and agent configuration
CREATE TABLE vm_vsock_config (
    id TEXT PRIMARY KEY,
    vm_id TEXT NOT NULL UNIQUE,
    guest_cid INTEGER NOT NULL UNIQUE,
    uds_path TEXT NOT NULL,
    port INTEGER NOT NULL,          -- no DEFAULT: caller provides the port
    token TEXT NOT NULL,             -- random UUID, used for agent auth
    agent_version TEXT NOT NULL DEFAULT '',  -- last known agent version
    upgrading INTEGER NOT NULL DEFAULT 0,    -- boolean: upgrade in progress
    upgrade_started_at TIMESTAMP NULL,       -- for stale lock detection
    FOREIGN KEY (vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE
);
CREATE INDEX idx_vsock_config_vm ON vm_vsock_config(vm_id);
CREATE INDEX idx_vsock_config_cid ON vm_vsock_config(guest_cid);
```

No `enabled` column — presence of a record means vsock is configured. No `DEFAULT` on port — default (`1024`) is overridable at the API/CLI layer via `--vsock-port` flag, not in the schema.

### `FirecrackerVMConfig` vsock field

The vsock section is added to the Firecracker JSON config so the device is created automatically when Firecracker reads the config at boot. No separate API call needed.

```go
type FirecrackerVMConfig struct {
    // ... existing fields ...

    Vsock *VsockConfig `json:"vsock,omitempty"`  // model.VsockConfig — guest_cid + uds_path only
}
```

### Enricher relation

```go
var VMRelations = map[string]model.RelationSpec{
    // ... existing relations ...

    "vsock": {
        FKField: "id", Resolver: "vsock",
        Method: "get_by_vm_id", RelationName: "vsock",
        IsReverse: true,
    },
}
```

Enrichable via `mvm vm inspect --include vsock`.

## Alternatives Considered

### Pure `socat` approach
Inject `socat` + a systemd unit instead of a custom agent. Simpler to implement but:
- `socat` not present in minimal images (Alpine: need `apk add socat`)
- No structured protocol — plain byte stream, can't multiplex commands
- No timeout control, no exit code reporting
- Window resize requires additional hacks

### Extend SSH with vsock ProxyCommand
Use `ssh -o ProxyCommand="socat UNIX-CONNECT:./v.sock STDIO"` with vsock as SSH transport. Proven pattern but:
- Still requires SSH daemon + key setup in guest
- SSH adds ~500ms connection overhead
- Need SSH in the image (not always present)

### Use the serial console
Already works via `mvm vm console my-vm`. But serial console is:
- Single session (one client at a time)
- No structured command output
- No exit codes
- No file transfer
