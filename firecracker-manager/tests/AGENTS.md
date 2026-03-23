# tests/ — Test Suite

**Scope:** Unit and integration tests  
**Coverage Gate:** 79% branch coverage (enforced in CI)  
**Rule:** Tests must NEVER require root, KVM, or real network stack

## STRUCTURE

```
tests/
├── unit/
│   ├── conftest.py          # Shared fixtures
│   ├── test_*.py            # 30 unit test files
│   ├── test_vm_manager.py   # VM registry tests
│   ├── test_cli_vm.py       # CLI command tests
│   └── test_host.py         # Host init tests
└── integration/
    └── test_cli_smoke.py    # Smoke tests (no mocks)
```

## WHERE TO LOOK

| Task | Location |
|------|----------|
| Shared fixtures | `unit/conftest.py` |
| CLI tests | `unit/test_cli_*.py` |
| Network tests | `unit/test_network.py` |
| Host tests | `unit/test_host.py` |
| Smoke tests | `integration/test_cli_smoke.py` |

## CONVENTIONS

### Test Naming
- File: `test_{module}.py`
- Function: `test_{action}_{condition}`
- Class: `Test{Feature}` (for grouped tests)

### Fixtures (conftest.py)
```python
@pytest.fixture
def mock_cache_dir(tmp_path: Path, monkeypatch) -> Path:
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    return tmp_path

@pytest.fixture
def sample_vm() -> VMInstance:
    return VMInstance(name="test-vm", ip="10.20.0.2", ...)
```

### Mocking Patterns

**pytest-mock:**
```python
def test_list_vms_empty(mocker: MockerFixture):
    mocker.patch("fcm.cli.vm.list_vms", return_value=[])
```

**unittest.mock.patch:**
```python
@patch("fcm.core.host_setup.subprocess.run")
def test_get_ip_forward_status_success(mock_run):
    mock_run.return_value = MagicMock(stdout="1\n")
```

### CLI Testing
```python
from typer.testing import CliRunner
from fcm.cli.vm import app

runner = CliRunner()
result = runner.invoke(app, ["list", "--json"])
```

## ANTI-PATTERNS

### NEVER
- Real subprocess calls — Mock ALL system commands
- Write to real filesystem — Use `tmp_path` fixture
- Skip tests for coverage — Coverage drop = CI failure
- Hardcode paths — Use fixtures

### MyPy Exemption
Tests exempt from strict typing (configured in `pyproject.toml`)

## COMMANDS

```bash
# All tests with coverage gate
uv run pytest tests/ --cov=src/fcm --cov-branch --cov-fail-under=79

# Single file
uv run pytest tests/unit/test_vm_manager.py -v

# Smoke tests (no mocks)
uv run pytest tests/integration/ -v
```

## NOTES

- **36 test files**, ~9,724 lines of test code
- **All subprocess mocked** — Tests run without root/KVM
- **Fixtures auto-imported** from `conftest.py`
- **79% coverage gate** enforced in CI
