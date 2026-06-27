> **STATUS: Current — fully accurate.** All patterns (OperationResult, BatchResult, NeedsInteraction, Progress) match the current codebase at `pkg/errs/result.go` and `internal/infra/event/event.go`.
>
> **Last verified:** 2026-06-27

# API-to-Consumer Communication Pattern

## Overview

A standardized protocol for the mvmctl API layer to communicate **what happened**, **what is happening**, and **what is needed** to any consumer (CLI, TUI, GUI, headless) — without the API doing any I/O or output formatting itself.

## The Three Concerns

| Concern | Timing | Mechanism | What Problem It Solves |
|---------|--------|-----------|----------------------|
| **"What happened?"** | After operation | `OperationResult` / `BatchResult` | CLI needs to know success vs skip vs error per item |
| **"What is happening now?"** | During operation | `event.Progress` via optional callback | Rich UIs need spinner/progress bar updates |
| **"I need something from you"** | Mid-operation interruption | `NeedsInteraction` as error return | Sudo escalation, confirmations, user input |

These are **independent concerns** that compose together. A single API method can use all three:

```go
for {
    vms, err := op.VMCreate(ctx, input, func(e event.Progress) {
        prog.UpdateText(e.Message)
    })
    var ni *errs.NeedsInteraction
    if errors.As(err, &ni) {
        if !handleInteraction(ni) {
            break // user aborted
        }
        continue // retry with interaction handled
    }
    if err != nil {
        return err
    }
    // vms is the result — render it
    renderResult(vms)
    break
}
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

### Struct Definitions

Located at `pkg/errs/result.go`:

```go
type OperationStatus string

const (
    StatusSuccess OperationStatus = "success"
    StatusSkipped OperationStatus = "skipped"
    StatusWarning OperationStatus = "warning"
    StatusError   OperationStatus = "error"
    StatusFailure OperationStatus = "failure"
)

type OperationResult struct {
    Status    string         `json:"status"`
    Code      string         `json:"code"`
    Message   string         `json:"message"`
    Item      any            `json:"item,omitempty"`
    Exception error          `json:"-"`
    Metadata  map[string]any `json:"-"`
    Warnings  []string       `json:"-"`
}

func (r *OperationResult) IsOK() bool    { return r.Status == "success" || r.Status == "skipped" || r.Status == "warning" }
func (r *OperationResult) IsError() bool { return r.Status == "error" || r.Status == "failure" }
func (r *OperationResult) ToError() *DomainError { /* converts error-status to DomainError */ }
```

### BatchResult

```go
type BatchResult struct {
    Items    []OperationResult `json:"items"`
    Warnings []string          `json:"warnings,omitempty"`
    Metadata map[string]any    `json:"metadata,omitempty"`
}

func (b *BatchResult) Errors() []OperationResult { /* returns all failed items */ }
func (b *BatchResult) HasErrors() bool           { /* true if any item has error/failure status */ }
```

### Go-Specific: DomainError with Code + Class

Go has a parallel error path via `DomainError`:

```go
type DomainError struct {
    Code    Code
    Message string
    Op      string         // Operation that failed (e.g. "VMCreate", "NetworkRemove")
    Entity  string         // Entity being operated on (e.g. "my-vm", "default")
    Class   Class
    Err     error          // Wrapped underlying error
    Details map[string]any // Structured extra data
}

type Class int
const (
    ClassUnknown          Class = iota
    ClassValidation
    ClassConflict
    ClassRetryable
    ClassInternal
    ClassNeedsInteraction
)
```

Every `Code` maps to a `Class` via `codeClassMap` in `pkg/errs/domain.go`. The CLI's `common.HandleErrors()` dispatches on `Class` for rendering.

### Code Convention

The `code` field MUST follow the convention `<domain>.<noun>.<verb>`:

```go
result := errs.OperationResult{
    Status:  "success",
    Code:    "vm.created",
    Message: fmt.Sprintf("VM '%s' created", name),
    Item:    vmInstance,
}
```

All `code` values are defined as constants in `pkg/errs/codes.go`.

---

## Mechanism 2: Progress (During Operation)

Optional callback for **long-running or multi-phase operations** so consumers can show spinners, progress bars, or phase transitions.

### Which Operations Get Progress

| Operation | Has progress? | Mechanism |
|-----------|--------------|-----------|
| `Operation.VMCreate` | Yes | `onProgress` callback — network → rootfs → firecracker |
| `Operation.VMRemove` | No | Single subprocess call, fast (< 1s) |
| `Operation.VMStart/Stop` | No | Single subprocess call, fast |
| `Operation.CacheInitAll` | Yes | `onProgress` callback — appliance build phase |
| `Operation.BinaryPull` | Yes | `FormatProgress` bridge (download has known content-length) |
| `Operation.HostInit` | No | Individual fast subprocess calls |
| `Operation.NetworkCreate` | No | Fast, single subprocess |
| `Operation.ImagePull` | Yes | `onProgress` callback — extract → optimize phases |
| `Operation.KernelPull` | Yes | `onProgress` callback — download → build phases |

### Struct Definition

```go
// internal/infra/event/event.go

type Progress struct {
    Phase   string   `json:"phase"`
    Status  string   `json:"status"`
    Message string   `json:"message"`
    Current int64    `json:"current,omitempty"`
    Total   int64    `json:"total,omitempty"`
    Percent *float64 `json:"percent,omitempty"`
}

type OnProgressCallback func(Progress)
type OnDownloadCallback func(currentBytes, totalBytes int64)
```

### How the API Emits Events

The direct pattern via helper:

```go
// pkg/api/operation.go
func emitProgress(onProgress event.OnProgressCallback, phase, status, msg string) {
    if onProgress == nil {
        return
    }
    onProgress(event.Progress{Phase: phase, Status: status, Message: msg})
}
```

Or directly for structured progress:

```go
// pkg/api/image.go
onProgress(event.Progress{Phase: "download", Status: "running", Message: "Downloading image..."})
```

### Download Progress Bridge

For downloads with known content-length, `FormatProgress` bridges byte-level progress to phase-level events:

```go
// internal/infra/event/event.go
func FormatProgress(onProgress OnProgressCallback) OnDownloadCallback {
    // Returns an OnDownloadCallback that throttles and formats
    // byte-level progress into event.Progress events
}
```

Usage:

```go
progressBridge := event.FormatProgress(onProgress)
// Pass progressBridge to download function
```

### Consumer Patterns

**CLI — spinner:**
```go
prog := common.NewProgress()
prog.Start("Creating VM...")
defer prog.Stop()

vms, err := op.VMCreate(ctx, input, func(e event.Progress) {
    if e.Message != "" {
        prog.UpdateText(e.Message)
    }
})
```

**Headless:**
```go
vms, err := op.VMCreate(ctx, input, nil) // zero overhead
```

---

## Mechanism 3: NeedsInteraction (Mid-Operation Interruption)

When the API detects it cannot proceed without user input (sudo, confirmation, choice), it returns a `NeedsInteraction` error **instead of** a normal result. The consumer handles the interaction and calls the same API method again.

### Why an error, not a return value

In Go, `NeedsInteraction` implements the `error` interface so it flows naturally through `(T, error)` return types. The consumer checks via `errors.As()`. This is NOT an unexpected error — it is recognized control flow.

### Struct Definition

```go
// pkg/errs/result.go

type NeedsInteraction struct {
    Code      string         `json:"code"`
    Message   string         `json:"message"`
    InputType string         `json:"input_type"` // "sudo", "confirm", "choice", "input"
    Context   map[string]any `json:"context,omitempty"`
}

func (n *NeedsInteraction) Error() string { return n.Message }
```

### Consumer Flow (Retry Pattern)

Check with `errors.As()` — there is no `status` field on `NeedsInteraction`:

```go
// internal/cli/init.go
for {
    result := state.runInit(ctx)
    if result.NeedsInteraction == nil {
        return result, nil // done
    }
    if err := state.dispatch(ctx, result.NeedsInteraction); err != nil {
        return lastResult, nil
    }
    // Loop continues — re-run init with updated state
}
```

The `dispatch` method switches on `interaction.Code`:
- `"privilege.sudo_required"` → prompts user, runs `sudo mvm host init`
- `"binary.confirm_download"` → prompts user, sets download version
- `"guestfs.confirm_enable"` → prompts user, sets guestfs flag

**In host init:**
```go
rawResult, err := op.HostInit(ctx, nil)
if err != nil {
    var ni *errs.NeedsInteraction
    if errors.As(err, &ni) {
        // Handle sudo escalation: prompt, re-exec with sudo
    }
}
```

---

## Consumer Cheat Sheet

| Consumer | `OperationResult` | `Progress` | `NeedsInteraction` |
|----------|------------------|------------|---------------------|
| **CLI** | Inspect `Status` + `Code` for icons/colors; use `Message` as fallback. Check `IsError()` for error branches. | Pass `onProgress` → update `common.Progress` spinner | Check `errors.As()` → read `Code`/`InputType`/`Context` → prompt → retry |
| **TUI** | Inspect `Status` + `Code` + `Metadata` + `Item` for rich widget rendering | Pass `onProgress` → update phase timeline, progress bars | Check `errors.As()` → show dialog widget → retry |
| **GUI** | Inspect `Status` + `Code` + `Metadata` for dialog/modal | Pass `onProgress` → update notification, progress bar | Check `errors.As()` → show modal → retry |
| **Headless** | Inspect `Status` + `Code`; log `Message` with appropriate level | Pass `nil`; zero overhead | Check `errors.As()` → fail with descriptive message |

---

## Implementation Status

All domains are converted. The following table shows which patterns each API operation uses.

| Domain | File | Mutation methods return | Progress via |
|--------|------|------------------------|--------------|
| **Host** | `pkg/api/host.go` | `(any, error)` / `*errs.NeedsInteraction` | N/A |
| **VM** | `pkg/api/vm.go` | `([]*model.VMItem, error)` / `*errs.BatchResult` | `onProgress` (create only) |
| **Volume** | `pkg/api/volume.go` | `(*model.VolumeItem, error)` / `*errs.BatchResult` | N/A |
| **Network** | `pkg/api/network.go` | `(*model.NetworkItem, error)` / `error` | N/A |
| **Image** | `pkg/api/image.go` | `(*model.ImageItem, error)` / `*errs.BatchResult` | `onProgress` (pull, import) |
| **Kernel** | `pkg/api/kernel.go` | `(*model.KernelItem, error)` / `*errs.BatchResult` | `onProgress` (pull only) |
| **Binary** | `pkg/api/binary.go` | `([]*model.BinaryItem, error)` / `*errs.BatchResult` | `FormatProgress` bridge |
| **Key** | `pkg/api/key.go` | `(*model.KeyItem, error)` / `*errs.BatchResult` | N/A |
| **Cache** | `pkg/api/cache.go` | `*errs.OperationResult` | `onProgress` (init only) |
| **Config** | `pkg/api/config.go` | `(any, error)` | N/A |
| **SSH** | `pkg/api/ssh.go` | `error` | N/A |
| **CP** | `pkg/api/cp.go` | `(*results.CPCopyResult, error)` | `OnDownloadCallback` (bytes-chunk callback) |
| **Console** | `pkg/api/console.go` | `(*results.ConsoleStateResult, error)` / `error` | N/A |
| **Init** | `pkg/api/init.go` | `*InitResult` with `NeedsInteraction` | `onProgress` threaded to cache |
| **Snapshot** | `pkg/api/snapshot.go` | `(*model.SnapshotItem, error)` / `[]*model.VMItem` / `*errs.BatchResult` | `onProgress` (create only) |

---

## Guidelines for API Developers

### When to return what

| Situation | Return |
|-----------|--------|
| Operation succeeded, state changed | `result := OperationResult{Status: "success", ...}` |
| Already in desired state, no change | `result := OperationResult{Status: "skipped", Code: "vm.already_exists", ...}` |
| Succeeded with known caveat | `result := OperationResult{Status: "warning", Code: "vm.with_warnings", ...}` |
| Foreseeable domain error (in use, not found, permission) | `return nil, errs.New(errs.CodeVMNotFound, "VM not found")` |
| Unexpected system error (DB connection, OSError) | `return nil, errs.New(errs.CodeInternal, "database error")` |
| Need user input to continue | `return nil, &errs.NeedsInteraction{...}` (caller handles and retries) |
| Batch operation on multiple items | `&errs.BatchResult{Items: results}` wrapping per-item `OperationResult` |

### Batch operations

For operations that act on multiple items (remove several VMs, remove several kernels), use `BatchResult`:

```go
results := make([]errs.OperationResult, 0)
for _, vm := range vms {
    err := op.doRemove(ctx, vm)
    if err != nil {
        results = append(results, errs.OperationResult{
            Status:  "error",
            Code:    "vm.remove_failed",
            Message: fmt.Sprintf("Failed to remove %s: %v", vm.Name, err),
            Item:    vm,
        })
    } else {
        results = append(results, errs.OperationResult{
            Status:  "success",
            Code:    "vm.removed",
            Message: fmt.Sprintf("Removed %s", vm.Name),
            Item:    vm,
        })
    }
}
return &errs.BatchResult{Items: results}
```

### CLI consumer pattern for batches

```go
removeResult := op.VMRemove(ctx, inputs.VMInput{Identifiers: identifiers, Force: force})
if removeResult.HasErrors() {
    for _, r := range removeResult.Items {
        if r.IsOK() {
            common.Cli.Success(r.Message)
        } else {
            common.Cli.Error(r.Message)
        }
    }
    return fmt.Errorf("one or more removals failed")
}
common.Cli.Success(fmt.Sprintf("Removed: %s", strings.Join(names, ", ")))
```

### Do NOT

- Do NOT `fmt.Println()` in the API layer — return `OperationResult` with a `Message`
- Do NOT return `NeedsInteraction` for errors — use `DomainError` with appropriate `Code`
- Do NOT use `NeedsInteraction` for errors — use `OperationResult{Status: "error"/"failure"}`
- Do NOT pass `onProgress` for fast/simple operations — it adds unnecessary complexity

### Code Convention Summary

- All mutation methods return `OperationResult` or `BatchResult` (or `(T, error)` with `DomainError`)
- Read methods (get, list, inspect) return data directly or return `error`
- All `OperationResult` use a `code` following `<domain>.<noun>.<verb>` convention
- The `code` IS the audit log event name — no separate naming
- `onProgress` is always `event.OnProgressCallback` (which is `func(event.Progress)`) — optional, nil means no progress
- Every emitted `Progress` uses the direct `if onProgress == nil { return }` check pattern (or `emitProgress` helper)

## Appendix: Master Code Table

Every `code` value currently used in the codebase (defined in `pkg/errs/codes.go`):

| Code | Status | Domain | Meaning |
|------|--------|--------|---------|
| `vm.not_found` | error | VM | VM not found |
| `vm.already_exists` | error | VM | VM already exists |
| `vm.state.invalid` | error | VM | Invalid state transition |
| `vm.create.failed` | error | VM | VM creation failed |
| `vm.create.builder_failed` | failure | VM | VM builder failed |
| `vm.resolve.failed` | error | VM | VM resolution failed |
| `vm.resource.exhausted` | error | VM | Resource exhausted |
| `vm.create.binary_not_found` | error | VM | Binary not found for VM creation |
| `vm.create.image_not_found` | error | VM | Image not found for VM creation |
| `vm.create.kernel_not_found` | error | VM | Kernel not found for VM creation |
| `vm.create.network_not_found` | error | VM | Network not found for VM creation |
| `vm.create.ssh_key_not_found` | error | VM | SSH key not found for VM creation |
| `vm.name_collision` | error | VM | VM name collision during batch create |
| `vm.atomic_failed` | failure | VM | Atomic batch create failed |
| `vm.create_failure` | failure | VM | VM creation failed unexpectedly |
| `vm.snapshot_failed` | error | VM | VM snapshot failed |
| `vm.load_snapshot_failed` | error | VM | VM snapshot load failed |
| `network.subnet.overlap` | error | Network | Subnet overlap |
| `network.not_found` | error | Network | Network not found |
| `network.already_exists` | error | Network | Network already exists |
| `network.bridge.failed` | error | Network | Bridge creation failed |
| `network.nat.failed` | error | Network | NAT setup failed |
| `network.lease.failed` | error | Network | Lease allocation failed |
| `network.lease.exhausted` | error | Network | Lease pool exhausted |
| `network.firewall.failed` | error | Network | Firewall setup failed |
| `network.create_failed` | error | Network | Network creation failed |
| `network.remove_failed` | error | Network | Network removal failed |
| `network.default_set_failed` | error | Network | Failed to set default network |
| `network.default_created_failed` | error | Network | Default network creation failed |
| `image.not_found` | error | Image | Image not found |
| `image.already_exists` | error | Image | Image already exists |
| `image.pull.failed` | error | Image | Image pull failed |
| `image.import.failed` | error | Image | Image import failed |
| `image.checksum.mismatch` | error | Image | Checksum mismatch |
| `image.corrupt` | error | Image | Image is corrupt |
| `image.empty` | error | Image | Image is empty |
| `image.format.invalid` | error | Image | Invalid image format |
| `image.error` | error | Image | Image error |
| `image.compression.failed` | error | Image | Compression failed |
| `image.decompression.failed` | error | Image | Decompression failed |
| `image.root_partition_detection` | error | Image | Root partition detection failed |
| `image.tie_detected` | error | Image | Tie detected in partition selection |
| `image.acquire_failed` | error | Image | Image acquire failed |
| `image.warm_failed` | error | Image | Image warm failed |
| `kernel.not_found` | error | Kernel | Kernel not found |
| `kernel.build.failed` | error | Kernel | Kernel build failed |
| `kernel.config.failed` | error | Kernel | Kernel config failed |
| `kernel.pull_failed` | error | Kernel | Kernel pull failed |
| `kernel.import.failed` | error | Kernel | Kernel import failed |
| `kernel.default_set_failed` | error | Kernel | Failed to set default kernel |
| `binary.not_found` | error | Binary | Binary not found |
| `binary.already_exists` | error | Binary | Binary already exists |
| `binary.version.gate` | error | Binary | Version gate |
| `binary.error` | error | Binary | Binary error |
| `binary.pull_failed` | error | Binary | Binary pull failed |
| `binary.remove_failed` | error | Binary | Binary removal failed |
| `binary.default_set_failed` | error | Binary | Failed to set default binary |
| `binary.ensure_default_failed` | failure | Binary | Failed to ensure default binary |
| `binary.no_ci_version` | error | Binary | No CI version available |
| `volume.not_found` | error | Volume | Volume not found |
| `volume.already_exists` | error | Volume | Volume already exists |
| `volume.error` | error | Volume | Volume error |
| `volume.resize_failed` | error | Volume | Volume resize failed |
| `key.not_found` | error | Key | Key not found |
| `key.already_exists` | error | Key | Key already exists |
| `key.export.failed` | error | Key | Key export failed |
| `key.dependency.missing` | error | Key | Key dependency missing |
| `key.create_failed` | error | Key | Key creation failed |
| `key.add_failed` | error | Key | Key add failed |
| `key.default_set_failed` | error | Key | Failed to set default key |
| `key.defaults_clear_failed` | error | Key | Failed to clear default keys |
| `host.init.failed` | error | Host | Host init failed |
| `host.clean.failed` | error | Host | Host clean failed |
| `host.reset.failed` | error | Host | Host reset failed |
| `host.privilege.required` | error | Host | Privilege required |
| `host.init.sudoers.failed` | error | Host | Sudoers setup failed |
| `host.info_failed` | error | Host | Host info failed |
| `host.capacity_failed` | error | Host | Capacity detection failed |
| `cloudinit.provision.failed` | error | Cloud-init | Cloud-init provision failed |
| `cloudinit.net_mode.failed` | error | Cloud-init | Cloud-init net mode failed |
| `cloudinit.iso_mode.failed` | error | Cloud-init | Cloud-init ISO mode failed |
| `cloudinit.inject.failed` | error | Cloud-init | Cloud-init inject failed |
| `cloudinit.mode.error` | error | Cloud-init | Cloud-init mode error |
| `cloudinit.off_mode.error` | error | Cloud-init | Cloud-init off mode error |
| `console.relay.failed` | error | Console | Console relay failed |
| `console.not_running` | error | Console | Console relay not running |
| `console.kill_failed` | error | Console | Console relay kill failed |
| `logs.error` | error | Logs | Logs error |
| `firecracker.error` | error | Firecracker | Firecracker error |
| `firecracker.client.error` | error | Firecracker | Firecracker client error |
| `firecracker.spawn.failed` | error | Firecracker | Firecracker spawn failed |
| `firecracker.config.failed` | error | Firecracker | Firecracker config failed |
| `firecracker.socket.not_found` | error | Firecracker | Firecracker socket not found |
| `guestfs.error` | error | GuestFS | GuestFS error |
| `guestfs.not_available` | error | GuestFS | GuestFS not available |
| `guestfs.write.failed` | error | GuestFS | GuestFS write failed |
| `loopmount.error` | error | LoopMount | LoopMount error |
| `loopmount.binary.not_found` | error | LoopMount | LoopMount binary not found |
| `loopmount.timeout` | error | LoopMount | LoopMount timeout |
| `ssh.error` | error | SSH | SSH error |
| `cp.error` | error | CP | CP error |
| `cp.source.not_found` | error | CP | CP source not found |
| `cp.source.failed` | error | CP | CP source failed |
| `cp.copy.failed` | error | CP | CP copy failed |
| `cp.destination.exists` | error | CP | CP destination exists |
| `cp.destination.failed` | error | CP | CP destination failed |
| `cp.destination.not_directory` | error | CP | CP destination not directory |
| `cp.multi_source_no_vm_destination` | error | CP | CP multi-source no VM destination |
| `cp.resolve_failed` | error | CP | CP resolve failed |
| `cp.no_vm_specified` | error | CP | CP no VM specified |
| `cp.vm_no_ip` | error | CP | CP VM has no IP |
| `cp.vm_not_found` | error | CP | CP VM not found |
| `internal` | failure | Common | Internal error |
| `not_implemented` | error | Common | Not implemented |
| `validation.failed` | error | Common | Validation failed |
| `database.error` | error | Common | Database error |
| `database.migration.failed` | failure | Common | Database migration failed |
| `process.error` | error | Common | Process error |
| `download.failed` | error | Common | Download failed |
| `http.error` | error | Common | HTTP error |
| `config.error` | error | Common | Config error |
| `cache.clean_failed` | error | Common | Cache clean failed |
| `snapshot.not_found` | error | Snapshot | Snapshot not found |
| `snapshot.already_exists` | error | Snapshot | Snapshot already exists |
| `snapshot.create_failed` | failure | Snapshot | Snapshot creation failed |
| `snapshot.restore_failed` | failure | Snapshot | Snapshot restore failed |
| `snapshot.remove_failed` | failure | Snapshot | Snapshot removal failed |
| `vsock.not_found` | error | Vsock | Vsock not found |
| `vsock.connection_failed` | error | Vsock | Vsock connection failed |
| `vsock.handshake_failed` | error | Vsock | Vsock handshake failed |
| `vsock.agent_unreachable` | error | Vsock | Vsock agent unreachable |
| `vsock.exec_failed` | error | Vsock | Vsock exec failed |
| `vsock.upgrade_in_progress` | error | Vsock | Vsock upgrade in progress |
| `bundled_asset.error` | failure | BundledAsset | Bundled asset error |
| `bundled_asset.not_found` | error | BundledAsset | Bundled asset not found |
| `network.error` | error | Network | Network error |
| `key.error` | error | Key | Key error |
| `version.resolve.failed` | error | Common | Version resolve failed |
