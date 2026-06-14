> **STATUS: Draft — design proposal.** Not yet implemented. Describes embedding a Go guest agent into the single `mvm` binary for vsock-based command execution and interactive shells, bypassing SSH.

# Vsock Exec Agent — `mvm vm exec`

## Problem

Currently, running commands inside a VM requires SSH:
- SSH needs the networking stack to be up (~1-2s after boot)
- SSH needs `sshd` running inside the guest
- SSH key injection adds ~200ms to pre-boot provisioning
- No interactive shell access before SSH is ready

For the `mvm env` workflow, the `ssh:` step type already runs post-boot commands. But there's a gap: **no way to run commands or get a shell the instant the VM boots**, and no way to do it without a network dependency.

## Solution

A **Go guest agent** embedded into the `mvm` binary via `go:embed`, injected into the rootfs by both provisioner backends at VM creation time, listening on a vsock port for command execution and PTY shell sessions.

### Architecture

```
mvm vm exec my-vm -- ls -la /etc
       │
       ▼
┌──────────────────────────────────┐
│  API layer (pkg/api/vm.go)        │
│  op.VMExec(ctx, input)            │
│    ├── vsock.NewClient(item)       │
│    │   ├── dial Firecracker UDS   │
│    │   ├── CONNECT handshake      │
│    │   └── JSON frame exchange    │
│    │                               │
│  op.VMCreate(ctx, input)          │
│    ├── provcontent.BuildVsockOps()│
│    ├── vsock section in JSON cfg  │
│    └── vsockRepo.Upsert()         │
└──────────┬───────────────────────┘
           │ AF_VSOCK (Firecracker virtio-vsock device)
           ▼
┌──────────────────────────────────┐
│  Guest: mvm-guest-agent           │
│  • Embedded Go binary             │
│  • Injected by loop-mount/guestfs │
│  • Runs as systemd service        │
│  • Listens on vsock port 1024     │
│  • JSON protocol:                 │
│    - exec (captured stdout)       │
│    - exec-tty (PTY shell)         │
│    - ping (heartbeat)             │
└──────────────────────────────────┘
```

## Domain Structure

```
internal/
├── core/
│   └── vsock/                    ← new domain
│       ├── client.go             ← NewClient(item), Exec/Shell/Teardown
│       ├── repository.go         ← GetByVMID, Upsert, DeleteByVMID
│       ├── sqlite.go             ← SQLite implementation
│       ├── resolver.go           ← GetByVMID for enrichment
│       └── protocol.go           ← dial UDS, CONNECT, JSON framing (unexported)
├── guest/
│   └── vsockagent/               ← guest agent binary + go:embed
│       ├── agent.go
│       ├── protocol.go
│       ├── exec.go
│       ├── pty.go
│       └── build.go              ← //go:embed agent-linux-{amd64,arm64}
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
| **UDS path** | Absolute, explicit — consistent with existing model paths (APISocketPath, ConfigPath, PIDPath). |
| **Enrichment** | 1:1 reverse relation on VM. `VMRelations["vsock"]` in enricher — `vm.id → vsock_config.vm_id`. Resolver method `get_by_vm_id`. |
| **Teardown** | `vsock.Client.Teardown()` — delete stale socket. DB record deleted explicitly in API layer during VM removal, with CASCADE as fallback. |
| **Lifecycle** | vsock domain has no hooks in vm domain. API layer orchestrates setup in VMCreate, cleanup in VMRemove. |
| **Firecracker version gate** | Not needed — vsock requires Firecracker v1.0+. Minimum supported version in mvmctl is above that. |
| **Agent arch** | Matches host build arch. `--arch` flag (default `amd64`, option `arm64`) determines which embedded binary is used. Build script cross-compiles both. |
| **FirecrackerClient location** | Stays in `internal/core/vm/` — NOT moved to `internal/lib/firecracker/`. vsock domain doesn't need the HTTP client. |
| **Vsock device config** | Via JSON config file only — vsock section in `FirecrackerVMConfig`. No `PUT /vsock` API call. Firecracker creates the device at boot. |
| **Auth token** | Random UUID generated at VM creation, stored in `VsockConfigItem.Token`. Agent accepts via flag (`-token`) or file (`/etc/mvm-guest-agent.token`). `exec`/`exec-tty` require token; `ping` does not. |
| **Agent binaries in git?** | No — built by `scripts/build.sh` before main binary. Not committed to the repo. `go build ./cmd/mvm` alone will fail without a prior agent build. |

## VM Creation Sequence

```
op.VMCreate():

  1. Build provision ops:
     provcontent.BuildVsockAgentOps(agentBinary, port)
       ├── FileOp: /usr/bin/mvm-guest-agent (agent binary, mode 0755)
       ├── FileOp: /etc/systemd/system/mvm-guest-agent.service
        ├── FileOp: /etc/init.d/mvm-guest-agent (OpenRC init script)
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
            Path: "/usr/bin/mvm-guest-agent",
            Data: agentBinary,
            Mode: 0755,
        },
        FileOp{
            Path: "/etc/mvm-guest-agent.token",
            Data: []byte(token),
            Mode: 0644,
        },
        FileOp{
            Path: "/etc/systemd/system/mvm-guest-agent.service",
            Data: []byte(fmt.Sprintf(`[Unit]
Description=MVM Guest Agent

[Service]
Type=simple
ExecStart=/usr/bin/mvm-guest-agent -port %d
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`, port)),
            Mode: 0644,
        },
        FileOp{
            Path: "/etc/init.d/mvm-guest-agent",
            Data: []byte(fmt.Sprintf(`#!/bin/sh
# OpenRC init script for mvm-guest-agent
case "$1" in
  start) /usr/bin/mvm-guest-agent -port %d & ;;
  stop)  pkill -f "mvm-guest-agent -port %d" || true ;;
  *)     echo "Usage: $0 {start|stop}"; exit 1 ;;
esac
`, port, port)),
            Mode: 0755,
        },
        ChrootOp{
            Command: `
if command -v systemctl >/dev/null 2>&1; then
    systemctl enable mvm-guest-agent
elif rc-update >/dev/null 2>&1; then
    rc-update add mvm-guest-agent default
else
    echo "mvm: warning - unknown init system, mvm-guest-agent not auto-enabled"
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
Host ← UDS: "OK 1024\n"         ← connection established, bytes now flow to guest agent
```

Implemented in `protocol.go` — no user-visible configuration needed.

#### JSON exchange (guest agent protocol)

Simple framed JSON messages, one per line (`\n` delimiter):

**Request:**
```json
{"id":"1","type":"exec","command":"ls -la /etc","timeout":10}
{"id":"2","type":"exec-tty","cmd":"/bin/bash","env":{"TERM":"xterm-256color"}}
{"id":"3","type":"ping"}
```

**Response:**
```json
{"id":"1","type":"result","status":0,"stdout":"...","stderr":"","duration_ms":15}
{"id":"2","type":"tty","error":"no auth token"}
{"id":"3","type":"pong"}
```

#### Auth token

Each VM gets a random UUID token at creation time, stored in `VsockConfigItem.Token`. The guest agent reads the token from **both** sources:
- **File (primary):** `/etc/mvm-guest-agent.token` written by `BuildVsockAgentOps()`. Used by the systemd/OpenRC service at startup.
- **Flag (debug):** `-token <value>` passed to the agent binary. Overrides the file when set. For debugging and manual agent runs only — the systemd unit does NOT use it.

The `exec-tty` request includes the token:
```json
{"id":"2","type":"exec-tty","cmd":"/bin/bash","token":"abc-123","env":{"TERM":"xterm-256color"}}
```

If the token doesn't match, the agent rejects with `{"error":"no auth token"}`. The host CLI reads the token from the DB and includes it in every exec/exec-tty request. No `ping` auth needed — it's a heartbeat, not a privileged operation.

### Agent structure

```
internal/guest/vsockagent/
├── agent.go        # main(): listen on vsock, dispatch
├── protocol.go     # JSON frame types, read/write helpers
├── exec.go         # exec handler: cmd.Run() captured
├── pty.go          # exec-tty handler: PTY + relay
├── cmdlistener.go  # vsock listener loop (accept, spawn handler goroutine)
└── build.go        //go:embed marker + build target
```

**PTY handler** (`pty.go`) provides an interactive shell — no password authentication required. The agent runs as root inside the guest and can start any user's shell.

1. Allocate a PTY pair
2. Set raw mode via `term.MakeRaw` on stdin
3. Fork `exec.Command("/bin/bash")` (or default shell) with slave as TTY
4. If `--user` flag is passed, use `su - <user>` or `runuser` to switch (still passwordless)
5. Relay PTY master ↔ vsock (raw mode)
6. Handle SIGWINCH for terminal resize:
   ```go
   sigwinch := make(chan os.Signal, 1)
   signal.Notify(sigwinch, syscall.SIGWINCH)
   go func() {
       for range sigwinch {
           rows, cols, _ := term.GetSize(int(os.Stdout.Fd()))
           pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
       }
   }()
   ```

### Embedding into the binary

```go
// internal/guest/vsockagent/build.go
package vsockagent

import _ "embed"

//go:embed agent-linux-amd64
var BinaryAmd64 []byte

//go:embed agent-linux-arm64
var BinaryArm64 []byte
```

Build process (added to `scripts/build.sh`):

```bash
# Step 1: Cross-compile agent for both supported architectures
GOOS=linux GOARCH=amd64 go build \
  -o internal/guest/vsockagent/agent-linux-amd64 \
  -ldflags="-s -w" \
  ./internal/guest/vsockagent/cmd/

GOOS=linux GOARCH=arm64 go build \
  -o internal/guest/vsockagent/agent-linux-arm64 \
  -ldflags="-s -w" \
  ./internal/guest/vsockagent/cmd/

# Step 2: Build host binary (embeds agent for target arch)
go build -o dist/mvm ./cmd/mvm
```

`scripts/build.sh` selects the correct embedded binary via `--arch` flag (default `amd64`). The agent binary is ~3MB statically linked — negligible compared to the existing ~30MB `mvm` binary.

## Host Client — `mvm vm exec`

### CLI interface

```bash
# Run a command (captured output)
mvm vm exec my-vm -- ls -la /etc

# Interactive shell
mvm vm exec my-vm

# With timeout
mvm vm exec my-vm --timeout 30 -- apt-get update

# Specify vsock port
mvm vm exec my-vm --port 1025 -- /bin/bash

# Without agent (if VM was created with --no-vsock)
```

### CLI flags on `mvm vm create`

```bash
mvm vm create my-vm \
  --no-vsock                # Skip agent injection and vsock device
  --vsock-port 1024         # Default: 1024
```

### Host-side client flow

```
1. Resolve VM → get vsock config from vsockRepo.GetByVMID(vmID)
2. Dial Firecracker vsock UDS (cfg.UDSPath)
3. Send "CONNECT <port>\n" handshake
4. For one-shot exec:
   a. Send JSON exec request
   b. Read JSON result
   c. Print stdout/stderr, exit with status code
5. For interactive shell:
   a. Send JSON exec-tty request
   b. Set terminal to raw mode
   c. Bidirectional relay: stdin ↔ vsock, vsock ↔ stdout
   d. Handle SIGWINCH (send window resize JSON)
```

### Integration points

| File | Change |
|------|--------|
| `internal/cli/vm.go` | New `mvm vm exec` cobra command. Add `--no-vsock`, `--vsock-port` flags on create. |
| `pkg/api/vm.go` | New `op.VMExec(ctx, input)` method. Inject vsock setup into `VMCreate`. |
| `pkg/api/inputs/vm.go` | `VMExecInput{Identifiers, Command, Port, Timeout}`. `VMInput{VmCreateFlags..., VsockPort}`. |
| `internal/core/vsock/` | New domain — Client, Repository, SQLite, Resolver, protocol. |
| `internal/guest/vsockagent/` | Guest agent binary + go:embed. |
| `internal/infra/provcontent/content.go` | Add `BuildVsockAgentOps()`. |
| `internal/enricher/enrich.go` | Add `VMRelations["vsock"]`. |
| `scripts/build.sh` | Add agent cross-compile step. Add `--arch` flag. |
| `internal/lib/db/migrations/` | New migration for `vm_vsock_config` table. |

## Model & Schema

### `model.VsockConfigItem` (DB record)

```go
type VsockConfigItem struct {
    ID       string `db:"id" json:"id"`
    VmID     string `db:"vm_id" json:"vm_id"`
    GuestCID int    `db:"guest_cid" json:"guest_cid"`   // unique constraint
    UDSPath  string `db:"uds_path" json:"uds_path"`
    Port     int    `db:"port" json:"port"`
    Token    string `db:"token" json:"token"`
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
