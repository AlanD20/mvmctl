# Vsock File Transfer — `mvm cp` over vsock

> **STATUS: Implemented.** Replaces SSH-based tar pipe with a vsock-based binary frame protocol for `mvm cp`, leveraging the existing vsock agent infrastructure.

## Problem

Previously, `mvm cp` transferred files via tar-over-SSH:

1. Establishes SSH connection, probes readiness (~2-10s)
2. Pipes tar archive through SSH (`tar cf -` → `ssh` → `tar xf -`)
3. Requires `sshd`, SSH keys, network stack, and GNU tar in the guest

Fundamental limitations:

- **SSH dependency** — needs `sshd`, keys, network. Not available at early boot.
- **Slow startup** — SSH probing adds seconds before transfer starts
- **tar limitations** — no per-file control, can't overwrite individual files, can't resume
- **No large file optimizations** — tar archives entire directory trees in memory; 5GB+ files stress the pipe
- **Encryption overhead** — SSH encrypts everything even though vsock is already isolated

## Solution Overview

Replace the tar-over-SSH pipe with a **binary frame protocol over vsock**. The host and guest agent communicate using length-prefixed frames:

- **Control plane**: Compact JSON for metadata, handshake, errors (<1 KB messages)
- **Data plane**: Raw binary payloads with length prefixes — no encoding overhead
- **Transport**: Zero-copy via `sendfile()` where available, 256 KB buffered I/O otherwise
- **Integrity**: SHA-256 streaming verification on both sides
- **No SSH, no tar, no shell commands** — the agent handles file operations natively

## Protocol Design

### Handshake

The existing JSON framging protocol (newline-delimited JSON over vsock) is used for the initial handshake:

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
[JSON] {"type":"file-transfer",...} ────►
                  ◄────────── [JSON] {"type":"ft-ready",...}
```

After the JSON handshake, both sides switch to **binary frames** on the same connection (no further JSON framing).

### Frame format

Binary length-prefixed frames:

```
┌─────────────────────────────────────────────┐
│  [4 bytes] Frame length (big-endian uint32)  │
│  [1 byte]  Frame type                        │
│  [N bytes] Payload                           │
└─────────────────────────────────────────────┘
```

`total_length = len(payload) + 1` (the +1 is for the type byte).

Frame types:

| Type | Code | Payload | Direction |
|------|------|---------|-----------|
| `OP_PUSH` | `0x10` | JSON: source paths, destination | host → agent |
| `OP_PULL` | `0x11` | JSON: source path, destination | host → agent |
| `FILE_META` | `0x20` | JSON: path, size, mode, sha256 | both |
| `FILE_DATA` | `0x21` | Raw binary chunk (empty = end-of-stream) | both |
| `FILE_OK` | `0x22` | JSON: bytes received, sha256 (verification) | both |
| `MKDIR` | `0x30` | JSON: path | agent → host |
| `ERROR` | `0x40` | JSON: code, message | both |
| `PROGRESS` | `0x50` | JSON: path, bytes, total | agent → host (pull only) |
| `DONE` | `0x60` | JSON: summary (files, bytes, errors) | both |

Key design decisions:
- **End-of-stream**: An empty `FILE_DATA` frame (0-byte payload) signals end of file. This avoids deadlocks where both sides simultaneously wait for each other.
- **No binary HANDSHAKE frame**: The JSON handshake serves as version/feature negotiation.
- **No SYMLINK frames**: Not yet implemented (constant defined but unused).

### Push flow (host → VM)

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
OP_PUSH: {"paths":["/local/file"],"dest":"/remote/dir/"}
                  ◄────────── MKDIR: {"path":"/remote/dir"}
FILE_META: {"path":"file.txt","size":5000000000,"mode":644,"sha256":"abc..."}
                  ◄────────── FILE_META: {"accepted":true}
FILE_DATA: <256 KB raw chunk> ────────►
FILE_DATA: <256 KB raw chunk> ────────►
...
FILE_DATA: <final chunk> ─────────────►
FILE_DATA: <empty — end-of-stream> ───►
                  ◄────────── FILE_OK: {"bytes":5000000000,"sha256":"abc..."}
DONE: {"files":1,"bytes":5000000000,"errors":0} ──►
                  ◄────────── DONE: (echo)
```

The host streams chunks, then sends an empty `FILE_DATA` to signal completion. The agent verifies the SHA-256 hash against the expected hash from `FILE_META`, cleans up on mismatch, and sends `FILE_OK`.

**Destination mode decision** (made by agent via `os.Stat`):
- Trailing `/` on dest path → forced directory mode: `filepath.Join(dest, meta.Path)`
- Existing directory → directory mode: `filepath.Join(dest, meta.Path)`
- Existing file or non-existent → file mode: exact `dest` path (ignores `meta.Path`)
- Multi-source + non-directory dest → error `"not_a_directory"`

### Pull flow (VM → host)

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
OP_PULL: {"path":"/remote/file.bin","dest":"/local/dir"}
                  ◄────────── FILE_META: {"path":"file.bin","size":5000000000,"mode":644,"sha256":"abc..."}
FILE_META: {"accepted":true} ──────────►
                  ◄────────── FILE_DATA: <256 KB raw chunk>
                  ◄────────── FILE_PROGRESS: {"bytes":N,"total":SIZE}
                  ◄────────── FILE_DATA: <256 KB raw chunk>
                  ◄────────── FILE_PROGRESS: {"bytes":N,"total":SIZE}
...
                  ◄────────── FILE_DATA: <final chunk>
                  ◄────────── FILE_PROGRESS: {"bytes":SIZE,"total":SIZE}
                  ◄────────── FILE_DATA: <empty — end-of-stream>
FILE_OK: {"bytes":5000000000,"sha256":"abc..."} ──►
                  ◄────────── DONE: {"files":1,"bytes":5000000000,"errors":0}
```

The agent streams chunks with interleaved `PROGRESS` frames, then sends an empty `FILE_DATA` to signal completion. The host verifies SHA-256, cleans up on mismatch, and sends `FILE_OK`.

**Destination mode** (host decides locally via `os.Stat`):
- Trailing `/` → directory mode: `filepath.Join(dest, meta.Path)`
- Existing local directory → directory mode: `filepath.Join(dest, meta.Path)`
- Non-existent or file → file mode: write to exact `dest` path

### VM → VM relay flow

The host acts as a transparent relay between two vsock connections:

```
Host                                    Source VM           Dest VM
────                                    ────────           ───────
OP_PULL: {"path":"/src/file"} ────────►
OP_PUSH: {"paths":[],"dest":"/dest/"} ─────────────────────►
                  ◄────────── MKDIR ────────────────────────
                  ◄────────── FILE_META: {"path":"file",...}
FILE_META: (forward) ──────────────────────────────────────►
                  ◄──────────────────── FILE_META: {"accepted":true}
FILE_META: (forward) ──►
                  ◄────────── FILE_DATA: <256KB>
FILE_DATA: (forward) ─────────────────────────────────────►
                  ◄────────── FILE_PROGRESS: (tracked locally, not forwarded)
                  ◄────────── FILE_DATA: <empty — eos>
FILE_DATA: (empty, forward) ──────────────────────────────►
                  ◄──────────────────── FILE_OK
FILE_OK: (forward) ──►
                  ◄────────── DONE
DONE: (forward) ──────────────────────────────────────────►
                  ◄──────────────────── DONE (echo, discarded)
```

The host doesn't write data to disk — it pipes frames between connections. The dest agent's `os.Stat` decides dir/file mode for the destination.

### All three directions

| Direction | Host role | Data flow |
|-----------|-----------|-----------|
| **host → VM** | Source: reads local files, sends frames | local FS → vsock → agent → VM FS |
| **VM → host** | Destination: receives frames, writes files | VM FS → agent → vsock → local FS |
| **VM → VM** | Relay: pipes frames between two vsock connections | VM1 FS → agent1 → vsock1 → host (256KB buffer) → vsock2 → agent2 → VM2 FS |

### End-of-stream signaling

After the last `FILE_DATA` chunk, the sender transmits an empty `FILE_DATA` frame (0-byte payload, length = 1). This is unambiguous because:

1. A 0-byte file sends no DATA frames before the EOS (just META → EOS)
2. Empty payloads are never valid mid-stream
3. Both sides always agree on the protocol state after an empty DATA frame

Without this signal, all three flows deadlock: the sender finishes transmitting and enters `readFTFrame` waiting for a response, while the receiver reads the last data frame and enters `readFTFrame` waiting for more data.

### SHA-256 verification

Both sides verify integrity. Four verification paths:

| Path | Verification | Status |
|------|-------------|--------|
| **Push: agent** | Compares computed hash vs `FILE_META.SHA256`. Sends `FILE_OK` on match, `ERROR` + `os.Remove` on mismatch | ✅ |
| **Push: host** | Compares agent's `FILE_OK.SHA256` vs original hash. Returns error on mismatch | ✅ |
| **Pull: host** | Compares computed hash vs `FILE_META.SHA256`. Sends `FILE_OK` on match, `os.Remove` + error on mismatch | ✅ |
| **Pull: agent** | Compares host's `FILE_OK.SHA256` vs original hash. Logs error on mismatch | ✅ |

## Performance Design

### Buffer size: 256 KB

Go's default `io.Copy` buffer (32 KB) is inadequate for virtio-vsock:

| Buffer | Relative throughput | Notes |
|--------|-------------------|-------|
| 32 KB (Go default) | ~50% | Too many syscalls, high per-packet overhead |
| 64 KB | ~65% | Minimum viable |
| 256 KB | ~90% | **Sweet spot** |
| 1 MB | ~95% | Diminishing returns |

### Zero-copy via sendfile()

On the host side (AF_UNIX socket), `*os.File.WriteTo()` uses `sendfile()` internally when the destination is a socket. This is zero-copy — data goes from page cache directly to the socket buffer, bypassing userspace.

```go
// Zero-copy path: uses sendfile() internally
// file.WriteTo(unixConn) is called by io.Copy(unixConn, file)
io.Copy(vsockConn, sourceFile)  // uses sendfile() on Linux
```

On the guest side, the agent writes to a regular file. `io.CopyBuffer(file, vsockConn, 256*1024)` with a 256 KB buffer provides optimal throughput.

### Firecracker vsock throughput

virtio-vsock (Firecracker soft-MMIO, no vhost) achieves ~2-5 Gbps (250-625 MB/s) for large transfers. This is **5-20x faster** than SSH pipes (~25-62 MB/s).

For a 5 GB file:
- SSH pipe: ~80-200 seconds
- vsock raw: ~8-20 seconds

### Integrity verification

The receiver side uses streaming SHA-256 via `hasher.Write()` on each chunk — no extra pass:

```go
hasher := sha256.New()
for {
    frameType, chunk := readFTFrame(conn)
    if frameType == ftData && len(chunk) == 0 {
        break // end of stream
    }
    f.Write(chunk)
    hasher.Write(chunk)
}
gotHash := hex.EncodeToString(hasher.Sum(nil))
```

## File layout

```
internal/service/vsockagent/
├── file_transfer.go          # Agent handler: push/pull, frame helpers, SHA-256
├── file_transfer_test.go     # Tests: frame helpers, push/pull protocols, modes
├── protocol.go               # Constants: request types, buffer size
├── cmdlistener.go            # Dispatch: file-transfer request → handleFileTransfer

internal/core/vsock/
├── file_transfer.go          # Host client: FTCopyToVM, FTCopyFromVM, FTCopyVMToVM
├── file_transfer_test.go     # Tests: frame helpers, protocol exchange
├── protocol.go               # Constants: request types, buffer size
├── client.go                 # Base vsock Client (waitForAgent, Exec, Shell)

pkg/api/
├── cp.go                     # CPCopy orchestrator: resolve → vsock client → result
├── inputs/cp_input.go        # CPInput, ResolvedCPInfo (adds Vsock config)

internal/cli/
├── cp.go                     # CLI command: --force flag, progress bar, no SSH flags
```

## Implementation notes

### Context propagation

All main loops check `ctx.Done()` at every iteration boundary (between files/chunks). This allows cancellation between transfers but not mid-chunk. For vsock connections, which don't support `SetDeadline`, this is the practical approach.

### Partial file cleanup

On SHA-256 mismatch, the receiving side calls `os.Remove(destPath)` before reporting the error. This ensures no corrupt partial files remain in the filesystem.

### Destination mode (`os.Stat`-based)

The agent's push handler decides dir vs file mode via local `os.Stat`:
- Trailing `/` → forced directory mode
- Existing directory → directory mode (join with source filename)
- Everything else → file mode (exact dest path)

This requires zero round-trips and matches standard `cp` semantics exactly.

## Edge cases

### 5 GB+ files

No issues. The streaming frame protocol handles arbitrary sizes:
- `FILE_META` includes the total size, so the receiver can pre-allocate or show progress
- `sendfile()` on the host side handles multi-GB files without loading them into memory
- 256 KB buffer keeps memory pressure low regardless of file size
- SHA-256 streams without extra pass

### Partial transfers

If the vsock connection drops mid-transfer:
- The receiver has an incomplete file
- No partial files left behind (agent deletes incomplete files on error)
- No context cancellation mid-blocking-read (vsock doesn't support deadlines)

### Directory creation

The agent calls `os.MkdirAll` on the destination base directory (push) or parent directory (file mode). Subdirectories in file paths are created via `os.MkdirAll(filepath.Dir(destPath), 0755)` before opening the file.

### Empty files

0-byte files are handled correctly: `FILE_META` with `size:0` is sent, followed immediately by an empty `FILE_DATA` (end-of-stream). The agent creates the file and skips the data loop.

### Overwrite behavior

Controlled by the `overwrite` field in `OP_PUSH`/`OP_PULL`:

```json
{"paths":["src"],"dest":"/dest/","overwrite":false}
```

If `overwrite` is false and the destination file exists, the agent responds with `ERROR` and skips to the next file. If `overwrite` is true, the agent uses `O_TRUNC`.

## Domain placement

The file transfer implementation lives in the **vsock domain**, not SSH:

- **Transport**: vsock (AF_UNIX ↔ Firecracker ↔ AF_VSOCK)
- **Protocol**: binary frames over vsock (not SSH pipe)
- **Agent handler**: `internal/service/vsockagent/file_transfer.go`
- **Host client**: `internal/core/vsock/file_transfer.go` — methods on `*vsock.Client`
- **API/CLI**: `pkg/api/cp.go` and `internal/cli/cp.go` delegate to vsock backend

The old SSH-based cp is backed up at `backup/ssh_cp/cp.go.bak`.

## Differences from SSH-based cp

| Aspect | SSH (old) | Vsock (new) |
|--------|-----------|-------------|
| Transport | SSH pipe (TCP) | Vsock binary frames |
| Startup | ~2-10s SSH probe | ~5ms vsock dial |
| Encryption | SSH layer (redundant) | None (vsock-isolated) |
| Per-file control | No (tar archive) | Yes (FILE_META per file) |
| Integrity | None | SHA-256, 4 verification paths |
| Progress | Tar pipe bytes | Per-chunk callback + PROGRESS frames (pull) |
| Destination | Dir only (must end with /) | Standard cp semantics (dir or file) |
| Error recovery | Abort on any error | Per-file skip, partial cleanup |
| Guest dependencies | sshd, tar, network | None (native Go) |
