# tests/ — Test Suite for mvmctl

**Scope:** Unit, integration, system, and layer-compliance tests
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Coverage Gate:** 80% branch coverage (`pyproject.toml --cov-fail-under=80`)
**Rule:** Tests must NEVER require root, KVM, or real network stack (except system tests)
**Files:** 149 test files (111 unit + 17 integration + 14 system + 7 layer_compliance)

## STRUCTURE

```
tests/
├── conftest.py              # Root: _mock_sudo_cache, isolate_config_and_cache, _isolate_iptables_rules, _setup_database (autouse)
├── helpers/
│   └── paths.py             # make_test_paths(tmp_path) — single source of truth for canonical test paths
├── unit/                    # 111 unit test files across all modules
├── integration/
│   ├── conftest.py          # Integration-specific fixtures
│   └── test_*.py            # Multi-module workflow tests (17 files)
├── system/
│   ├── conftest.py          # Real hardware fixtures; _restore_real_dirs override
│   └── test_*.py            # Black-box CLI tests via subprocess (14 files)
├── layer_compliance/
│   ├── test_imports.py      # Enforces import boundaries (cli→api→core only)
│   ├── test_constants.py    # Ensures constants.py is single source of truth
│   ├── test_privilege.py    # Verifies privilege checks in api/ layer
│   ├── test_startup_time.py # Enforces <200ms startup time
│   ├── test_cleanup.py      # Pytest temp dir cleanup behavior
│   ├── test_memory_leak_patterns.py  # Detects potential memory leak patterns
│   └── test_blocking_loops.py       # Detects blocking calls in async paths
```

## KEY FIXTURES (conftest.py)

### Root Conftest (autouse — ALL tests)

Prevents real sudo/system calls across entire test suite.

```python
@pytest.fixture(autouse=True)
def _mock_sudo_cache():
    # Prevents real sudo -n/-v calls during tests
    # Blocks subprocess invokes to sudo, sudoedit, pkexec

@pytest.fixture(autouse=True)
def isolate_config_and_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path / "config"))
    monkeypatch.setenv("MVM_CACHE_DIR",  str(tmp_path / "cache"))
    monkeypatch.setenv("MVM_TEMP_DIR",   str(tmp_path / "temp"))

@pytest.fixture(autouse=True)
def _isolate_iptables_rules():  # Clears iptables before each test

@pytest.fixture(autouse=True)
def _setup_database():  # Sets up test database
```

Guarantees no test touches `~/.config/mvmctl/`, `~/.cache/`, or `~/.local/share/mvmctl/`.

### Test Path Helper

```python
from tests.helpers.paths import make_test_paths

paths = make_test_paths(tmp_path)
# paths.config, paths.cache, paths.temp — canonical isolated directories
```

### VM Fixtures

| Fixture | Type | Purpose |
|---------|------|---------|
| `sample_vm` | `VMInstanceItem` | Valid VM with default config |
| `stopped_vm` | `VMInstanceItem` | VM in stopped state (idempotent cleanup) |
| `running_vm` | `VMInstanceItem` | VM in running state (mocks socket) |
| `error_vm` | `VMInstanceItem` | VM in error state |

### Network Fixtures

```python
@pytest.fixture
def sample_network():
    return NetworkItem(name="testnet", subnet="10.0.0.0/24", ...)
```

### Key Fixtures

| Fixture | Type | Purpose |
|---------|------|---------|
| `mock_keys_dir` | `Path` | Temporary `.ssh/` dir with mock keys |
| `sample_key_info` | `dict` | Valid key metadata for tests |

### Subprocess Mocks

| Fixture | Behavior |
|---------|----------|
| `mock_subprocess_run_success` | Returns `returncode=0, stdout="", stderr=""` |
| `mock_subprocess_run_failure` | Returns `returncode=1, stdout="", stderr="error"` |

**Note:** Mocks patch `subprocess.run` globally via `monkeypatch.setattr("subprocess.run", ...)` — not module-scoped.

## MOCKING PATTERNS

### pytest-mock (preferred)

For simple return-value patches and Spy calls.

```python
def test_list_vms_empty(mocker: MockerFixture):
    mocker.patch("mvmctl.cli.vm.list_vms", return_value=[])
```

```python
def test_create_vm_calls_api(mocker: MockerFixture):
    mock_api = mocker.patch("mvmctl.api.VMOperation.create")
    mock_api.assert_called_once()
```

### unittest.mock.patch

For subprocess/OS calls and complex mocks.

```python
@patch("mvmctl.core.host_setup.subprocess.run")
def test_host_init_calls_subprocess(mock_run):
    mock_run.return_value = MagicMock(returncode=0, stdout="")
    init_host()
    mock_run.assert_any_call(["sudo", ...], check=True, ...)
```

### CLI Testing (CliRunner)

Always use CliRunner for CLI layer; never invoke real subprocess.

```python
from typer.testing import CliRunner

runner = CliRunner()

def test_rm_success():
    result = runner.invoke(app, ["rm", "--name", "myvm", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.stdout
```

### Patching VMRepository (hash-keyed API)

VMRepository uses SHA256 hash keys; mock the return structure.

```python
def test_vm_lookup_by_name(mocker: MockerFixture):
    mock_repo = mocker.MagicMock(spec=VMRepository)
    sample = VMInstanceItem(name="test", id="abc123def...")
    mock_repo.get_by_name.return_value = sample
    mock_repo.find_by_prefix.return_value = []
    mocker.patch("mvmctl.core.vm._repository.VMRepository", return_value=mock_repo)
```

## LAYER COMPLIANCE TESTS

`tests/layer_compliance/` enforces architectural boundaries.

### test_imports.py

Verifies import boundaries are not violated.

### test_constants.py

Ensures constants.py is single source of truth — no hardcoded defaults, magic numbers, or missing env vars.

### test_privilege.py

Verifies privilege checks exist in api/ layer, not in cli/.

### test_startup_time.py

Enforces CLI startup time limit (< 200ms) for all modules. Uses subprocess isolation for accurate cold-start measurement.

To exempt a slow module, add to `STARTUP_ALLOWLIST`:
```python
STARTUP_ALLOWLIST = {
    "mvmctl.module.name": "Reason for exemption (Issue #X)",
}
```

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Real subprocess calls | `@patch("...subprocess.run")` |
| Real sudo invocations | Root conftest `_mock_sudo_cache` blocks; mock `ensure_default_network` in host tests |
| `tempfile.mkdtemp()` | `tmp_path` pytest fixture |
| Skip test for coverage | Fix it; coverage drop fails CI |
| Hardcoded `~/.cache/` paths | `monkeypatch.setenv("MVM_CACHE_DIR", ...)` |
| `type: ignore` in tests | Allowed for mocks; document reason |

## COMMANDS

```bash
# Fast run (stop on first failure)
uv run pytest tests/ -x -q

# Single file with verbose output
uv run pytest tests/unit/test_vm_manager.py -v

# Integration tests only
uv run pytest tests/integration/ -v

# Layer compliance tests
uv run pytest tests/layer_compliance/ -v

# Coverage report (fails if <80% branch)
uv run pytest tests/ --cov=src/mvmctl --cov-fail-under=80

# Run specific test class
uv run pytest tests/unit/test_vm_manager.py::TestVMManagerGetByName -v

# Run tests matching pattern
uv run pytest tests/ -k "test_create" -v

# With full traceback
uv run pytest tests/ --tb=long

# Parallel execution (requires pytest-xdist)
uv run pytest tests/ -n auto
```

## NOTES

- **149 total test files**: 111 unit + 17 integration + 14 system + 7 layer_compliance
- mypy strict exempted for tests (`pyproject.toml` overrides: no `disallow_untyped_defs`)
- All tests run as non-root; no KVM access required (except system tests)
- Fixtures in `unit/conftest.py` auto-used for all unit tests
- CliRunner invoked against `mvmctl.main.app` (Click group), NOT Typer app
- System tests excluded from default CI run via markers
