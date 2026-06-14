> **STATUS: Draft — proposed design, not yet implemented.**
>
> **Last updated:** 2026-06-14

# API Per-Domain Interfaces

## Overview

The `pkg/api/` package defines the **contract** between mvmctl's orchestrator layer and its consumers (CLI, TUI, workflow engine). This contract is formalised as **one Go interface per domain**, not one monolithic `API` interface.

The interfaces sit at the **boundary** — they define what consumers can call, not how `*Operation` implements those calls internally.

## Why Per-Domain, Not One Big API

```
┌──────────────────────────────────────────────────────────────────┐
│                        Operation struct                           │
│                                                                   │
│  CachePruneAll() {                                                │
│      ids, _ := op.VMPrune(...)          ← direct struct method   │
│      ids, _ := op.NetworkPrune(...)     ← direct struct method   │
│      ids, _ := op.ImagePrune(...)       ← direct struct method   │
│  }                                                                │
│                                                                   │
│  VMPrune() { ... }                                                │
│  NetworkPrune() { ... }                                           │
│  ImagePrune() { ... }                                             │
└──────────────────────────────────────────────────────────────────┘
         │ implements VMAPI    │ implements NetworkAPI    │ implements ImageAPI
         ▼                     ▼                         ▼
    ┌──────────┐         ┌──────────────┐          ┌────────────┐
    │  VMAPI   │         │  NetworkAPI  │          │  ImageAPI  │
    └──────────┘         └──────────────┘          └────────────┘
         ▲                     ▲                         ▲
         │ accepts             │ accepts                  │ accepts
    ┌──────────┐         ┌──────────────┐          ┌────────────┐
    │cli/vm.go │         │tui/screen/   │          │workflow/   │
    │          │         │network.go    │          │step_image  │
    └──────────┘         └──────────────┘          └────────────┘

    ┌──────────────────────────────────────────────────────────┐
    │ Composite API (for TUI/model.go that needs everything):  │
    │                                                          │
    │  type API interface {                                    │
    │      VMAPI                                               │
    │      ImageAPI                                            │
    │      NetworkAPI                                          │
    │      // ... one line per domain                          │
    │  }                                                       │
    └──────────────────────────────────────────────────────────┘
```

**Key insight:** `Operation`'s internal cross-calls (`op.VMPrune()` inside `CachePruneAll`) are **direct method calls on the same struct**. They are unaffected by the interfaces. The interfaces exist only at the boundary between the API layer and its consumers.

## What This Achieves

| Concern | Per-Domain Interfaces | One Big `API` Interface |
|---------|----------------------|------------------------|
| **ISP compliance** | Consumer declares only the domain it needs | Consumer imports all 83 methods |
| **Mock size** | ~10-15 function fields per domain mock | 83 function fields in a single mock |
| **Test isolation** | `vm` tests don't import `ImageAPI` types | All tests import all types |
| **CLI clarity** | `func run(op api.VMAPI)` says exactly what it needs | `func run(op api.API)` hides actual dependencies |
| **Adding a domain** | New file, new interface, one line in composite | Append to single interface, all consumers recompile |
| **TUI convenience** | Holds `api.API` composite (one type) | Holds `api.API` (one type) — same |
| **Internal cross-calls** | Unaffected — `op.Method()` on the same struct | Unaffected — same |

## The Interfaces

Each domain in `pkg/api/` gets an interface in its existing file:

| File | Interface | Methods |
|------|-----------|---------|
| `vm.go` | `VMAPI` | 15 |
| `image.go` | `ImageAPI` | 9 |
| `network.go` | `NetworkAPI` | 10 |
| `volume.go` | `VolumeAPI` | 6 |
| `kernel.go` | `KernelAPI` | 8 |
| `key.go` | `KeyAPI` | 10 |
| `binary.go` | `BinaryAPI` | 8 |
| `host.go` | `HostAPI` | 15 |
| `console.go` | `ConsoleAPI` | 4 |
| `ssh.go` | `SSHAPI` | 1 |
| `config.go` | `ConfigAPI` | 4 |
| `cache.go` | `CacheAPI` | 12 |
| `logs.go` | `LogAPI` | 2 |
| `cp.go` | `CPAPI` | 1 |
| `init.go` | `InitAPI` | 4 |

The composite `API` interface lives in `interfaces.go`:

```go
type API interface {
    VMAPI
    ImageAPI
    NetworkAPI
    VolumeAPI
    KernelAPI
    KeyAPI
    BinaryAPI
    HostAPI
    ConsoleAPI
    SSHAPI
    ConfigAPI
    CacheAPI
    LogAPI
    CPAPI
    InitAPI
}
```

`*Operation` already satisfies all of them — **zero changes to the Operation struct itself**.

## Consumer Migration Pattern

Existing code uses `*api.Operation` everywhere. The interfaces enable a **gradual** migration:

1. **Define interfaces** — `*Operation` satisfies them immediately. No breakage.
2. **Create mocks** — `MockVMAPI`, `MockImageAPI`, etc. in `internal/testutil/`. Function-field pattern matching existing `FakeRunner`/`FakeNetOps` conventions.
3. **Migrate consumers one at a time** — change `*api.Operation` to `api.VMAPI` in CLI's `vm.go`, test with `MockVMAPI`. No big-bang refactor.
4. **TUI uses composite** — `Model.op` becomes `api.API`, `MockOperation` embeds per-domain mocks.

## The Mock

Per-domain mocks live in `internal/testutil/mock_<domain>_api.go`:

```go
type MockVMAPI struct {
    VMCreateFunc func(ctx, input, onProgress) ([]*model.VM, error)
    VMRemoveFunc func(ctx, input) *errs.BatchResult
    // ...
}

func (m *MockVMAPI) VMCreate(ctx, input, onProgress) ([]*model.VM, error) {
    if m.VMCreateFunc != nil { return m.VMCreateFunc(ctx, input, onProgress) }
    return nil, nil
}
```

Default return values are zero/nil. Tests set only the functions they need.

`MockOperation` embeds all per-domain mocks and satisfies the composite `API` interface.
