# Vsock File Transfer — `mvm cp` over vsock

## Problem

`mvm cp` transfers files between the host and guest VMs across three directions:
host → VM, VM → host, and VM → VM (relayed through the host).

The previous approach used tar-over-SSH, which had fundamental limitations:

- **SSH dependency** — requires `sshd`, SSH keys, and a fully booted network stack.
  Not available during early boot or on minimal images.
- **Slow startup** — SSH connection probing adds 2-10 seconds before any data
  transfers.
- **Tar limitations** — no per-file control, cannot overwrite individual files,
  cannot resume partial transfers. Entire directory trees are buffered in memory
  before writing.
- **Large file bottlenecks** — tar archives the entire payload in memory; files
  over 5 GB stress the pipe and the guest's memory.
- **Encryption overhead** — SSH encrypts all traffic even though vsock is
  already isolated at the hypervisor level.

A binary frame protocol over vsock eliminates all five limitations by operating
directly on the vsock connection — no network stack, no SSH daemon, no tar
binary, no encryption overhead. The vsock agent is already deployed inside every
VM for `mvm exec`, so no additional guest-side dependencies are needed.

## Architecture

`mvm cp` uses a **binary frame protocol over vsock**. The host and guest agent communicate using length-prefixed frames:

- **Control plane**: Compact JSON for metadata, handshake, errors
- **Data plane**: Raw binary payloads with length prefixes — no encoding overhead
- **Transport**: 256 KB buffered chunked I/O with binary frame protocol
- **Integrity**: SHA-256 streaming verification on both sides
- **No guest-side dependencies** — the agent handles file operations natively
- **Recursive directory copy** — source directories are auto-detected and walked recursively; each file's relative path is preserved on the guest via `filepath.Walk` + `os.MkdirAll`

## Entry point

File transfer is initiated from the CLI via `internal/cli/cp.go`, which calls `op.CPCopy()` in `pkg/api/cp.go`. The API layer resolves the source and destination, creates a `vsock.Client`, and calls `FTCopyToVM()` or `FTCopyFromVM()` in `internal/core/vsock/file_transfer.go`.

Inside the guest, the vsock agent's `handleConnection()` in `cmdlistener.go` receives the `"file-transfer"` request type, sends a `"ft-ready"` acknowledgement, then calls `handleFileTransfer()` in `internal/service/vsockagent/file_transfer.go`.

## Protocol design

### Handshake

The existing JSON framing protocol (newline-delimited JSON over vsock) is used for the initial handshake:

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
[JSON] {"type":"file-transfer",...} ────►
                  ◄────────── [JSON] {"type":"ft-ready",...}
```

After the JSON handshake, both sides switch to **binary frames** on the same connection.

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

### Frame types

| Type | Code | Payload | Direction |
|------|------|---------|-----------|
| `FtPush` | `0x10` | JSON: source paths, destination | host → agent |
| `FtPull` | `0x11` | JSON: source path, destination | host → agent |
| `FtMeta` | `0x20` | JSON: path, size, mode, sha256 | both |
| `FtData` | `0x21` | Raw binary chunk (empty = end-of-stream) | both |
| `FtOK` | `0x22` | JSON: bytes received, sha256 (verification) | both |
| `FtMkdir` | `0x30` | JSON: path | agent → host |
| `FtSymlink` | `0x31` | JSON: path, target | (unused) |
| `FtError` | `0x40` | JSON: code, message | both |
| `FtProgress` | `0x50` | JSON: path, bytes, total | agent → host (pull only) |
| `FtDone` | `0x60` | JSON: summary (files, bytes, errors) | both |

### Push flow (host → VM)

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
FtPush: {"paths":["/local/file"],"dest":"/remote/dir/"} ──►
                  ◄────────── FtMkdir: {"path":"/remote/dir"}
FtMeta: {"path":"file.txt","size":5000000000,"mode":644,"sha256":"abc..."} ──►
                  ◄────────── FtMeta: {"accepted":true}
FtData: <256 KB chunk> ──────────────────►
FtData: <256 KB chunk> ──────────────────►
FtData: <final chunk> ───────────────────►
FtData: <empty — end-of-stream> ─────────►
                  ◄────────── FtOK: {"bytes":5000000000,"sha256":"abc..."}
FtDone: {"files":1,"bytes":5000000000,"errors":0} ──►
                  ◄────────── FtDone: (echo)
```

1. Host sends `FtPush` with source paths and destination
2. Agent creates destination directory, sends `FtMkdir` acknowledgement
3. Host sends `FtMeta` with file info; agent accepts
4. Host streams `FtData` chunks (256 KB) then sends empty `FtData` (end-of-stream)
5. Agent calls `f.Sync()`, verifies SHA-256, sends `FtOK`
6. After all files: agent calls `unix.Sync()` (unless `NoSync`), sends `FtDone`

### Pull flow (VM → host)

```
Host                                    Guest (vsock agent)
────                                    ────────────────────
FtPull: {"path":"/remote/file.bin","dest":"/local/dir"} ──►
                  ◄────────── FtMeta: {"path":"file.bin","size":5000000000,"mode":644,"sha256":"abc..."}
FtMeta: {"accepted":true} ───────────────►
                  ◄────────── FtData: <256 KB chunk>
                  ◄────────── FtProgress: {"bytes":N,"total":SIZE}
                  ◄────────── FtData: <256 KB chunk>
                  ◄────────── FtProgress: {"bytes":N,"total":SIZE}
                  ◄────────── FtData: <empty — end-of-stream>
FtOK: {"bytes":5000000000,"sha256":"abc..."} ──►
                  ◄────────── FtDone: {"files":1,"bytes":5000000000,"errors":0}
```

1. Host sends `FtPull` with source path and local destination
2. Agent stats the source, sends `FtMeta` with file info and SHA-256
3. Host sends `FtMeta{accepted:true}`
4. Agent streams `FtData` chunks (256 KB) with interleaved `FtProgress` frames, then empty `FtData` (EOS)
5. Host writes chunks to destination, verifies SHA-256 on EOS, sends `FtOK`
6. Agent verifies host's hash matches, sends `FtDone`

### VM → VM relay flow

The host acts as a transparent relay between two vsock connections — pulling from the source VM and pushing to the destination VM without writing data to disk between them.

### End-of-stream signaling

After the last `FtData` chunk, the sender transmits an empty `FtData` frame (0-byte payload, length = 1). This is unambiguous because:
1. A 0-byte file sends no DATA frames before EOS (just META → EOS)
2. Empty payloads are never valid mid-stream
3. Both sides always agree on the protocol state after an empty DATA frame

Without this signal, all three flows deadlock: the sender finishes transmitting and enters `readFTFrame` waiting for a response, while the receiver reads the last data frame and enters `readFTFrame` waiting for more data.

### SHA-256 verification

Both sides verify integrity:

| Path | Verification | Status |
|------|-------------|--------|
| **Push: agent** | Compares computed hash vs `FtMeta.SHA256`. Sends `FtOK` on match, `FtError` + `os.Remove` on mismatch | Active |
| **Push: host** | Compares agent's `FtOK.SHA256` vs original hash. Returns error on mismatch | Active |
| **Pull: host** | Compares computed hash vs `FtMeta.SHA256`. Sends `FtOK` on match, `os.Remove` + error on mismatch | Active |
| **Pull: agent** | Compares host's `FtOK.SHA256` vs original hash. Logs error on mismatch | Active |

## Happy path

### Host → VM push

1. `expandSources()` resolves source paths — regular files get `filepath.Base` as relative path, directories are walked recursively via a symlink-aware walker
2. Host connects to the VM's vsock agent, sends JSON handshake requesting `"file-transfer"`
3. Host sends binary frame `FtPush` with paths and destination
4. Agent determines destination mode via `os.Stat`: trailing `/` or existing directory means directory mode (join with source filename), otherwise file mode
5. Agent creates destination directory via `os.MkdirAll`, sends `FtMkdir` acknowledgement
6. For each file, host sends `FtMeta` with file info, agent accepts, host streams `FtData` chunks (256 KB) then empty `FtData` (EOS)
7. Agent verifies SHA-256, calls `f.Sync()` on the written file, sends `FtOK`
8. After all files, agent calls `unix.Sync()` (unless `NoSync`), sends `FtDone`

### VM → host pull

1. Host sends `FtPull` with source path and local destination
2. Agent stats the source, computes SHA-256, sends `FtMeta` with file info
3. Host determines local destination mode via `os.Stat`, sends `FtMeta{accepted:true}`
4. Agent opens the source file and streams `FtData` chunks (256 KB) with interleaved `FtProgress` frames, then empty `FtData` (EOS)
5. Host writes chunks to the destination file, computes streaming SHA-256, verifies on EOS
6. Host sends `FtOK` with computed hash
7. Agent verifies host's hash matches, sends `FtDone`

### Recursive directory pull

When `FtPull` has `Recursive: true` and the source is a directory, the agent walks the source directory using the same symlink-aware logic as the host and streams each regular file individually using the same META/DATA/OK protocol. Subdirectories are descended into, symlinks to directories are followed, broken symlinks and non-regular files are skipped. The directory structure is preserved through file paths via `filepath.Rel` on the agent and `os.MkdirAll` on the host).

## Buffer size and throughput

Go's default `io.Copy` buffer (32 KB) is inadequate for virtio-vsock:

| Buffer | Relative throughput | Notes |
|--------|-------------------|-------|
| 32 KB (Go default) | ~50% | Too many syscalls, high per-packet overhead |
| 64 KB | ~65% | Minimum viable |
| 256 KB | ~90% | **Sweet spot** |
| 1 MB | ~95% | Diminishing returns |

Both sides use a 256 KB fixed-size buffer with explicit read/write loops. The
binary frame protocol adds a 5-byte header (4 bytes length + 1 byte type) per
chunk, which is negligible at 256 KB:

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
```

The receiver uses streaming SHA-256 via `hasher.Write()` on each chunk — the
hash is computed incrementally during the transfer with no extra pass:

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

virtio-vsock (Firecracker soft-MMIO, no vhost) achieves ~2-5 Gbps
(250-625 MB/s) for large transfers. For a 5 GB file: ~8-20 seconds.

## Edge cases

### 5 GB+ files

No issues. The streaming frame protocol handles arbitrary sizes. The `FtMeta`
frame includes the total size so the receiver can pre-allocate or show progress.
The 256 KB buffer keeps memory pressure low regardless of file size. SHA-256
streams without an extra pass.

### Empty files (0 bytes)

The sender sends `FtMeta` with `size:0`, followed immediately by an empty
`FtData` (end-of-stream). The agent creates the file and skips the data loop.
No special-case handling needed — the protocol handles it naturally because
an empty `FtData` means end-of-stream regardless of file size.

### Symlinks and special files

Symlinks are followed, not preserved as symlinks. A symlink to a regular file
is transferred as the target file's content under the symlink's logical path.
A symlink to a directory is recursively walked; files inside appear under the
symlink's path, not the physical target's path.

Broken symlinks and non-regular files (sockets, FIFOs, character/block
devices) are skipped with a log warning. The transfer continues and reports
success for the files that were copied.

Symlink cycles are detected by tracking the resolved physical path of every
directory on the current traversal branch. If a symlink target resolves to a
directory already on that branch stack, it is skipped. Sibling symlinks that
point to the same physical directory are not cycles and are followed normally.

### Overwrite behavior

Controlled by the `overwrite` field in `FtPush`/`FtPull`:

```json
{"paths":["src"],"dest":"/dest/","overwrite":false}
```

If `overwrite` is false and the destination file exists, the agent responds
with `FtError` and skips to the next file. If `overwrite` is true, the agent
uses `os.O_TRUNC` when opening the destination.

### Destination mode decision

The agent's push handler decides directory vs file mode via local `os.Stat`:
- Trailing `/` on destination path → forced directory mode:
  `filepath.Join(dest, meta.Path)`
- Existing directory → directory mode:
  `filepath.Join(dest, meta.Path)`
- Existing file or non-existent → file mode: exact `dest` path
  (ignores `meta.Path`)
- Multi-source paths + non-directory dest → error `"not_a_directory"`

This requires zero round-trips and matches standard `cp` semantics exactly.

### Directory creation

The agent calls `os.MkdirAll` on the destination base directory (push) or
parent directory (file mode). Subdirectories in file paths are created via
`os.MkdirAll(filepath.Dir(destPath), 0755)` before opening the file.

### Recursive directory copy

Source directories are auto-detected and expanded transparently — no `-r`
flag needed:
1. `expandSources()` calls `os.Stat` on each source path
2. Regular files get `filepath.Base` as the relative path
3. Directories are walked recursively using a symlink-aware walker. Each file
   gets a `relativePath` relative to the source root.
4. Files in subdirectories carry their full relative path
   (e.g., `sub/dir/file.txt`)
5. The agent's `os.MkdirAll` creates parent directories as needed

Files inside a directory are streamed one at a time. Empty directories are
skipped (parent directories are created when files land in them).

Symlinks are followed: a symlink to a regular file is copied as the target's
content under the symlink's name, and a symlink to a directory is descended
into, preserving the symlink's logical path. Broken symlinks and non-regular
files (sockets, FIFOs, devices) are skipped with a warning and the transfer
continues.

### Partial transfer recovery

If the vsock connection drops mid-transfer, the receiver has an incomplete
file. The agent deletes incomplete files on error to prevent stale data.
Context cancellation is checked at every iteration boundary (between
files/chunks) — not mid-chunk, since vsock does not support
`SetDeadline()`.

## Domain placement

The file transfer implementation lives in the **vsock domain**:
- **Transport**: vsock (AF_UNIX ↔ Firecracker ↔ AF_VSOCK)
- **Protocol**: binary frames over vsock
- **Agent handler**: `internal/service/vsockagent/file_transfer.go`
- **Host client**: `internal/core/vsock/file_transfer.go` — methods on
  `*vsock.Client`
- **API/CLI**: `pkg/api/cp.go` and `internal/cli/cp.go` delegate to vsock
  backend

## Failure modes

### SHA-256 mismatch

On mismatch, the receiving side calls `os.Remove(destPath)` before reporting the error. No corrupt partial files remain in the filesystem.

### Partial transfer on connection drop

If the vsock connection drops mid-transfer, the receiver has an incomplete file. The agent deletes incomplete files on error. No context cancellation mid-blocking-read is possible (vsock doesn't support deadlines).

### Context cancellation

All main loops check `ctx.Done()` at every iteration boundary (between files/chunks). This allows cancellation between transfers but not mid-chunk.

### Incomplete file due to missing fsync

Each file is `f.Sync()`'d after writing (on the agent side for push) before the `FtOK` acknowledgement. Additionally, after all files are transferred, a `unix.Sync()` call triggers `VIRTIO_BLK_T_FLUSH` on the virtio-blk device, ensuring data reaches the backing file before the DONE frame.

### Dest mode ambiguity (multi-source non-directory)

If multiple source files are specified but the destination is not a directory and has no trailing `/`, the agent returns `FtError` with code `"not_a_directory"`.

## Key files

| File | Purpose |
|------|---------|
| `internal/service/vsockagent/file_transfer.go` | Agent-side handler: `handleFTPush()`, `handleFTPull()`, binary frame helpers, SHA-256 verification |
| `internal/service/vsockagent/protocol.go` | Constants: request types (`requestTypeFileTransfer`), buffer size (`ftBufferSize = 262144`) |
| `internal/service/vsockagent/cmdlistener.go` | Dispatch: `"file-transfer"` request → `sendFrame(ft-ready)` → `handleFileTransfer()` |
| `internal/core/vsock/file_transfer.go` | Host client: `FTCopyToVM()`, `FTCopyFromVM()`, `FTCopyVMToVM()`, `expandSources()` |
| `internal/core/vsock/client.go` | Base vsock `Client` with `ensureAgent()`, `Exec()`, `Shell()` |
| `internal/core/vsock/protocol.go` | vsock protocol constants: `ftBufferSize`, `requestTypeFileTransfer` |
| `pkg/api/cp.go` | `CPCopy()` orchestrator: resolve → vsock client → result |
| `pkg/api/inputs/cp_input.go` | `CPInput`, `ResolvedCPInfo` |
| `internal/cli/cp.go` | CLI command: `--force` flag, progress bar |

## Design decisions

**Binary frames after JSON handshake.** The JSON handshake handles version/feature negotiation; the binary frames provide zero-overhead data transfer for large payloads. This hybrid avoids the complexity of a pure binary protocol while keeping data plane overhead to 5 bytes per 256 KB chunk.

**256 KB chunk size.** Balances throughput (~90% of peak) against memory pressure. Larger chunks (1 MB) give only marginal throughput gains while doubling memory per transfer.

**Streaming SHA-256 via `hasher.Write()` on each chunk.** No extra pass over the file. The hash is computed incrementally during the transfer.

**Empty DATA frame as end-of-stream marker.** Avoids deadlocks where both sides simultaneously wait for each other. An empty payload is never valid data, so there is no ambiguity with a 0-byte file.

**Agent-side `os.Stat` for destination mode.** The destination mode (directory vs file) is decided locally by the agent via `os.Stat` — no round-trip needed. Trailing `/` forces directory mode, matching standard `cp` semantics.
