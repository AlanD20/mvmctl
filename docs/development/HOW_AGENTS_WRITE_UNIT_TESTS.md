# How Agents Write Go Unit Tests

## Purpose

This is a **specification**, not a tutorial. It defines:

- The **exact patterns** every Go unit test must follow
- The **forbidden patterns** that produce worthless tests
- The **verification checklist** before submitting any test code

Agents do NOT invent test structure. They replicate the patterns below
verbatim. Any deviation must be flagged and approved.

## Table of Contents

1. [The Foundation — What Makes a Test Trustworthy](#1-the-foundation--what-makes-a-test-trustworthy)
2. [Dependencies & Tooling](#2-dependencies--tooling)
3. [Pattern 1: Pure Function Table-Driven Test](#3-pattern-1-pure-function-table-driven-test)
4. [Pattern 2: Repository via In-Memory Mock](#4-pattern-2-repository-via-in-memory-mock)
5. [Pattern 3: Service with Subprocess Mock](#5-pattern-3-service-with-subprocess-mock)
6. [Pattern 4: Error-Path-First Table](#6-pattern-4-error-path-first-table)
7. [Iron Rules (Violation = Rejected)](#7-iron-rules-violation--rejected)
8. [What to Assert — And What NOT to Assert](#8-what-to-assert--and-what-not-to-assert)
9. [How to Derive Expected Values — The Three-Source Rule](#8a-how-to-derive-expected-values--the-three-source-rule)
10. [File Structure Template](#9-file-structure-template)
11. [Verification Checklist](#10-verification-checklist)
12. [Mandatory Blind Adversarial Review](#11-mandatory-blind-adversarial-review)
13. [Appendix: Examples of Worthless Tests (DO NOT WRITE THESE)](#appendix-examples-of-worthless-tests-do-not-write-these)
14. [Appendix: Example of a GOOD Trustworthy Test](#appendix-example-of-a-good-trustworthy-test)

---

## 1. The Foundation — What Makes a Test Trustworthy

A test is **trustworthy** if ALL three are true:

1. **RED-GREEN**: If the behavior is wrong, the test fails. If the behavior is
   correct, the test passes. There is no third state.

2. **SURVIVAL**: If you delete the function body and return zero values, the
   test MUST fail. If the test still passes, it's worthless — delete it.

3. **DIFF**: When the test fails, the output shows EXACTLY what field differs
   and what the expected vs actual values are. Not just "not equal".

These three properties are non-negotiable. Every test is reviewed against them.

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

### Import pattern for every test file

```go
package vm_test  // external test package (black-box — tests public API only)

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

**RULE:** Always use external test packages (`package vm_test`, not `package vm`).
This ensures you test the public API, not unexported internals.

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

- `map[string]struct{...}` — NOT a slice. Map iteration order is randomized,
  which detects tests that accidentally depend on global state ordering.
- `t.Run(name, ...)` — NEVER loop directly with `for`. Each case must be a
  subtest so failures are independent and `-run` filtering works.
- Error assertion BEFORE output assertion. If an error was expected, `return`
  immediately after asserting it.
- `cmp.Diff` for every output comparison. Never use `assert.Equal(t, want, got)`
  without diff (see Iron Rule #3).

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

## 7. Iron Rules (Violation = Rejected)

### R1: Every table must have at least one error/invalid case

If a function returns `error`, you MUST test at least one path where it errors.
No "happy path only" tables. A function that never errors shouldn't return
`error`.

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

### R7: Every cleanup path must be tested

If a function creates temporary resources, ensure you test what happens on
cleanup failure. At minimum, verify the cleanup runs. A function that leaks
resources on error paths is a bug.

### R8: Context cancellation must be tested on any function that takes context

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

DETECTION: If you remove the function body and return a zero value,
would the test fail? Yes? Good. But there's a more subtle variant:

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

This test would pass with buggy code because the expected value WAS
the buggy output. The SURVIVAL check ("delete function body → fail?")
passes because zero values don't match `"a.txt"`. But the test is
still worthless — it only proves the function is consistent with
itself.

PREVENTION: Ask THREE questions before every expected value:

1. **Where did this value come from?** If the answer is "the function's
   output when I ran it", delete it and derive from contract instead.

2. **Could I compute this value from the test inputs without calling
   the function?** If yes, compute it inline. If no, your test is
   under-specified (you don't know what correct behavior looks like).

3. **Does a contract comment explain why this value is correct?**
   If a reviewer can't tell why `"a.txt"` is the expected value
   (vs `"tests/a.txt"` or `"./a.txt"`), add a contract comment.

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

### R0: Mandatory SURVIVAL — every test must fail when the function is gutted

The SURVIVAL property from Section 1 is non-negotiable.

Take the function under test, delete its body, and return zero values
(zero, nil, empty struct, empty slice). If ANY test case still passes,
that test case is worthless and must be removed or rewritten before the
test file can be committed.

This applies to EVERY table row and EVERY subtest — not just the test
function as a whole. A table with 5 rows where 4 survive the gutted
function is still defective.

```
FORBIDDEN:
    // Function:  func Sum(a, b int) int { return a + b }
    // Test row:  {a: 1, b: 2, want: 3}
    // Gutted:    func Sum(a, b int) int { return 0 }
    //
    // SURVIVAL: 0 != 3 → PASS (dies on gutted function) ✓

    // Function:  func GetName(u *User) string { return u.Name }
    // Test row:  {input: &User{Name: "alice"}, want: "alice"}
    // Gutted:    func GetName(u *User) string { var s string; return s }
    //
    // SURVIVAL: "" != "alice" → PASS (dies on gutted function) ✓

    // Function:  func Validate(s string) error
    // Test row:  {input: "", wantErr: "empty"}
    // Gutted:    func Validate(s string) error { return nil }
    //
    // SURVIVAL: nil != error → PASS (dies on gutted function) ✓

WORTHLESS (survives gutted function):
    // Function:  func Exists(path string) bool { return fileExists(path) }
    // Test row:  {input: "/tmp/exists.txt", want: true}
    // Gutted:    func Exists(path string) bool { return true }
    //
    // SURVIVAL: true == true → FAIL (test passes with gutted function)
    // The test doesn't prove Exists works — it just proves that
    // returning true is accepted. Gutting to `return false` would
    // fix survival but expose that no real file check happens.

    // Function:  func ToUpper(s string) string { return strings.ToUpper(s) }
    // Test row:  {input: "a", want: "A"}
    // But the test uses `assert.Equal(t, want, want)` — tautology.
    // Gutted function returns "" — but the assertion compares want
    // to want, not to got. Test never fails.
```

DETECTION: Before submitting, mentally inline each assertion. Replace
the function body with `return zero`. If the assertion passes, delete
the test or strengthen the assertion until it fails.

### R11: The Mirror Test — every write must be read back

If the function under test writes, creates, or mutates data, the test
must read back the result and verify it byte-for-byte or field-for-field.
"Error is nil" or "file exists" is NOT sufficient verification.

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

READ THE WHOLE PAYLOAD. Not just metadata (exists, size, mode).
Not just error. Read the actual bytes or retrieve the actual entity
and compare every field.

### R12: Assert every output field

If the function returns a struct, map, slice, or multi-value output,
every meaningful field must be explicitly asserted. A field that isn't
asserted is a field that can be wrong without detection.

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
| Internal/private functions | External test package enforces this |
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

Every expected value must come from EXACTLY ONE of three sources:

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

If the expected value is a hardcoded literal in the test body (not
computed from inputs), the test MUST explain in a comment what
contract rule that literal satisfies:

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

### The SURVIVAL check for expected values

In addition to the test-level SURVIVAL check ("would deleting the
function body fail this test?"), apply SURVIVAL to EACH expected value:

Take the expected value, replace it with a clearly wrong value, and
mentally check: would the test fail? If the answer is "maybe not"
(e.g., because the assertion is on length, not content, or because
the other fields would still match), the assertion is too weak.

### Independence test

Before submitting, ask: "Could I have written this expected value
before I ever saw the function's output?" If the answer is no, the
expected value is implementation-derived and must be replaced.

---

## 9. File Structure Template

Every test file follows this exact structure:

```go
package <domain>_test  // external test package

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

// ─── <FunctionName> ────────────────────────────────────────────────
// Rationale: <one line explaining why these tests matter>

func Test<FunctionName>(t *testing.T) {
    // ... table or subtest structure
}

// ─── <NextFunction> ─────────────────────────────────────────────────
// Rationale: <one line>

func Test<NextFunction>(t *testing.T) {
    // ...
}
```

**Rules:**
- Section comments use `// ───` with em-dash borders (80 chars wide)
- Every test function has a `// Rationale:` comment explaining why this
  test exists and what real bug it prevents
- Test functions are ordered by dependency (foundation first, consumers later)
- Each file tests ONE Go file from the source package (file name match)

---

## 10. Verification Checklist

Before submitting ANY test file, verify every item:

```
[ ] Does every test function have at least one error/invalid case?
[ ] Is every assertion on BEHAVIOR, not implementation (mock calls)?
[ ] Would deleting the production function body make this test fail?
    (Try it mentally — if not, the test is worthless)
[ ] Does every cmp.Diff call use the (-want +got) format string?
[ ] Does every table use map[string]struct{...} with t.Run()?
[ ] Does every error case return immediately after asserting the error?
[ ] Is `require` used for setup and `assert` for test logic?
[ ] Are there zero tautological assertions (echoing inputs)?
[ ] Does the test use external package (_test suffix)?
[ ] Does go test ./... compile cleanly?
[ ] Does go test -race ./... pass with no data races?
[ ] Is every expected value independently derivable from the function's
    contract without running the function? (Not copied from its output)
[ ] Does every hardcoded expected literal have a CONTRACT comment
    explaining WHY that value is correct?
[ ] SURVIVAL: Would replacing the function body with `return zero`
    make this test fail? (Every row, every subtest.)
[ ] Mirror Test: Does every write/create/mutate read back the payload
    and compare byte-for-byte or field-for-field?
[ ] Are ALL fields of every returned struct/slice/map asserted? Any
    unasserted fields documented with a reason?
```

---

## 11. Mandatory Blind Adversarial Review

Every test file MUST pass a blind adversarial review before submission. The
review is performed by a SEPARATE agent instance that has ZERO knowledge of
what the test is supposed to do or what bug it was written to catch.

### Why blind review is mandatory

If the reviewer knows "this test is for `ToInt`", they will subconsciously
confirm that the test looks correct — even if the assertion is tautological,
the edge cases are missing, or the expected value is wrong. A blind reviewer
with no context can only judge what the code ACTUALLY does, not what it was
INTENDED to do.

### The blind review protocol

**Step 1: Generate the diff.**

Before spawning the reviewer, the writer agent records the current state:

```bash
git rev-parse HEAD   # save baseline SHA
```

Then makes ALL changes. After the last edit, run:

```bash
git diff <BASELINE_SHA>   # diff against the commit before any changes
```

Do NOT use plain `git diff` (which compares against HEAD — if previous changes
were committed, HEAD already includes them and the reviewer won't see them).

**Step 2: Spawn a reviewer with NO context.**

The writer agent spawns a `general` subagent with this EXACT prompt (do NOT
modify it):

```
You are a BLIND adversarial code reviewer. You do NOT know what the author
intended to fix or test. You judge only what the code ACTUALLY does.

Review this git diff of Go test files against the following rules.
The rules are non-negotiable — if ANY rule is violated, report FAIL with
the exact file:line and the rule violated.

RULES:
1. Every table must have at least one error/invalid/boundary case.
2. No tautological assertions (asserting something the test just constructed).
3. cmp.Diff must be used for all structural comparisons (structs, slices, maps).
4. require.* for setup, assert.* for test logic.
5. After asserting an expected error, the test must RETURN immediately.
6. Primary assertion must be on BEHAVIOR (return value, state change),
   not on mock wiring (mock.Calls).
7. Must use `t.Run(name, ...)` for every row — no flat loops.
8. Context cancellation must be tested if the function takes context.Context.
9. Every cleanup or error path must be tested.
10. The test file MUST compile with `go vet` and pass `go test -race`.
11. Expected values must be derivable from the function's contract, not
    its implementation. If a hardcoded literal is used as the expected
    value, the test must include a comment explaining what contract rule
    produces that value. A test that passes only because the expected
    value matches the current implementation (not an independent spec)
    is circular and must be rejected.
12. SURVIVAL: If the function body were replaced with `return zero` /
    `return nil`, the test MUST fail. If any assertion survives a gutted
    function body, that assertion is redundant or tautological.
13. Mirror Test: Every write/create/mutate must be followed by a read-back
    that verifies the payload byte-for-byte. Checking only error or
    existence is insufficient.
14. Every field of a returned struct, slice element, or map value must be
    explicitly asserted unless documented as non-deterministic or derived.

Report:
- PASS: no issues found
- FAIL: [rule X violated] [file:line] — explain what's wrong

Read every changed file in full to verify context. Do NOT assume anything
about what the author intended.
```

**Step 3: The reviewer reads changed files, not just the diff.**

The prompt MUST instruct the reviewer to read every changed file in full.
A diff alone can hide context (e.g., a test that constructs a value and then
asserts it's the same — the diff shows the assertion but not the construction).

**Step 4: Fix ALL violations — no debt accumulation.**

If the reviewer reports FAIL, the writer agent fixes EVERY violation. The fix
must REPLACE the offending code, not ADD a band-aid on top. Examples:

```
WRONG (adds a patch on top of buggy code):
    if diff := cmp.Diff(want, got); diff != "" {
        t.Errorf("mismatch: %s", diff)  // wrong format string
    }
    // ADDED: sorry, let me also add the proper format
    t.Logf("for debugging: want=%v got=%v", want, got)

RIGHT (replaces the buggy line entirely):
    if diff := cmp.Diff(want, got); diff != "" {
        t.Errorf("(-want +got):\n%s", diff)  // correct format string
    }
```

After fixing, re-run steps 1-3 (re-diff, re-review) until the reviewer
reports PASS. Loop until clean.

**Step 5: Writer reports the reviewer's verdict to the user.**

```
Tests written for: internal/core/vm/service_test.go
Blind review verdict: PASS
Changes: 1 file, +187 lines
All 10 rules verified.
```

### Why debt accumulation is forbidden

Agents often fix a violation by ADDING code that compensates for the buggy
code, rather than REPLACING the buggy code with correct code. This creates
test debt: the file becomes longer, harder to read, and the original bug
remains dormant. The blind review catches this because the reviewer sees
both the old and new code, and if the old code is still present, the
violation is still there.

### Enforcement

This step is NOT optional. Any test file submitted without a blind adversarial
review report is considered UNREVIEWED and MUST be rejected in code review.
The reviewing engineer checks that the report exists and that the reviewer
agent was given the EXACT prompt above (no modifications that could bias the
result).

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
2. **SURVIVAL**: If ToInt unconditionally returns 0, every non-zero test fails
3. **DIFF**: `cmp.Diff` shows exact value mismatch with `(-want +got)` format
4. **EDGE CASES**: nil, non-numeric string, bool, zero value — all tested
5. **NO TAUTOLOGY**: Input values are different from expected outputs
