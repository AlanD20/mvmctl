# Unified test architecture with Go pre-filters and Python system tests

**Status:** Active, amended for the three-tier runner on 2026-08-26
**Date:** 2026-06-20

## Context

At the time of this decision, the project had fast Go unit tests and a large Python system-test suite with little
coverage between them. Many Python tests shared host state, depended on ordering, skipped scenarios when infrastructure
was missing, or asserted only command return codes. Those properties made a passing run weak release evidence.

The exact test counts and directory layout from that period are historical facts, not a live inventory. Read
`tests/system/COVERAGE_MATRIX.md` and the registry in `scripts/run-system-tests.py` for the current suite.

The CLI already exposed narrow `api.*API` interfaces. `internal/testutil/` also provided in-memory repositories and a
`FakeRunner`. The project could therefore add hermetic Go coverage without mocking the production binary used by system
tests.

## Decision

Use three test levels with a two-language boundary:

| Level | Purpose | Language | Boundary |
|---|---|---|---|
| L0 | Pure function checks | Go | No I/O, repositories, or subprocesses |
| L1 | Hermetic integration checks | Go | Controlled SQLite and files, with `FakeRunner` only at subprocess boundaries |
| L2 | Real-binary system checks | Python | Installed CLI and real Linux infrastructure in the execution tier selected by the runner |

L0 and L1 are fast pre-filters. They do not replace L2 coverage for user-visible behavior. L2 verifies the installed
binary through the CLI and observes the resulting operating-system state.

The system-test runner uses a separate tier classification inside L2:

| Tier | Execution location | Use |
|---|---|---|
| T1 | Disposable runner VM | Host-level CLI behavior that does not require nested KVM |
| T2 | Disposable runner VM with nested virtualization | VM lifecycle and interaction through nested Firecracker |
| T3 | Clean host-direct qualification machine | Behavior that cannot be qualified correctly under nesting |

Test levels answer what kind of proof a scenario needs. Execution tiers answer where an L2 scenario can run. Do not use
the level and tier numbers interchangeably.

### Sub-decisions

1. Do not add a mocked CLI-handler layer. L1 calls real package behavior with controlled dependencies. L2 calls the real
   installed binary.
2. Keep user-visible behavior in the Python system-test coverage matrix. Run each scenario in T1 or T2 when nesting can
   represent it. Reserve T3 for the documented host-only exceptions.
3. Keep destructive tests. Disposable runners and scoped fixtures provide isolation. Filename ordering is not a safety
   mechanism.
4. Keep L0 and L1 tests next to Go source. The `engineer` role owns them.
5. Keep Python L2 tests under `tests/system/`. The `qa-engineer` role owns them and the runner.
6. Treat collection, skips, deselection, cleanup failure, and malformed outcome reports as release failures. A zero exit
   code alone is not sufficient evidence.
7. Keep the installed outer controller separate from the release candidate. Install the candidate inside T1 and T2
   runners. T3 requires exact candidate identity at the canonical system path on a clean qualification machine.

## Considered options

### Rewrite the Python suite in Go

Rejected. Pytest already provides fixture scoping, parametrization, filtering, and teardown. Rebuilding those facilities
would not improve the real-binary boundary.

### Build a tagged mock binary

Rejected. A build tag would create a second executable behavior that release testing does not ship. L1 already provides
controlled dependency tests without changing the candidate binary.

### Keep only Go unit tests and host-direct Python tests

Rejected. Go-only tests miss subprocess and Linux integration failures. Host-direct tests expose developer state and
make cleanup failures dangerous.

### Use Go pre-filters and isolated Python system tests

Selected. Go catches local defects early. Python verifies the installed CLI against real infrastructure. Disposable
runners contain most destructive behavior, while T3 remains an explicit release-only exception.

## Consequences

Positive consequences:

- L0 and L1 failures arrive before expensive VM provisioning.
- T1 and T2 isolate databases, bridges, processes, and destructive cleanup from the outer host.
- The runner can execute independent domains concurrently in separate VMs.
- Strict outcome reports distinguish a real pass from an empty, skipped, or partially collected run.

Costs and limits:

- The project maintains Go and Python test code.
- T1 and T2 require runner provisioning and nested virtualization support.
- T3 still mutates the outer machine. It is release-only and requires a clean qualification host.
- The coverage matrix and executable registry must remain synchronized.

## Current implementation record

- `tests/system/` remains the canonical Python system-test directory.
- `scripts/run-system-tests.py` is the only supported outer orchestrator.
- `tests/system/COVERAGE_MATRIX.md` remains the coverage inventory.
- T1 and T2 use controller and candidate separation.
- T3 requires explicit `--host-direct`. Release qualification verifies candidate identity. The remaining clean-host
  ownership and teardown gates stay release blockers in Task 17.
- Tier assignment and fixture scoping replace a global `pytest.mark.serial` convention.

## Related documents

| Document | Authority |
|---|---|
| `docs/system-test-architecture.md` | Architecture, tiers, fixture scope, runner protocol, and known limitations |
| `docs/development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md` | L0, L1, and L2 classification plus system-test authoring patterns |
| `docs/development/HOW_AGENTS_WRITE_UNIT_TESTS.md` | Go test authoring patterns |
| `docs/development/HOW_TO_RUN_SYSTEM_TESTS.md` | Operator procedure for executing the runner |
| `docs/RC_QA.md` | Release-candidate qualification and evidence |
| `tests/system/COVERAGE_MATRIX.md` | Command, flag, and scenario coverage |
| `AGENTS.md` | Role ownership and safety rules |
