# Two-Phase Validation with Caller-Trusts-Callee

**Status:** Active
**Date:** 2026-05-22
**Last Updated:** 2026-06-19 (Go input pattern v2 per ADR-0011)

Validation is split into two phases by layer. The API layer handles structural validation (format, existence, cross-field). Core Service/Controller packages do not validate caller input — they execute, detect state, and guard invariants. The trade-off favors speed over defensive duplication.

**Table of Contents**

- [Context](#context)
- [Decision](#decision)
- [Implementation](#implementation)
- [Considered Options](#considered-options)
- [Consequences](#consequences)
- [Related Decisions](#related-decisions)

## Context

mvmctl is a speed-first CLI. Every redundant subprocess call in a defensive validation check adds 10-50ms of latency. Many of these checks duplicate what the operation naturally detects — calling a bridge existence check once to "validate" and again to branch execution.

The codebase had three problems:
1. **Validation scattered across all layers** — Input, Service, and Controller all had overlapping checks.
2. **Speed erosion** — redundant subprocess calls accumulated across operations.
3. **Blurred responsibility** — Service packages mixed validation, state detection, and execution in the same methods.

## Decision

**Phase 1 — Structural Validation (API layer, always):**
- Format checks (CIDR syntax, name length, port ranges)
- Existence/duplicate checks (does this ID/name exist?)
- Cross-field constraints (cannot set X when Y is Z)
- Implemented via `Validate()` and `Resolve()` methods on `*Input` structs in `pkg/api/inputs/` (see ADR-0011 for the v2 pattern). The `Resolve()` method calls `Validate()` internally; callers that need granular error reporting may call `Validate()` first, then `Resolve()`.

**Phase 2 — Execution (Core layer, no validation):**
- Service receives clean, validated data from the caller
- Service performs state detection ("does bridge exist?") as part of the operation, not as a pre-flight check
- Service guards invariants that protect against system damage (e.g., TAPs still attached before NAT removal)
- Controller handles entity state transitions only (start/stop/pause/resume)

**Caller-Trusts-Callee convention:**
The API layer is responsible for passing clean data down. Service and Controller trust that data. Defensive validation in Service is considered a code smell — it adds latency and conflates concerns.

**One narrow exception — invariant guards:**
A Service may check preconditions before an irreversible action (e.g., "are TAPs still attached?" before NAT teardown). These guards protect against system-level damage, not invalid input. They are part of the operation, not validation.

## Implementation

The v1 pattern used a three-struct approach (`*Input` / `*Request` / `Resolved*`) where `Resolve()` lived on a `*Request` wrapper. This was collapsed in ADR-0011 (June 2026) into a single `*Input` struct with `Validate()` and `Resolve()` methods. The `Resolve()` signature varies by domain:

- Simple lookups: `Resolve(ctx, repo) ([]*model.Entity, error)` — returns domain entities directly.
- Complex create flows: `Resolve(ctx, cfg, repo...) (*ResolvedXxxInput, error)` — returns a structurally different `Resolved*` struct with defaults filled in.

Cross-domain orchestration stays in the API layer (`pkg/api/`). Resolvers on Input structs take exactly one repo interface; branching and enrichment belong in API callers.

## Considered Options

- **Validate at every layer** — defensive, slows down operations, duplicates work, common in enterprise architectures but wrong for a speed-focused CLI.
- **Validator objects per domain** — clean but adds a file per domain and ceremony for trivial checks; doesn't solve the speed problem.
- **All validation in Service** — System-aware but defeats the speed-first principle since Service doesn't know whether caller already checked.

## Consequences

- **Service methods become simpler and faster** — fewer subprocess calls, fewer conditionals.
- **API layer carries more responsibility** — callers must validate completely before delegating.
- **Cross-domain data access requires API as intermediary** — API layer queries VMRepository for a network operation, passes results to NetworkService.
- **No defensive safety net** — a bug in the API layer's validation may reach Service. Mitigated by testing at the API boundary.
- **Future reader may add validation "for safety"** — the ADR and docs/STANDARDS.md exist to prevent this.

## Related Decisions

- ADR-0011: Input Pattern v2 — collapsed the `*Request` struct pattern into `Validate()`/`Resolve()` on `*Input` structs.
- CONTEXT.md "Validation (caller's responsibility)" — the validation boundary is enforced by the layer separation.
