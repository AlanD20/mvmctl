# Per-Domain API Interfaces (not One Monolithic Interface)

**Status:** Active
**Date:** 2026-06-14

The API layer (`pkg/api/`) is defined as multiple small Go interfaces — one per domain — rather than a single `API` interface with all 131 methods. A composite `API` interface embedding all domains is provided for consumers that need everything (TUI, advanced workflows).

**Table of Contents**

- [Context](#context)
- [Decision](#decision)
- [Considered Options](#considered-options)
- [Consequences](#consequences)

## Context

`pkg/api/` exposes orchestration methods on the `*Operation` struct across 17 domains (VM, Image, Network, Volume, Kernel, Key, Binary, Host, Console, SSH, Exec, Config, Cache, Log, CP, Init, Snapshot). Three consumer layers depend on it:

- **CLI** (`internal/cli/`) — one domain per command file
- **TUI** (`internal/tui/`) — multiple domains per screen, all domains in the model
- **Workflow engine** (`internal/workflow/env/`) — one or two domains per step type

No interface existed. All consumers referenced `*api.Operation` directly (concrete struct), making unit tests impossible without real infrastructure (DB, services, subprocesses).

## Decision

Define **one Go interface per domain** in `pkg/api/`. Each domain's interface lives in its existing file (e.g., `VMAPI` in `vm.go`). A composite `API` interface in `interfaces.go` embeds all domains.

`*Operation` satisfies all interfaces automatically — zero changes to the struct itself.

### What This Is Not

- NOT an abstraction for the Operation's implementation — `*Operation` continues to call its own methods directly via `op.Method()`.
- NOT an internal refactor of the API layer — the interfaces exist only at the boundary between the API layer and its consumers.

## Considered Options

### Option A: One Monolithic `API` Interface

A single interface with all ~131 methods. One mock with ~131 function fields.

- **Test imports** — VM tests must import Image, Network, Host types even though they don't use them
- **Mock maintenance** — adding one VM method adds a field to a mock that Image, Network, and Host tests also import
- **Consumer clarity** — `func run(op api.API)` hides whether the function needs VM, Image, or both
- **Growth** — every new domain appends to the same interface, every existing consumer recompiles

### Option B: Per-Domain Interfaces + Composite (selected)

Small, focused interfaces per domain. A composite `API` interface for consumers that need everything.

- **Test isolation** — VM tests only import `VMAPI` and `MockVMAPI`
- **Consumer clarity** — `func run(op api.VMAPI)` declares exactly what it needs
- **Gradual migration** — consumers migrate one at a time, no big-bang refactor
- **Growth** — new domain = new file, new interface, one line in composite. Existing interfaces unchanged.

### Option C: No Interface (Status Quo)

Keep referencing `*api.Operation` directly in all consumers.

- **Untestable** — CLI/TUI/workflow tests need real DB, services, and subprocesses
- **No seam** — no way to substitute behavior in tests without heavy infrastructure
- **Rejected because:** testability is the goal of this work

## Consequences

**Positive:**

- CLI commands accept narrow interfaces showing their true dependencies
- Tests use small, focused mocks per domain (10-15 functions, not ~131)
- The composite `API` means TUI still holds one type, not 16 separate interfaces
- Adding a new domain is additive — no existing interfaces or mocks change
- Internal cross-calls within `Operation` are completely unaffected

**Negative:**

- More interfaces to manage (18 total: 17 domain + 1 composite)
- CLI command signatures change from `*api.Operation` to `api.VMAPI` (mechanical migration)
- Slightly more files in `internal/testutil/` (one mock file per domain)

**Neutral:**

- `*Operation` still works as a concrete type everywhere — no code outside `pkg/api/` needs to change immediately
- Migration is gradual, per-consumer, not a big-bang refactor
