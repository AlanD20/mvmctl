# How Agents Write Go Unit Tests

## Purpose

This guide defines the default patterns for Go tests in this project. Apply each
rule when its condition matches. Use a different idiomatic structure when the
test contract needs it, and explain the choice in the review.

## Table of Contents

1. [The Foundation — What Makes a Test Trustworthy](#1-the-foundation--what-makes-a-test-trustworthy)
2. [Dependencies & Tooling](#2-dependencies--tooling)
3. [Pattern 1: Pure Function Table-Driven Test](#3-pattern-1-pure-function-table-driven-test)
4. [Pattern 2: Repository via In-Memory Mock](#4-pattern-2-repository-via-in-memory-mock)
5. [Pattern 3: Service with Subprocess Mock](#5-pattern-3-service-with-subprocess-mock)
6. [Pattern 4: Error-Path-First Table](#6-pattern-4-error-path-first-table)
7. [Test review rules](#7-test-review-rules)
8. [What to Assert — And What NOT to Assert](#8-what-to-assert--and-what-not-to-assert)
9. [How to Derive Expected Values — The Three-Source Rule](#8a-how-to-derive-expected-values--the-three-source-rule)
10. [File Structure Template](#9-file-structure-template)
11. [Verification Checklist](#10-verification-checklist)
12. [Risk-based adversarial review](#11-risk-based-adversarial-review)
13. [Appendix: Examples of Worthless Tests (DO NOT WRITE THESE)](#appendix-examples-of-worthless-tests-do-not-write-these)
14. [Appendix: Example of a GOOD Trustworthy Test](#appendix-example-of-a-good-trustworthy-test)

---

## 1. The Foundation — What Makes a Test Trustworthy

A test is trustworthy when these properties apply:

1. **RED-GREEN**: If the behavior is wrong, the test fails. If the behavior is
   correct, the test passes. There is no third state.

2. **REGRESSION SENSITIVITY**: A change that violates the tested contract makes
   the test fail. Do not require unrelated rows to fail for one synthetic stub.

3. **DIFF**: When the test fails, the output shows EXACTLY what field differs
   and what the expected vs actual values are. Not just "not equal".

Review each test against the behavior it claims to protect.

---

## 2. Dependencies & Tooling

### Two external libraries (already in go.mod)

| Library | Import path | When to use | Purpose |
|---------|-------------|-------------|---------|
| `testify` | `github.com/stretchr/testify/assert` | Non-fatal assertions (continue on failure) | Reports failure but continues the test |
| `testify` | `github.com/stretchr/testify/require` | Fatal assertions (cannot continue) | Stops the test immediately |
| `go-cmp` | `github.com/google/go-cmp/cmp` | Structural comparison | Shows WHAT differs with `(-want +got)` format |

### Project helpers (in `internal/testutil/`)

| Helper | Location | Purpose |
|--------|----------|---------|
| `AssertDiff(t, want, got)` | `testutil/assert.go` | Shorthand for `cmp.Diff` with `(-want +got)` formatting |
| `FakeRunner` | `testutil/fake_runner.go` | Mock `CommandRunner` for subprocess-dependent tests |
| `VMRepo`, `NetworkRepo`, ... | `testutil/*.go` | In-memory repository mocks for each domain |

### Default import pattern

```go
package vm_test  // external test package for public behavior

import (
    "context"
    "testing"

    "github.com/google/go-cmp/cmp"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "mvmctl/internal/core/vm"
    "mvmctl/internal/lib/model"
    "mvmctl/internal/testutil"
)
```

Prefer an external test package when the contract is public. Use the internal
package when the test must cover an unexported parser, state machine, or other
package invariant. Do not export production symbols only to make a test external.

---

## 3. Pattern 1: Pure Function Table-Driven Test

**Use when:** Testing a pure function (no I/O, no mocking needed). The function
takes inputs, returns outputs, and has no side effects.

**Template:**

```go
func TestMyFunc(t *testing.T) {
    tests := map[string]struct {
        input    string   // adjust types to match your function
        arg2     int
        want     string
        wantErr  string   // empty = no error expected
    }{
        // Happy paths (at least 2)
        "basic_case":            {input: "hello", arg2: 1, want: "hello1"},
        "edge_values":           {input: "", arg2: 0, want: ""},

        // Error paths (at least 1)
        "invalid_input_returns_error": {input: "bad", arg2: 0, wantErr: "invalid"},

        // Boundary cases (every relevant boundary)
        "nil_input":             {input: "", arg2: -1, want: "fallback"},
        "max_values":            {input: "a", arg2: 9999, want: "a9999"},
    }

    for name, tc := range tests {
        t.Run(name, func(t *testing.T) {
            got, err := MyFunc(tc.input, tc.arg2)

            // Check error FIRST
            if tc.wantErr != "" {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tc.wantErr)
                return  // stop here — no point checking output on error
            }
            require.NoError(t, err)

            // Compare output with diff
            if diff := cmp.Diff(tc.want, got); diff != "" {
                t.Errorf("MyFunc() mismatch (-want +got):\n%s", diff)
            }
        })
    }
}
```

**Key rules for this pattern:**

- Use `map[string]struct{...}` when case order must not matter. Use a named
  slice when deterministic order makes the contract or failure easier to read.
- Use `t.Run(name, ...)` for named table cases. Each case becomes a
  subtest so failures are independent and `-run` filtering works.
- Error assertion BEFORE output assertion. If an error was expected, `return`
  immediately after asserting it.
- Use `cmp.Diff` for structural output comparisons. See review rule R3.

---

## 4. Pattern 2: Repository via In-Memory Mock

**Use when:** Testing a Repository interface contract or a Service that reads
from/writes to a repository.

The existing `testutil/*.go` files provide thread-safe in-memory implementations
of every Repository interface. Use them directly — do NOT create new mocks.

**Template:**

```go
func TestRepo_CRUD(t *testing.T) {
    ctx := context.Background()
    repo := testutil.NewVMRepo()  // or NewNetworkRepo(), NewImageRepo(), etc.

    t.Run("create_and_get", func(t *testing.T) {
        vm := &model.VMItem{
            ID:     "vm-1",
            Name:   "test-vm",
            Status: model.VMStatusRunning,
        }
        require.NoError(t, repo.Upsert(ctx, vm))

        got, err := repo.Get(ctx, "vm-1")
        require.NoError(t, err)
        require.NotNil(t, got)

        if diff := cmp.Diff(vm, got); diff != "" {
            t.Errorf("Get() mismatch (-want +got):\n%s", diff)
        }
    })

    t.Run("get_not_found_returns_nil", func(t *testing.T) {
        got, err := repo.Get(ctx, "nonexistent")
        assert.NoError(t, err)
        assert.Nil(t, got)
    })

    t.Run("delete_removes_record", func(t *testing.T) {
        require.NoError(t, repo.Delete(ctx, "vm-1"))
        got, err := repo.Get(ctx, "vm-1")
        assert.NoError(t, err)
        assert.Nil(t, got)
    })
}

func TestRepo_CountByStatus(t *testing.T) {
    ctx := context.Background()
    repo := testutil.NewVMRepo()

    // Seed data
    require.NoError(t, repo.Upsert(ctx, &model.VMItem{ID: "v1", Status: model.VMStatusRunning}))
    require.NoError(t, repo.Upsert(ctx, &model.VMItem{ID: "v2", Status: model.VMStatusStopped}))
    require.NoError(t, repo.Upsert(ctx, &model.VMItem{ID: "v3", Status: model.VMStatusRunning}))

    t.Run("count_running", func(t *testing.T) {
        count, err := repo.CountByStatus(ctx, string(model.VMStatusRunning))
        require.NoError(t, err)
        assert.Equal(t, 2, count)
    })

    t.Run("count_stopped", func(t *testing.T) {
        count, err := repo.CountByStatus(ctx, string(model.VMStatusStopped))
        require.NoError(t, err)
        assert.Equal(t, 1, count)
    })

    t.Run("empty_statuses_returns_all", func(t *testing.T) {
        count, err := repo.CountByStatus(ctx) // no args
        require.NoError(t, err)
        assert.Equal(t, 3, count)
    })
}
```

**Key rules for this pattern:**

- `require.NoError` for setup operations (Upsert, Delete). If setup fails,
  the test is broken — no point continuing.
- `assert.NoError` + `assert.Nil` for "not found" checks. The contract
  is `(nil, nil)`, not an error.
- Test the **full round-trip**: Create → Get → verify → Update → Get → verify →
  Delete → Get → verify deleted.
- Each test scenario is its own `t.Run()`. Do not chain assertions in a single
  flat function.

---

## 5. Pattern 3: Service with Subprocess Mock

**Use when:** Testing a Service that orchestrates subprocess calls via
`CommandRunner`. The `FakeRunner` records calls and returns stubbed results.

**Template:**

```go
func TestService_Stop(t *testing.T) {
    ctx := context.Background()
    repo := testutil.NewVMRepo()
    runner := &testutil.FakeRunner{}
    svc := vm.NewService(repo, vm.WithRunner(runner))

    t.Run("stop_running_vm_succeeds", func(t *testing.T) {
        vm := &model.VMItem{
            ID:     "vm-1",
            Name:   "running-vm",
            Status: model.VMStatusRunning,
            PID:    12345,
        }
        require.NoError(t, repo.Upsert(ctx, vm))

        err := svc.Stop(ctx, vm, false)
        require.NoError(t, err)

        // Assert on STATE CHANGE, not on mock calls
        got, _ := repo.Get(ctx, "vm-1")
        require.NotNil(t, got)
        assert.Equal(t, model.VMStatusStopped, got.Status)
    })

    t.Run("stop_already_stopped_vm_is_noop", func(t *testing.T) {
        vm := &model.VMItem{
            ID:     "vm-2",
            Name:   "stopped-vm",
            Status: model.VMStatusStopped,
        }
        require.NoError(t, repo.Upsert(ctx, vm))

        beforeCalls := len(runner.Calls)

        err := svc.Stop(ctx, vm, false)
        assert.NoError(t, err)

        // No new subprocess calls — already stopped
        assert.Len(t, runner.Calls, beforeCalls,
            "Stop on already-stopped VM must not invoke subprocess")
    })

    t.Run("stop_nonexistent_vm_errors", func(t *testing.T) {
        vm := &model.VMItem{
            ID:     "vm-nonexistent",
            Name:   "ghost",
            Status: model.VMStatusRunning,
        }
        // Do NOT upsert this VM — it doesn't exist in the repo

        err := svc.Stop(ctx, vm, false)
        assert.Error(t, err)
        assert.Contains(t, err.Error(), "not found")
    })
}
```

**Key rules for this pattern:**

- **Primary assertion is on state change**, not on mock calls. Assert that
  `repo.Get().Status == Stopped`, not that `runner.Calls[0].Args` contains
  `"shutdown"`. The mock call assertion is a secondary sanity check at most.
- `runner.Calls` length assertions are acceptable ONLY to prove a NOOP
  (no call happened when it shouldn't have).
- Never assert on the exact arguments of a subprocess call unless the
  argument is the core business logic being tested (e.g., a flag value).
- Test the "already done" case (idempotency) — it's the most common source
  of bugs (double-stop, double-create).

---

## 6. Pattern 4: Error-Path-First Table

**Use when:** Testing functions that return errors. The error path MUST be
tested before the success path in every table.

```go
func TestParseDiskSize(t *testing.T) {
    tests := map[string]struct {
        input    string
        want     int64
        wantErr  string
    }{
        // Error paths FIRST — they establish the contract
        "empty_string":          {input: "", wantErr: "cannot parse empty size"},
        "invalid_unit":          {input: "42xyz", wantErr: "unknown unit"},
        "negative_value":        {input: "-1G", wantErr: "size must be positive"},
        "non_numeric":           {input: "abcG", wantErr: "unable to parse"},

        // Happy paths AFTER
        "gigabytes":             {input: "2G", want: 2 * 1024 * 1024 * 1024},
        "megabytes":             {input: "512M", want: 512 * 1024 * 1024},
        "kilobytes":             {input: "1024K", want: 1024 * 1024},
        "bytes_raw":             {input: "42", want: 42},
        "zero":                  {input: "0", want: 0},
    }

    for name, tc := range tests {
        t.Run(name, func(t *testing.T) {
            got, err := ParseDiskSize(tc.input)

            if tc.wantErr != "" {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tc.wantErr)
                return
            }
            require.NoError(t, err)
            if diff := cmp.Diff(tc.want, got); diff != "" {
                t.Errorf("ParseDiskSize() mismatch (-want +got):\n%s", diff)
            }
        })
    }
}
```

**Why error paths FIRST:** It trains the reader to think about failure modes
before the happy path. It also prevents the agent from writing a table with
only success cases and forgetting errors.

---

## 7. Test review rules

### R1: Cover applicable error and boundary behavior

If the contract defines an observable error or boundary, cover it. Do not add an
invented invalid case to a function that cannot reject its input.

### R2: No tautological assertions

```
FORBIDDEN:  assert.Equal(t, "hello", result)  when result was just set to "hello" by the test setup
FORBIDDEN:  assert.Contains(t, output, name)   when name was the input string the test just constructed
FORBIDDEN:  assert.True(t, true)               literally asserting true is true
FORBIDDEN:  assert.Equal(t, 3, len(items))     hardcoding counts that depend on setup
```

The test must construct the INPUT, let the CODE run, and assert on the CODE'S
OUTPUT — not echo the input back into the assertion.

### R3: `cmp.Diff` required for all structural comparisons

`assert.Equal(t, want, got)` is FORBIDDEN when comparing structs, slices, maps,
or any multi-field value. When the test fails, the developer needs to see
EXACTLY which field differs. `cmp.Diff` provides this.

Allowed uses of `assert.Equal`: comparing primitives (int, string, bool) where
the diff is obvious from the line number, e.g., `assert.Equal(t, 3, count)`.

### R4: `require` for setup, `assert` for test logic

`require.*` = setup precondition failure. If the repo can't be seeded, the test
cannot run. Use `require.NoError`, `require.NotNil`.

`assert.*` = test logic failure. If the output doesn't match, the test fails
but other subtests should still run. Use `assert.Equal`, `assert.Contains`,
`assert.Error`.

### R5: After asserting an expected error, RETURN immediately

```go
if tc.wantErr != "" {
    require.Error(t, err)
    assert.Contains(t, err.Error(), tc.wantErr)
    return  // ← THIS IS MANDATORY
}
```

Do not check `got` after error. The function returned an error — the output is
undefined.

### R6: Never test mock wiring as the primary assertion

```
FORBIDDEN:  Assert that mock.Get() was called, but NOT what it returned
FORBIDDEN:  Only assert on mock.Calls, not on state changes
```

If your test only asserts `fakeRunner.Calls` contains `["ip", "link", ...]`,
you're testing that your mock wiring is correct — nothing else. Assert on the
**state change** in the repo, or the **return value** of the function.

Exception: asserting `runner.Calls` is empty is acceptable to prove a NOOP
(operation was correctly skipped).

### R7: Test cleanup that can leak state

If a function owns temporary resources, test the cleanup paths that can leak or
leave authority behind. Pure functions and read-only operations have no cleanup
requirement.

### R8: Test cancellation when the function owns cancellable work

A `context.Context` parameter alone does not require a cancellation test. Add
one when the function blocks, retries, or owns side effects that cancellation
must stop.

```go
t.Run("context_cancelled", func(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    cancel()  // immediately cancel

    _, err := svc.SomeOperation(ctx, ...)
    assert.Error(t, err)
    assert.ErrorIs(t, err, context.Canceled)
})
```

### R9: One `t.Run` per row in the table, never a flat loop

```
FORBIDDEN:
    for _, tc := range tests {
        got := fn(tc.input)
        assert.Equal(t, tc.want, got)
    }

REQUIRED:
    for name, tc := range tests {
        t.Run(name, func(t *testing.T) {
            got := fn(tc.input)
            if diff := cmp.Diff(tc.want, got); diff != "" {
                t.Errorf("...(-want +got):\n%s", diff)
            }
        })
    }
```

Flat loops hide which case failed. Subtests enable `-run` filtering and
isolate failures (one failure doesn't stop the rest).

### R10: Expected values from contract, not implementation

The expected value in every assertion must be independently derivable
from the function's documented contract — NOT from its current
implementation body.

**Circular test injection:** If the test expected values were obtained
by running the function under test and copying its output, the test is
circular. It will pass even if the function is buggy, because the
expected value was derived from the same buggy code.

A zero-value implementation can expose an assertion that never observes the
result, but it cannot prove that the expected value is independent. Consider
this subtler variant:

```
// SUBTLE CIRCULAR: setup() returns both (input, want) and the agent
// fills want from the function's output. The values are NOT zero,
// they're "correct" — but only because they match the current
// (possibly buggy) implementation.
func setup(t *testing.T) ([]string, []fileEntry) {
    dir := t.TempDir()
    f := filepath.Join(dir, "a.txt")
    os.WriteFile(f, []byte("a"), 0644)
    return []string{dir}, []fileEntry{
        {absPath: f, relativePath: "a.txt"},  // ← copied from buggy output
    }
}
```

This test passes because the expected value came from the buggy output. A
zero-value probe also fails, but that does not make the expectation correct.
The test proves only that the function is consistent with itself.

PREVENTION: Ask THREE questions before every expected value:

1. **Where did this value come from?** If the answer is "the function's
   output when I ran it", delete it and derive from contract instead.

2. **Could I compute this value from the test inputs without calling
   the function?** If yes, compute it inline. If no, your test is
   under-specified (you don't know what correct behavior looks like).

3. **Can a reviewer tell why this value is correct?** If the derivation is not
   obvious from the test input and name, add a contract comment.

PRACTICE: Prefer computing expected values from test inputs rather
than hardcoding them:

```go
// CONTRACT: expandSources of a directory produces
// relativePath = <source_basename>/<walk_rel>.
// Source dir is "mydir", file is "mydir/a.txt".
// Expected: relativePath = "mydir/a.txt".
wantRel := filepath.Base(dir) + "/" + "a.txt"
```

This is harder for the agent to get wrong because the contract rule
is written down and the expected value is computed by a different
expression than the function under test.

### R11: Every primary assertion must detect a contract regression

Mentally replace the behavior under test with a plausible wrong implementation.
The primary assertion must fail. A zero-value stub is one useful probe, but it
is not a universal quality test. Some valid contracts return zero values for
valid inputs.

A test is weak when its assertion ignores the returned value, repeats a value
created by setup, or checks only a count when item content is the contract.
Strengthen or remove that assertion.

### R12: Read back persistence contracts

If the contract promises durable content or state, read it back through an
independent path and compare the meaningful payload. A mutation whose contract
is only an emitted call or returned value can use that observable result.

```
FORBIDDEN — only checks error, never reads the written file:
    err := os.WriteFile(path, data, 0644)
    require.NoError(t, err)
    // File could be empty, truncated, or corrupted.
    // The test would never know.

FORBIDDEN — only checks existence, not content:
    err := repo.Upsert(ctx, &entity)
    require.NoError(t, err)
    got, err := repo.Get(ctx, entity.ID)
    require.NoError(t, err)
    // got could be a struct with zeroed fields.
    // Test passes because `err == nil`.

FORBIDDEN — only checks count, not items:
    entries, err := expandSources(paths)
    require.NoError(t, err)
    assert.Len(t, entries, 1)
    // What if entries[0].relativePath is ""?

REQUIRED — Mirror Test:
    err := os.WriteFile(path, data, 0644)
    require.NoError(t, err)

    got, err := os.ReadFile(path)
    require.NoError(t, err)
    if diff := cmp.Diff(data, got); diff != "" {
        t.Errorf("written file content mismatch (-want +got):\n%s", diff)
    }
```

Read the bytes or fields promised by the persistence contract. Existence or a
nil error alone does not prove stored content.

### R13: Assert every field in the tested contract

For a struct, map, slice, or multi-value result, assert each field that belongs
to the behavior named by the test. Use a full structural comparison when the
whole value is the contract. Do not make a focused test brittle by asserting
unrelated fields.

```
GOOD (every field checked):
    require.Len(t, results, 1)
    entry := results[0]
    assert.Equal(t, "tests/a.txt", entry.relativePath)
    assert.Equal(t, absPath, entry.absPath)

BAD (field silently dropped):
    require.Len(t, results, 1)
    // relativePath never checked. Can be wrong, test passes.

    files, err := os.ReadDir(dir)
    require.NoError(t, err)
    assert.Len(t, files, 3)
    // Names, sizes, contents — none checked. Directory could
    // contain "a.txt", "b.txt", "c.txt" or ".", "..", "tmp".
    // Both pass.
```

EXCEPTIONS (must be documented with a comment):
- **Non-deterministic fields** (timestamps, UUIDs, PIDs):
  assert with a loose bound (`assert.WithinRange`, `assert.Len(t, id, 36)`)
  or explicitly skip with `// non-deterministic`.
- **Derived fields** that cannot independently be wrong because they are
  computed from already-asserted fields: annotate with
  `// derived from <field_name>`.

```
EXCEPTION EXAMPLE:
    type Result struct {
        ID        string    // UUID — non-deterministic
        CreatedAt time.Time // timestamp — non-deterministic
        Name      string    // from input
        Slug      string    // derived from Name
    }
    assert.Equal(t, "my-thing", result.Name)
    assert.Equal(t, "my-thing", result.Slug) // derived from Name
    assert.Len(t, result.ID, 36)  // UUID format
    // CreatedAt: non-deterministic, skip
```

---

## 8. What to Assert — And What NOT to Assert

### Assert on these (in priority order)

| Priority | What | Example |
|----------|------|---------|
| 1 | **Return values** | `cmp.Diff(want, got)` |
| 2 | **State changes in repo** | Full read-back: `got := repo.Get(id); cmp.Diff(want, got)` |
| 3 | **Side effects on filesystem** | Content read-back: `got := os.ReadFile(path); cmp.Diff(want, got)` |
| 4 | **Subprocess calls (as secondary)** | `len(runner.Calls) > 0` |

### Do NOT assert on these

| What | Why |
|------|-----|
| Exact mock call arguments | Tests implementation, not behavior |
| String the test constructed | Tautology — proves nothing |
| Line numbers in errors | Brittle — change with file edits |
| Private implementation details in public-contract tests | Assert through the package contract instead |
| Order of map iteration | Undefined by Go spec |
| Timestamps or durations | Flaky — use `assert.WithinRange` or don't assert |
| Count without content | `assert.Len(t, items, 3)` doesn't verify WHICH items are there |
| Existence without content | `assert.FileExists(path)` doesn't verify the file content is correct |

---

## 8a. How to Derive Expected Values — The Three-Source Rule

### Why this section exists

The most common test failure pattern is a test that passes but asserts the
wrong thing. The test runs, the code runs, they match — but both are wrong.
This happens when the agent derives expected values from the function's
output instead of from the function's contract.

Every expected value must come from one or more independent sources:

| Source | What it means | Example |
|--------|---------------|---------|
| **CONTRACT** | The function's documented behavior | `expandSources` says: "for a dir source, relativePath = `<source_basename>/<rel>`" → `wantRel = "mydir/a.txt"` |
| **INPUT** | A direct literal from the test inputs | `input = "myfile.txt"` → `want = "myfile.txt"` (basename of the only input file) |
| **REVERSE** | Computed via a DIFFERENT algorithm/round-trip | Serialize → deserialize → compare (NOT serialize → compare). The read path is a different code path than the write path, so it's independent. |

### The `setup()` trap

A common pattern in this codebase is:

```go
func setup(t *testing.T) (input, want SomeType) {
    // ... create input ...
    return input, SomeType{Field: "value"}  // ← want is hardcoded
}
```

DANGER: When `setup()` returns both `input` and `want`, the agent can
trivially make `want` match whatever the function currently returns.
The derivation is hidden from the test body. The blind reviewer sees
`setup()` as a black box and cannot verify that `want` is correct.

PREFERRED: Compute `want` inline with an explicit contract reference:

```go
t.Run("preserves_source_dir_name", func(t *testing.T) {
    dir := t.TempDir()
    aFile := filepath.Join(dir, "a.txt")
    os.WriteFile(aFile, []byte("a"), 0644)

    entries, err := expandSources([]string{dir})
    require.NoError(t, err)

    // CONTRACT: expandSources of a directory produces
    // relativePath = <source_basename>/<rel_from_walk>.
    // This preserves the source directory name so that
    // `cp ./mydir /dst` creates `/dst/mydir/...`.
    wantRel := filepath.Base(dir) + "/" + "a.txt"

    require.Len(t, entries, 1)
    if diff := cmp.Diff(wantRel, entries[0].relativePath); diff != "" {
        t.Errorf("relativePath (-want +got):\n%s", diff)
    }
})
```

`wantRel` came from the CONTRACT plus the test INPUT — NOT from
calling `expandSources`. If `expandSources` returns `"a.txt"` (wrong),
the test fails.

### The LITERAL rule

If a hardcoded expected value is not self-explanatory from the test name and
input, add a comment that names the contract rule it satisfies:

```
GOOD (contract documented):
    // CONTRACT: For a file source, relativePath = <file_basename>.
    // input = "/path/to/report.pdf" → basename = "report.pdf"
    want := []fileEntry{{relativePath: "report.pdf"}}

ACCEPTABLE (input is self-evident):
    // Input is "test.txt", output basename must be "test.txt".
    want := []fileEntry{{relativePath: "test.txt"}}

BAD (no rationale):
    want := []fileEntry{{relativePath: "a.txt"}}
    // Where did "a.txt" come from? Was it the filename? Was it
    // copied from the function output? The reviewer cannot tell.
```

### Assertion sensitivity

Replace an expected value with a clearly wrong value and check whether the test
would fail. If it might still pass because the assertion checks only a count or
an unrelated field, strengthen the assertion.

### Independence test

Before submitting, ask: "Could I have written this expected value
before I ever saw the function's output?" If the answer is no, the
expected value is implementation-derived and must be replaced.

---

## 9. File Structure Template

Use this structure when it fits the package:

```go
package <domain>_test  // default for a public package contract

import (
    "context"
    "testing"

    "github.com/google/go-cmp/cmp"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "mvmctl/internal/<domain>"
    "mvmctl/internal/lib/model"
    "mvmctl/internal/testutil"
)

// --- <FunctionName> ---
// Rationale: <one line explaining why these tests matter>

func Test<FunctionName>(t *testing.T) {
    // ... table or subtest structure
}

// --- <NextFunction> ---
// Rationale: <one line>

func Test<NextFunction>(t *testing.T) {
    // ...
}
```

Keep related tests together and order them so setup helpers appear before their
consumers. Add a rationale comment only when the contract or regression is not
clear from the test name and assertions. A test file may cover several source
files when they implement one package-level behavior.

---

## 10. Verification checklist

Before submitting a Go test change, verify the applicable items:

```text
[ ] The test names the behavior or regression it protects.
[ ] Primary assertions observe behavior, durable state, or a documented emitted call.
[ ] Expected values come from the contract or an independent computation.
[ ] A plausible wrong implementation makes each primary assertion fail.
[ ] Error and boundary cases cover the contract without invented inputs.
[ ] Named table cases use t.Run.
[ ] Structural comparisons show a useful (-want +got) diff.
[ ] Setup failures stop the test before later assertions run.
[ ] Persistence contracts read back the meaningful payload.
[ ] Cancellation, cleanup, and race cases exist when the function owns those risks.
[ ] The chosen external or internal test package matches the boundary under test.
[ ] The smallest affected go test command passes.
[ ] go test -race passes for changed concurrent code.
```

---

## 11. Risk-based adversarial review

Routine, focused Go test changes require the author to run the checklist and the
smallest affected `go test` command. They do not require a second agent.

Require independent adversarial review when the test change covers any of these
areas:

- privileged dispatch, caller identity, path safety, or authorization
- concurrency, locking, cancellation, crash recovery, or partial failure
- strict codecs, parsers, protocol limits, or untrusted input
- process identity, cgroups, namespaces, mounts, firewall policy, or cleanup
- release qualification or a broad change that becomes the only proof for several
  production paths

Give the reviewer the contract, the production files, the test diff, and the
failure that the tests must detect. A blind reviewer is not the default. Hiding
intent can also hide the contract that distinguishes a valid assertion from a
plausible but wrong one.

The reviewer checks these points:

1. Each primary assertion fails for a plausible contract regression.
2. Expected values come from the contract or an independent computation.
3. Error, cancellation, cleanup, and boundary cases match the behavior under test.
4. Structural comparisons reveal the field that differs.
5. Persistence tests read back the meaningful payload through an independent path.
6. Mock-call assertions remain secondary unless the emitted call is the contract.
7. The focused test command, `go vet` scope, and `go test -race` scope match the
   risk.

Use pstack `interrogate` or another independent reviewer when available. Treat
findings as claims to verify against the contract. Fix valid findings and record a
specific reason for rejecting invalid ones. Re-run the focused checks after every
accepted fix.

Report the reviewed files, the reviewer verdict, accepted findings, rejected
findings with reasons, and the commands that passed.

---

## Appendix: Examples of Worthless Tests (DO NOT WRITE THESE)

```go
// WORTHLESS — tautology. Tests that the string "hello" contains "hello".
func TestWorthless1(t *testing.T) {
    name := "hello"
    result := doSomething(name)
    assert.Contains(t, result, name)  // we know "hello" contains "hello"
}

// WORTHLESS — change-detector. Only tests mock wiring.
func TestWorthless2(t *testing.T) {
    runner := &testutil.FakeRunner{}
    svc := NewService(runner)
    svc.DoThing(context.Background())
    assert.Len(t, runner.Calls, 1)  // so what? what did it DO?
}

// WORTHLESS — only tests happy path with no edge cases.
func TestWorthless3(t *testing.T) {
    result := ParseSize("10G")
    assert.Equal(t, int64(10737418240), result)  // what about ""? "0"? "-1"? "abc"?
}

// WORTHLESS — tests the stdlib, not your code.
func TestWorthless4(t *testing.T) {
    result := strings.Join([]string{"a", "b"}, ",")
    assert.Equal(t, "a,b", result)  // this is testing Go's stdlib, not our code
}
```

## Appendix: Example of a GOOD Trustworthy Test

```go
// Rationale: ToInt is used by config parsing and disk size resolution.
// A bug here would cause incorrect defaults silently — the function
// returns a defaultVal on failure, so callers see no error.

func TestToInt(t *testing.T) {
    tests := map[string]struct {
        input      any
        defaultVal int
        want       int
    }{
        // Happy paths
        "int_direct":       {input: 42, defaultVal: 0, want: 42},
        "string_numeric":   {input: "100", defaultVal: 0, want: 100},
        "string_zero":      {input: "0", defaultVal: 99, want: 0},

        // Edge cases — fallback to default
        "nil":              {input: nil, defaultVal: -1, want: -1},
        "string_not_a_number": {input: "abc", defaultVal: 99, want: 99},
        "bool_value":       {input: true, defaultVal: 99, want: 99},
    }

    for name, tc := range tests {
        t.Run(name, func(t *testing.T) {
            got := infra.ToInt(tc.input, tc.defaultVal)
            // No error path in ToInt — it always returns a value
            if diff := cmp.Diff(tc.want, got); diff != "" {
                t.Errorf("ToInt() mismatch (-want +got):\n%s", diff)
            }
        })
    }
}
```

Why this is trustworthy:
1. **RED-GREEN**: If ToInt returns wrong value, assertion fails
2. **REGRESSION SENSITIVITY**: An unconditional zero result fails the non-zero cases
3. **DIFF**: `cmp.Diff` shows exact value mismatch with `(-want +got)` format
4. **EDGE CASES**: nil, non-numeric string, bool, zero value — all tested
5. **NO TAUTOLOGY**: Input values are different from expected outputs
