# Vsock File Transfer — `mvm cp` over vsock

> **STATUS: Implemented.** Vsock-based binary frame protocol for `mvm cp`, leveraging the existing vsock agent infrastructure. The sole transport for `mvm cp`.

## Overview

`mvm cp` transfers files using a **binary frame protocol over vsock**. The host and guest agent communicate using length-prefixed frames:

- **Control plane**: Compact JSON for metadata, handshake, errors (<1 KB messages)
- **Data plane**: Raw binary payloads with length prefixes — no encoding overhead
- **Transport**: 256 KB buffered chunked I/O with binary frame protocol
- **Integrity**: SHA-256 streaming verification on both sides
- **No guest-side dependencies** — the agent handles file operations natively
- **Recursive directory copy** — source directories are auto-detected and walked recursively; each file's relative path is preserved on the guest via `filepath.Walk` + `os.MkdirAll`, no `-r` flag needed

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
| `FtPush` | `0x10` | JSON: source paths, destination | host → agent |
| `FtPull` | `0x11` | JSON: source path, destination | host → agent |
| `FtMeta` | `0x20` | JSON: path, size, mode, sha256 | both |
| `FtData` | `0x21` | Raw binary chunk (empty = end-of-stream) | both |
| `FtOK` | `0x22` | JSON: bytes received, sha256 (verification) | both |
| `FtMkdir` | `0x30` | JSON: path | agent → host |
| `FtError` | `0x40` | JSON: code, message | both |
| `FtProgress` | `0x50` | JSON: path, bytes, total | agent → host (pull only) |
| `FtDone` | `0x60` | JSON: summary (files, bytes, errors) | both |

Key design decisions:
- **End-of-stream**: An empty `FtData` frame (0-byte payload) signals end of file. This avoids deadlocks where both sides simultaneously wait for each other.
- **No binary HANDSHAKE frame**: The JSON handshake serves as version/feature negotiation.
- **No SYMLINK frames**: Not yet implemented (constant defined but unused).

### Push flow (host → VM)

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
FtPush: {"paths":["/local/file"],"dest":"/remote/dir/"}
                  ◄────────── FtMkdir: {"path":"/remote/dir"}
FtMeta: {"path":"file.txt","size":5000000000,"mode":644,"sha256":"abc..."}
                  ◄────────── FtMeta: {"accepted":true}
FtData: <256 KB raw chunk> ────────────►
FtData: <256 KB raw chunk> ────────────►
...
FtData: <final chunk> ────────────────►
FtData: <empty — end-of-stream> ──────►
                  ◄────────── FtOK: {"bytes":5000000000,"sha256":"abc..."}
FtDone: {"files":1,"bytes":5000000000,"errors":0} ──►
                  ◄────────── FtDone: (echo)
```

The host streams chunks, then sends an empty `FtData` to signal completion. The agent verifies the SHA-256 hash against the expected hash from `FtMeta`, cleans up on mismatch, and sends `FtOK`.

**Destination mode decision** (made by agent via `os.Stat`):
- Trailing `/` on dest path → forced directory mode: `filepath.Join(dest, meta.Path)`
- Existing directory → directory mode: `filepath.Join(dest, meta.Path)`
- Existing file or non-existent → file mode: exact `dest` path (ignores `meta.Path`)
- Multi-source + non-directory dest → error `"not_a_directory"`

### Pull flow (VM → host)

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
FtPull: {"path":"/remote/file.bin","dest":"/local/dir"}
                  ◄────────── FtMeta: {"path":"file.bin","size":5000000000,"mode":644,"sha256":"abc..."}
FtMeta: {"accepted":true} ─────────────►
                  ◄────────── FtData: <256 KB raw chunk>
                  ◄────────── FtProgress: {"bytes":N,"total":SIZE}
                  ◄────────── FtData: <256 KB raw chunk>
                  ◄────────── FtProgress: {"bytes":N,"total":SIZE}
...
                  ◄────────── FtData: <final chunk>
                  ◄────────── FtProgress: {"bytes":SIZE,"total":SIZE}
                  ◄────────── FtData: <empty — end-of-stream>
FtOK: {"bytes":5000000000,"sha256":"abc..."} ────►
                  ◄────────── FtDone: {"files":1,"bytes":5000000000,"errors":0}
```

The agent streams chunks with interleaved `FtProgress` frames, then sends an empty `FtData` to signal completion. The host verifies SHA-256, cleans up on mismatch, and sends `FtOK`.

**Destination mode** (host decides locally via `os.Stat`):
- Trailing `/` → directory mode: `filepath.Join(dest, meta.Path)`
- Existing local directory → directory mode: `filepath.Join(dest, meta.Path)`
- Non-existent or file → file mode: write to exact `dest` path

### VM → VM relay flow

The host acts as a transparent relay between two vsock connections:

```
Host                                    Source VM           Dest VM
────                                    ────────           ───────
FtPull: {"path":"/src/file"} ──────────►
FtPush: {"paths":[],"dest":"/dest/"} ─────────────────────►
                  ◄────────── FtMkdir ──────────────────────
                  ◄────────── FtMeta: {"path":"file",...}
FtMeta: (forward) ────────────────────────────────────────►
                  ◄──────────────────── FtMeta: {"accepted":true}
FtMeta: (forward) ──►
                  ◄────────── FtData: <256KB>
FtData: (forward) ───────────────────────────────────────►
                  ◄────────── FtProgress: (tracked locally, not forwarded)
                  ◄────────── FtData: <empty — eos>
FtData: (empty, forward) ────────────────────────────────►
                  ◄──────────────────── FtOK
FtOK: (forward) ──►
                  ◄────────── FtDone
FtDone: (forward) ───────────────────────────────────────►
                  ◄──────────────────── FtDone (echo, discarded)
```

The host doesn't write data to disk — it pipes frames between connections. The dest agent's `os.Stat` decides dir/file mode for the destination.

### All three directions

| Direction | Host role | Data flow |
|-----------|-----------|-----------|
| **host → VM** | Source: reads local files, sends frames | local FS → vsock → agent → VM FS |
| **VM → host** | Destination: receives frames, writes files | VM FS → agent → vsock → local FS |
| **VM → VM** | Relay: pipes frames between two vsock connections | VM1 FS → agent1 → vsock1 → host (256KB buffer) → vsock2 → agent2 → VM2 FS |

### End-of-stream signaling

After the last `FtData` chunk, the sender transmits an empty `FtData` frame (0-byte payload, length = 1). This is unambiguous because:

1. A 0-byte file sends no DATA frames before the EOS (just META → EOS)
2. Empty payloads are never valid mid-stream
3. Both sides always agree on the protocol state after an empty DATA frame

Without this signal, all three flows deadlock: the sender finishes transmitting and enters `readFTFrame` waiting for a response, while the receiver reads the last data frame and enters `readFTFrame` waiting for more data.

### SHA-256 verification

Both sides verify integrity. Four verification paths:

| Path | Verification | Status |
|------|-------------|--------|
| **Push: agent** | Compares computed hash vs `FtMeta.SHA256`. Sends `FtOK` on match, `FtError` + `os.Remove` on mismatch | ✅ |
| **Push: host** | Compares agent's `FtOK.SHA256` vs original hash. Returns error on mismatch | ✅ |
| **Pull: host** | Compares computed hash vs `FtMeta.SHA256`. Sends `FtOK` on match, `os.Remove` + error on mismatch | ✅ |
| **Pull: agent** | Compares host's `FtOK.SHA256` vs original hash. Logs error on mismatch | ✅ |

## Performance Design

### Buffer size: 256 KB

Go's default `io.Copy` buffer (32 KB) is inadequate for virtio-vsock:

| Buffer | Relative throughput | Notes |
|--------|-------------------|-------|
| 32 KB (Go default) | ~50% | Too many syscalls, high per-packet overhead |
| 64 KB | ~65% | Minimum viable |
| 256 KB | ~90% | **Sweet spot** |
| 1 MB | ~95% | Diminishing returns |

### Buffered chunked I/O

Both sides use a 256 KB fixed-size buffer with explicit read/write loops. The binary frame protocol adds a 5-byte header (4 bytes length + 1 byte type) per chunk, which is negligible at 256 KB chunk size.

```go
// Host side: read source into buffer, write framed chunks to vsock
buf := make([]byte, 256*1024)
for {
    n, err := f.Read(buf)
    if n > 0 {
        writeFTFrame(conn, FtData, buf[:n])
    }
    if err == io.EOF {
        writeFTFrame(conn, FtData, nil) // end-of-stream
        break
    }
}

// Guest side: read framed chunks from vsock, write to destination file
for {
    frameType, chunk := readFTFrame(conn)
    if frameType == FtData && len(chunk) == 0 {
        break // end of stream
    }
    f.Write(chunk)
}
```

### Firecracker vsock throughput

virtio-vsock (Firecracker soft-MMIO, no vhost) achieves ~2-5 Gbps (250-625 MB/s) for large transfers.

For a 5 GB file: ~8-20 seconds.

### Integrity verification

The receiver side uses streaming SHA-256 via `hasher.Write()` on each chunk — no extra pass:

```go
hasher := sha256.New()
for {
    frameType, chunk, err := ReadFTFrame(conn)
    if frameType == FtData && len(chunk) == 0 {
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
├── file_transfer_test.go     # Tests: frame helpers, protocol exchange, expandSources
├── protocol.go               # Constants: request types, buffer size
├── client.go                 # Base vsock Client (ensureAgent, Exec, Shell, Teardown)

pkg/api/
├── cp.go                     # CPCopy orchestrator: resolve → vsock client → result
├── inputs/cp_input.go        # CPInput, ResolvedCPInfo (adds Vsock config)

internal/cli/
├── cp.go                     # CLI command: --force flag, progress bar
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
- `FtMeta` includes the total size, so the receiver can pre-allocate or show progress
- 256 KB buffer keeps memory pressure low regardless of file size
- SHA-256 streams without extra pass

### Partial transfers

If the vsock connection drops mid-transfer:
- The receiver has an incomplete file
- No partial files left behind (agent deletes incomplete files on error)
- No context cancellation mid-blocking-read (vsock doesn't support deadlines)

### Directory creation

The agent calls `os.MkdirAll` on the destination base directory (push) or parent directory (file mode). Subdirectories in file paths are created via `os.MkdirAll(filepath.Dir(destPath), 0755)` before opening the file.

### Recursive directory copy

Source directories are auto-detected and expanded transparently — no `-r` flag needed:

1. `expandSources()` calls `os.Stat` on each source path
2. Regular files use `filepath.Base` as the relative path
3. Directories are walked via `filepath.Walk`, each file gets a `relativePath` relative to the source root
4. Files in subdirectories carry their full relative path (e.g. `sub/dir/file.txt`)
5. The agent's existing `os.MkdirAll` creates parent dirs as needed

Files inside a directory are streamed one at a time. Empty directories are skipped (their parent directories are created when files land in them).

### Empty files

0-byte files are handled correctly: `FtMeta` with `size:0` is sent, followed immediately by an empty `FtData` (end-of-stream). The agent creates the file and skips the data loop.

### Overwrite behavior

Controlled by the `overwrite` field in `FtPush`/`FtPull`:

```json
{"paths":["src"],"dest":"/dest/","overwrite":false}
```

If `overwrite` is false and the destination file exists, the agent responds with `FtError` and skips to the next file. If `overwrite` is true, the agent uses `O_TRUNC`.

## Domain placement

The file transfer implementation lives in the **vsock domain**:

- **Transport**: vsock (AF_UNIX ↔ Firecracker ↔ AF_VSOCK)
- **Protocol**: binary frames over vsock
- **Agent handler**: `internal/service/vsockagent/file_transfer.go`
- **Host client**: `internal/core/vsock/file_transfer.go` — methods on `*vsock.Client`
- **API/CLI**: `pkg/api/cp.go` and `internal/cli/cp.go` delegate to vsock backend
