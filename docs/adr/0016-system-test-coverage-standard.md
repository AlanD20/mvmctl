# System Test Coverage Standard — Spec-Catalog Model

System tests (`tests/system/`) are black-box CLI subprocess tests. They are the
**primary release gate**: a domain is not production-ready until its system tests
pass on real hardware with verified depth.

## Context

The original system test specification (`docs/development/HOW_AGENTS_WRITE_TESTS.md`)
was a **process guide** — it described *how* to write tests (fixture order, markers,
assertion patterns) but provided zero *what*. Every agent invented its own test
scenarios, producing:

1. **No spec → random coverage**: With no scenario catalog, each agent decides what
   to test independently. The result is coverage that looks random because it *is*
   random — whatever the agent happened to think of.

2. **Shallow verification**: Tests optimized for "pass" not "find bugs". A test for
   `--enable-logging` checks `vm["status"] == "running"` but never verifies log
   files exist on disk. A test for `--enable-metrics` never verifies metrics are
   being written.

3. **Silent skips (`pytest.skip` as escape hatch)**: 200 `pytest.skip()` calls
   across the suite (as of May 2026). A test that skips produces no output, no
   metric, no CI failure. CI passes green while large portions of the suite
   silently do nothing.

4. **No coverage accountability**: There is no document mapping which CLI flags
   are tested by which tests and at what depth. Without this, gaps are invisible
   until a production bug hits.

5. **Fragile error assertions**: The convention of
   `assert any(s in combined for s in ["not found", "no such"])` matches by
   substring guesswork. A typo in an error message silently breaks the test,
   or worse, the test passes against a different error than expected.

6. **No maintenance rule**: The original spec forbade modifying existing test
   files ("only write new files"). This meant accumulated cruft — empty stubs,
   duplicated patterns, shallow tests — could never be fixed.

## Decision

Replace the process-guide model with a **spec-catalog model** with three tiers:

### Tier 1: Architecture Decision Record (this document)

Documents the policy change, the rationale, and the consequences.

### Tier 2: Coverage Matrix (`tests/system/COVERAGE_MATRIX.md`)

A living document that maps every CLI command and every flag to its test
coverage status. This is the **accountability document**. Every change to
the CLI must update this matrix.

### Tier 3: Scenario Specification (`docs/development/HOW_AGENTS_WRITE_TESTS.md`)

The rewritten spec document. Contains:
- Per-domain scenario catalogs listing every required test scenario
- Four-level verification depth standard (instead of "cheapest resource")
- Skip discipline rules
- Structured error assertion requirements
- Modified test file rules

### Key Policy Changes

#### 1. Verification Depth Replaces "Cheapest Resource"

The old "cheapest resource wins" rule produced shallow tests. Replace it with
a four-level depth standard:

| Level | Name | What It Verifies | Example |
|-------|------|------------------|---------|
| L0 | Returncode | Process exit code | `assert result.returncode == 0` |
| L1 | Output | stdout/stderr content | `assert "pulled" in result.stdout` |
| L2 | Structured JSON | Parsed JSON field correctness | `assert data["status"] == "running"` |
| L3 | System State (Option C) | Deepest practical verification: filesystem, process table, iptables, SQLite, guest-visible | `assert Path(log_file).exists()`, `assert bridge in nft output`, `assert "vdb" in guest's lsblk` |

Every test MUST achieve at least L2 (JSON). Tests that verify infrastructure
(bridges, iptables, volumes, binaries) MUST achieve L3 (system state).

#### 2. Skip Discipline

- Every `pytest.skip()` must include a `skip-reason` comment explaining why
  the test cannot run and what condition would make it runnable.
- CI MUST enforce a **skip ratio gate**: if >10% of system tests skip in a
  run, the run FAILS. The ratio is computed per file, not globally.
- Tests that skip on environmental conditions (network unavailable, binary not
  found) should prefer a minimal-fallback assertion over a full skip where
  possible.

#### 3. Structured Error Assertions

Error message assertions must match against a canonical structure, not a
substring guess. Preferred patterns:

```python
# Preferred: match against specific error code or prefix
assert "MVM-NOT-FOUND" in result.stdout
# or
assert result.returncode == 66  # EX_NOINPUT

# Acceptable: match against a specific phrase (not a list of guess-words)
assert "network 'foo' not found" in (result.stdout + result.stderr).lower()
```

The common pattern of `assert any(s in combined for s in ["a", "b", "c"])`
where none of the strings is specific to the expected error is FORBIDDEN.

#### 4. Test File Maintenance

The rule "do not modify any existing test file — only write new files" is
**replaced** with:

- Tests MAY be modified when the change improves coverage, fixes a skip,
  deepens verification, or fixes a bug.
- Tests SHOULD consolidate duplicated setups into module-scoped fixtures
  when multiple read-only tests share infrastructure.
- Tests MUST NOT be modified to silence a failing assertion without
  understanding and fixing the root cause.

## Status

Accepted.

## Consequences

- **Coverage matrix is mandatory**: Every CLI flag must have a documented
  test status. Gaps are visible and actionable.
- **CI skip gate required**: A new script or pytest plugin must track skip
  ratios and fail runs exceeding the threshold.
- **Existing test retrofitting needed**: Hundreds of tests need deeper
  verification, skip removal, or consolidation.
- **Error messages become contract**: Changing an error message now breaks
  tests intentionally, which is correct — it forces awareness of the change.
- **Slower individual tests**: L3 verification takes more time (checking
  filesystem, iptables, guest SSH). This is acceptable — speed is secondary
  to confidence at the system test level.
- **Spec maintenance burden**: The scenario catalog must be updated when
  CLI flags are added or changed.
