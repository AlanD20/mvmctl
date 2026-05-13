# API-to-Consumer Communication Pattern

## Overview

A standardized protocol for the mvmctl API layer to communicate **what happened**, **what is happening**, and **what is needed** to any consumer (CLI, TUI, GUI, headless) — without the API doing any I/O or output formatting itself.

## The Three Concerns

| Concern | Timing | Mechanism | What Problem It Solves |
|---------|--------|-----------|----------------------|
| **"What happened?"** | After operation | `OperationResult` / `BatchResult` | CLI needs to know success vs skip vs error per item |
| **"What is happening now?"** | During operation | `ProgressEvent` via optional callback | Rich UIs need spinner/progress bar updates |
| **"I need something from you"** | Mid-operation interruption | `NeedsInteraction` as return value | Sudo escalation, confirmations, user input |

These are **independent concerns** that compose together. A single API method can use all three:

```python
while True:
    result = VMOperation.create(
        inputs,
        on_progress=_update_spinner,  # ← optional progress callback
    )
    if isinstance(result, NeedsInteraction):
        if not _handle_interaction(result):
            break  # user aborted
        continue  # retry with interaction handled

    # result is OperationResult — render it
    _render_result(result)
    break
```

---

## Mechanism 1: OperationResult (Retrospective)

Tells the consumer what happened **after** an operation completes. Used for every public API mutation method.

### Status Model

Five statuses, distinguished by whether the consumer should be happy, informed, or alarmed:

| Status | Meaning | Consumer Action | Example |
|--------|---------|-----------------|---------|
| `success` | Operation completed, state was modified | Show as positive | `"VM 'foo' created"` |
| `skipped` | Operation was unnecessary (already in desired state) | Show as info | `"Group 'mvm' already exists"` |
| `warning` | Operation succeeded but with caveats | Show with attention | `"Appliance built (but may hang — no virtio kernel)"` |
| `error` | Known, foreseeable failure the user can act on | Show as failure | `"Network 'bar' is in use by 3 VMs"` |
| `failure` | Unexpected system error (bug, crash, connectivity) | Show as critical | `"Database connection lost"` |

### Dataclass Definitions

Located at `src/mvmctl/models/result.py`:

```python
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Generic, Literal, TypeVar

T = TypeVar("T")

OperationStatus = Literal[
    "success",
    "skipped",
    "warning",
    "error",
    "failure",
]


@dataclass
class OperationResult(Generic[T]):
    """
    Result of a single operation on a single item.

    Generic over T = the domain model type (VMInstanceItem, NetworkItem, ...).
    The consumer inspects status + code to decide UX, not the message string.
    """

    # Machine-readable status — one of the five Literal values above.
    status: OperationStatus

    # Machine-readable reason code — used for:
    #   1. Consumer UX decisions (formatting, icons, colors)
    #   2. Audit log event names (single source of truth)
    # Convention: <domain>.<verb>[.<reason>]
    #   e.g. "vm.created", "vm.already_stopped", "network.in_use"
    code: str

    # Human-readable message. CLI uses directly.
    # TUI/GUI may format based on status + code + item instead.
    message: str = ""

    # The domain object that was operated on (if applicable).
    # None for delete operations, or if the item was not found.
    item: T | None = None

    # Structured extra data for rich consumer output.
    # Examples:
    #   {"tap_name": "mvm-foo-0", "guest_ip": "10.0.0.2"} for VM creation
    #   {"attached_vms": ["vm1", "vm2"]} for network deletion failure
    #   {"gid": 1001} for group creation
    metadata: dict[str, Any] = field(default_factory=dict)

    # Only populated for "failure" status — the underlying exception.
    exception: BaseException | None = None

    @property
    def is_ok(self) -> bool:
        """True if the operation completed without error."""
        return self.status in ("success", "skipped", "warning")

    @property
    def is_error(self) -> bool:
        """True if the operation failed."""
        return self.status in ("error", "failure")


@dataclass
class BatchResult(Generic[T]):
    """
    Result of a batch operation on multiple items.

    Collects per-item OperationResult into a single response
    with aggregated summaries for consumer convenience.
    """

    items: list[OperationResult[T]]

    # Batch-level warnings (distinct from per-item warnings).
    warnings: list[str] = field(default_factory=list)

    # Batch-level metadata (e.g. duration, parallel vs sequential).
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def status_summary(self) -> dict[str, int]:
        """Count of each status across all items."""
        counts: dict[str, int] = {}
        for r in self.items:
            counts[r.status] = counts.get(r.status, 0) + 1
        return counts

    @property
    def successes(self) -> list[OperationResult[T]]:
        return [r for r in self.items if r.status == "success"]

    @property
    def skipped(self) -> list[OperationResult[T]]:
        return [r for r in self.items if r.status == "skipped"]

    @property
    def errors(self) -> list[OperationResult[T]]:
        return [r for r in self.items if r.status in ("error", "failure")]

    @property
    def has_any_error(self) -> bool:
        return any(r.status in ("error", "failure") for r in self.items)

    @property
    def all_ok(self) -> bool:
        return all(r.is_ok for r in self.items)
```

### Code Convention

The `code` field MUST follow the convention `<domain>.<verb>[.<reason>]` and doubles as the audit log event name:

```python
# API layer (src/mvmctl/api/vm_operations.py)
result = OperationResult(
    status="success",
    code="vm.created",         # ← IS the audit event name
    message=f"VM '{name}' created",
    item=vm_instance,
)
```

All `code` values MUST be documented in the master code table (see Appendix).

---

## Mechanism 2: ProgressEvent (During Operation)

Optional callback for **long-running or multi-phase operations** so consumers can show spinners, progress bars, or phase transitions.

### Which Operations Get Progress

| Operation | Has progress? | Mechanism |
|-----------|--------------|-----------|
| `VMOperation.create` | ✅ | `on_progress` callback — network → rootfs → firecracker |
| `VMOperation.remove` | ❌ | Single subprocess call, fast (< 1s) |
| `VMOperation.start/stop` | ❌ | Single subprocess call, fast |
| `CacheOperation.init_all` | ✅ | `on_progress` callback — appliance build phase |
| `BinaryOperation.fetch` | ✅ | `ASCIIProgressBar` via `HttpDownload.download_file()` (not `on_progress` — download has known content-length) |
| `HostOperation.init` | ❌ | Individual fast subprocess calls |
| `NetworkOperation.create` | ❌ | Fast, single subprocess |
| `ImageOperation.fetch` | ✅ | `on_progress` callback — extract → optimize phases |
| `ImageOperation.import_` | ✅ | `on_progress` callback — extract → optimize phases |
| `ImageOperation.warm` | ✅ | `on_progress` callback — decompress phase |
| `KernelOperation.fetch` | ✅ | `on_progress` callback — download → build phases |

### Dataclass Definition

```python
# src/mvmctl/models/result.py


@dataclass
class ProgressEvent:
    """
    Emitted during long-running operations to inform the consumer
    of progress, phase transitions, or failures.

    Consumers pass on_progress: Callable[[ProgressEvent], None] as an
    optional parameter to API methods that support progress reporting.
    """

    # Logical phase name (e.g. "network", "image", "cloud_init", "spawn").
    phase: str

    # Current status of this phase.
    # "running" — phase is in progress
    # "complete" — phase finished successfully
    # "failed"   — phase failed (operation will abort)
    status: Literal["running", "complete", "failed"]

    # Overall progress percentage (0.0–100.0) if known, else None.
    percent: float | None = None

    # Human-readable status message for this moment.
    message: str = ""
```

### How the API Emits Events

The direct pattern — no helper function needed:

```python
# src/mvmctl/api/vm_operations.py

class VMCreateContext:
    def execute(self) -> None:
        # ... VM dir setup ...

        if self._on_progress is not None:
            self._on_progress(ProgressEvent(
                phase="network", status="running",
                message="Configuring network...",
            ))
        # ... network work ...

        if self._on_progress is not None:
            self._on_progress(ProgressEvent(
                phase="rootfs", status="running",
                message="Copying root filesystem...",
            ))
        # ... image clone work ...

        if self._on_progress is not None:
            self._on_progress(ProgressEvent(
                phase="firecracker", status="running",
                message="Starting Firecracker microVM...",
            ))
        # ... Firecracker spawn ...

        if self._on_progress is not None:
            self._on_progress(ProgressEvent(
                phase="complete", status="complete",
                message="VM created successfully",
            ))
```

### Consumer Patterns

**CLI — spinner (Rich):**
```python
from rich.console import Console

console = Console()

with console.status("", spinner="dots") as status:

    def _on_progress(event: ProgressEvent) -> None:
        if event.message:
            status.update(f"[dim]{event.message}[/dim]")

    result = ImageOperation.fetch(inputs, on_progress=_on_progress)
```

**Headless / GUI:**
```python
# Pass None or a no-op — zero overhead in the API (every emit checks on_progress first)
result = VMOperation.create(inputs, on_progress=None)
```

**TUI — phase timeline:**
```python
def on_progress(event: ProgressEvent):
    if event.status == "running":
        timeline.add_phase(event.phase, event.message)
    elif event.status == "complete":
        timeline.mark_complete(event.phase)
    elif event.status == "failed":
        timeline.mark_failed(event.phase, event.message)
```

---

## Mechanism 3: NeedsInteraction (Mid-Operation Interruption)

When the API detects it cannot proceed without user input (sudo, confirmation, choice), it returns a `NeedsInteraction` object **instead of** an `OperationResult`. The consumer handles the interaction and calls the same API method again.

### Why a return value, not an exception

`NeedsInteraction` is not an error. It is a recognized control flow the API handles explicitly. Exceptions are reserved for truly unexpected system failures.

### Dataclass Definition

```python
# src/mvmctl/models/result.py


@dataclass
class NeedsInteraction:
    """
    Returned **instead of** :class:`OperationResult` when the API
    cannot proceed without user input (e.g. sudo escalation).

    The consumer should handle the interaction and call the same
    API method again (retry pattern).

    This is **not** an exception — it is normal control flow.
    """

    # Machine-readable reason code.
    # Convention: <domain>.<interaction_type>
    # e.g. "privilege.sudo_required", "binary.confirm_download"
    code: str

    # Human-readable prompt for the consumer to display.
    message: str

    # The type of interaction needed.
    # "sudo"    — Consumer must spawn a sudo subprocess
    # "confirm" — Consumer must ask Yes/No
    # "choice"  — Consumer must present multiple options
    # "input"   — Consumer must collect free-form input
    input_type: Literal["sudo", "confirm", "choice", "input"]

    # Structured context for the consumer to act on.
    context: dict[str, Any] = field(default_factory=dict)
```

### Consumer Flow (Retry Pattern)

Check with `isinstance()` — there is no `status` field on `NeedsInteraction`:

```python
# cli/init.py
while True:
    result = HostOperation.init(cache_dir)

    if isinstance(result, NeedsInteraction):
        if result.code == "privilege.sudo_required":
            print_warning(result.message)
            if typer.confirm("Proceed?", default=True):
                subprocess.run(result.context["command"], shell=True)
                continue  # ← retry the API call with sudo done
            else:
                print_info("Aborted.")
                break
        elif result.code == "binary.confirm_download":
            # ... handle confirmation ...
        else:
            print_error(f"Unhandled interaction: {result.code}")
            break

    # result is OperationResult — render it
    _render(result)
    break
```

---

## Consumer Cheat Sheet

| Consumer | `OperationResult` | `ProgressEvent` | `NeedsInteraction` |
|----------|------------------|-----------------|--------------------|
| **CLI** | Inspect `status` + `code` for icons/colors; use `message` as fallback. Check `is_error` for error branches. | Pass `on_progress` → update Rich `console.status()` | Check `isinstance()` → read `code`/`input_type`/`context` → prompt → retry with `continue` |
| **TUI** | Inspect `status` + `code` + `metadata` + `item` for rich widget rendering | Pass `on_progress` → update phase timeline, progress bars | Check `isinstance()` → show dialog widget → retry |
| **GUI** | Inspect `status` + `code` + `metadata` for dialog/modal | Pass `on_progress` → update notification, progress bar | Check `isinstance()` → show modal → retry |
| **Headless** | Inspect `status` + `code`; log `message` with appropriate level | Pass `None` or no-op; zero overhead | Check `isinstance()` → fail with descriptive message |

---

## Implementation Status

All domains are converted. The following table shows which patterns each API operation uses.
All files are under `src/mvmctl/api/`:

| Domain | File | Mutation methods return | Progress via |
|--------|------|------------------------|--------------|
| **Host** | `host_operations.py` | `OperationResult` / `NeedsInteraction` | N/A |
| **VM** | `vm_operations.py` | `OperationResult` / `BatchResult` / `NeedsInteraction` | `on_progress` (create only) |
| **Volume** | `volume_operations.py` | `OperationResult` / `BatchResult` | N/A |
| **Network** | `network_operations.py` | `OperationResult` / `NeedsInteraction` | N/A |
| **Image** | `image_operations.py` | `OperationResult` / `BatchResult` / `NeedsInteraction` | `on_progress` (fetch, import, warm) |
| **Kernel** | `kernel_operations.py` | `OperationResult` / `BatchResult` / `NeedsInteraction` | `on_progress` (fetch only) |
| **Binary** | `binary_operations.py` | `OperationResult` / `BatchResult` | `ASCIIProgressBar` (not `on_progress`) |
| **Key** | `key_operations.py` | `OperationResult` / `BatchResult` | N/A |
| **Cache** | `cache_operations.py` | `OperationResult` | `on_progress` (init only) |
| **Config** | `config_operations.py` | `OperationResult` | N/A |
| **SSH** | `ssh_operations.py` | `OperationResult` | N/A |
| **Console** | `console_operations.py` | `OperationResult` | N/A |
| **Logs** | `logs_operations.py` | `OperationResult` | N/A |
| **Init** | `init_operations.py` | `InitResult` (wizard-specific) with `NeedsInteraction` | `on_progress` threaded to cache |

---

## Guidelines for API Developers

### When to return what

| Situation | Return |
|-----------|--------|
| Operation succeeded, state changed | `OperationResult(status="success", ...)` |
| Already in desired state, no change | `OperationResult(status="skipped", code="*.already_*", ...)` |
| Succeeded with known caveat | `OperationResult(status="warning", code="*.with_warnings", ...)` |
| Foreseeable domain error (in use, not found, permission) | `OperationResult(status="error", code="*.in_use" / "*.not_found" / "*.permission_denied", ...)` |
| Unexpected system error (DB connection, OSError) | `OperationResult(status="failure", code="system.*", exception=e, ...)` |
| Need user input to continue | `NeedsInteraction(...)` (caller handles and retries) |
| Batch operation on multiple items | `BatchResult[T]` wrapping per-item `OperationResult[T]` |

### Batch operations

For operations that act on multiple items (remove several VMs, remove several kernels), use `BatchResult[T]`:

```python
results: list[OperationResult[VMInstanceItem]] = []
for vm in resolved.vms:
    try:
        # ... do work ...
        results.append(OperationResult(
            status="success", code="vm.removed",
            message=f"VM '{vm.name}' removed", item=vm,
        ))
    except MVMError as e:
        results.append(OperationResult(
            status="error", code="vm.remove_failed",
            message=str(e), item=vm, exception=e,
        ))

return BatchResult(items=results)
```

### CLI consumer pattern for batches

```python
result = VMOperation.remove(VMInput(identifiers=ids))
for item in result.items:
    if item.status == "success":
        print_success(item.message)
    elif item.status in ("error", "failure"):
        print_error(item.message)
if result.has_any_error:
    raise typer.Exit(code=1)
```

### Do NOT

- Do NOT `print()` in the API layer — return `OperationResult` with a `message`
- Do NOT raise exceptions for user-input needs — return `NeedsInteraction`
- Do NOT use `NeedsInteraction` for errors — use `OperationResult(status="error"/"failure")`
- Do NOT pass `on_progress` for fast/simple operations — it adds unnecessary complexity

### Code Convention Summary

- All mutation methods return `OperationResult` or `BatchResult`
- Read methods (get, list, inspect) return data directly or raise exceptions
- All `OperationResult` use a `code` following `<domain>.<verb>.<reason>` convention
- The `code` IS the audit log event name — no separate naming
- `on_progress` is always `Callable[[ProgressEvent], None] | None` — keyword-only, optional
- Add `from collections.abc import Callable` for the callback type hint
- Every emitted `ProgressEvent` uses the direct `if on_progress is not None:` check pattern

---

## Appendix: Master Code Table

Every `code` value currently used in the codebase:

| Code | Status | Domain | Meaning |
|------|--------|--------|---------|
| `vm.created` | success | VM | VM was created successfully |
| `vm.name_collision` | error | VM | VM name collision during batch create |
| `vm.atomic_failed` | failure | VM | Atomic batch create failed |
| `vm.create_failure` | failure | VM | VM creation failed unexpectedly |
| `vm.created_batch` | success | VM | Batch VMs created |
| `vm.not_found` | error | VM | VM not found |
| `vm.removed` | success | VM | VM was removed |
| `vm.remove_failed` | error | VM | VM removal failed |
| `vm.started` | success | VM | VM was started |
| `vm.start_failed` | error | VM | VM start failed |
| `vm.stopped` | success | VM | VM was stopped |
| `vm.stop_failed` | error | VM | VM stop failed |
| `vm.rebooted` | success | VM | VM was rebooted |
| `vm.reboot_failed` | error | VM | VM reboot failed |
| `vm.paused` | success | VM | VM was paused |
| `vm.pause_failed` | error | VM | VM pause failed |
| `vm.resumed` | success | VM | VM was resumed |
| `vm.resume_failed` | error | VM | VM resume failed |
| `vm.snapshot_created` | success | VM | VM snapshot saved |
| `vm.snapshot_failed` | error | VM | VM snapshot failed |
| `vm.snapshot_loaded` | success | VM | VM snapshot loaded |
| `vm.load_snapshot_failed` | error | VM | VM snapshot load failed |
| `vm.imported` | success | VM | VM was imported from config |
| `vm.import_failed` | error | VM | VM import failed |
| `vm.volume_attached` | success | VM | Volume attached to VM |
| `vm.volume_detached` | success | VM | Volume detached from VM |
| `host.init.complete` | success | Host | Host initialization complete |
| `host.init.noop` | skipped | Host | Host already configured |
| `host.kvm.missing` | error | Host | KVM device not found |
| `host.kvm.unreadable` | error | Host | KVM exists but not readable |
| `host.iptables.conflict` | error | Host | Mixed iptables backend |
| `host.binaries.missing` | error | Host | Required system binaries missing |
| `host.cleaned` | success | Host | Host networking cleaned |
| `host.clean_failed` | failure | Host | Host networking clean failed |
| `host.reset` | success | Host | Host reset to pre-init state |
| `host.reset_failed` | failure | Host | Host reset failed |
| `privilege.sudo_required` | needs_input | Host | Root privileges required |
| `network.created` | success | Network | Network created successfully |
| `network.create_failed` | error | Network | Network creation failed |
| `network.default_set` | success | Network | Default network changed |
| `network.default_set_failed` | error | Network | Failed to set default network |
| `network.default_created` | success | Network | Default network created |
| `network.default_created_failed` | error | Network | Default network creation failed |
| `network.removed` | success | Network | Network removed |
| `network.remove_failed` | error | Network | Network removal failed |
| `network.in_use` | error | Network | Network has attached VMs |
| `network.synced` | success | Network | Network rules synced |
| `network.sync_failed` | error | Network | Network sync failed |
| `network.restored` | success | Network | Network state restored |
| `network.restore_failed` | error | Network | Network state restoration failed |
| `cache.initialized` | success | Cache | Cache initialized |
| `cache.pruned` | success | Cache | Cache resources pruned |
| `cache.cleaned` | success | Cache | Full cache clean completed |
| `binary.downloaded` | success | Binary | Binary downloaded successfully |
| `binary.already_present` | skipped | Binary | Binary already exists in cache |
| `binary.pull_failed` | error | Binary | Binary pull failed |
| `binary.removed` | success | Binary | Binary removed from cache |
| `binary.remove_failed` | error | Binary | Binary removal failed |
| `binary.not_found` | error | Binary | Binary not found |
| `binary.default_set` | success | Binary | Default binary changed |
| `binary.default_set_failed` | error | Binary | Failed to set default binary |
| `binary.default_repaired` | success | Binary | Default binary was repaired |
| `binary.default_unchanged` | skipped | Binary | Default binary already correct |
| `binary.ensure_default_failed` | failure | Binary | Failed to ensure default binary |
| `binary.confirm_download` | needs_input | Binary | Ask user before downloading |
| `kernel.already_present` | skipped | Kernel | Kernel already exists |
| `kernel.pulled` | success | Kernel | Kernel pulled successfully |
| `kernel.pull_failed` | error | Kernel | Kernel pull failed |
| `kernel.removed` | success | Kernel | Kernel removed |
| `kernel.remove_failed` | error | Kernel | Kernel removal failed |
| `kernel.default_set` | success | Kernel | Default kernel changed |
| `kernel.default_set_failed` | error | Kernel | Failed to set default kernel |
| `image.acquired` | success | Image | Image downloaded and cached |
| `image.already_present` | skipped | Image | Image already exists in cache |
| `image.acquire_failed` | error | Image | Image download/processing failed |
| `image.imported` | success | Image | Image imported from local file |
| `image.import_failed` | error | Image | Image import failed |
| `image.removed` | success | Image | Image removed from cache |
| `image.remove_failed` | error | Image | Image removal failed |
| `image.default_set` | success | Image | Default image changed |
| `image.warmed` | success | Image | Image warmed to ready pool |
| `image.warm_failed` | error | Image | Image warming failed |
| `key.created` | success | Key | SSH key created |
| `key.create_failed` | error | Key | SSH key creation failed |
| `key.added` | success | Key | SSH key added to cache |
| `key.add_failed` | error | Key | SSH key add failed |
| `key.removed` | success | Key | SSH key removed |
| `key.remove_failed` | error | Key | SSH key removal failed |
| `key.exported` | success | Key | SSH key exported |
| `key.export_failed` | error | Key | SSH key export failed |
| `key.default_set` | success | Key | Default key(s) set |
| `key.default_set_failed` | error | Key | Failed to set default key |
| `key.defaults_cleared` | success | Key | Default keys cleared |
| `key.defaults_clear_failed` | error | Key | Failed to clear default keys |
| `config.set` | success | Config | Configuration value updated |
| `config.reset` | success | Config | Configuration value reset |
| `ssh.connected` | success | SSH | SSH connection succeeded |
| `ssh.failed` | error | SSH | SSH connection failed |
| `console.killed` | success | Console | Console relay stopped |
| `console.not_running` | skipped | Console | Console relay not running |
| `console.kill_failed` | error | Console | Console relay stop failed |
| `volume.created` | success | Volume | Volume created successfully |
| `volume.already_exists` | error | Volume | Volume already exists |
| `volume.not_found` | error | Volume | Volume not found |
| `volume.removed` | success | Volume | Volume removed |
| `volume.remove_failed` | error | Volume | Volume removal failed |
| `volume.resized` | success | Volume | Volume resized |
| `guestfs.confirm_enable` | needs_input | Init | Confirm guestfs enable |
| `binary.confirm_download` | needs_input | Init | Confirm binary download |
