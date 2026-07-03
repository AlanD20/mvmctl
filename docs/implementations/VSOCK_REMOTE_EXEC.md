# Vsock Remote Exec — Inter-VM Command Execution

## Problem

VM A needs to run a command inside VM B without SSH. The existing `mvm exec` opens a vsock connection from host to guest over which the guest agent streams stdout/stderr frames back. The host already has all the infrastructure: it knows how to resolve VMs, fetch vsock configs from the DB, and create vsock clients to any running VM. The missing piece is allowing the guest agent to **send a frame back** through the existing vsock connection requesting execution on another VM, with the host acting as relay.

## Design

### Core insight

`mvm exec vm-a` opens a vsock connection (host → guest). That connection is full-duplex. The guest agent already writes response frames (`"stdout"`, `"stderr"`, `"result"`) back to the host through it. The same channel can carry a new frame type — `"remote_vm"` — that tells the host: "run this command on VM B and stream the output back to me."

### Data flow (step by step)

User runs:

```
mvm exec vm-a -- mvm-vsock-agent remote vm-b -- ls -la
```

| Step | Location | What happens |
|------|----------|-------------|
| 1 | Host `mvm` | Opens vsock to VM A, sends `{"type":"exec","command":"mvm-vsock-agent remote vm-b -- ls -la"}` |
| 2 | Guest agent daemon (VM A) | `handleConnection` reads exec frame → `handleExec` runs |
| 3 | Guest agent daemon (VM A) | `handleExec` calls `exec.Command("sh", "-c", "mvm-vsock-agent remote vm-b -- ls -la")`, blocks on `cmd.Wait()` |
| 4 | Child process (VM A) | `mvm-vsock-agent remote` CLI connects to daemon's local Unix socket at `/var/run/mvm-vsock-agent.sock` |
| 5 | Guest agent daemon (VM A) | Background goroutine `handleLocalConn` reads JSON frame from local socket |
| 6 | Guest agent daemon (VM A) | Locks `connMu`, writes `{"type":"remote_vm","data":"{\"destination\":\"vm-b\",\"command\":\"ls -la\"}"}` to the **same vsock connection** |
| 7 | Host `mvm` | `Client.Exec()` read loop receives the frame, `resp.Type == "remote_vm"` |
| 8 | Host `mvm` | Checks source VM A has `remote_exec == true` (fetched via `c.item.VmID`) — if not, rejects immediately |
| 9 | Host `mvm` | Parses `frame.Data` as JSON → extracts `destination`, `command`, `user`, `timeout` |
| 10 | Host `mvm` | Resolves "vm-b" → gets target VM B's record |
| 11 | Host `mvm` | Checks target VM B has `remote_exec == true` and status is `running` |
| 12 | Host `mvm` | Fetches VM B's vsock config from `vm_vsock_config` table |
| 13 | Host `mvm` | Opens new vsock connection to VM B via `dialAndHandshake()`, sends `exec` frame |
| 14 | Host `mvm` | **Streaming relay loop:** for each frame from VM B: `"stdout"` → write `{"type":"stdout","data":"..."}` to VM A's connection immediately; `"stderr"` → same; `"result"` → write `{"type":"remote_vm","status":<exit_code>}` to VM A, then close VM B's connection and return |
| 15 | Guest agent daemon (VM A) | `handleLocalConn` reads those response frames from vsock (handleExec is blocked in `cmd.Wait()`, not reading), forwards them to local socket client in real time |
| 16 | Child process (VM A) | Receives output via local socket, prints to its own stdout/stderr, exits |
| 17 | Guest agent daemon (VM A) | `cmd.Wait()` returns, `handleExec` flushes remaining output, sends final `"result"` frame, returns |
| 18 | Host `mvm` | Reads final `"result"` frame, returns `ExecResult` to caller |

### Protocol frames

**Guest → Host (via existing vsock exec connection):**
```json
{"type":"remote_vm","data":"{\"destination\":\"vm-b\",\"command\":\"ls -la\",\"user\":\"root\",\"timeout\":30}"}
```

**Host → Guest (relayed response, streaming):**
```json
{"type":"stdout","data":"file1\nfile2\n"}
{"type":"stderr","data":"warning: ..."}
{"type":"remote_vm","status":0}
```

The host uses `"remote_vm"` (not `"result"`) as the final response type so the guest's `handleConnection` dispatch loop does not confuse it with the response of the original exec command.

### Frame relay format

The `Data` field of a `"remote_vm"` request from guest contains JSON-encoded parameters:

| Field | Type | Description |
|-------|------|-------------|
| `destination` | string | Target VM name or ID |
| `command` | string | Shell command to run |
| `user` | string | Run as this user (default "root") |
| `timeout` | int | Timeout in seconds (0 = no timeout) |

### Protocol constants

Both the guest agent (`internal/service/vsockagent/protocol.go`) and the host vsock package (`internal/core/vsock/protocol.go`) define the same `"remote_vm"` frame type constant.

### Exported protocol primitives

`internal/core/vsock/protocol.go` exports three functions for external consumers like the handler package:

- `SendFrame(conn, v)` — writes any value as a newline-delimited JSON frame
- `ReadFrame(conn)` — reads one frame, returns its type string and raw data bytes
- `DialVM(ctx, udsPath, port)` — connects to a VM's vsock via UDS + CONNECT handshake

## Security model

**Disabled by default.** Both the source VM and the target VM must have `remote_exec = true` to participate.

**`--allow-remote-exec` flag on `mvm vm create`:**
```bash
mvm vm create my-vm --image ubuntu-noble --allow-remote-exec
```

Persisted as a `remote_exec` boolean column on `vm_instances` (default `0`). The flag means both:
- This VM can **send** remote exec commands to other VMs
- This VM can **accept** remote exec commands from other VMs

**Config default** is configurable via `defaults.vm.allow_remote_exec` in the project YAML config (default `false`). The CLI flag overrides the config.

**Host checks both:**
```
source_vm.remote_exec == true AND target_vm.remote_exec == true → proceed
```

Source VM is checked **before** parsing the frame payload — fail fast on unauthorized sources.

**All remote execs are logged** with structured `slog` fields: `source_vm`, `target_vm`, `command`, `exit_code`, `error`.

### Guest agent architecture

The guest agent daemon runs two listeners concurrently:

```
┌───────────────────────────────────────────┐
│          Guest Agent Daemon               │
│                                           │
│  ┌─────────────────┐  ┌────────────────┐  │
│  │ AF_VSOCK listener│  │ Local Unix     │  │
│  │ (existing)       │  │ socket listener│  │
│  │ port 1024        │  │ (new)          │  │
│  │                  │  │ /var/run/      │  │
│  │                  │  │ mvm-vsock-agent│  │
│  │                  │  │ .sock          │  │
│  └────────┬─────────┘  └───────┬────────┘  │
│           │                    │            │
│           ▼                    ▼            │
│  ┌─────────────────────────────────────┐    │
│  │       Dispatch / shared conn        │    │
│  │  handleConnection reads vsock       │    │
│  │  handleLocalConn reads local socket │    │
│  │                                     │    │
│  │  Both share the same vsock conn     │    │
│  │  via activeConn + connMu sync       │    │
│  └─────────────────────────────────────┘    │
└───────────────────────────────────────────┘
```

- The vsock listener (`handleConnection`) handles requests from the host — `exec`, `ping`, etc.
- The local socket listener (`handleLocalConn`) handles requests from inside the VM — `remote_vm`
- Both share access to `activeConn` (the current vsock connection) via `activeConnMu` and `connMu`

### Guest agent CLI subcommand

`mvm-vsock-agent remote <destination> -- <command>` is a one-shot CLI mode:

```bash
mvm-vsock-agent remote vm-b -- ls -la
```

When the daemon binary is invoked with `remote` as the first argument, it connects to the daemon's local Unix socket, sends the `remote_vm` request, relays frames to stdout/stderr in real time, and exits with the remote exit code.

## Host-side architecture: vsockhandler

The host executes `mvm exec vm-a` which opens a vsock connection to the guest agent. The response read loop receives frames back from the guest. Frames that the host doesn't understand (including guest-initiated frames like `"remote_vm"`) are dispatched to a **handler** via `Client.OnHostFrame`.

This keeps `internal/core/vsock/` domain-pure (no imports of `vm`, `model`, etc.) while allowing an extensible handler in its own package that imports whatever it needs.

### Client.OnHostFrame

`internal/core/vsock/client.go` adds a single field: `OnHostFrame func(ctx, sourceVMID, conn, type, data)`. In the `Exec()` read loop, any frame that isn't `"stdout"`, `"stderr"`, or `"result"` is dispatched to this callback instead of being logged as unknown. If nil, the frame is silently ignored.

### The handler package: `internal/vsockhandler/`

A new package with a single `Handler` struct holding a `VMResolver` and `VsockRepo`. Its `Handle` method switches on the frame type and dispatches to per-type methods. Currently only `"remote_vm"` is handled; adding a new frame type means adding a new case to the switch and a new method on `Handler`.

### handleRemoteVM — streaming relay

Uses the exported vsock protocol primitives for true frame-by-frame streaming. The flow within `handleRemoteVM`:

1. Resolve source VM by `sourceVMID`, check `RemoteExec` — reject before parsing anything
2. Parse the `RemoteVMRequest` from the frame data (destination, command, user, timeout)
3. Resolve target VM via the standard VM resolver
4. Check target VM's `RemoteExec`
5. Check target VM's status is `running`
6. Get target VM's vsock config
7. Dial target vsock via `DialVM`, send an `exec` frame
8. Streaming relay loop: for each frame from target, if it's `"stdout"` or `"stderr"`, forward it to source immediately; if it's `"result"`, parse exit code, send final `"remote_vm"` frame to source with the exit status, and return

Every frame from VM B is forwarded to VM A immediately — no buffering, no `targetClient.Exec()`.

### Wiring

In `pkg/api/cp.go`, `newVsockClient()` creates a Handler instance and assigns its `Handle` method to `client.OnHostFrame`. Since every API operation that creates a vsock client goes through `newVsockClient()`, all Exec/Shell sessions automatically get the handler.

## Implementation plan

### File changes

| Step | File | Change |
|------|------|--------|
| 1 | `internal/lib/db/migrations/001_initial_schema.sql` | Add `remote_exec INTEGER DEFAULT 0 NOT NULL` to `vm_instances` CREATE TABLE |
| 2 | `internal/lib/model/vm.go` | Add `RemoteExec bool` field to `VMItem` |
| 3 | `internal/service/vsockagent/protocol.go` | Add `RemoteVMRequest` and `RemoteVMResponse` structs, `responseTypeRemoteVM` constant |
| 4 | `internal/core/vsock/protocol.go` | Add `ResponseTypeRemoteVM` constant. Export `SendFrame`, `ReadFrame`, `DialVM`. Add unexported `readFrameRaw`. |
| 5 | `internal/core/vsock/client.go` | Add `OnHostFrame func(ctx, sourceVMID, conn, type, data)` to `Client`. In `Exec()` read loop, call it in `default:` case. Remove `VmRepo`, `VsockRepo`, `handleRemoteVM`. |
| 6 | `internal/service/vsockagent/agent.go` | Add `localSocket`, `activeConn`, `activeConnMu` to `Agent`. Start local UDS listener in `Run()`. Set/clear `activeConn` in `handleConnection()`. |
| 7 | `internal/service/vsockagent/local.go` (new) | `handleLocalConn()` — reads `RemoteVMRequest` from local socket, writes to vsock via `activeConn`, reads response frames, forwards to local socket |
| 8 | `internal/service/vsockagent/cmd/main.go` | Detect `remote` subcommand via `flag.NArg()` check. |
| 9 | `internal/service/vsockagent/cmd/remote.go` (new) | `runRemoteSubcommand()` — connects to daemon local socket, sends request, relays response frames to stdout/stderr via `json.NewDecoder` |
| 10 | **`internal/vsockhandler/handler.go` (new)** | `Handler` struct with `VMResolver`/`VsockRepo`. `Handle(ctx, sourceVMID, conn, type, data)` switch. `handleRemoteVM()` with auth checks and streaming relay. |
| 11 | `pkg/api/inputs/vm_create.go` | Add `AllowRemoteExec *bool` to `VMCreateInput`, `AllowRemoteExec bool` to `ResolvedVMCreateInput`. Resolve with config default. |
| 12 | `internal/cli/vm.go` | Add `--allow-remote-exec` flag to `mvm vm create` |
| 13 | `pkg/api/vm.go` | Persist `remote_exec` to DB when creating VM |
| 14 | `pkg/api/cp.go` | Wire `handler := &vsockhandler.Handler{...}; client.OnHostFrame = handler.Handle` in `newVsockClient()` |
| 15 | `pkg/errs/codes.go` | Add `CodeUnauthorized`, `CodeVMNotRunning`, `CodeVsockConfigNotFound` |
| 16 | `internal/infra/constants.go` | Add `"allow_remote_exec": false` to `OverridableDefaults["defaults.vm"]` |

### Config default

Add `"allow_remote_exec": false,` to `OverridableDefaults["defaults.vm"]` in `internal/infra/constants.go`. The CLI flag overrides the config default.

The resolution in `Resolve()` follows the existing pattern used by `enable_console`, `nested_virt`, `pci_enabled`: read from config first, override if CLI flag is set.

### Auth check flow (in `vsockhandler.handleRemoteVM`)

```
1. Resolve source VM by sourceVMID, check RemoteExec → reject before parsing anything
2. Parse data → RemoteVMRequest (destination, command, user, timeout)
3. Resolve target VM by name/ID/IP/MAC
4. Check target VM has RemoteExec → reject
5. Check target VM is running → reject
6. Get target vsock config from DB
7. Dial target vsock, send exec
8. Streaming relay loop (frame-by-frame, no buffering)
```

### Error codes

| Code | When |
|------|------|
| `CodeUnauthorized` | Source or target VM missing `remote_exec` |
| `CodeVMNotFound` | Target VM cannot be resolved |
| `CodeVMNotRunning` | Target VM is not running |
| `CodeVsockConfigNotFound` | Target VM has no vsock config |
| `CodeVsockExecFailed` | Relay exec fails |

## Relationship to existing features

| Feature | Relation |
|---|---|
| `mvm exec` | Host → VM. Remote exec reuses the exec read loop and vsock connection. Shares `client.Exec()`. |
| Guest agent local socket | New. `/var/run/mvm-vsock-agent.sock` for in-VM IPC to the daemon. |
| Console relay | Different mechanism (AF_UNIX relay), but similar relay architecture. |
