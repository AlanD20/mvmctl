# Input Pattern v2: Collapse Request/Resolved into Input

**Status:** accepted
**Date:** 2026-06-19

## Context

Every public-facing domain in `pkg/api/inputs/` followed a three-struct pattern:

| Struct | Job |
|--------|-----|
| `*Input` | Raw user input, `*T` for optionals |
| `*Request` | Bundles Input + dependencies (repos, DB, config), has `Resolve()` |
| `Resolved*` | Validated, defaults-filled version of Input |

The `*Request` struct was a boilerplate wrapper that existed solely to receive dependencies
and forward them to `Resolve()`. The `Resolved*` struct was useful only when the output
shape differed from the input shape (create flows with dozens of resolved defaults).
For simple lookup inputs (`VMInput`, `KernelInput`, `SnapshotInput`), it was a 1:1 mirror.

This triple added ceremony without proportional value — every new input type required
three structs, a constructor, and a resolve method, all spread across the same file.

## Decision

Collapse the triple into a single `*Input` struct with `Validate()` and per-domain
`ResolveXxx()` methods:

```go
// ─── Old (three structs) ──────────────────────────────────────────────

type SnapshotInput struct {
    Identifiers []string
    Force       bool
}

type SnapshotRequest struct {
    input   SnapshotInput
    repo    snapshot.Repository
    result  *ResolvedSnapshotInput
}

type ResolvedSnapshotInput struct {
    Snapshots []*model.SnapshotItem
    Force     bool
}

func (r *SnapshotRequest) Resolve(ctx) (*ResolvedSnapshotInput, error) { ... }

// ─── New (one struct) ─────────────────────────────────────────────────

type SnapshotInput struct {
    Identifiers []string
    Force       bool
}

func (i *SnapshotInput) Validate() error { ... }
func (i *SnapshotInput) Resolve(ctx, repo) ([]*model.SnapshotItem, error) { ... }
```

### Rules

1. **`Validate()` is always called first** — either by the API layer directly,
   or as the first thing inside `Resolve()`. Never skip validation.

2. **`ResolveXxx()` calls `Validate()` internally** — so callers get validation
   for free. Callers that need granular per-identifier error reporting (like
   `Remove` with `BatchResult`) call `Validate()` separately.

3. **One `ResolveXxx()` per single domain** — each method takes exactly one
   repo interface. If the input needs to resolve entities from multiple domains
   (e.g., snapshot + network), it gets multiple methods: `ResolveSnapshot()`,
   `ResolveNetwork()`. Cross-domain orchestration (branching, fallback,
   enrichment) stays in the API layer.

4. **No `Resolved*` struct unless the output shape differs** — for simple
   lookups, `Resolve()` returns domain entities directly. Keep `Resolved*`
   only when the output shape is structurally different (create flows with
   resolved defaults, or bundles like `ResolvedVMExecInput`).

5. **No `*Request` struct** — the bundling of input + deps is unnecessary.
   Methods on Input take deps as parameters.

### When to keep `Resolved*`

Only when the output type is structurally different from the input:

| Input | Resolved needed? | Reason |
|-------|-----------------|--------|
| `VMInput` | ❌ No | Returns `[]*model.VMItem`. Shape matches input (Identifiers). |
| `SnapshotInput` | ❌ No | Returns `[]*model.SnapshotItem`. Shape matches input. |
| `VMCreateInput` | ✅ Yes | Input has `*T` optionals; Resolved has concrete values. Dozens of config defaults resolved. |
| `KernelPullInput` | ✅ Yes | Input has version/type; Resolved has full specs, download paths. |
| `VMExecInput` | ✅ Yes | Resolved bundles VM + vsock config + resolved user. |

### Cross-domain resolution

When an input needs to resolve entities from multiple domains:

1. **Single-domain methods** — each `ResolveXxx()` takes exactly one repo:
   ```go
   func (i *Input) ResolveSnapshot(ctx, snapRepo) (*model.SnapshotItem, error)
   func (i *Input) ResolveNetwork(ctx, netRepo) (*model.NetworkItem, error)
   ```

2. **Branching in API layer** — the API layer decides which to call and in
   what order, including fallback logic:
   ```go
   net, err := input.ResolveNetwork(ctx, netRepo)
   if net == nil { net = snap.Network } // fallback
   ```

3. **Enrichment stays in API layer** — after resolution, the API layer calls
   the enricher. Resolution is pure data lookup; enrichment is cross-domain
   decoration.

## Consequences

### Positive

- **Fewer structs per domain** — 1 struct instead of 3. No constructor boilerplate.
- **Clearer data flow** — `input.Resolve(ctx, repo)` instead of
  `NewXxxRequest(input, deps).Resolve(ctx)`.
- **Dead code removed** — `db *sqlx.DB` was stored on every `*Request` but never
  used. Gone.
- **Consistent partial-error pattern** — all lookup `Resolve()` methods return
  `(items, combinedError)` for partial failures, handled consistently in the
  API layer.

### Negative

- **Caller signatures changed** — all API-layer callers had to be updated to
  the new pattern.
- **`Resolved*` ambiguity** — the rule "keep only if shape differs" requires
  judgment on each case.

### Migration

All 11 domains were migrated in a single session (June 2026):
image, volume, key, log, binary, kernel, network, ssh, console, config, cp, vm.
The `VMCreateRequest` struct in `vm_create.go` was kept — it uses a builder
pattern (not the old triple) and is a separate concern.

## Considered Options

- **Keep the triple** — more ceremony, more files. Rejected for being
  unnecessarily verbose.
- **Validate-only on Input, separate Resolver objects** — alternative to
  putting Resolve on Input. Rejected because it adds a file per domain for
  trivial resolver methods.
- **Generic `Resolve(ctx, ...any)` with variadic deps** — loses type safety.
  Rejected.
