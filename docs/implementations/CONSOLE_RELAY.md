# Console Relay

## Problem

The console relay converts a Firecracker VM's serial console into an interactive terminal session accessible through `mvm console`. Without it, the VM serial output is discarded once the creating process exits, and there is no way to attach to a running VM's console later. The relay runs as an independent subprocess so the console stays accessible across CLI invocations and terminal sessions.

## Architecture

Three processes participate in console I/O:

```
┌─────────────────┐     Unix socket      ┌──────────────────┐
│  CLI client     │◄────────────────────►│  Relay daemon    │
│  (mvm console)  │  raw bytes (data)    │  (runRelayIO)    │
│  InteractiveAtt.│  + control header    │                  │
└─────────────────┘                      └────────┬─────────┘
                                                   │ PTY master fd
                                                   │ (passed via fork)
                                                   ▼
                                           ┌──────────────────┐
                                           │  Firecracker VM  │
                                           │  serial console  │
                                           └──────────────────┘
```

The relay opens a PTY master file descriptor and multiplexes data between three endpoints: the PTY device, a Unix socket at `~/.cache/mvmctl/vms/<vm-id>/console.sock`, and a log file at `~/.cache/mvmctl/vms/<vm-id>/firecracker.console.log`. The relay uses Go channels for multiplexing — data arriving from the PTY is forwarded to the socket and log file; data arriving from the socket is written to the PTY.

**Why a separate subprocess?** The relay must survive the CLI process that created the VM. If it were a goroutine in the parent, it would die when the CLI exits. Running it as an independent daemon with its own PID, Unix socket, and file-based lifecycle means the console stays accessible across terminal sessions.

## Entry point

The relay is started by the console controller (`internal/core/console/controller.go`) when a VM boots. The controller calls `consolesvc.Spawn()` in `internal/service/console/spawn.go`, which launches the relay subprocess via `system.SpawnService()` and passes the PTY master file descriptor as `ExtraFiles[0]` (inherited as fd 3).

The subprocess entry point is `Run()` in `internal/service/console/entry.go`, which parses CLI flags, opens the inherited PTY fd, sets up the Unix socket listener, and calls `runRelayIO()`.

The user attaches to a running relay via `mvm console <vm>`, which triggers `InteractiveAttach()` in `internal/service/console/client.go`.

## Happy path

### 1. VM start — PTY creation and relay spawn

When a VM boots with console support, the API layer calls `Controller.CreatePTY()` in `internal/core/console/controller.go`. This opens `/dev/ptmx`, gets the slave number via `TIOCGPTN` ioctl, unlocks via `TIOCSPTLCK`, and opens `/dev/pts/<N>` as the slave fd. The slave fd is passed to Firecracker as the serial console backend.

`Controller.Start()` then calls `consolesvc.Spawn()` in `spawn.go`, which constructs `system.SpawnService()` with the PTY master fd and the args `["console", "relay", "--vm-id", ..., "--pty-fd", "3"]`. The relay subprocess starts, writes a PID file, then creates a Unix listener socket at `<vm-dir>/console.sock` and enters the I/O loop in `runRelayIO()`.

### 2. Attach — CLI connects to the relay

The user runs `mvm console <vm>`. The CLI calls `InteractiveAttach()` in `client.go`, which creates a `RelayClient`, connects to the relay's Unix socket, sends an 8-byte control header with window size (magic "MVM", version 1, rows, cols), enters raw terminal mode, and starts the interact loop.

### 3. Interactive I/O — bidirectional relay

The relay's main event loop in `runRelayIO()` (entry.go) uses a `select` over four channels:
- **Context cancellation** — triggers graceful shutdown
- **Accepted connections** — starts new client reader goroutine (`startClientReader`)
- **PTY output** — writes to log file and forwards to client socket
- **Client input** — writes to PTY master (VM serial input)

The PTY reader goroutine performs blocking reads from the PTY master fd and sends data to `ptyCh` (buffered, 256 entries). When a client is connected, `startClientReader` reads from the client socket and sends to `clientCh` (buffered, 256 entries). The main loop receives from both channels symmetrically — neither direction blocks the other.

### 4. Detach — Ctrl+X then D

The detach sequence (Ctrl+X, byte 0x18, followed by `d`, byte 0x64) is handled entirely on the client side in `InteractiveAttach()`. The stdin goroutine reads one byte at a time. Bytes are accumulated in a buffer and checked against the detach sequence. On a match, any data before the sequence is flushed to the relay, terminal mode is restored, and the client exits. The relay detects the client disconnect (closed channel) and returns to accept state.

**Why one byte at a time?** If the stdin goroutine read larger chunks, Ctrl+X and `d` could arrive in separate reads, making detach detection racy. Reading one byte ensures the sequence is never split across read boundaries.

### 5. VM stop — relay shutdown

The relay is stopped via `SIGTERM` (graceful) or `SIGKILL` (forced). On graceful shutdown, context cancellation closes the PTY master fd, which unblocks the PTY reader goroutine and exits the main loop. The socket and PID files are cleaned up.

## Failure modes

### Client socket read deadline expiry

The CLI's socket reader goroutine uses a 50ms read deadline (`SetReadDeadline`). This is a trade-off: shorter deadlines consume more CPU polling for context cancellation; longer delays shutdown during detach. On timeout, the reader retries. On connection reset or close, it exits.

### Accept deadlock (historical)

The original implementation attempted to make `Accept` non-blocking by calling `listener.SetDeadline(time.Now())`. This set an absolute deadline equal to the current time — already in the past — causing every subsequent `Accept` to return `i/o timeout` immediately, even with pending connections. The fix moved `Accept` into a dedicated background goroutine delivering connections via a channel.

### Only one client at a time

When the main loop receives a connection while one is already active, the new connection is immediately closed. Firecracker's serial console supports one user at a time, so the relay enforces this at the connection level. The accept channel has a buffer of 1, so a second connection can be queued briefly; a third is dropped immediately.

### Log write failure

PTY output is written to the log file on a best-effort basis. A failed log write logs a warning and continues. The relay's primary job is forwarding PTY↔client bytes; logging is secondary and must never interrupt the data path.

## Key files

| File | Purpose |
|------|---------|
| `internal/service/console/entry.go` | Relay entry point, `Run()` and `runRelayIO()` I/O loop |
| `internal/service/console/client.go` | CLI client attach, `InteractiveAttach()`, detach detection |
| `internal/service/console/relay.go` | Relay process management, PID tracking, `Stop()` |
| `internal/service/console/spawn.go` | `Spawn()` — launches relay subprocess via `system.SpawnService()` |
| `internal/core/console/controller.go` | VM-side controller, `CreatePTY()`, `Start()`, `Cleanup()` |
| `internal/cli/console.go` | Cobra command for `mvm console` |
| `pkg/api/console.go` | API orchestration for console relay lifecycle |

## Design decisions

**Goroutine-based multiplexing over deadline-based polling.** The original implementation used a single-threaded loop with 100ms read deadlines on the client socket, followed by a non-blocking check on PTY output. When PTY echo arrived while the client read was blocking, it was delayed by up to 100ms, causing ~100-150ms round-trip echo latency per keystroke. Moving both reads to background goroutines with buffered channels and a single `select` eliminated this bottleneck.

**Terminal handling on the client, not the relay.** The relay is a raw byte pipe with no knowledge of terminal modes, detach sequences, or window sizes. Terminal handling is a CLI concern. Keeping the relay ignorant of terminal details keeps it simple and testable as a pure I/O multiplexer.

**PTY channel left open on goroutine exit.** The PTY reader goroutine sends an error on `ptyCh` and returns without closing the channel. If it closed the channel, a race would occur: the main loop might receive from a closed channel first (returning `ok=false`) before the error, exiting without logging the error. Closing could also race with a pending error send, causing a panic. The client reader channel IS closed because client disconnection is detected via `ok=false` on receive.

**Window size header as first 8 bytes.** The 8-byte control header (magic + version + rows + cols) is sent at the start of every connection and on `SIGWINCH`. This enables terminal resize propagation without a separate protocol message. The magic and version fields allow future wire format changes to be detected and rejected early.
