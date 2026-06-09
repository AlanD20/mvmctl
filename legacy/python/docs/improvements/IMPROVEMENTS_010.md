# `mvm cp` — Tar-Over-SSH File Copy for MicroVMs

> **STATUS: IMPLEMENTED — all features completed.**
>
> | Section | Status |
> |---------|--------|
> | CLI command (`mvm cp`) | ✅ Implemented at `cli/cp.py` — Typer app with `--user`, `--key`, `--force`, Rich progress bar |
> | API layer (`CPOperation`) | ✅ Implemented at `api/cp_operations.py` — `CPOperation.copy()` with host→VM, VM→host, VM→VM |
> | Input classes (`CPInput`/`CPRequest`/`ResolvedCPInput`) | ✅ Implemented at `api/inputs/_cp_input.py` — `CPInput`, `CPRequest`, `ResolvedCPInput`, `ResolvedCPInfo` |
> | Core service (`CPService` in `core/ssh/_cp.py`) | ✅ Implemented — full tar-over-SSH with progress, GNU/BusyBox tar detection |
> | Exception hierarchy (under SSHError) | ✅ Implemented — `CPError`, `CPSourceNotFoundError`, `CPDestinationExistsError`, `CPDestinationNotDirectoryError` |
> | main.py registration | ✅ Registered in `_COMMAND_SPECS` and `_COMMAND_ORDER` |
>
> **Additional implementation notes:**
> - `CPDestinationNotDirectoryError` — an extra exception beyond the original plan, raised when destination path does not end with `/` (tar-pipe cannot rename files)
> - GNU tar extras (`--xattrs`, `--acls`, `--delay-directory-restore`) are auto-detected per host with caching
> - SSH connection uses same hardening as other SSH commands (no StrictHostKeyChecking, batch mode, keepalive)
> - Progress bar is Rich-based (SpinnerColumn + BarColumn + TransferSpeedColumn)
> - Multi-source copy (e.g. `mvm cp file1.txt file2.txt dir/ my-vm:/dst/`) is fully supported
> - Registered in `core/ssh/__init__.py`, `api/__init__.py`, and `main.py` lazy import maps
>
> **Last verified:** 2026-05-18

---

**Phase:** Standalone
**Complexity:** Medium
**Depends on:** SSH resolution (existing)

---

## Table of Contents

- [1. Rationale](#1-rationale)
- [2. CLI Interface](#2-cli-interface)
- [3. Path Detection & Resolution](#3-path-detection--resolution)
- [4. Transfer Mechanism](#4-transfer-mechanism)
- [5. Input/Request/Resolved Pattern](#5-inputrequestresolved-pattern)
- [6. Core CPService](#6-core-cpservice)
- [7. File Map & Registration](#7-file-map--registration)
- [8. Edge Cases & Error Handling](#8-edge-cases--error-handling)
- [9. Implementation Roadmap](#9-implementation-roadmap)

---

## 1. Rationale

`docker cp` and `kubectl cp` are essential tools for file transfer to/from running containers/VMs. mvmctl needs the same for Firecracker microVMs.

**Design choice:** tar-over-SSH rather than a guest agent (vsock), FUSE mount (libguestfs), or SCP. Tar is guaranteed in every Linux installation (POSIX-mandated), preserves metadata (permissions, ownership, timestamps), handles sparse files, and recurses directories naturally. Zero guest dependencies beyond what's already there (tar + SSH).

## 2. CLI Interface

```
mvm cp [OPTIONS] SRC DST
```

**Positional arguments:**
- `SRC` — Source path. Either `/local/path` (host) or `vm-name:/remote/path` (VM).
- `DST` — Destination path. Same format as SRC.

**Options:**

| Flag | Description |
|------|-------------|
| `--user`, `-u` | Override SSH user for VM connections |
| `--key` | Key name from mvmctl's key management system |
| `--force` | Overwrite existing files at destination |

**No `--recursive`.** Auto-detected from path type.

**Examples:**
```bash
# Host → VM
mvm cp ./config.yaml my-vm:/etc/config.yaml
mvm cp ./data-dir/ my-vm:/var/data/

# VM → Host
mvm cp my-vm:/var/log/app.log ./app.log
mvm cp my-vm:/etc/ ./backup/etc/

# VM → VM (pipe through host, no host filesystem touch)
mvm cp prod-vm:/etc/config.yaml staging-vm:/etc/config.yaml
```

**Output:**
```bash
$ mvm cp ./config.yaml my-vm:/etc/
⠋ Copying...
Copied ./config.yaml to my-vm:/etc/config.yaml (1.2 KiB)
```

## 3. Path Detection & Resolution

### Parse direction from SRC/DST

```python
def _parse_path(path: str) -> tuple[str | None, str]:
    """Returns (vm_identifier, remote_path) or (None, local_path)."""
    if ":" in path:
        idx = path.index(":")
        return path[:idx], path[idx + 1:]
    return None, path
```

Three modes:
- SRC has VM, DST is local → VM → host
- SRC is local, DST has VM → host → VM
- Both have VMs → VM → VM

### Auto-detect file vs directory (internal only)

**Host side** — instant, no subprocess:
```python
from pathlib import Path
p = Path(local_path)
if p.is_dir():
    # tar cf - -C /path .    → archive contents of dir
elif p.is_file():
    # tar cf - -C /parent basename   → archive single file, strip path
```

**VM side** — one SSH probe per VM:
```bash
test -f /path && echo FILE || (test -d /path && echo DIR || echo NONE)
```
Combined with size estimation in same SSH call:
```bash
# For files:
FILE;stat -c%s /path
# For dirs:
DIR;du -sb /path | cut -f1
```

Single SSH round-trip (~5-20ms on same bridge) returns both type and total bytes (for progress bar).

### VM resolution (reuses existing SSH resolution)

```python
class CPRequest:
    def resolve(self) -> ResolvedCPInput:
        src_vm, src_path = _parse_path(self._inputs.src)
        dst_vm, dst_path = _parse_path(self._inputs.dst)
        
        # Resolve any VM identifiers
        src_info = None
        dst_info = None
        if src_vm:
            src_info = self._resolve_vm(src_vm)
        if dst_vm:
            dst_info = self._resolve_vm(dst_vm)
        
        # Validate
        self._ensure_validate()
        return ResolvedCPInput(...)
```

## 4. Transfer Mechanism

### tar flags

**Create side** (sender):
```
tar --sparse --xattrs --acls -cf -
```

**Extract side** (receiver):
```
tar --no-overwrite-dir --keep-old-files --delay-directory-restore \
    --preserve-permissions --same-owner --sparse --xattrs --acls \
    -xf - -C /destination
```

### Path-stripping for correct `cp` behavior

When copying a single file, we don't want the full source path winding up at the destination.

```python
# File: source is /home/user/file.txt, destination is /etc/
tar cf - -C /home/user file.txt | ssh ... tar xf - -C /etc/
# Result: /etc/file.txt ✅ (not /etc/home/user/file.txt)

# Directory: source is /var/data/, destination is /mnt/
tar cf - -C /var/data . | ssh ... tar xf - -C /mnt/
# Result: /mnt/<contents> ✅ (not /mnt/var/data/...)
```

### Error detection — `pipefail`

```bash
bash -c 'set -o pipefail && tar cf - /src | ssh ... "tar xf - -C /dst"'
```

The pipe's exit code reflects failure of EITHER side. Without `pipefail`, the exit code is only the last command (SSH), which may succeed even if tar failed to read the source.

### Progress — pre-probe size + chunk counting

Since we already probe the source for type detection, we get the total size for free. During transfer, read from the pipe in 64KB chunks, count bytes, and feed `on_progress()`:

```python
src_proc = subprocess.Popen(ssh_cmd, stdout=subprocess.PIPE)
dst_proc = subprocess.Popen(extract_cmd, stdin=subprocess.PIPE)

progress = ASCIIProgressBar(total=total_size, title="Copying")
while True:
    chunk = src_proc.stdout.read(65536)
    if not chunk:
        break
    dst_proc.stdin.write(chunk)
    progress.update(len(chunk))

dst_proc.stdin.close()
src_proc.wait()
dst_proc.wait()
progress.finish()
```

## 5. Input/Request/Resolved Pattern

### `CPInput` (in `api/inputs/_cp_input.py`)

```python
@dataclass
class CPInput:
    src: str                    # "vm-name:/path" or "/local/path"
    dst: str                    # "/local/path" or "vm-name:/path"
    user: str | None = None     # SSH user override
    key: str | None = None      # Key name from mvmctl key system
    force: bool = False         # Overwrite existing files
```

### `CPRequest`

On `resolve()`:
1. Parse src/dst into (vm_identifier, remote_path) or (None, local_path)
2. Determine copy direction
3. Resolve any VM identifiers via existing resolver → (ip, user, key_path)
4. Check source exists on host side (instant Python) or probe VM side (SSH)
5. Auto-detect path types (file/dir) on both sides
6. Call `ensure_validate()`
7. Return `ResolvedCPInput`

### `ResolvedCPInput`

```python
@dataclass(frozen=True)
class ResolvedCPInfo:       # One per VM involved
    identifier: str
    ip: str
    user: str
    key_path: Path | None
    remote_path: str
    is_directory: bool
    total_bytes: int

@dataclass(frozen=True)
class ResolvedCPInput:
    direction: Literal["host_to_vm", "vm_to_host", "vm_to_vm"]
    local_path: str | None          # host-side path if applicable
    vm_dir_path: str | None         # local VM directory for probe context
    src: ResolvedCPInfo | str       # str if local
    dst: ResolvedCPInfo | str       # str if local
    force: bool
```

## 6. Core CPService

### File: `core/ssh/_cp.py`

```python
class CPService:
    """File copy using tar-over-SSH pipes. Zero guest dependencies beyond tar."""

    CREATE_FLAGS = ["--sparse", "--xattrs", "--acls"]

    EXTRACT_FLAGS = [
        "--no-overwrite-dir", "--keep-old-files",
        "--delay-directory-restore",
        "--preserve-permissions", "--same-owner",
        "--sparse", "--xattrs", "--acls",
    ]

    @staticmethod
    def copy_host_to_vm(local_path, vm_ip, vm_user, vm_key, remote_dst,
                        force=False, on_progress=None) -> CopyResult: ...

    @staticmethod
    def copy_vm_to_host(vm_ip, vm_user, vm_key, remote_path, local_dst,
                        force=False, on_progress=None) -> CopyResult: ...

    @staticmethod
    def copy_vm_to_vm(src_vm, dst_vm, src_path, dst_path,
                      force=False, on_progress=None) -> CopyResult: ...

    @staticmethod
    def _probe_remote_path(ssh_service, path) -> tuple[str, int]:
        """Returns ('FILE'|'DIR', size_bytes). Raises on NONE."""
        ...

    @staticmethod
    def _build_tar_cmd(source_path, is_directory) -> list[str]:
        """Build tar create command with correct path-stripping."""
        ...

    @staticmethod
    def _pipe_with_progress(source_cmd, dest_cmd, total_size, on_progress) -> None:
        """Pipe source → dest with progress callback. Uses pipefail for error detection."""
        ...
```

## 7. File Map & Registration

### New files

| File | Contents |
|------|----------|
| `cli/cp.py` | `cp_app` Typer, `mvm cp` command with `--user`, `--key`, `--force` |
| `api/cp_operations.py` | `CPOperation` with `copy_to_vm()`, `copy_from_vm()`, `copy_between_vms()` |
| `api/inputs/_cp_input.py` | `CPInput`, `CPRequest`, `ResolvedCPInput`, `ResolvedCPInfo` |
| `core/ssh/_cp.py` | `CPService` with all transfer logic |

### Modified files

| File | Change |
|------|--------|
| `exceptions.py` | Add `CPError(SSHError)`, `CPSourceNotFoundError(CPError)`, `CPDestinationExistsError(CPError)` |
| `core/ssh/__init__.py` | Add `CPService` to lazy import map |
| `api/__init__.py` | Add `CPOperation`, `CPInput` to `__all__` and `_LAZY_MAP` |
| `main.py` | Add `"cp"` to `_COMMAND_SPECS` and `_COMMAND_ORDER` |

### Registration in `main.py`

```python
"cp": _LazyCommandSpec(
    "mvmctl.cli.cp", "cp_app", "Copy files between host and microVMs"
)
```

Insert `"cp"` into `_COMMAND_ORDER` after `"bin"` (or appropriate position — it's a standalone command, not domain-specific).

## 8. Edge Cases & Error Handling

| Scenario | Error | Behavior |
|----------|-------|----------|
| Source path doesn't exist | `CPSourceNotFoundError(CPError)` | Exit non-zero, message: "Source not found: /path" |
| Destination file exists | `CPDestinationExistsError(CPError)` | Exit non-zero, message: "File exists: /path. Use --force to overwrite." |
| VM unreachable | `ProcessError` (SSH timeout) | Exit non-zero, message: "Cannot connect to VM 'name': connection timed out" |
| Tar fails (disk full, permissions) | `CPError(CPError)` | Exit non-zero, message from tar's stderr |
| Both src and dst are local | `CPError` | "One of SRC or DST must be a VM path (vm-name:/path)" |
| Neither src nor dst has a VM | `CPError` | Same error |
| SRC == DST (same VM, same path) | `CPError` | "Source and destination are the same" |
| Pipefail non-zero with no stderr | `CPError` | "Copy failed: exit code <N>. Check VM connectivity and disk space." |

### Exception hierarchy

```python
class SSHError(MVMError):           # existing
    pass

class CPError(SSHError):             # new
    """Base for all copy errors."""

class CPSourceNotFoundError(CPError): # new
    """Source path does not exist."""

class CPDestinationExistsError(CPError): # new
    """Destination file exists and --force not set."""

class CPDestinationNotDirectoryError(CPError): # new (added beyond original plan)
    """Destination path must end with / (tar-pipe can't rename files)."""
```

## 9. Implementation Roadmap

### Phase 1: Foundation (1-2 days)

| Step | Files | Description |
|------|-------|-------------|
| Add exception classes | `exceptions.py` | CPError, CPSourceNotFoundError, CPDestinationExistsError |
| Create CPService | `core/ssh/_cp.py` | Core tar pipe logic with all three transfer directions |
| Add to core/ssh exports | `core/ssh/__init__.py` | Lazy import for CPService |

### Phase 2: API Layer (1 day)

| Step | Files | Description |
|------|-------|-------------|
| Create CPInput/Request/Resolved | `api/inputs/_cp_input.py` | Input parsing, VM resolution, path detection |
| Create CPOperation | `api/cp_operations.py` | Orchestration layer |
| Add to api exports | `api/__init__.py` | Lazy import for CPOperation, CPInput |

### Phase 3: CLI Layer (0.5 day)

| Step | Files | Description |
|------|-------|-------------|
| Create CLI command | `cli/cp.py` | Typer app with --user, --key, --force |
| Register in main.py | `main.py` | Add to _COMMAND_SPECS and _COMMAND_ORDER |

### Phase 4: Tests (1-2 days — handled by qa-engineer)

| Step | Files | Description |
|------|-------|-------------|
| Unit tests for CPService | `tests/unit/core/ssh/test_cp.py` | Tar pipe construction, path parsing, probe parsing |
| System tests | `tests/system/cp/test_cp.py` | Black-box CLI tests for all three directions |
