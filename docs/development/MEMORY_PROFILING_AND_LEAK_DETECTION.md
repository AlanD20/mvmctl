# Memory Profiling and Leak Detection

## Overview

This project uses a two-layer defense against memory leaks in the test suite:

1. **Static analysis (layer compliance tests)** — catches anti-patterns before they merge
2. **Runtime profiling (profiler script)** — isolates actual leaks when they occur

Both layers matter because pytest's default behavior runs all tests in a single process. A single leaky test can consume all system memory and OOM-kill the test runner, masking which test was responsible and losing the entire session.

## Tools

### 1. Memory Profiler Script

**Location:** `scripts/profile_test_memory.py`

**What it does:**

- Discovers tests via `pytest --collect-only`
- Runs **each** test in its own subprocess
- Monitors peak RSS memory via `psutil`
- Sorts results by peak memory usage (descending)
- Flags tests exceeding a configurable threshold

**Why subprocess isolation matters:**

> **Warning:** If a test leaks memory and you run the full suite normally, pytest will OOM-kill. You lose the session and don't know which test caused it. The profiler runs each test in isolation — only the child dies.

#### Usage

Basic file-level profiling:

```bash
uv run python scripts/profile_test_memory.py tests/unit/services/test_console_relay_client.py --level file --threshold-mb 500
```

Individual test profiling (pinpoints exact test):

```bash
uv run python scripts/profile_test_memory.py tests/unit/services/test_console_relay_client.py --level test --threshold-mb 500
```

Profile an entire directory:

```bash
uv run python scripts/profile_test_memory.py tests/unit/ --level file --threshold-mb 200
```

With verbose output (shows stdout on failures):

```bash
uv run python scripts/profile_test_memory.py tests/unit/ --level test --threshold-mb 200 -v
```

Save results to TSV:

```bash
uv run python scripts/profile_test_memory.py tests/unit/ --level file --output memory_report.tsv
```

#### Command Reference

| Flag | Description | Default |
|------|-------------|---------|
| `target` | pytest node ID or path (required) | — |
| `--level` | `file` or `test` granularity | `test` |
| `--timeout` | seconds per subprocess | `120` |
| `--threshold-mb` | memory threshold for leak flagging | `200` |
| `--output` | TSV output file path | — |
| `-v` / `--verbose` | print stdout tail on failures | `false` |

#### Interpreting Results

The profiler always runs tests with `--no-cov` to avoid coverage-instrumentation overhead in the memory measurement. Example output:

```
Name                                                                          Status    Peak MB
---------------------------------------------------------------------------   ------   --------
tests/unit/services/test_console_relay_client.py::test_receive_skips_          FAIL       4426.0
tests/unit/services/test_console_relay_client.py::test_receive_yields_         PASS          0.0
tests/unit/services/test_console_relay_client.py::test_start_listens_          PASS          0.0

Flagged potential leaks (Peak > 500.0 MB):
  tests/unit/services/test_console_relay_client.py::test_receive_skips_when_socket_not_in_ready  — 4426.0 MB
```

- `PASS` / `FAIL` — test result
- `Peak MB` — maximum RSS observed (parent + children)
- **Flagged leaks section** — tests above the `--threshold-mb` threshold

#### Limitations

- Only works on pytest-discoverable targets (tests, not arbitrary scripts)
- Measures RSS, not Python heap specifically (includes interpreter overhead)
- The profiler always passes `--no-cov` and `--timeout=60` to pytest; results reflect bare test overhead, not coverage instrumentation

---

### 2. Layer Compliance Tests

**Location:** `tests/layer_compliance/test_memory_leak_patterns.py`

**What it does:**

- AST-based static analysis of source code
- Catches known memory leak anti-patterns
- Runs in ~2 seconds
- Fails CI if violations are found

#### Detection Categories

| Category | Class | What it catches | Confidence |
|----------|-------|----------------|------------|
| Infinite Loops | `TestInfiniteLoops` | `while True:` with no `break`/`return`/`raise`/`sys.exit()` | **High** |
| Unbounded Accumulation | `TestUnboundedAccumulation` | `.append()`/`.extend()` in infinite loops without cleanup | Medium |
| Resource Leaks | `TestResourceLeaks` | `socket.socket()`, `open()`, `subprocess.Popen()` without cleanup | Medium |
| Mock Abuse | `TestMockAbuse` | `patch(return_value=...)` on blocking I/O inside loops | **High** |

#### Running

```bash
# Run only memory leak compliance tests
uv run scripts/run_tests.py --compliance --pytest-extra "-v --no-cov -k test_memory_leak_patterns"

# Run all layer compliance tests (includes imports, constants, startup time, etc.)
uv run scripts/run_tests.py --compliance --pytest-extra "-v --no-cov"
```

#### Adding Allowlist Entries

If a violation is a false positive (legitimate infinite loop in a daemon, etc.), add the file path to the appropriate category allowlist in the test with a documented justification:

```python
CATEGORY_1_ALLOWLIST: dict[str, str] = {
    "src/mvmctl/core/logs/_service.py": (
        "Log-following generator intentionally runs until the consumer closes it. "
        "yield statement provides cooperative termination."
    ),
}
```

The four category allowlists map to the detection classes above:
- `CATEGORY_1_ALLOWLIST` — Infinite Loops
- `CATEGORY_2_ALLOWLIST` — Unbounded Accumulation
- `CATEGORY_3_ALLOWLIST` — Resource Leaks
- `CATEGORY_4_ALLOWLIST` — Mock Abuse

> **Note:** Allowlists are documentation. Don't add entries to silence failures — fix the underlying pattern or justify why it's safe.

---

## Workflow: Investigating a Suspected Leak

Step-by-step guide:

1. **Run the compliance test first** (fast, catches obvious patterns):

   ```bash
   uv run scripts/run_tests.py --compliance --pytest-extra "-v --no-cov"
   ```

2. **If compliance passes, use the profiler to isolate the leak:**

   ```bash
   # Start broad — file level
   uv run python scripts/profile_test_memory.py tests/unit/ --level file --threshold-mb 500

   # Drill down — test level on the worst file
   uv run python scripts/profile_test_memory.py tests/unit/services/test_console_relay_client.py --level test --threshold-mb 500
   ```

3. **Fix the root cause** (usually one of):
   - Missing `return`/`break` in a blocking I/O loop
   - Unbounded `.append()` without `.clear()`
   - Infinite `MagicMock` recording in tight loops
   - Resource opened without cleanup

4. **Verify the fix:**

   ```bash
   # Re-run the specific test via profiler
   uv run python scripts/profile_test_memory.py <test_id> --level test --threshold-mb 200

   # Re-run compliance to ensure no new patterns were introduced
   uv run scripts/run_tests.py --compliance --pytest-extra "-v --no-cov"
   ```

---

## Real-World Example

The console relay client leak we fixed demonstrates the full workflow:

| Metric | Value |
|--------|-------|
| Test | `test_receive_skips_when_socket_not_in_ready` |
| Symptom | 4,426 MB peak memory, 68 s runtime |
| Root cause | `receive()` busy-waited when `select.select` timed out; the mock returned instantly, causing an infinite loop and unbounded `MagicMock` call recording |
| Source fix | Added `if fd not in ready: return` to terminate the generator on timeout |
| Test fix | Changed `return_value=([], [], [])` to `side_effect=[([], [], [])]` |

---

## When to Use Which Tool

| Situation | Tool | Why |
|-----------|------|-----|
| Writing new code with loops / blocking I/O | Compliance test | Catches patterns before they become leaks |
| CI gate on PR | Compliance test | Runs in 2 s, prevents anti-patterns from merging |
| Test suite OOMs or slows down mysteriously | Profiler | Isolates exact test causing the leak |
| Reviewing a PR with blocking I/O changes | Compliance test | Automated check for resource loop patterns |
| Suspected leak in existing code | Both | Compliance first (fast), profiler second (precise) |

---

## Limitations and Known Gaps

- **Compliance tests are static analysis** — they catch patterns, not runtime behavior. A loop with a complex exit condition may pass compliance but still leak under specific inputs.
- **Profiler measures RSS** — it includes interpreter overhead and pytest plugins. A test using 800 MB doesn't necessarily have an 800 MB leak; compare relative differences between tests.
- **Neither tool catches reference cycles** — Python's cyclic garbage collector handles most cycles automatically. For cycle leaks, use `tracemalloc` or `objgraph`.
- **Neither tool catches C-extension leaks** — if a C library (e.g., libguestfs) leaks memory, these tools will show elevated RSS but won't identify the C code.
