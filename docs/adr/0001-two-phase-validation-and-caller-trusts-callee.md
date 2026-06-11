# Two-Phase Validation with Caller-Trusts-Callee

**Status:** accepted
**Date:** 2026-05-22

Validation is split into two phases by layer. The API layer handles structural validation (format, existence, cross-field). Core Service/Controller classes do not validate caller input — they execute, detect state, and guard invariants. The trade-off favors speed over defensive duplication.

## Context

mvmctl is a speed-first CLI. Every redundant subprocess call in a defensive validation check adds 10-50ms of latency. Many of these checks duplicate what the operation naturally detects — `bridge_exists()` is called once to "validate" and again to branch execution.

The codebase had three problems:
1. **Validation scattered across all layers** — Input, Service, and Controller all had overlapping checks.
2. **Speed erosion** — redundant subprocess calls accumulated across operations.
3. **Blurred responsibility** — Service classes mixed validation, state detection, and execution in the same methods.

## Decision

**Phase 1 — Structural Validation (API layer, always):**
- Format checks (CIDR syntax, name length, port ranges)
- Existence/duplicate checks (does this ID/name exist?)
- Cross-field constraints (cannot set X when Y is Z)
- Lives in `*Input`/`*Request` structs in `pkg/api/inputs/`

**Phase 2 — Execution (Core layer, no validation):**
- Service receives clean, validated data from the caller
- Service performs state detection ("does bridge exist?") as part of the operation, not as a pre-flight check
- Service guards invariants that protect against system damage (e.g., TAPs still attached before NAT removal)
- Controller handles entity state transitions only (start/stop/pause/resume)

**Caller-Trusts-Callee convention:**
The API layer is responsible for passing clean data down. Service and Controller trust that data. Defensive validation in Service is considered a code smell — it adds latency and conflates concerns.

**One narrow exception — invariant guards:**
A Service may check preconditions before an irreversible action (e.g., "are TAPs still attached?" before NAT teardown). These guards protect against system-level damage, not invalid input. They are part of the operation, not validation.

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

- CONTEXT.md "Validation (caller's responsibility)" — the validation boundary is enforced by the layer separation.
