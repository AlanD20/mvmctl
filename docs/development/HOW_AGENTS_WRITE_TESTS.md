# How Agents Write System Tests

## Philosophy

Agents do NOT decide what to test. They translate a concrete specification
into working Python test code. The specification tells the agent exactly:

- What CLI commands to run
- In what order
- What assertions to make
- What cleanup is required

---

## Core Principle: Cheapest Resource Wins

This is the **most important rule** in this document. Every test must use
the cheapest (least expensive) resource that satisfies its assertions.

### Resource Cost Hierarchy

```
Creating a VM          →  30-120  seconds  ← EXPENSIVE, AVOID
Creating a network     →  5-10   seconds  ← MODERATE
Creating a volume      →  1-3    seconds  ← CHEAP
Creating an SSH key    →  0.5-1  seconds  ← CHEAPEST
Running `mvm ls/json`  →  0.1    seconds  ← FREE
```

### Decision Tree

Before creating ANY resource in a test, ask:

```
Does my test need a RUNNING VM?          → YES → requires_kvm + VM fixture
  No → Does my test need a STOPPED VM?   → YES → requires_kvm + VM fixture
    No → Does my test need a NETWORK?    → YES → requires_network + network fixture
      No → Does my test need a VOLUME?   → YES → volume fixture
        No → Does my test need a KEY?    → YES → key fixture
          No → DON'T CREATE ANYTHING     → use `mvm ls --json` or similar
```

### Examples of Wasteful vs Efficient Tests

**WASTEFUL** — creates a VM just to test JSON field naming:
```python
def test_vm_ls_json_has_name_field(self, mvm_binary):
    # Creates a VM (30-120s) just to check JSON output format
    vm_name = f"wasteful-{uuid.uuid4().hex[:6]}"
    _run_mvm(mvm_binary, "vm", "create", "--name", vm_name, ...)
    try:
        result = _run_mvm(mvm_binary, "vm", "ls", "--json")
        data = json.loads(result.stdout)
        assert "name" in data[0]
    finally:
        _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force")
```

**EFFICIENT** — uses existing VM or checks format without creating one:
```python
def test_vm_ls_json_has_name_field(self, mvm_binary):
    # Assumes at least one VM exists (from prepare step or previous test)
    # If no VMs exist, skip — the format test runs when there's data
    result = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        pytest.skip("No VMs to inspect")
    data = json.loads(result.stdout)
    if data:
        assert "name" in data[0]
```

**WASTEFUL** — creates a new network for every test in a 10-test file:
```python
class TestNetworkEdgeCases:
    def test_a(self, mvm_binary, unique_network_name):
        net = unique_network_name  # creates network
        # ... test ...
        _run_mvm(mvm_binary, "network", "rm", net, check=False)

    def test_b(self, mvm_binary, unique_network_name):
        net = unique_network_name  # creates ANOTHER network
        # ... test ...
        _run_mvm(mvm_binary, "network", "rm", net, check=False)
```

**EFFICIENT** — uses a module-scoped fixture for read-only tests:
```python
@pytest.fixture(scope="module")
def shared_network(mvm_binary) -> Generator[str, None, None]:
    """One network for all read-only tests in this module."""
    name = f"sys-shared-{uuid.uuid4().hex[:6]}"
    _run_mvm(mvm_binary, "network", "create", name,
             "--subnet", _unique_subnet(name), "--non-interactive")
    try:
        yield name
    finally:
        _run_mvm(mvm_binary, "network", "rm", name, check=False)

class TestNetworkEdgeCases:
    def test_inspect_json(self, mvm_binary, shared_network):
        result = _run_mvm(mvm_binary, "network", "inspect",
                          shared_network, "--json")
        assert json.loads(result.stdout).get("name") == shared_network

    def test_list_after_create(self, mvm_binary, shared_network):
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        names = [n["name"] for n in json.loads(result.stdout)]
        assert shared_network in names
```

---

## The Specification Format

Every test in the specification document follows this exact structure:

```yaml
test_name: test_<resource>_<action>_<expected_behavior>
file: coverage/test_<category>.py
fixtures: [mvm_binary, unique_vm_name, ...]     # cheapest possible
markers: [requires_kvm, slow, ...]               # only if truly needed
steps:
  - action: <mvm command and arguments>
  - action: <mvm command and arguments>
    expect_fail: true
    assert_stderr_contains: ["error substring 1", "error substring 2"]
  - action: <mvm command and arguments>
    assert_stdout_json:
      field: "status"
      equals: "attached"
cleanup:
  - <mvm command> --force
  - <mvm command>
rationale: Why we need this resource level  # explains cost decision
```

---

## Rules the Agent Must Follow

### Rule 1: Cheapest resource possible
Before adding a fixture or creating a resource, check the cost hierarchy.
If you can test with a key instead of a VM, do it. If you can skip the
test when no target resource exists, do that instead.

### Rule 2: One file per agent
Each agent writes to exactly one file. No two agents touch the same file.
Files live in `tests/system/coverage/`.

### Rule 3: Self-contained tests
Every test creates its own resources with unique names. No shared fixtures
between tests within a file (except `mvm_binary` which is session-scoped).

### Rule 4: Cleanup in `finally`
Every resource created must be destroyed in a `finally` block.
Use `check=False` on cleanup commands.

### Rule 5: Use available fixtures
- `mvm_binary: str` — path to the mvm binary
- `unique_vm_name: str` — uuid-based unique VM name per test
- `unique_key_name: str` — uuid-based unique key name per test
- `unique_network_name: str` — uuid-based unique network name
- `tmp_path: Path` — temporary directory for file operations

Do NOT use `created_vm`, `created_network`, or `created_key` fixtures
because they use module-scoped resources that create state pollution.

### Rule 6: Fixture order
When using multiple unique_* fixtures, they must appear in this order
in the function signature: `mvm_binary, unique_vm_name, unique_key_name`.

### Rule 7: Markers
```python
pytestmark = [pytest.mark.system, pytest.mark.domain_<category>]
```
Each test also gets individual markers as specified:
- `@pytest.mark.requires_kvm` — tests that create VMs
- `@pytest.mark.requires_network` — tests that create networks
- `@pytest.mark.slow` — tests taking >30 seconds

Only add markers that are TRULY needed. If a test doesn't create a VM,
do NOT mark it `requires_kvm`.

### Rule 8: File structure
```python
"""Docstring describing what this file covers."""

from __future__ import annotations

import json
import uuid
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [pytest.mark.system, pytest.mark.domain_<category>]


class TestCategoryName:
    """Tests for <category>."""

    # ... tests ...

    def test_example(
        self, mvm_binary, unique_vm_name, unique_key_name
    ) -> None:
        """Docstring describing the scenario."""
        # ... setup, action, assertions, cleanup ...
```

### Rule 9: Destructive tests last
Tests that remove or destroy resources must appear at the END of the
file, after all read-only and state-inspection tests.

### Rule 10: Assertion patterns
- Expected success: use `check=True` (default), no assertion needed
- Expected failure: use `check=False`, then `assert result.returncode != 0`
- Expected stderr content: `assert "substring" in (result.stdout + result.stderr).lower()`
- Expected JSON output: parse with `json.loads(result.stdout)`, then assert fields
- Expected empty output: no assertion beyond zero returncode
- Expected specific status: check JSON field with `.get("field")`

### Rule 11: JSON assertion pattern
```python
result = _run_mvm(mvm_binary, "resource", "inspect", name, "--json")
data = json.loads(result.stdout)
assert data.get("status") == "expected_value"
```

### Rule 12: Every test must have a `rationale` comment
Explain WHY this test exists at the resource level it uses:
```python
def test_something(self, mvm_binary, unique_vm_name):
    # Rationale: Needs a real VM because we're testing volume attachment
    # which requires a stopped VM state. A key or volume fixture won't do.
```

---

## What the Agent Must NOT Do

- ❌ Decide what to test — the spec decides
- ❌ Research test scenarios from the internet — the spec is the source
- ❌ Create unnecessary expensive resources — always use the cheapest fixture
- ❌ Add `requires_kvm` to tests that don't create VMs
- ❌ Add a 4-line cleanup section to a test that only reads `--json` output
- ❌ Modify any existing test file — only write new files
- ❌ Modify `conftest.py` or `pyproject.toml` (marker registration)
- ❌ Run the tests — just write the code
- ❌ Import from `mvmctl.*` — tests are black-box subprocess only
- ❌ Use `created_vm`, `created_network`, or `created_key` fixtures

---

## Before Submitting, Self-Check

```
[ ] Did I use the cheapest possible resource for this test?
[ ] Did I skip creating resources that already exist in the system?
[ ] Does every `requires_kvm` test ACTUALLY create a VM?
[ ] Is cleanup in a `finally` block, not after the assertion?
[ ] Did I avoid `created_vm` / `created_network` / `created_key`?
[ ] Did I add a `# Rationale:` comment?
[ ] Is `ruff check` and `ruff format` clean?
```
