---
name: qa
version: 1.0.0
description: Ensure comprehensive testing and CI compliance for mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "testing", "pytest", "coverage"]
  workflow: ci
---

## What I do

I ensure comprehensive testing for new functionality:

- **Test structure** — Guide placement (unit vs integration vs layer_compliance)
- **Mocking patterns** — Configure pytest-mock, unittest.patch, CliRunner
- **Fixtures** — Use autouse fixtures for isolation (_mock_sudo_cache, isolate_config_and_cache)
- **Coverage requirements** — Maintain 80% branch minimum
- **Layer compliance** — Validate import boundaries and constants
- **CI commands** — Provide exact test commands for local and CI

## When to use me

Use me when:
- Writing tests for new features
- Setting up test infrastructure
- Debugging test failures
- Ensuring CI compliance

I am NOT for code review or security — use `@.agents/skills/code-review/` or `@.agents/skills/security/` skills for that.

## Testing Philosophy

**Golden Rules**:
- 80% branch coverage minimum (CI-enforced)
- Tests must NEVER require root, KVM, or real network
- Mock all subprocess calls
- Test isolation: every test uses fresh config/cache dirs

## Test Structure

```
tests/
├── conftest.py              # Root: _mock_sudo_cache (autouse ALL)
├── unit/
│   ├── conftest.py          # isolate_config_and_cache (autouse)
│   ├── test_cli_*.py        # CLI layer tests (CliRunner)
│   └── test_*.py            # Core/API unit tests (54 files)
├── integration/
│   └── test_*.py            # Multi-module workflows (7 files)
└── layer_compliance/
    ├── test_imports.py      # Enforces import boundaries
    ├── test_constants.py    # No hardcoded defaults
    └── test_privilege.py    # Privilege checks in api/
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

## Key Fixtures

| Fixture | Location | Purpose | Scope |
|---------|----------|---------|-------|
| `_mock_sudo_cache` | tests/conftest.py | Prevents real sudo calls | autouse ALL |
| `isolate_config_and_cache` | tests/unit/conftest.py | Fresh config/cache dirs | autouse unit |
| `vm_manager` | unit/conftest.py | Real VMManager instance | unit |
| `sample_vm` | unit/conftest.py | Valid VM dataclass | unit |
| `mock_subprocess_run_success` | unit/conftest.py | Mock subprocess success | unit |

## Layer Compliance Tests

- **test_imports.py**: cli/ never imports from core/ directly
- **test_constants.py**: No hardcoded defaults, constants.py is single source
- **test_privilege.py**: Privilege checks exist in api/ layer, NOT in cli/

## CI Commands

### Essential Commands
```bash
# Fast run (stop on first failure)
uv run pytest tests/ -x -q

# Coverage report (fails if <80% branch)
uv run pytest tests/ --cov=mvmctl --cov-fail-under=80

# Layer compliance only
uv run pytest tests/layer_compliance/ -v

# Single file with verbose output
uv run pytest tests/unit/test_vm_manager.py -v
```

### Quality Gates (MUST ALL PASS)
```bash
uv run ruff check src/
uv run ruff format --check src/
uv run mypy src/
uv run pytest tests/ -q --cov=src/mvmctl --cov-fail-under=80
```

## TDD Checklist for New Features

- [ ] Write failing test first (red)
- [ ] Implement minimal code to pass (green)
- [ ] Refactor while maintaining coverage (refactor)
- [ ] Test covers both success and error paths
- [ ] Test mocks all subprocess/OS calls
- [ ] Test uses appropriate fixtures (VM/network/key)
- [ ] Layer compliance tests pass
- [ ] 80% branch coverage maintained
