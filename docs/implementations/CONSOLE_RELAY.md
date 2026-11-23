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

The relay receives a PTY master and pinned runtime-directory and log descriptors from the root launch receiver.
It binds `console.sock` beneath that admitted directory and multiplexes PTY, socket, and log I/O through Go channels.
Data arriving from the PTY is forwarded to the socket and log; data arriving from the socket is written to the PTY.
The relay does not reopen caller-selected cache paths.

**Why a separate subprocess?** The relay must survive the CLI process that created the VM. If it were a goroutine in the parent, it would die when the CLI exits. Running it as an independent daemon with its own PID, Unix socket, and file-based lifecycle means the console stays accessible across terminal sessions.

## Entry point

Managed VM launch starts the relay through `spawnBlockedLaunchConsole()` in
`internal/service/jailer/lifecycle_console_spawn.go`, called by the root lifecycle server. This boundary creates the
PTY and private channels and spawns the fixed binary with the admitted caller's UID/GID and no supplementary groups.
The older console-controller/`SpawnService()` path is not the managed boot entry point.

The private child entry, `RunLaunchConsole()` in `internal/service/jailer/lifecycle_console.go`, admits the inherited
plan and descriptors before calling `console.RunDescriptors()`. That adapter binds the listener through the pinned
runtime-directory descriptor, acknowledges readiness, and enters `runRelayIO()` in `internal/service/console/entry.go`.

The user attaches to a running relay via `mvm console <vm>`, which triggers `InteractiveAttach()` in `internal/service/console/client.go`.

## Happy path

### 1. VM start — PTY creation and relay spawn

When a managed VM boots with console support, the root lifecycle receiver creates the PTY through
`openLaunchConsolePTY()`. It retains the slave for the Firecracker descriptor packet and passes the master to the
blocked relay child through the closed launch protocol.

The lifecycle receiver prepares the admitted runtime and log descriptors before releasing relay setup.
`RunDescriptors()` creates the listener and an exclusive PID mirror beneath the pinned runtime directory, then sends
the readiness acknowledgement. Root lifecycle authority owns process identity and cleanup; the PID file is a mirror.

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

The relay is stopped via `SIGTERM` (graceful) or `SIGKILL` (forced). On graceful shutdown, context cancellation closes the PTY master fd, which unblocks the PTY reader goroutine and exits the main loop. Root lifecycle cleanup owns the fixed
socket and PID leaves; the descriptor-based relay closes its listener without automatically unlinking the socket path.

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
| `internal/service/jailer/lifecycle_console_spawn.go` | Root-owned PTY creation and authenticated relay spawn |
| `internal/service/jailer/lifecycle_console.go` | Private child entry and descriptor admission |
| `internal/service/jailer/lifecycle_console_setup.go` | Pinned resource setup and readiness acknowledgement |
| `internal/service/console/descriptors.go` | `RunDescriptors()` serves admitted inherited resources |
| `internal/service/console/entry.go` | Shared `runRelayIO()` I/O loop and older path-based `Run()` entry |
| `internal/service/console/client.go` | CLI client attach, `InteractiveAttach()`, detach detection |
| `internal/service/console/relay.go` | Relay process management, PID tracking, `Stop()` |
| `internal/service/console/spawn.go` | Older `SpawnService()` adapter; not the managed launch entry |
| `internal/service/jailer/lifecycle_console.go` | Typed root operations for managed console lifecycle |
| `internal/cli/console.go` | Cobra command for `mvm console` |
| `pkg/api/console.go` | API orchestration for console relay lifecycle |

## Design decisions

**Goroutine-based multiplexing over deadline-based polling.** The original implementation used a single-threaded loop with 100ms read deadlines on the client socket, followed by a non-blocking check on PTY output. When PTY echo arrived while the client read was blocking, it was delayed by up to 100ms, causing ~100-150ms round-trip echo latency per keystroke. Moving both reads to background goroutines with buffered channels and a single `select` eliminated this bottleneck.

**Terminal handling on the client, not the relay.** The relay is a raw byte pipe with no knowledge of terminal modes, detach sequences, or window sizes. Terminal handling is a CLI concern. Keeping the relay ignorant of terminal details keeps it simple and testable as a pure I/O multiplexer.

**PTY channel left open on goroutine exit.** The PTY reader goroutine sends an error on `ptyCh` and returns without closing the channel. If it closed the channel, a race would occur: the main loop might receive from a closed channel first (returning `ok=false`) before the error, exiting without logging the error. Closing could also race with a pending error send, causing a panic. The client reader channel IS closed because client disconnection is detected via `ok=false` on receive.

**Window size header as first 8 bytes.** The 8-byte control header (magic + version + rows + cols) is sent at the start of every connection and on `SIGWINCH`. This enables terminal resize propagation without a separate protocol message. The magic and version fields allow future wire format changes to be detected and rejected early.
