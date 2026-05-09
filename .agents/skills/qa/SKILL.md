---
name: qa
version: 1.0.0
description: Ensure comprehensive testing discipline and CI compliance for mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "testing", "pytest", "coverage"]
  workflow: ci
---

## What I do

I ensure comprehensive testing discipline:

- **Coverage is a floor, not a ceiling** — 80% is the minimum, not the goal
- **Hermeticity is sacred** — Tests must never touch the real system
- **Isolation is non-negotiable** — Each test owns its own universe
- **Mock with surgical precision** — Stub only what you must, not what you can
- **TDD is a rhythm** — Red, green, refactor. Always in that order.

## When to use me

Use me when:
- Writing tests for new features
- Setting up test infrastructure
- Debugging test failures
- Ensuring CI compliance

I am NOT for code review or security — use `@.agents/skills/code-review/` or `@.agents/skills/security/` skills for that.

## Core Principles

### Principle 1: COVERAGE IS A FLOOR, NOT A CEILING

The 80% branch coverage gate exists for a REASON:

- It catches bugs before they escape
- It documents behavior through tests
- It forces you to think about edge cases

If you find yourself writing a test that exists ONLY to hit coverage, ask: "Am I testing behavior, or gaming metrics?"

**MEMO**: "The number is a floor. Walk on it, do not dance on it."

### Principle 2: HERMETICITY IS SACRED

Tests must NEVER require:
- Root access
- KVM hardware
- Real network
- External services

If your test needs any of these, it is BROKEN. Mock it. The real system is not your playground — it is your target.

**MEMO**: "The test environment is a sealed jar. Nothing gets in, nothing gets out."

### Principle 3: ISOLATION IS NON-NEGOTIABLE

Every test gets:
- Fresh `~/.config/mvmctl/` directory
- Fresh `~/.cache/mvmctl/` directory
- Fresh iptables rules

If tests share state, they influence each other. Influence begets flakiness. Flakiness begets distrust.

**MEMO**: "Your test's world begins when it starts, and ends when it finishes. No inheritance."

### Principle 4: MOCK WITH SURGICAL PRECISION

Stub ONLY what you must. When you mock too much:
- You test the mock, not the code
- Refactoring breaks tests unnecessarily
- The test becomes documentation of the mock, not the behavior

**What to mock**:
- Subprocess calls (they touch the real system)
- Network operations (they are non-deterministic)
- File system operations (they depend on environment)

**What NOT to mock**:
- Pure functions with no side effects
- Dataclass creations
- Simple data transformations

**MEMO**: "Mock the door, not the room. The occupant must still prove they can open it."

### Principle 5: TDD IS A RHYTHM

The rhythm is SACRED:

1. **RED** — Write a failing test that describes the behavior you want
2. **GREEN** — Write minimal code to make it pass
3. **REFACTOR** — Clean the code while keeping tests passing

Skipping steps creates debt. Writing tests after code is NOT TDD — it is test-implementation.

**MEMO**: "Red before green. Green before clean. Never skip the dance."

## Test Structure (The Map)

```
tests/
├── conftest.py              # Root: _mock_sudo_cache, isolate_config_and_cache, _isolate_iptables_rules, _setup_database, _block_real_sudo_invocations, _mock_privilege_checks (autouse ALL) — seals the jar
├── unit/
│   ├── test_cli_*.py        # CLI layer tests — CliRunner
│   └── test_*.py            # Core/API unit tests — 118 files
├── integration/
│   └── test_*.py            # Multi-module workflows — 18 files
├── system/
│   └── test_*.py            # Full-system tests — 18 files
└── layer_compliance/
    ├── test_imports.py      # cli/ never imports from core/
    ├── test_constants.py    # No hardcoded defaults
    ├── test_privilege.py    # Privilege checks in api/
    ├── test_startup_time.py # Module import startup benchmarks
    ├── test_cleanup.py      # Pytest temp dir cleanup behavior
    ├── test_memory_leak_patterns.py  # Detects potential memory leak patterns
    └── test_blocking_loops.py       # Detects blocking calls in async paths
```

## Mocking Patterns

### pytest-mock (simple mocks)
```python
def test_list_vms_empty(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])
```

### unittest.mock.patch (subprocess)
```python
@patch("mvmctl.core.host_setup.subprocess.run")
def test_host_init_calls_subprocess(mock_run):
    mock_run.return_value = MagicMock(returncode=0, stdout="")
```

### CLI Testing (always CliRunner)
```python
from typer.testing import CliRunner

runner = CliRunner()
result = runner.invoke(app, ["rm", "--name", "myvm", "--force"])
assert result.exit_code == 0
```

## Key Fixtures (The Guardians)

| Fixture | Location | Purpose | Scope |
|---------|----------|---------|-------|
| `_mock_sudo_cache` | tests/conftest.py | Prevents real sudo calls | autouse ALL |
| `isolate_config_and_cache` | tests/conftest.py | Fresh config/cache dirs | autouse ALL |
| `_setup_database` | tests/conftest.py | Fresh in-memory database | autouse ALL |
| `_isolate_iptables_rules` | tests/conftest.py | Fresh iptables rules | autouse ALL |
| `_block_real_sudo_invocations` | tests/conftest.py | Blocks real sudo calls | autouse ALL (enforced) |
| `_mock_privilege_checks` | tests/conftest.py | Mocks HostPrivilegeHelper | autouse ALL |

## Layer Compliance Tests (7 files)

These are the WATCHTOWERS:

- **test_imports.py**: cli/ never imports from core/ directly
- **test_constants.py**: No hardcoded defaults, constants.py is single source of truth
- **test_privilege.py**: Privilege checks exist in api/ layer, NOT in cli/
- **test_startup_time.py**: Module import startup benchmark checks
- **test_cleanup.py**: Pytest temp dir cleanup behavior
- **test_memory_leak_patterns.py**: Detects potential memory leak patterns
- **test_blocking_loops.py**: Detects blocking calls in async paths

## CI Commands (The Gauntlet)

### Essential Commands
```bash
# Fast run (stop on first failure, parallel)
uv run pytest tests/ -x -q -n auto

# Coverage report (fails if <80% branch)
uv run pytest tests/ --cov=mvmctl -n auto --cov-fail-under=80

# Layer compliance only
uv run pytest tests/layer_compliance/ -v

# Single file with verbose output
uv run pytest tests/unit/test_vm_manager.py -v
```

### Quality Gates (ALL MUST PASS)
```bash
uv run ruff check src/
uv run ruff format --check src/
uv run mypy src/
uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80
```

## TDD Checklist (The Dance Steps)

- [ ] Write failing test first (RED)
- [ ] Implement minimal code to pass (GREEN)
- [ ] Refactor while maintaining coverage (REFACTOR)
- [ ] Test covers both success and error paths
- [ ] Test mocks all subprocess/OS calls
- [ ] Test uses appropriate fixtures (VM/network/key)
- [ ] Layer compliance tests pass
- [ ] 80% branch coverage maintained

## Quick Reference

| Question | Answer |
|----------|--------|
| Test subprocess? | `@patch` in tests/, mock in code |
| Test CLI? | CliRunner.invoke() |
| Coverage gate? | 80% branch minimum |
| Real sudo/KVM/network? | NEVER — always mock |
| TDD rhythm? | RED → GREEN → REFACTOR |

