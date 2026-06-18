# Console Relay

## Purpose

The console relay provides bidirectional I/O between a user's terminal and a
Firecracker microVM's serial console via a PTY (pseudo-terminal). It runs as an
independent subprocess (daemon) so the VM console remains accessible even after
the `mvm vm create` or `mvm vm start` command that spawned it exits.

## Process model

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

### Relay daemon (server)

Started when the VM boots, runs in the background, and manages the PTY↔socket
multiplexing. It owns two goroutines (PTY reader, client reader) plus a main
select loop that dispatches between them symmetrically. Only one CLI client
can be connected at a time; a second connection is rejected.

**Why a separate subprocess?** The relay must survive the CLI process that
created the VM. If it were a goroutine in the parent, it would die when the
CLI (or the API server) exits. Running it as an independent daemon with its
own PID, Unix socket, and file-based lifecycle means the console stays
accessible across CLI invocations and terminal sessions. The relay is spawned
via `SpawnService` with `ExtraFiles` to pass the PTY master fd.

### CLI client

Started when the user runs `mvm console <vm>`. Connects to the relay's Unix
socket, enters raw terminal mode, and runs an event loop forwarding stdin to
the socket and socket output to stdout. Detects the detach sequence (Ctrl+X
then D) locally without involving the relay.

**Why is terminal handling on the client, not the relay?** The relay is a raw
byte pipe. It has no knowledge of terminal modes, detach sequences, or window
sizes. Terminal handling is a CLI concern. Keeping the relay ignorant of
terminal details means it stays simple, testable as a pure I/O multiplexer,
and independent of terminal libraries (`x/term`, `signal`, etc.).

## Lifecycle

1. **VM start:** The controller creates a PTY pair (master + slave), passes the
   slave fd to Firecracker (as the serial console backend), and spawns the relay
   subprocess with the master fd as an inherited file descriptor (fd 3). The
   relay writes its PID file, creates a Unix listener socket, and enters the
   I/O loop (`runRelayIO`).

2. **Attach:** The user runs `mvm console <vm>`. The CLI resolves the VM,
   verifies the relay is running, connects to the relay's Unix socket, sends
   the 8-byte window size header (see Wire Protocol), enters raw mode, and
   starts the interact loop.

3. **Interactive I/O:** The relay forwards PTY output to the client and client
   input to the PTY via a symmetric `select` on two channels. Detach is
   handled purely on the client side.

4. **Detach:** The user presses Ctrl+X then D. The client detects this in the
   stdin buffer, sends any buffered data before the detach sequence to the
   relay, restores terminal mode, and exits. The relay detects the client
   disconnect (closed channel) and returns to accept state.

5. **VM stop / relay kill:** The relay is stopped via `SIGTERM` (graceful) or
   `SIGKILL` (forced). On graceful shutdown, context cancellation closes the
   PTY master fd, which unblocks the PTY reader goroutine and exits the main
   loop. The socket and PID files are cleaned up.

## Wire protocol

The relay and client communicate over a single Unix stream socket. Every new
connection starts with a fixed 8-byte control header, followed by raw bytes
for the duration of the session.

### Control header (8 bytes)

```
Offset  Size  Type        Field     Notes
─────────────────────────────────────────────────────
0       3     ASCII       magic     "MVM"
3       1     uint8       version   Currently 1
4       2     uint16 LE   rows      Terminal height
6       2     uint16 LE   cols      Terminal width
```

The header constants (`wsMagic`, `wsVersion`, `wsHeaderSize`) are package-level
constants in the `console` package, shared between `entry.go` (relay decode)
and `client.go` (client encode). The magic and version fields are validated
by the relay. A mismatch causes the connection to be dropped with a warning.
This ensures future wire format changes are detected rather than silently
misinterpreted.

Rows and cols are uint16 (65535 is effectively unbounded for terminal
dimensions). The header is exactly 8 bytes so the data stream after it is
unaligned raw bytes with zero per-byte overhead.

**Why `io.ReadFull` on the relay side?** Unix stream sockets have no message
boundaries. A single `conn.Read` might return 3 bytes, then 5 in the next
call, splitting the header. `io.ReadFull` guarantees an atomic read of exactly
8 bytes or returns an error. This is a socket programming fundamental that
applies to any fixed-size header over a stream transport.

### Data stream

After the 8-byte control header, all bytes are opaque terminal I/O:

- **Client → Relay:** Raw terminal input (keystrokes, escape sequences, paste)
- **Relay → Client:** Raw VM serial output (console text, escape sequences)

There is no framing, no length prefixes, and no message boundaries on the data
stream. The relay's `startClientReader` goroutine reads available bytes and
forwards them to the PTY. The socket reader goroutine reads available bytes and
writes them to stdout.

### SIGWINCH handling

When the user's terminal is resized (e.g., tmux pane resize), the CLI client
receives `SIGWINCH` via `signal.Notify`, queries the new terminal dimensions
via `term.GetSize`, and sends a fresh 8-byte control header (same wire format)
to the relay. The relay applies the new size to the PTY master fd via the
`TIOCSWINSZ` ioctl. The VM serial console's next terminal query will see the
updated dimensions.

The write is best-effort — the socket may be closed during detach, and an error
is silently discarded.

## Relay I/O architecture

### Channel types and patterns

The relay uses two channel patterns that are worth noting:

**`ptyCh` is `chan ptyRead` (bidirectional, send+receive).** The `ptyRead`
struct bundles data and error into a single type, avoiding the need for
separate data and error channels. The PTY reader goroutine sends to it; the
main loop receives from it.

**`clientCh` is `<-chan []byte` (receive-only).** The `<-chan` type annotation
prevents the main loop from accidentally sending to it — it can only receive.
This is a compile-time safety guarantee. When no client is connected, the
variable is set to `nil`. A nil channel in a `select` blocks forever, which
effectively disables that case without needing an `if` guard around the
`select`. This is a deliberate Go idiom.

### PTY reader goroutine

A single goroutine performs blocking reads from the PTY master fd. When data
arrives (VM serial output), it copies the bytes into a newly allocated slice
and sends it to the `ptyCh` channel (buffered, 256 entries). If the channel is
full, the goroutine blocks — this provides natural back-pressure against a
fast-producing VM when the client is slow to consume.

The goroutine exits when `ptyFile.Read` returns an error (PTY closed, context
cancelled). On exit, it sends the error to `ptyCh` so the main loop can
terminate cleanly.

**Why 256 buffer depth?** At 115200 baud serial, one character takes ~87μs.
256 entries provide ~22ms of buffering, enough to absorb VM boot output bursts
and fast typing without blocking the goroutine. The client-side socket channel
(`socketCh`) has only 10 entries because client consumption (stdout writes) is
typically fast enough to keep up.

**Why does the goroutine NOT close the channel on exit?** The goroutine sends
an error on `ptyCh` and then returns. If it also closed the channel, there
would be a race: the main loop might try to receive from a closed channel
before the error is picked up, causing it to see `ok=false` first and exit
without logging the error. More critically, closing the channel could race
with a pending error send, causing a panic (send on closed channel). By leaving
the channel open, the error is always delivered, and the channel is garbage
collected when the main loop exits.

### Client reader goroutine (`startClientReader`)

When a client connects, the relay launches a goroutine that performs blocking
reads from the client socket. Data is sent to the `clientCh` channel (buffered,
256 entries). The channel is **closed** (not left open) when the goroutine
exits — unlike the PTY reader, because the client reader exits for benign
reasons (client disconnect) and close detection is how the main loop learns
about disconnection.

The goroutine is started per-connection. The main loop reads from `clientCh`;
when it receives a close (`ok=false`), it cleans up the connection state.

### Accept goroutine

Accepting new client connections runs in a separate goroutine to avoid blocking
the main event loop. Accepted connections are delivered to the main loop via
the `acceptCh` channel (buffered, 1 entry).

**Only one client at a time.** When the main loop receives a connection from
`acceptCh`, it checks whether a client is already connected (`clientConn !=
nil`). If one is, the new connection is immediately closed. If not, it reads
the 8-byte window size header, applies it to the PTY, and starts the client
reader goroutine. This single-client model matches Firecracker's serial console
architecture (one serial port, one user at a time).

The 1-entry channel buffer means a second connection can be queued briefly if
the main loop is processing PTY data. If a third arrives, the accept goroutine
drops it (the channel's `default` case closes the connection).

**Why a goroutine instead of deadline-based polling?** The original attempt
used `listener.SetDeadline(time.Now())` to make `Accept` non-blocking. This
does not work: `SetDeadline` sets an absolute deadline. When set to the current
time, the deadline is already expired, and every subsequent `Accept` returns
`i/o timeout` immediately — even when there IS a pending connection in the
kernel's accept queue. The client's `Dial` succeeds (the handshake completes),
but the relay never picks up the connection. Moving Accept to a separate
goroutine with no deadline avoids this entirely.

### Main event loop (`select`)

The main loop iterates over four channels in a single `select`:

1. **Context cancellation** — triggers graceful shutdown
2. **Accepted connections** — starts new client reader goroutine
3. **PTY output** — writes to log file and forwards to client socket
4. **Client input** — writes to PTY master (VM serial input)

All channels are symmetric — no direction is polled or prioritized over the
other. This eliminates the ~100ms echo latency that the original deadline-based
sequential loop suffered from (where client read blocked PTY output processing).

### Log file

PTY output is also written to a log file (`firecracker.console.log`) before
being forwarded to the client socket. This provides a persistent record of
console output for debugging.

**Best-effort policy:** Log writes, SIGWINCH writes, and initial window size
sends are all best-effort. The guiding principle is that non-critical side
effects must never crash the relay or interrupt the data path. The relay's
primary job is forwarding PTY↔client bytes; everything else is secondary.
A failed log write logs a warning and continues. A failed SIGWINCH write is
silently discarded.

## PTY lifecycle

The PTY pair is created in the controller (`internal/core/console/controller.go`):

1. Open `/dev/ptmx` → master fd
2. Get slave number via `TIOCGPTN` ioctl
3. Unlock via `TIOCSPTLCK` with argument 0
4. Open `/dev/pts/<N>` → slave fd

The slave fd is passed to Firecracker as the serial console backend. The master
fd is passed to the relay subprocess as an inherited file descriptor (fd 3,
since ExtraFiles[0] starts at fd 3 after stdin/stdout/stderr).

The controller has a method `CreatePTY` that returns the slave client FD and
`CloseClientFD` that closes only the slave side (called after spawning
Firecracker). `ClosePTY` closes both ends. `Cleanup` stops the relay and closes
both PTY FDs.

## Detach sequence

The default detach sequence is `Ctrl+X` (byte 0x18) followed by `d` (byte 0x64).
This is handled entirely on the client side in `InteractiveAttach`:

- The stdin goroutine reads **one byte at a time** (buffer size 1)
- Bytes are accumulated in an input buffer (`inputBuf`)
- The last two bytes are checked against the detach sequence
- If matched, any data before the sequence is flushed to the relay, then the
  client exits (restoring terminal mode)
- If the buffer starts with 0x18 but has fewer than 2 bytes, the client waits
  for more before sending (to avoid splitting the detach sequence across sends)
- Any byte not starting with 0x18 is sent immediately (no buffering delay)

**Why one byte at a time?** If the stdin goroutine read in larger chunks (e.g.,
4096 bytes), the Ctrl+X and `d` could arrive in separate reads, making detach
detection racy. Reading one byte ensures the detach sequence is never split
across read boundaries. The cost is more goroutine scheduling, but stdin I/O
is not a performance path — it's key-by-key human interaction.

The detach sequence is configurable via the `RelayClient` constructor.

### Client socket reader deadline (50ms)

The CLI's socket reader goroutine uses a 50ms read deadline (`SetReadDeadline`).
This is a trade-off: shorter deadlines would consume more CPU polling for
context cancellation; longer would delay shutdown during detach. 50ms matches
the `select.select` timeout used in the original implementation.

## Critical fixes

### Latency: sequential deadline-based polling

The original implementation used a single-threaded loop with 100ms read
deadlines on the client socket, followed by a non-blocking check on PTY output.
When PTY echo arrived while the client read was blocking, it was delayed by up
to 100ms until the deadline expired. This caused ~100-150ms round-trip echo
latency per keystroke.

The fix was to move both the client read and PTY read into background
goroutines with buffered channels, and use a single `select` to dispatch
between them. This eliminated the sequential bottleneck and reduced echo
latency to ~1ms.

### Accept deadlock: SetDeadline(time.Now())

The original fix attempted to make Accept non-blocking by calling
`listener.SetDeadline(time.Now())`. This sets an absolute deadline equal to
the current time. Since deadlines are absolute, the deadline is already in the
past by the time Accept checks it. This causes Accept to return `i/o timeout`
on every call — even when there IS a pending connection in the kernel's accept
queue. The client's `Dial` succeeds (TCP handshake completes), but the relay
never picks up the connection. All PTY output is written to the log file only.

The fix was to move Accept into a dedicated background goroutine (no deadline)
and deliver accepted connections to the main loop via a channel.

### Missing terminal resize

The PTY master fd had no terminal dimensions set (default 0×0 or 80×24
depending on kernel defaults). VM serial applications couldn't determine the
correct screen size, causing text wrapping and display corruption when the
user's terminal was smaller than the PTY default.

The fix added the 8-byte control header with magic/version/rows/cols at the
start of every connection, plus SIGWINCH handling on the client side to
propagate terminal resize events.

## Wire format versioning

The 3-byte magic (`"MVM"`) and 1-byte version field at the start of the
control header serve as a protocol negotiation mechanism. Future changes to the
wire format (additional control messages, framing, etc.) must:

1. Increment the version number
2. Document the new format here
3. Ensure the relay rejects connections with an unsupported version

Old clients connecting to a new relay will be rejected at the magic/version
validation gate. New clients connecting to an old relay will have their control
header misinterpreted — this is acceptable during co-ordinated upgrades where
the relay and client are always from the same build.

## Key source locations

| Component | File |
|---|---|
| Relay I/O loop | `internal/service/console/entry.go` |
| CLI client attach | `internal/service/console/client.go` |
| Relay subprocess management | `internal/service/console/relay.go` |
| Relay subprocess spawn | `internal/service/console/spawn.go` |
| VM-side controller | `internal/core/console/controller.go` |
| CLI command | `internal/cli/console.go` |
| API orchestration | `pkg/api/console.go` |
