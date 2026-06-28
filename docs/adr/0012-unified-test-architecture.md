# Unified Test Architecture — Go Hermetic + Nested-VM E2E

**Status:** Active  
**Date:** 2026-06-20

**Table of Contents**

- [Context](#context)
- [Decision](#decision)
- [Considered Options](#considered-options)
- [Consequences](#consequences)
- [Related Documents](#related-documents)

## Context

### The problem

The project had two test layers — Go unit tests (`go test ./...`) and Python system tests (`tests/system/`) — with a large gap between them. The system test layer has since been migrated to a three-level architecture (see implementation below):

| Layer | Language | Speed | Determinism | Lines |
|-------|----------|-------|-------------|-------|
| **Go unit tests** | Go | ms | ✅ Deterministic | ~8,000 |
| *(gap)* | — | — | — | *0* |
| **System tests** | Python | min | ❌ Flaky | ~22,000 |

The Python system tests (27 files, ~22,000 lines) suffer from seven structural problems: global shared state (~80% `@pytest.mark.serial`), pervasive skipping (43/341 skip-prone scenarios), dependency on real infrastructure, no parallel execution, tautological assertions, massive unfocused files, and `zzz_destructive/` ordering hacks.

### What already works

Per [ADR-0010](0010-per-domain-api-interfaces.md), the CLI layer accepts narrow `api.*API` interfaces. The `internal/testutil/` package provides in-memory repos, mock APIs, `FakeRunner`, and `FakeNetOps` — all Go-based, deterministic, and fast. These are currently only used for pure unit tests.

The `test_vm_nested_isolated.py` file (in `tests/system/vm/`) proves this pattern works.

### The "Zero Blindside" Principle

When all tests pass, the team must be able to ship with confidence that every public-facing CLI command and flag works correctly. This requires exhaustive coverage (every path), deterministic execution (no skips, no flakes), isolated state (disposable environments), and includable destructive tests (cleanup verified the same way as creation).

## Decision

Adopt a **three-level test architecture** with a two-language boundary:

| Level | Name | Language | Tests | Requirements |
|-------|------|----------|-------|--------------|
| **L0** | Pure function | Go | Table-driven tests, no I/O, no repos | None |
| **L1** | Hermetic integration | Go | Real SQLite (`:memory:`), real files (`t.TempDir()`), `FakeRunner` for subprocess calls only | None |
| **L2** | Runner VM E2E | Python | Real binary, real infrastructure inside a disposable Firecracker VM | Runner VM (nested KVM) |

### Key Sub-Decisions

1. **The mocked CLI handler layer (proposed "L2") is eliminated.** Mocking the API layer creates more maintenance than value — every new method needs a mock field, interface changes cascade, and assertions risk tautology. L1 tests cover the same ground without mocking: seed the real DB, call the real repository, check the output.

2. **L2 covers ALL user-facing features.** Every CLI command, every flag, every output format, every error path — verified with the real binary running inside a runner VM. No scenario is removed during migration. The Python test files stay; only the execution substrate changes (from the real host to a disposable VM).

3. **L0/L1 are fast pre-filters, NOT replacements for L2.** They catch bugs earlier during `go test ./...` (milliseconds instead of minutes), but passing L0/L1 alone means nothing — the feature could be completely broken at the subprocess level. The release gate is L2 passing.

4. **`zzz_destructive/` ordering-by-filename is eliminated.** Destructive tests stay and are essential — but the runner VM makes ordering a convenience concern, not a correctness requirement. Each session starts clean. Parallel workers each get their own VM.

5. **L0/L1 tests are written in Go, share file locations with existing unit tests** (`internal/*/*_test.go`), and follow [HOW_AGENTS_WRITE_UNIT_TESTS.md](../development/HOW_AGENTS_WRITE_UNIT_TESTS.md) patterns.

6. **The full decision tree and quick-reference table are documented in [HOW_AGENTS_WRITE_SYSTEM_TESTS.md](../development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md).** That document is the authoritative how-to reference for classifying and writing tests at all three levels.

## Considered Options

### Option A: Full Rewrite of Python Tests in Go (rejected)

Translate all 22,000 lines of Python to Go. Loses pytest's fixture scoping, parametrization, marker filtering, yield-based teardown, and plugin ecosystem. Would need extensive custom infrastructure for runner VM orchestration. The cost of rebuilding pytest's capabilities in Go exceeds the benefit of a single language.

### Option B: Build Tag for Hermetic Binary (rejected)

Add a `mvmtest` build tag that replaces `DefaultRunner` with a controllable fake. Creates a second testing surface (does the injection work? does the env var parse correctly?). The binary that runs in CI would differ from production. L1 tests achieve the same result without a build tag by testing the handler function directly.

### Option C: Three-Level Architecture (selected)

Go for fast pre-filters. Python (inside nested VM) for ground truth. Each layer uses the right tool for the job. Incremental migration — no big-bang rewrite.

### Option D: Status Quo (rejected)

Keep 22,000 lines with shared DB, pervasive skipping, and per-domain serial execution. The problems are structural, not cosmetic — no amount of test hardening fixes a shared-DB architecture.

## Consequences

**Positive:**
- Runner VM provides hermetic isolation — no shared DB, no orphaned bridges, no sudo on the host
- Parallel execution — multiple runner VMs, no `serial` markers
- `zzz_destructive/` ordering hack eliminated
- Skip culture eliminated — runner VM starts from pre-seeded snapshot
- L0/L1 fast pre-filters speed up the developer inner loop (milliseconds instead of minutes)
- SQLite-backed L1 tests catch schema bugs the current map-based mocks cannot detect

**Negative:**
- Migration effort (estimated 8-12 weeks)
- Two languages, two test runners
- Runner VM provisioning time (~60-120s first run, mitigated by snapshot/restore)
- Runner VM resource overhead (~4 vCPU + 4 GB RAM + 20 GB disk per VM)
- ~2,000 new Go test lines to maintain
**Neutral:**

- Per-domain conftest.py files consolidated into one
- `tests/system/` → `tests/e2e/` rename (pending — directory still at `tests/system/`)
- `COVERAGE_MATRIX.md` replacement with `go test -coverprofile` + L2 scenario manifest (pending — `COVERAGE_MATRIX.md` still exists)
- `scripts/run-system-tests.py` domain-looping replaced by `pytest tests/system/` (pending — CI still references `scripts/run_tests.py`)

## Related Documents

| Doc | Purpose |
|-----|---------|
| `docs/system-test-architecture.md` | **Primary implementation reference** — Three-tier orchestration, provisioning flow, fixture scoping, known-limitation patterns, per-file compliance checklist. Supersedes `DRAFT-system-test-architecture.md`. |
| `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` | L0/L1/L2 definitions, decision tree, quick-reference table, runner VM fixture pattern, migration phases. |
| `docs/development/HOW_AGENTS_WRITE_UNIT_TESTS.md` | L1 fast pre-filter tests (in-memory SQLite, temp dirs). |
| `docs/development/HOW_AGENTS_RUN_SYSTEM_TESTS.md` | Execution plan for running system tests. |
| _(no RC_QA.md — this doc section moved into system-test-architecture.md)_ | Release gates: "L2 tests in runner VM" now in system-test-architecture.md. |
| `STANDARDS.md` §12 | Three-level architecture description. |
| `tests/system/COVERAGE_MATRIX.md` | Coverage tracking. |
| `AGENTS.md` | `engineer` owns L0/L1 (Go), `qa-engineer` owns L2 (Python runner VM). |
