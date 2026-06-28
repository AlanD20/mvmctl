# API Per-Domain Interfaces

## Problem

The `pkg/api/` package defines the contract between mvmctl's orchestrator layer and its consumers (CLI, TUI, workflow engine). Without per-domain interfaces, every consumer must import the entire `*api.Operation` struct — 80+ methods — even when it needs only VM operations. This creates large mock surface areas, unnecessary import coupling, and unclear dependencies. Per-domain interfaces give each consumer exactly the methods it needs.

## Architecture

Each domain in `pkg/api/` exposes a Go interface in its own file. Consumers declare only the interfaces they need. A composite `API` interface embeds all per-domain interfaces for consumers that need everything.

```
┌──────────────────────────────────────────────────────────────────┐
│                        Operation struct                           │
│                                                                   │
│  // Internal cross-calls are direct struct methods,               │
│  // unaffected by the interfaces.                                │
│  CachePruneAll() {                                                │
│      ids, _ := op.VMPrune(...)          ← direct struct method   │
│      ids, _ := op.NetworkPrune(...)     ← direct struct method   │
│  }                                                                │
│                                                                   │
│  VMPrune() { ... }                                                │
│  NetworkPrune() { ... }                                           │
└──────────────────────────────────────────────────────────────────┘
         │ implements VMAPI    │ implements NetworkAPI    
         ▼                     ▼                        
    ┌──────────┐         ┌──────────────┐              
    │  VMAPI   │         │  NetworkAPI  │              
    └──────────┘         └──────────────┘              
         ▲                     ▲                        
         │ accepts             │ accepts                
    ┌──────────┐         ┌──────────────┐              
    │cli/vm.go │         │tui/screen/   │              
    │          │         │network.go    │              
    └──────────┘         └──────────────┘              

    ┌──────────────────────────────────────────────────┐
    │ Composite API (for TUI/model.go):                │
    │                                                  │
    │  type API interface {                            │
    │      VMAPI                                       │
    │      ImageAPI                                    │
    │      NetworkAPI                                  │
    │      // ... one line per domain                  │
    │  }                                               │
    └──────────────────────────────────────────────────┘
```

**Key insight:** `Operation`'s internal cross-calls (`op.VMPrune()` inside `CachePruneAll`) are direct method calls on the same struct. They are unaffected by the interfaces. The interfaces exist only at the boundary between the API layer and its consumers.

## The interfaces

Each domain in `pkg/api/` has an interface in its existing file:

| File | Interface | Methods |
|------|-----------|---------|
| `vm.go` | `VMAPI` | 11 |
| `image.go` | `ImageAPI` | 9 |
| `network.go` | `NetworkAPI` | 10 |
| `volume.go` | `VolumeAPI` | 8 |
| `kernel.go` | `KernelAPI` | 8 |
| `key.go` | `KeyAPI` | 10 |
| `binary.go` | `BinaryAPI` | 8 |
| `host.go` | `HostAPI` | 15 |
| `console.go` | `ConsoleAPI` | 4 |
| `exec.go` | `ExecAPI` | 1 |
| `ssh.go` | `SSHAPI` | 1 |
| `config.go` | `ConfigAPI` | 4 |
| `cache.go` | `CacheAPI` | 11 |
| `logs.go` | `LogAPI` | 2 |
| `cp.go` | `CPAPI` | 1 |
| `init.go` | `InitAPI` | 4 |
| `snapshot.go` | `SnapshotAPI` | 5 |

All interfaces are satisfied by `*Operation` with zero changes to the Operation struct itself.

## The composite API interface

Defined in `pkg/api/interfaces.go`:

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
    ExecAPI
    SSHAPI
    ConfigAPI
    CacheAPI
    LogAPI
    CPAPI
    InitAPI
    SnapshotAPI
}
```

## What per-domain interfaces achieve

| Concern | Per-Domain Interfaces | Single `API` Interface |
|---------|----------------------|------------------------|
| **ISP compliance** | Consumer declares only the domain it needs | Consumer imports all methods |
| **Mock size** | ~10-15 function fields per domain mock | 80+ function fields in a single mock |
| **Test isolation** | Domain tests don't import other domain types | All tests import all types |
| **CLI clarity** | `func run(op api.VMAPI)` says exactly what it needs | `func run(op api.API)` hides actual dependencies |
| **Adding a domain** | New file, new interface, one line in composite | Append to single interface, all consumers recompile |
| **TUI convenience** | Holds `api.API` composite (one type) | Holds `api.API` (one type) — same |
| **Internal cross-calls** | Unaffected — `op.Method()` on the same struct | Unaffected — same |

## The mock pattern

Per-domain mocks live in `internal/testutil/mock_<domain>_api.go`:

```go
type MockVMAPI struct {
    VMCreateFunc func(ctx, input, onProgress) ([]*model.VMItem, error)
    VMRemoveFunc func(ctx, input) *errs.BatchResult
    // ...
}

func (m *MockVMAPI) VMCreate(ctx, input, onProgress) ([]*model.VMItem, error) {
    if m.VMCreateFunc != nil { return m.VMCreateFunc(ctx, input, onProgress) }
    return nil, nil
}
```

Default return values are zero/nil. Tests set only the functions they need. `MockOperation` embeds all per-domain mocks and satisfies the composite `API` interface.

## Key files

| File | Purpose |
|------|---------|
| `pkg/api/interfaces.go` | Composite `API` interface embedding all per-domain interfaces |
| `pkg/api/vm.go` | `VMAPI` interface (11 methods) + `Operation` implementation |
| `pkg/api/image.go` | `ImageAPI` interface (9 methods) |
| `pkg/api/network.go` | `NetworkAPI` interface (10 methods) |
| `pkg/api/volume.go` | `VolumeAPI` interface (8 methods) |
| `pkg/api/kernel.go` | `KernelAPI` interface (8 methods) |
| `pkg/api/key.go` | `KeyAPI` interface (10 methods) |
| `pkg/api/binary.go` | `BinaryAPI` interface (8 methods) |
| `pkg/api/host.go` | `HostAPI` interface (15 methods) |
| `pkg/api/console.go` | `ConsoleAPI` interface (4 methods) |
| `pkg/api/exec.go` | `ExecAPI` interface (1 method) |
| `pkg/api/ssh.go` | `SSHAPI` interface (1 method) |
| `pkg/api/config.go` | `ConfigAPI` interface (4 methods) |
| `pkg/api/cache.go` | `CacheAPI` interface (11 methods) |
| `pkg/api/logs.go` | `LogAPI` interface (2 methods) |
| `pkg/api/cp.go` | `CPAPI` interface (1 method) |
| `pkg/api/init.go` | `InitAPI` interface (4 methods) |
| `pkg/api/snapshot.go` | `SnapshotAPI` interface (5 methods) |
