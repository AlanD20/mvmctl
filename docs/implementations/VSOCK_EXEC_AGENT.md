# Vsock Exec Agent — `mvm exec`

## Problem

Running commands inside a Firecracker microVM normally requires SSH, which has several limitations. SSH needs the networking stack to be up (~1-2s after boot), requires `sshd` running inside the guest, and adds ~200ms of key injection overhead. There is no way to run commands or get a shell before SSH is ready, and no way to do structured command execution with exit code reporting without SSH. The vsock exec agent solves this by embedding a lightweight Go binary into the VM rootfs that listens on a vsock port for command execution and PTY shell sessions.

## Architecture

```
mvm exec my-vm -- ls -la /etc
       │
       ▼
┌──────────────────────────────────┐
│  API layer (pkg/api/exec.go)     │
│  op.Exec(ctx, input)             │
│    └── vsock.NewClient(item,...) │
│        └── dial Firecracker UDS  │
│        └── CONNECT handshake     │
│        └── JSON frame exchange   │
│                                  │
│  op.VMCreate(ctx, input)         │
│    └── provcontent.BuildAgentOps()│
│    └── vsock section in JSON cfg │
│    └── vsockRepo.Upsert()        │
└──────────┬───────────────────────┘
           │ AF_VSOCK (Firecracker virtio-vsock device)
           ▼
┌──────────────────────────────────┐
│  Guest: mvm-agent          │
│  • Embedded Go binary            │
│  • Injected by loop-mount/guestfs│
│  • Runs as systemd/OpenRC service│
│  • Listens on vsock port 1024    │
│  • JSON protocol:                │
│    - exec (streaming stdout/     │
│      stderr frames)              │
│    - exec-tty (PTY shell)        │
│    - ping (heartbeat)            │
│    - version (agent version)     │
│    - file-transfer (binary frame)│
└──────────────────────────────────┘
```

## Entry point

The agent is embedded into the VM rootfs at creation time. During `op.VMCreate()`, the API layer calls `provcontent.BuildAgentOps()` in `internal/infra/provcontent/content.go`, which produces five operations: the agent binary at `/usr/bin/mvm-agent`, an auth token at `/var/run/mvm-agent.token`, a systemd unit at `/etc/systemd/system/mvm-agent.service`, an OpenRC init script at `/etc/init.d/mvm-agent`, and a chroot command to enable the agent in the detected init system. Both provisioner backends (loop-mount and guestfs) consume the same operation types.

The agent starts automatically when the VM boots. `Agent.Run()` in `internal/service/agent/agent.go` creates a raw AF_VSOCK socket on the configured port, binds with `VMADDR_CID_ANY`, listens, and accepts connections in a loop — dispatching each to `handleConnection()`.

`mvm exec` from the host triggers `op.Exec()` in `pkg/api/exec.go`, which creates a `vsock.Client` via `vsock.NewClient()`, dials the Firecracker vsock UDS, performs the CONNECT handshake, and exchanges JSON frames with the agent.

## Happy path

### 1. Agent injection at VM creation

`provcontent.BuildAgentOps()` generates:
- `FileOp` at `/usr/bin/mvm-agent` with the compressed agent binary (mode 0755)
- `FileOp` at `/var/run/mvm-agent.token` with a random UUID auth token (mode 0600)
- `FileOp` at `/etc/systemd/system/mvm-agent.service` — systemd unit:
  ```ini
  [Unit]
  Description=MVM VSock Agent
  DefaultDependencies=no

  [Service]
  Type=simple
  ExecStart=/usr/bin/mvm-agent -port <port>
  Restart=always
  RestartSec=2

  [Install]
  WantedBy=sysinit.target
  ```
- `FileOp` at `/etc/init.d/mvm-agent` — OpenRC init script:
  ```sh
  #!/sbin/openrc-run

  description="MVM VSock Agent"

  command=/usr/bin/mvm-agent
  command_args="-port <port>"
  pidfile=/var/run/mvm-agent.pid
  command_background=true

  depend() {
      need localmount
  }
  ```
- `ChrootOp` that detects the init system and enables the agent:
  ```sh
  if command -v systemctl >/dev/null 2>&1; then
      ln -sf /etc/systemd/system/mvm-agent.service \
          /etc/systemd/system/multi-user.target.wants/mvm-agent.service
  elif rc-update >/dev/null 2>&1; then
      rc-update add mvm-agent default
  fi
  ```

The vsock device is configured via the Firecracker JSON config file (`model.VsockConfig` with `guest_cid` and `uds_path`), written to disk before Firecracker starts. The vsock config is persisted to the `vm_vsock_config` database table via `vsockRepo.Upsert()`.

### 2. Agent startup inside the guest

On VM boot, the init system starts `mvm-agent`. `Agent.Run()` creates a raw AF_VSOCK socket, sets `SO_REUSEADDR`, binds with `VMADDR_CID_ANY`, listens with backlog 10, and accepts connections. Each connection is handled in its own goroutine via `handleConnection()`.

### 3. Host dial and handshake

The host client calls `dialAndHandshake()` in `internal/core/vsock/protocol.go`, which:
1. Dials the Firecracker UDS (`unix` socket at `<vm-dir>/vsock.sock`)
2. Sends `CONNECT <port>\n` (port defaults to 1024)
3. Reads the response — Firecracker responds with `OK <host-side-port>\n`, where the host-side port is dynamically assigned
4. On success, clears the read deadline and returns the connection

### 4. Command execution (one-shot)

The `vsock.Client.Exec()` method sends a JSON frame with type `"exec"`, the command, and optional timeout/user/env. The agent's `handleExec()` in `internal/service/agent/exec.go`:
1. Creates an `exec.Cmd` — either `su - <user> -c <command>` for non-root users, or `sh -c <command>` for root
2. Sets `cmd.Stdout` and `cmd.Stderr` to `streamingWriter` instances, which emit JSON frames ("stdout" / "stderr") as data arrives
3. Starts the command, waits for completion, flushes remaining output
4. Sends a `"result"` frame with exit code and duration
5. Calls `unix.Sync()` unless `NoSync` is set, to flush Firecracker's writeback cache

The host reads frames in a loop, printing stdout/stderr to the terminal immediately and returning on the `result` frame.

### 5. Interactive shell (exec-tty)

The `vsock.Client.Shell()` method sends a JSON frame with type `"exec-tty"`. The agent's `handleTTY()` in `internal/service/agent/pty.go`:
1. Sends a `"tty"` acknowledgement frame
2. Allocates a PTY pair
3. Configures the slave termios with `ICRNL | ICANON | ECHO | ECHOE | ISIG | OPOST | ONLCR`
4. Forks the shell on the slave side
5. Relays bytes bidirectionally between the vsock connection and the PTY master
6. Detects `"exit\n"` in the data flow and arms a 5-second kill timer — SIGKILL forces cleanup if the shell doesn't exit

The host switches to raw terminal mode and enters a bidirectional relay loop (`relayTTY`) with `\r` → `\n` conversion for raw terminal input.

### 6. Ping (heartbeat)

The host sends a JSON frame with type `"ping"`. The agent responds with `"pong"`. No authentication required.

## Agent binary embedding

The agent binary is cross-compiled for `linux/amd64` and `linux/arm64`, zstd-compressed, and embedded via `//go:embed` in build-tagged files:
- `internal/service/agent/build_amd64.go` builds on `//go:build amd64`, embeds `agent-linux-amd64.zst`
- `internal/service/agent/build_arm64.go` builds on `//go:build arm64`, embeds `agent-linux-arm64.zst`

`AgentBinary()` decompresses once on first call via `sync.Once`, saving ~60% in embedded binary size.

Build script (`scripts/build.sh`):
1. Cross-compiles the agent for both architectures
2. Compresses with `zstd`
3. Builds the host binary (embeds the compressed agent for the host's arch)

## Auth token

Each VM gets a random UUID token at creation time, stored in `VsockConfigItem.Token`. The agent reads the token from `/var/run/mvm-agent.token` (written by `BuildAgentOps()`). The `exec` and `exec-tty` requests include the token; `ping` does not. If the token doesn't match (checked via `subtle.ConstantTimeCompare`), the agent rejects with `{"error":"invalid auth token"}`.

## Failure modes

### Agent not reachable

The vsock client probes the agent with 20ms intervals up to `ProbeTimeout` (default 5s, configurable via `defaults.vm.vsock_probe_timeout`). If the agent doesn't respond, the error `vsock.agent_unreachable` is returned.

### CONNECT handshake failure

If Firecracker's vsock proxy doesn't respond to `CONNECT` within the timeout, or responds with something other than `OK ...`, the error `vsock.handshake_failed` is returned.

### Command timeout

The `exec` request type supports a `timeout` field in seconds. The agent uses `context.WithTimeout` — if the command doesn't complete within the deadline, the context is cancelled, the process is killed, and a `result` frame with status -1 and error description is sent.

### Exit hang workaround for exec-tty

When the user types `exit` + Enter, the PTY handler arms a 5-second kill timer. If the shell exits normally, `cmd.Wait()` returns and the connection closes cleanly. If the shell is stuck, the timer fires, kills the shell with SIGKILL, and force-closes the connection.

### Vsock close reliability

`vsockConn.Close()` calls `shutdown(fd, SHUT_RDWR)` before `close(fd)`. A raw `close(fd)` on virtio-vsock may race with socket teardown and leave the host-side UDS connection open, causing the host's `conn.Read()` to block indefinitely. `shutdown(SHUT_RDWR)` ensures the VIRTIO_VSOCK_OP_SHUTDOWN packet is sent before the socket is freed.

### Error codes

| Code | Class | When |
|------|-------|------|
| `vsock.not_found` | `ClassValidation` | VM has no vsock config |
| `vsock.connection_failed` | `ClassInternal` | Cannot dial Firecracker UDS |
| `vsock.handshake_failed` | `ClassInternal` | CONNECT response is not "OK" |
| `vsock.agent_unreachable` | `ClassRetryable` | Agent does not respond to ping within probe timeout |

## Key files

| File | Purpose |
|------|---------|
| `internal/service/agent/agent.go` | `Agent` struct, `Run()`, vsock listener, `vsockConn` with `SHUT_RDWR` close |
| `internal/service/agent/protocol.go` | JSON frame types, `readFrame`/`writeFrame`, request/response type constants |
| `internal/service/agent/exec.go` | `handleExec()` — streaming command execution via `streamingWriter` |
| `internal/service/agent/pty.go` | `handleTTY()` — PTY shell, `configurePTY()` termios, exit detection + 5s kill timer |
| `internal/service/agent/cmdlistener.go` | `handleConnection()` — dispatch to exec/tty/ping/version/file-transfer |
| `internal/service/agent/file_transfer.go` | Binary frame protocol handler for push/pull/recursive pull |
| `internal/service/agent/build_amd64.go` | `//go:embed agent-linux-amd64.zst` (amd64 build tag) |
| `internal/service/agent/build_arm64.go` | `//go:embed agent-linux-arm64.zst` (arm64 build tag) |
| `internal/service/agent/cmd/main.go` | Standalone agent entry point |
| `internal/core/vsock/client.go` | `Client` — `Exec()`, `Shell()`, `ensureAgent()`, `Teardown()` |
| `internal/core/vsock/protocol.go` | `dialAndHandshake()`, JSON framing helpers |
| `internal/core/vsock/file_transfer.go` | `FTCopyToVM()`, `FTCopyFromVM()`, `FTCopyVMToVM()` |
| `internal/core/vsock/service.go` | `Service` — CID allocation, config persistence |
| `internal/core/vsock/agent.go` | `AgentBinary()` — wrapper delegating to `vsockagent` |
| `internal/infra/provcontent/content.go` | `BuildAgentOps()` — generates injection operations |
| `pkg/api/exec.go` | `op.Exec()` — API orchestration |
| `internal/cli/exec.go` | `mvm exec` cobra command |

## Design decisions

**Embedded Go agent over socat or SSH.** A pure `socat` approach would avoid the custom agent binary but breaks on minimal images (Alpine needs `apk add socat`), provides no structured protocol, and cannot report exit codes. SSH over vsock ProxyCommand still requires SSH daemon + key setup and adds ~500ms connection overhead.

**Agent injected via provisioner, not VM domain changes.** The agent is injected as FileOp + ChrootOp operations — the same mechanism used for SSH keys and cloud-init files. No VM domain changes are needed.

**Auth token via file, not flag.** The token is written to `/var/run/mvm-agent.token` by the provisioner, so the systemd unit doesn't need the token on the command line (avoiding `/proc` exposure). A `-token` flag exists for debugging but is not used by the init system.

## Model and schema

### `model.VsockConfigItem` (DB record)

```go
type VsockConfigItem struct {
    ID               string     `db:"id" json:"id"`
    VmID             string     `db:"vm_id" json:"vm_id"`
    GuestCID         int        `db:"guest_cid" json:"guest_cid"`
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

The vsock section is added to the Firecracker JSON config so the device is
created when Firecracker reads the config at boot:

```go
type FirecrackerVMConfig struct {
    // ... existing fields ...
    Vsock *VsockConfig `json:"vsock,omitempty"`
}
```

### DB schema

The `vm_vsock_config` table is created in
`internal/lib/db/migrations/001_initial_schema.sql`:

```sql
CREATE TABLE vm_vsock_config (
    id TEXT PRIMARY KEY,
    vm_id TEXT NOT NULL UNIQUE,
    guest_cid INTEGER NOT NULL UNIQUE,
    uds_path TEXT NOT NULL,
    port INTEGER NOT NULL,
    token TEXT NOT NULL,
    agent_version TEXT NOT NULL DEFAULT '',
    upgrading INTEGER NOT NULL DEFAULT 0,
    upgrade_started_at TIMESTAMP NULL,
    FOREIGN KEY (vm_id) REFERENCES vm_instances(id) ON DELETE CASCADE
);
CREATE INDEX idx_vsock_config_vm ON vm_vsock_config(vm_id);
CREATE INDEX idx_vsock_config_cid ON vm_vsock_config(guest_cid);
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

Enrichable in the CLI via `mvm vm inspect --include vsock`.

## Alternatives considered

### Pure `socat` approach

Inject `socat` plus a systemd unit instead of a custom agent. Simpler to
implement but:

- `socat` is not present in minimal images (Alpine requires `apk add socat`)
- No structured protocol — plain byte stream, cannot multiplex commands
- No timeout control, no exit code reporting
- Window resize requires additional hacks

### Extend SSH with vsock ProxyCommand

Use `ssh -o ProxyCommand="socat UNIX-CONNECT:./v.sock STDIO"` with vsock as
SSH transport. This is a proven pattern but:

- Still requires SSH daemon and key setup inside the guest
- SSH adds ~500ms connection overhead per operation
- SSH must be present in the image (not always the case on minimal builds)

### Use the serial console

Already works via `mvm console my-vm`. But the serial console is:
- Single session — one client at a time
- No structured command output
- No exit codes
- No file transfer
